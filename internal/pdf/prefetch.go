package pdf

import "sync"

// Prefetcher renders pages on a background goroutine and stores them in
// a Cache so the main thread doesn't wait on PDFium when the user
// (or auto-advance) flips to an already-anticipated page.
type Prefetcher struct {
	doc      *Doc
	cache    *Cache
	requests chan CacheKey
	stop     chan struct{}
	wg       sync.WaitGroup
}

// NewPrefetcher returns a Prefetcher that pushes rendered pages into cache.
// queueSize is the bounded channel capacity for pending requests; further
// non-blocking Requests drop on the floor when full.
func NewPrefetcher(doc *Doc, cache *Cache, queueSize int) *Prefetcher {
	if queueSize < 1 {
		queueSize = 1
	}
	return &Prefetcher{
		doc:      doc,
		cache:    cache,
		requests: make(chan CacheKey, queueSize),
		stop:     make(chan struct{}),
	}
}

// Start launches the background worker.
func (p *Prefetcher) Start() {
	p.wg.Add(1)
	go p.worker()
}

// Stop signals the worker to exit and blocks until it does.
func (p *Prefetcher) Stop() {
	close(p.stop)
	p.wg.Wait()
}

// Request asks the worker to render and cache key k. Non-blocking:
// if the queue is full, the request is silently dropped.
func (p *Prefetcher) Request(k CacheKey) {
	if p.cache.Has(k) {
		return
	}
	select {
	case p.requests <- k:
	default:
	}
}

func (p *Prefetcher) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stop:
			return
		case k := <-p.requests:
			if p.cache.Has(k) {
				continue
			}
			img, cleanup, err := p.doc.RenderPage(k.Page, k.W, k.H)
			if err != nil {
				// Errors are benign here: the main thread will re-attempt
				// synchronously when the user navigates to this page.
				continue
			}
			p.cache.Put(k, img, cleanup)
		}
	}
}
