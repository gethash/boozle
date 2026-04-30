package pdf

import (
	"image"
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

type cacheItem struct {
	img     *image.RGBA
	cleanup func()
}

// Cache is a thread-safe LRU of rasterized PDF pages plus their
// PDFium WASM cleanup callbacks.
type Cache struct {
	mu    sync.Mutex
	cap   int
	items map[CacheKey]*cacheItem
	order []CacheKey // front (index 0) is least-recently-used
}

// NewCache returns a cache that holds at most capacity entries.
func NewCache(capacity int) *Cache {
	if capacity < 1 {
		capacity = 1
	}
	return &Cache{
		cap:   capacity,
		items: make(map[CacheKey]*cacheItem, capacity),
	}
}

// Get returns the cached image for k and marks it most-recently-used.
// Returns (nil, false) on miss.
func (c *Cache) Get(k CacheKey) (*image.RGBA, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[k]
	if !ok {
		return nil, false
	}
	c.touch(k)
	return it.img, true
}

// Has reports whether k is cached, without updating LRU order.
func (c *Cache) Has(k CacheKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.items[k]
	return ok
}

// Put inserts (img, cleanup) at k as most-recently-used. If capacity is
// exceeded, the LRU entry is evicted and its cleanup is invoked. If k
// already exists, the previous entry's cleanup runs and the new entry replaces it.
func (c *Cache) Put(k CacheKey, img *image.RGBA, cleanup func()) {
	c.mu.Lock()
	evicted := []*cacheItem{}
	if existing, ok := c.items[k]; ok {
		evicted = append(evicted, existing)
		c.removeFromOrder(k)
	}
	c.items[k] = &cacheItem{img: img, cleanup: cleanup}
	c.order = append(c.order, k)
	for len(c.order) > c.cap {
		oldest := c.order[0]
		c.order = c.order[1:]
		if it, ok := c.items[oldest]; ok {
			evicted = append(evicted, it)
			delete(c.items, oldest)
		}
	}
	c.mu.Unlock()
	for _, it := range evicted {
		if it.cleanup != nil {
			it.cleanup()
		}
	}
}

// Clear evicts all entries and runs their cleanups.
func (c *Cache) Clear() {
	c.mu.Lock()
	evicted := make([]*cacheItem, 0, len(c.items))
	for _, it := range c.items {
		evicted = append(evicted, it)
	}
	c.items = make(map[CacheKey]*cacheItem, c.cap)
	c.order = c.order[:0]
	c.mu.Unlock()
	for _, it := range evicted {
		if it.cleanup != nil {
			it.cleanup()
		}
	}
}

// Len returns the current number of entries.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// touch must be called with mu held.
func (c *Cache) touch(k CacheKey) {
	c.removeFromOrder(k)
	c.order = append(c.order, k)
}

// removeFromOrder must be called with mu held.
func (c *Cache) removeFromOrder(k CacheKey) {
	for i, x := range c.order {
		if x == k {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}
