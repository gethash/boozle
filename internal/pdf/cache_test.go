package pdf

import (
	"sync"
	"sync/atomic"
	"testing"
)

// fakeImg implements gpuImage for tests; counts Deallocate calls.
type fakeImg struct {
	deallocated *atomic.Int32
}

func (f *fakeImg) Deallocate() {
	if f.deallocated != nil {
		f.deallocated.Add(1)
	}
}

func newFake(counter *atomic.Int32) *fakeImg { return &fakeImg{deallocated: counter} }

// keyAt returns a CacheKey with W*H*4 == bytes.
// W=H=sqrt(bytes/4) for clean accounting in tests.
func keyAt(page, pixelsW, pixelsH int) CacheKey {
	return CacheKey{Page: page, W: pixelsW, H: pixelsH}
}

func TestCachePutGet(t *testing.T) {
	c := NewCache(64)
	k := keyAt(0, 1, 1) // 4 bytes
	img := newFake(nil)
	c.Put(k, img)

	got, ok := c.Get(k)
	if !ok || got != gpuImage(img) {
		t.Fatalf("Get after Put: got=%v ok=%v", got, ok)
	}
	if c.Len() != 1 {
		t.Errorf("Len = %d, want 1", c.Len())
	}
	if c.Bytes() != 4 {
		t.Errorf("Bytes = %d, want 4", c.Bytes())
	}
}

func TestCacheLRUEviction(t *testing.T) {
	c := NewCache(8)
	var freed atomic.Int32

	k1 := keyAt(1, 1, 1) // 4 bytes
	k2 := keyAt(2, 1, 1) // 4 bytes
	k3 := keyAt(3, 1, 1) // 4 bytes
	c.Put(k1, newFake(&freed))
	c.Put(k2, newFake(&freed))

	// Touch k1 to make k2 the LRU.
	if _, ok := c.Get(k1); !ok {
		t.Fatal("k1 missing")
	}

	// Inserting k3 should evict k2 (8 + 4 > 8).
	c.Put(k3, newFake(&freed))

	if _, ok := c.Get(k2); ok {
		t.Error("k2 should have been evicted")
	}
	if _, ok := c.Get(k1); !ok {
		t.Error("k1 should still be present")
	}
	if _, ok := c.Get(k3); !ok {
		t.Error("k3 should still be present")
	}
	if got := freed.Load(); got != 1 {
		t.Errorf("Deallocate count = %d, want 1", got)
	}
}

func TestCacheReplaceDeallocatesOld(t *testing.T) {
	c := NewCache(64)
	var freed atomic.Int32

	k := keyAt(1, 2, 2) // 16 bytes
	c.Put(k, newFake(&freed))
	c.Put(k, newFake(&freed))
	if got := freed.Load(); got != 1 {
		t.Errorf("replace: Deallocate count = %d, want 1 (old only)", got)
	}
	if got := c.Bytes(); got != 16 {
		t.Errorf("Bytes after replace = %d, want 16", got)
	}
}

func TestCacheClearDeallocatesAll(t *testing.T) {
	c := NewCache(64)
	var freed atomic.Int32
	for i := 0; i < 3; i++ {
		c.Put(keyAt(i, 1, 1), newFake(&freed))
	}
	c.Clear()
	if got := freed.Load(); got != 3 {
		t.Errorf("Clear Deallocate count = %d, want 3", got)
	}
	if c.Len() != 0 {
		t.Errorf("Len after Clear = %d, want 0", c.Len())
	}
	if c.Bytes() != 0 {
		t.Errorf("Bytes after Clear = %d, want 0", c.Bytes())
	}
}

func TestCacheByteBudgetWithDifferentImageSizes(t *testing.T) {
	c := NewCache(20)
	var freed atomic.Int32

	small := keyAt(1, 1, 1) // 4 bytes
	large := keyAt(2, 2, 2) // 16 bytes
	next := keyAt(3, 1, 1)  // 4 bytes
	c.Put(small, newFake(&freed))
	c.Put(large, newFake(&freed))
	if got := c.Bytes(); got != 20 {
		t.Fatalf("Bytes = %d, want 20", got)
	}
	c.Put(next, newFake(&freed))

	if _, ok := c.Get(small); ok {
		t.Fatal("small should be evicted to stay within byte budget")
	}
	if _, ok := c.Get(large); !ok {
		t.Fatal("large should remain cached")
	}
	if _, ok := c.Get(next); !ok {
		t.Fatal("next should remain cached")
	}
	if got := c.Bytes(); got != 20 {
		t.Fatalf("Bytes after eviction = %d, want 20", got)
	}
	if got := freed.Load(); got != 1 {
		t.Errorf("Deallocate count = %d, want 1", got)
	}
}

func TestCacheOversizedNewestItemIsRetained(t *testing.T) {
	c := NewCache(4)
	var freed atomic.Int32

	c.Put(keyAt(1, 1, 1), newFake(&freed)) // 4 bytes — fits
	huge := keyAt(2, 2, 2)                 // 16 bytes — alone exceeds budget
	c.Put(huge, newFake(&freed))

	if c.Len() != 1 {
		t.Fatalf("Len = %d, want oversized newest item retained alone", c.Len())
	}
	if _, ok := c.Get(huge); !ok {
		t.Fatal("oversized newest item should remain cached")
	}
	if got := c.Bytes(); got != 16 {
		t.Fatalf("Bytes = %d, want oversized image size 16", got)
	}
	if got := freed.Load(); got != 1 {
		t.Errorf("Deallocate count = %d, want 1", got)
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	c := NewCache(32)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := keyAt(i%4, 1, 1)
			c.Put(k, newFake(nil))
			_, _ = c.Get(k)
		}(i)
	}
	wg.Wait()
	if c.Len() == 0 || c.Bytes() > 32 {
		t.Errorf("after concurrent access Len=%d Bytes=%d, want entries and <=32 bytes", c.Len(), c.Bytes())
	}
}

func TestCachePinPreventsEviction(t *testing.T) {
	c := NewCache(8)
	var freed atomic.Int32

	k1 := keyAt(1, 1, 1) // 4 bytes
	k2 := keyAt(2, 1, 1) // 4 bytes
	c.Put(k1, newFake(&freed))
	c.Put(k2, newFake(&freed))
	c.Pin(k1) // k1 is LRU but pinned

	// Inserting k3 would normally evict k1 (oldest); pin should redirect to k2.
	k3 := keyAt(3, 1, 1)
	c.Put(k3, newFake(&freed))

	// Use Has rather than Get so we don't perturb LRU order.
	if !c.Has(k1) {
		t.Error("pinned k1 should be retained")
	}
	if c.Has(k2) {
		t.Error("k2 should have been evicted in favor of pinned k1")
	}
	if !c.Has(k3) {
		t.Error("k3 should be present")
	}
	if got := freed.Load(); got != 1 {
		t.Errorf("Deallocate count = %d, want 1", got)
	}

	// After Unpin, k1 (still LRU) becomes evictable on the next over-budget Put.
	c.Unpin(k1)
	c.Put(keyAt(4, 1, 1), newFake(&freed))
	if c.Has(k1) {
		t.Error("after Unpin, k1 should be evictable")
	}
}

func TestCacheResizeShrinksAndEvicts(t *testing.T) {
	c := NewCache(32)
	var freed atomic.Int32
	for i := 0; i < 4; i++ {
		c.Put(keyAt(i, 2, 1), newFake(&freed)) // 8 bytes each
	}
	if c.Bytes() != 32 {
		t.Fatalf("Bytes = %d, want 32", c.Bytes())
	}

	c.Resize(16) // should evict 2 oldest

	if c.Bytes() > 16 {
		t.Errorf("Bytes after Resize = %d, want <=16", c.Bytes())
	}
	if got := freed.Load(); got != 2 {
		t.Errorf("Resize Deallocate count = %d, want 2", got)
	}
}

func TestCachePurgeNotMatching(t *testing.T) {
	// Budget large enough to hold all three so the LRU doesn't pre-evict.
	c := NewCache(2048)
	var freed atomic.Int32
	small := keyAt(1, 2, 2)   // 16 bytes
	medium := keyAt(2, 4, 4)  // 64 bytes
	large := keyAt(3, 8, 8)   // 256 bytes

	c.Put(small, newFake(&freed))
	c.Put(medium, newFake(&freed))
	c.Put(large, newFake(&freed))

	c.PurgeNotMatching(4, 4) // keeps small (2×2) and medium (4×4); evicts large (8×8)

	if !c.Has(small) {
		t.Error("small (≤limit) should be retained")
	}
	if !c.Has(medium) {
		t.Error("medium (==limit) should be retained")
	}
	if c.Has(large) {
		t.Error("large (>limit) should be purged")
	}
	if got := freed.Load(); got != 1 {
		t.Errorf("Purge Deallocate count = %d, want 1", got)
	}
}

func TestPrefetchRequestDeduplicatesPendingKeys(t *testing.T) {
	c := NewCache(32)
	p := NewPrefetcher(nil, c, 4)
	k := keyAt(1, 100, 100)

	p.Request(k)
	p.Request(k)
	p.Request(k)

	if got := p.PendingLen(); got != 1 {
		t.Fatalf("PendingLen = %d, want 1", got)
	}
}
