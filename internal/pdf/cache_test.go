package pdf

import (
	"image"
	"sync"
	"sync/atomic"
	"testing"
)

func makeRGBA() *image.RGBA { return image.NewRGBA(image.Rect(0, 0, 1, 1)) }

func TestCachePutGet(t *testing.T) {
	c := NewCache(3)
	k := CacheKey{Page: 0, W: 100, H: 100}
	img := makeRGBA()
	c.Put(k, img, nil)

	got, ok := c.Get(k)
	if !ok || got != img {
		t.Fatalf("Get after Put: got=%v ok=%v", got, ok)
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1", c.Len())
	}
}

func TestCacheLRUEviction(t *testing.T) {
	c := NewCache(2)
	var freed int32
	cleanup := func() { atomic.AddInt32(&freed, 1) }

	k1 := CacheKey{Page: 1}
	k2 := CacheKey{Page: 2}
	k3 := CacheKey{Page: 3}
	c.Put(k1, makeRGBA(), cleanup)
	c.Put(k2, makeRGBA(), cleanup)

	// Touch k1 to make k2 the LRU.
	if _, ok := c.Get(k1); !ok {
		t.Fatal("k1 missing")
	}

	// Inserting k3 should evict k2.
	c.Put(k3, makeRGBA(), cleanup)

	if _, ok := c.Get(k2); ok {
		t.Error("k2 should have been evicted")
	}
	if _, ok := c.Get(k1); !ok {
		t.Error("k1 should still be present")
	}
	if _, ok := c.Get(k3); !ok {
		t.Error("k3 should still be present")
	}
	if got := atomic.LoadInt32(&freed); got != 1 {
		t.Errorf("cleanup count = %d, want 1", got)
	}
}

func TestCacheReplaceRunsCleanup(t *testing.T) {
	c := NewCache(2)
	var freed int32
	cleanup := func() { atomic.AddInt32(&freed, 1) }

	k := CacheKey{Page: 1, W: 100, H: 100}
	c.Put(k, makeRGBA(), cleanup)
	c.Put(k, makeRGBA(), cleanup)
	if got := atomic.LoadInt32(&freed); got != 1 {
		t.Errorf("replacing entry: cleanup count = %d, want 1 (old entry only)", got)
	}
}

func TestCacheClearRunsAllCleanups(t *testing.T) {
	c := NewCache(4)
	var freed int32
	cleanup := func() { atomic.AddInt32(&freed, 1) }
	for i := 0; i < 3; i++ {
		c.Put(CacheKey{Page: i}, makeRGBA(), cleanup)
	}
	c.Clear()
	if got := atomic.LoadInt32(&freed); got != 3 {
		t.Errorf("Clear cleanup count = %d, want 3", got)
	}
	if c.Len() != 0 {
		t.Errorf("Len after Clear = %d, want 0", c.Len())
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	c := NewCache(8)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := CacheKey{Page: i % 4}
			c.Put(k, makeRGBA(), nil)
			_, _ = c.Get(k)
		}(i)
	}
	wg.Wait()
	if c.Len() == 0 || c.Len() > 8 {
		t.Errorf("Len after concurrent access = %d, want 1..8", c.Len())
	}
}
