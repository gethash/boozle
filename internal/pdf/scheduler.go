package pdf

import (
	"image"
	"sync"
	"time"
)

// Kind separates job namespaces that share a CacheKey shape but live in
// different caches (full slides vs overview thumbnails).
type Kind uint8

const (
	KindSlide Kind = iota
	KindThumb
)

// Job is one unit of render work.
type Job struct {
	Kind Kind
	Key  CacheKey
}

// Result carries a finished render to the main goroutine, which uploads it
// to the GPU. Release recycles RGBA's pixel buffer and may be nil.
type Result struct {
	Job
	RGBA    *image.RGBA
	Release func()
	Err     error
}

// LoadFunc produces the pixels for a job. It runs on the scheduler's worker
// goroutine.
type LoadFunc func(Job) (*image.RGBA, func(), error)

// Scheduler owns every PDFium render in a process. One worker goroutine
// picks the highest-priority job:
//
//  1. urgent     — the slide the user is looking at right now (single
//     slot; a newer request replaces an older one, so holding an arrow key
//     coalesces into one render)
//  2. visible    — on-screen but secondary (presenter's next pane, overview
//     preview, thumbnails near the selection)
//  3. prefetch   — what the next navigation will need
//  4. background — everything else (remaining overview thumbnails)
//
// SetVisible/SetPrefetch replace their queue wholesale, cancelling queued
// work the caller no longer wants. A job that is in flight, or whose result
// has not been drained yet, is never started twice.
type Scheduler struct {
	load LoadFunc
	skip func(Job) bool // true = already available, don't render

	mu          sync.Mutex
	urgent      *Job
	visible     []Job
	prefetch    []Job
	background  []Job
	bgSet       map[Job]struct{}
	outstanding map[Job]struct{} // in flight or waiting in results

	wake    chan struct{}
	results chan Result
	stop    chan struct{}
	wg      sync.WaitGroup

	statsMu    sync.Mutex
	renders    int
	renderTime time.Duration
}

// NewScheduler returns a stopped scheduler. skip may be nil.
func NewScheduler(load LoadFunc, skip func(Job) bool) *Scheduler {
	if skip == nil {
		skip = func(Job) bool { return false }
	}
	return &Scheduler{
		load:        load,
		skip:        skip,
		bgSet:       make(map[Job]struct{}),
		outstanding: make(map[Job]struct{}),
		wake:        make(chan struct{}, 1),
		results:     make(chan Result, 4),
		stop:        make(chan struct{}),
	}
}

// Start launches the worker goroutine.
func (s *Scheduler) Start() {
	s.wg.Add(1)
	go s.worker()
}

// Stop terminates the worker and releases any undelivered results.
func (s *Scheduler) Stop() {
	close(s.stop)
	s.wg.Wait()
	for {
		select {
		case r := <-s.results:
			if r.Release != nil {
				r.Release()
			}
		default:
			return
		}
	}
}

// Urgent makes j the next job to run, replacing any previous urgent job.
func (s *Scheduler) Urgent(j Job) {
	s.mu.Lock()
	s.urgent = &j
	s.mu.Unlock()
	s.signal()
}

// SetVisible replaces the visible queue (in priority order).
func (s *Scheduler) SetVisible(jobs []Job) {
	s.mu.Lock()
	s.visible = append(s.visible[:0], jobs...)
	s.mu.Unlock()
	s.signal()
}

// SetPrefetch replaces the prefetch queue (in priority order).
func (s *Scheduler) SetPrefetch(jobs []Job) {
	s.mu.Lock()
	s.prefetch = append(s.prefetch[:0], jobs...)
	s.mu.Unlock()
	s.signal()
}

// AddBackground appends jobs to the background queue, ignoring duplicates.
func (s *Scheduler) AddBackground(jobs ...Job) {
	s.mu.Lock()
	for _, j := range jobs {
		if _, ok := s.bgSet[j]; ok {
			continue
		}
		s.bgSet[j] = struct{}{}
		s.background = append(s.background, j)
	}
	s.mu.Unlock()
	s.signal()
}

// ClearBackground drops all queued background jobs.
func (s *Scheduler) ClearBackground() {
	s.mu.Lock()
	s.background = s.background[:0]
	clear(s.bgSet)
	s.mu.Unlock()
}

// Outstanding reports whether j is being rendered or awaits draining.
func (s *Scheduler) Outstanding(j Job) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.outstanding[j]
	return ok
}

// Drain hands finished results to fn on the calling goroutine until the
// results channel is empty or fn returns false. It returns how many results
// were handled. fn owns r.Release.
func (s *Scheduler) Drain(fn func(Result) bool) int {
	n := 0
	for {
		select {
		case r := <-s.results:
			s.mu.Lock()
			delete(s.outstanding, r.Job)
			s.mu.Unlock()
			n++
			if !fn(r) {
				return n
			}
		default:
			return n
		}
	}
}

// QueueLens returns the number of queued jobs per priority (urgent, visible,
// prefetch, background), for diagnostics.
func (s *Scheduler) QueueLens() (urgent, visible, prefetch, background int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.urgent != nil {
		urgent = 1
	}
	return urgent, len(s.visible), len(s.prefetch), len(s.background)
}

// RenderStats returns the number of completed loads and their mean duration.
func (s *Scheduler) RenderStats() (count int, mean time.Duration) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if s.renders == 0 {
		return 0, 0
	}
	return s.renders, s.renderTime / time.Duration(s.renders)
}

func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// next pops the highest-priority runnable job and marks it outstanding.
func (s *Scheduler) next() (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	take := func(j Job) bool {
		if _, busy := s.outstanding[j]; busy {
			return false
		}
		s.outstanding[j] = struct{}{}
		return true
	}
	if s.urgent != nil {
		j := *s.urgent
		s.urgent = nil
		if take(j) {
			return j, true
		}
	}
	for _, q := range []*[]Job{&s.visible, &s.prefetch, &s.background} {
		for len(*q) > 0 {
			j := (*q)[0]
			*q = (*q)[1:]
			if q == &s.background {
				delete(s.bgSet, j)
			}
			if take(j) {
				return j, true
			}
		}
	}
	return Job{}, false
}

func (s *Scheduler) worker() {
	defer s.wg.Done()
	for {
		j, ok := s.next()
		if !ok {
			select {
			case <-s.stop:
				return
			case <-s.wake:
				continue
			}
		}
		select {
		case <-s.stop:
			return
		default:
		}
		if s.skip(j) {
			s.mu.Lock()
			delete(s.outstanding, j)
			s.mu.Unlock()
			continue
		}
		start := time.Now()
		rgba, release, err := s.load(j)
		s.statsMu.Lock()
		s.renders++
		s.renderTime += time.Since(start)
		s.statsMu.Unlock()
		select {
		case s.results <- Result{Job: j, RGBA: rgba, Release: release, Err: err}:
		case <-s.stop:
			if release != nil {
				release()
			}
			return
		}
	}
}
