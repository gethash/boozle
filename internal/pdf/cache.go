package pdf

import (
	"container/list"
	"sync"
)

// CacheKey identifies a rasterized page at a specific pixel size.
// Different (W, H) values are different cache entries — when the window
// resizes or the active monitor's scale factor changes, the old entries
// are stale and naturally fall out via the LRU.
type CacheKey struct {
	Page int
	W, H int
}

// gpuImage is the minimal interface the cache needs from a renderable
// image. Production passes *ebiten.Image; tests pass a fake.
type gpuImage interface {
	Deallocate()
}

type cacheItem struct {
	img      gpuImage
	bytes    int
	pinCount int
	el       *list.Element
}

// Cache is a thread-safe LRU of rasterized PDF pages. Entries hold
// already-uploaded GPU images so a cache hit is a pointer swap — no
// CPU→GPU re-upload on the main goroutine. Eviction calls Deallocate()
// on the evicted image.
type Cache struct {
	mu       sync.Mutex
	maxBytes int
	used     int
	items    map[CacheKey]*cacheItem
	order    *list.List // Front = least-recently-used, Back = most-recently-used
}

// NewCache returns a cache that keeps rasterized pages within maxBytes.
// If one image is larger than maxBytes, the cache retains that newest image
// alone so the active slide is not immediately discarded.
func NewCache(maxBytes int) *Cache {
	if maxBytes < 1 {
		maxBytes = 1
	}
	return &Cache{
		maxBytes: maxBytes,
		items:    make(map[CacheKey]*cacheItem),
		order:    list.New(),
	}
}

// Get returns the cached image for k and marks it most-recently-used.
// Returns (nil, false) on miss. The returned image is owned by the cache
// — callers must not call Deallocate on it.
func (c *Cache) Get(k CacheKey) (gpuImage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[k]
	if !ok {
		return nil, false
	}
	c.order.MoveToBack(it.el)
	return it.img, true
}

// Has reports whether k is cached, without updating LRU order.
func (c *Cache) Has(k CacheKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.items[k]
	return ok
}

// Put inserts img at k as most-recently-used. If capacity is exceeded,
// LRU entries are evicted and their images are Deallocate()d. If k already
// exists, the previous entry's image is Deallocate()d before replacement.
// Pinned entries (see Pin) are never evicted.
func (c *Cache) Put(k CacheKey, img gpuImage) {
	c.mu.Lock()
	evicted := []gpuImage{}
	if existing, ok := c.items[k]; ok {
		evicted = append(evicted, existing.img)
		c.used -= existing.bytes
		c.order.Remove(existing.el)
		delete(c.items, k)
	}
	bytes := keyBytes(k)
	it := &cacheItem{img: img, bytes: bytes}
	it.el = c.order.PushBack(k)
	c.items[k] = it
	c.used += bytes
	c.evictLocked(&evicted)
	c.mu.Unlock()
	for _, im := range evicted {
		if im != nil {
			im.Deallocate()
		}
	}
}

// Pin marks the entry as protected from eviction. Multiple calls increment
// a refcount; Unpin must be called the same number of times to release.
// Pinning a missing key is a no-op.
func (c *Cache) Pin(k CacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if it, ok := c.items[k]; ok {
		it.pinCount++
	}
}

// Unpin decrements the pin refcount (clamped at 0). Missing key is a no-op.
func (c *Cache) Unpin(k CacheKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if it, ok := c.items[k]; ok && it.pinCount > 0 {
		it.pinCount--
	}
}

// Resize changes the cache budget and evicts as needed.
func (c *Cache) Resize(maxBytes int) {
	if maxBytes < 1 {
		maxBytes = 1
	}
	c.mu.Lock()
	c.maxBytes = maxBytes
	var evicted []gpuImage
	c.evictLocked(&evicted)
	c.mu.Unlock()
	for _, im := range evicted {
		if im != nil {
			im.Deallocate()
		}
	}
}

// PurgeNotMatching evicts entries whose dimensions exceed (W, H). Entries
// with smaller-or-equal dimensions are kept — they may still be useful as
// scaled-down placeholders during a resize. Pinned entries are kept.
func (c *Cache) PurgeNotMatching(W, H int) {
	c.mu.Lock()
	var evicted []gpuImage
	for el := c.order.Front(); el != nil; {
		k := el.Value.(CacheKey)
		next := el.Next()
		it := c.items[k]
		if it.pinCount == 0 && (k.W > W || k.H > H) {
			evicted = append(evicted, it.img)
			c.used -= it.bytes
			c.order.Remove(el)
			delete(c.items, k)
		}
		el = next
	}
	c.mu.Unlock()
	for _, im := range evicted {
		if im != nil {
			im.Deallocate()
		}
	}
}

// Range invokes f for each cached key in LRU→MRU order. f must not call
// other cache methods (re-entrancy will deadlock). Useful for mipmap-style
// scans.
func (c *Cache) Range(f func(k CacheKey)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for el := c.order.Front(); el != nil; el = el.Next() {
		f(el.Value.(CacheKey))
	}
}

// Clear evicts all entries (including pinned ones) and Deallocate()s their images.
func (c *Cache) Clear() {
	c.mu.Lock()
	evicted := make([]gpuImage, 0, len(c.items))
	for _, it := range c.items {
		evicted = append(evicted, it.img)
	}
	c.items = make(map[CacheKey]*cacheItem)
	c.order = list.New()
	c.used = 0
	c.mu.Unlock()
	for _, im := range evicted {
		if im != nil {
			im.Deallocate()
		}
	}
}

// Len returns the current number of entries.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Bytes returns the total bytes accounted to the cache (W*H*4 per entry).
func (c *Cache) Bytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// evictLocked drops entries from the LRU front until used <= maxBytes,
// skipping pinned entries and always retaining at least one entry. Callers
// must hold c.mu.
func (c *Cache) evictLocked(evicted *[]gpuImage) {
	for c.used > c.maxBytes && c.order.Len() > 1 {
		var victim *list.Element
		for el := c.order.Front(); el != nil; el = el.Next() {
			k := el.Value.(CacheKey)
			if c.items[k].pinCount == 0 {
				victim = el
				break
			}
		}
		if victim == nil {
			return // every remaining entry is pinned
		}
		k := victim.Value.(CacheKey)
		it := c.items[k]
		*evicted = append(*evicted, it.img)
		c.used -= it.bytes
		c.order.Remove(victim)
		delete(c.items, k)
	}
}

func keyBytes(k CacheKey) int {
	b := k.W * k.H * 4
	if b < 1 {
		return 1
	}
	return b
}
