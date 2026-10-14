package pdf

import (
	"image"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func slide(page int) Job { return Job{Kind: KindSlide, Key: CacheKey{Page: page, W: 10, H: 10}} }

func TestSchedulerPriorityOrder(t *testing.T) {
	s := NewScheduler(nil, nil)
	s.AddBackground(slide(4))
	s.SetPrefetch([]Job{slide(3)})
	s.SetVisible([]Job{slide(2)})
	s.Urgent(slide(1))

	for want := 1; want <= 4; want++ {
		j, ok := s.next()
		if !ok || j.Key.Page != want {
			t.Fatalf("next() = %v,%v; want page %d", j, ok, want)
		}
	}
	if j, ok := s.next(); ok {
		t.Fatalf("next() on empty scheduler = %v, want none", j)
	}
}

func TestSchedulerUrgentAndPrefetchReplace(t *testing.T) {
	s := NewScheduler(nil, nil)
	s.Urgent(slide(1))
	s.Urgent(slide(2)) // holding an arrow key: only the newest matters
	s.SetPrefetch([]Job{slide(10), slide(11)})
	s.SetPrefetch([]Job{slide(12)}) // cancels 10 and 11

	var got []int
	for {
		j, ok := s.next()
		if !ok {
			break
		}
		got = append(got, j.Key.Page)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 12 {
		t.Fatalf("order = %v, want [2 12]", got)
	}
}

func TestSchedulerNeverStartsOutstandingJobTwice(t *testing.T) {
	s := NewScheduler(nil, nil)
	s.Urgent(slide(1))
	if _, ok := s.next(); !ok {
		t.Fatal("expected urgent job")
	}
	// While page 1 is in flight, asking for it again must not queue a dupe.
	s.SetVisible([]Job{slide(1)})
	s.AddBackground(slide(1))
	if j, ok := s.next(); ok {
		t.Fatalf("next() = %v, want nothing while job is outstanding", j)
	}
	if !s.Outstanding(slide(1)) {
		t.Fatal("job should be outstanding")
	}
}

func TestSchedulerBackgroundDedupAndClear(t *testing.T) {
	s := NewScheduler(nil, nil)
	s.AddBackground(slide(1), slide(1), slide(2))
	if _, _, _, bg := s.QueueLens(); bg != 2 {
		t.Fatalf("background len = %d, want 2", bg)
	}
	s.ClearBackground()
	if _, ok := s.next(); ok {
		t.Fatal("ClearBackground should drop queued jobs")
	}
	s.AddBackground(slide(1)) // re-adding after clear works
	if _, _, _, bg := s.QueueLens(); bg != 1 {
		t.Fatalf("background len after re-add = %d, want 1", bg)
	}
}

func fakeLoad(calls *atomic.Int32) LoadFunc {
	return func(j Job) (*image.RGBA, func(), error) {
		calls.Add(1)
		return image.NewRGBA(image.Rect(0, 0, j.Key.W, j.Key.H)), nil, nil
	}
}

func waitDrain(t *testing.T, s *Scheduler, want int) []Result {
	t.Helper()
	var got []Result
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < want && time.Now().Before(deadline) {
		s.Drain(func(r Result) bool { got = append(got, r); return true })
		time.Sleep(time.Millisecond)
	}
	return got
}

func TestSchedulerDeliversEachResultOnce(t *testing.T) {
	var calls atomic.Int32
	s := NewScheduler(fakeLoad(&calls), nil)
	s.Start()
	defer s.Stop()

	s.Urgent(slide(1))
	s.SetVisible([]Job{slide(1), slide(2)})
	s.SetPrefetch([]Job{slide(2), slide(3)})
	got := waitDrain(t, s, 3)
	time.Sleep(20 * time.Millisecond)
	got = append(got, waitDrain(t, s, 0)...)

	seen := map[int]int{}
	for _, r := range got {
		seen[r.Key.Page]++
	}
	for p := 1; p <= 3; p++ {
		if seen[p] != 1 {
			t.Errorf("page %d delivered %d times, want 1 (all: %v)", p, seen[p], seen)
		}
	}
	if calls.Load() != 3 {
		t.Errorf("load called %d times, want 3", calls.Load())
	}
	if s.Outstanding(slide(1)) {
		t.Error("drained job should no longer be outstanding")
	}
}

func TestSchedulerSkip(t *testing.T) {
	var calls atomic.Int32
	cached := slide(7)
	s := NewScheduler(fakeLoad(&calls), func(j Job) bool { return j == cached })
	s.Start()
	defer s.Stop()

	s.SetPrefetch([]Job{cached, slide(8)})
	got := waitDrain(t, s, 1)
	if len(got) != 1 || got[0].Key.Page != 8 {
		t.Fatalf("results = %v, want only page 8", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("load called %d times, want 1", calls.Load())
	}
}

func TestSchedulerStopReleasesUndelivered(t *testing.T) {
	var mu sync.Mutex
	released := 0
	s := NewScheduler(func(j Job) (*image.RGBA, func(), error) {
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), func() { mu.Lock(); released++; mu.Unlock() }, nil
	}, nil)
	s.Start()
	s.SetPrefetch([]Job{slide(1), slide(2)})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(s.results) < 2 {
		time.Sleep(time.Millisecond)
	}
	s.Stop()
	mu.Lock()
	defer mu.Unlock()
	if released != 2 {
		t.Fatalf("released %d buffers on Stop, want 2", released)
	}
}
