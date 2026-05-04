package pdf

import (
	"image"
	"sync"
)

// UploadJob carries a freshly-rendered page from the prefetch worker to the
// main goroutine, which uploads the pixels to a GPU image and inserts the
// resulting *ebiten.Image into the cache. After uploading, the consumer
// must invoke Cleanup (which releases the PDFium WASM-side bitmap) and
// Done (which removes the key from the prefetcher's pending set).
type UploadJob struct {
	Key     CacheKey
	RGBA    *image.RGBA
	Cleanup func() // PDFium WASM cleanup; call once after WritePixels
	Done    func() // prefetcher bookkeeping; call after cache.Put
}

// Prefetcher renders pages on a background goroutine and emits UploadJobs
// on Uploads(). The main goroutine drains that channel each frame so all
// GPU operations happen on the Ebiten thread.
type Prefetcher struct {
	doc      *Doc
	cache    *Cache
	requests chan CacheKey
	uploads  chan UploadJob
	stop     chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	pending  map[CacheKey]struct{}
}

// NewPrefetcher returns a Prefetcher that renders pages off the main thread.
// queueSize bounds both the request channel and the outgoing uploads
// channel; further non-blocking Requests drop on the floor when full.
func NewPrefetcher(doc *Doc, cache *Cache, queueSize int) *Prefetcher {
	if queueSize < 1 {
		queueSize = 1
	}
	return &Prefetcher{
		doc:      doc,
		cache:    cache,
		requests: make(chan CacheKey, queueSize),
		uploads:  make(chan UploadJob, queueSize),
		stop:     make(chan struct{}),
		pending:  make(map[CacheKey]struct{}, queueSize),
	}
}

// Start launches the background worker.
func (p *Prefetcher) Start() {
	p.wg.Add(1)
	go p.worker()
}

// Stop signals the worker to exit and blocks until it does. Drains any
// remaining UploadJobs on the channel and runs their Cleanup so PDFium
// memory isn't leaked.
func (p *Prefetcher) Stop() {
	close(p.stop)
	p.wg.Wait()
	for {
		select {
		case job := <-p.uploads:
			if job.Cleanup != nil {
				job.Cleanup()
			}
		default:
			return
		}
	}
}

// Uploads returns the channel of completed render jobs awaiting GPU upload.
// The main goroutine reads this each frame.
func (p *Prefetcher) Uploads() <-chan UploadJob {
	return p.uploads
}

// Request asks the worker to render and cache key k. Non-blocking:
// if the queue is full or the entry is already cached/in-flight, drops.
func (p *Prefetcher) Request(k CacheKey) {
	if p.cache.Has(k) {
		return
	}
	p.mu.Lock()
	if _, ok := p.pending[k]; ok {
		p.mu.Unlock()
		return
	}
	select {
	case p.requests <- k:
		p.pending[k] = struct{}{}
		p.mu.Unlock()
	default:
		p.mu.Unlock()
	}
}

// PendingLen returns the number of queued or in-flight requests.
func (p *Prefetcher) PendingLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending)
}

func (p *Prefetcher) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stop:
			return
		case k := <-p.requests:
			if p.cache.Has(k) {
				p.markDone(k)
				continue
			}
			img, cleanup, err := p.doc.RenderPage(k.Page, k.W, k.H)
			if err != nil {
				// Errors are benign: the main thread re-attempts synchronously
				// when the user navigates to this page.
				p.markDone(k)
				continue
			}
			job := UploadJob{
				Key:     k,
				RGBA:    img,
				Cleanup: cleanup,
				Done:    func() { p.markDone(k) },
			}
			select {
			case p.uploads <- job:
				// The main goroutine will call Done() after cache.Put.
			case <-p.stop:
				if cleanup != nil {
					cleanup()
				}
				p.markDone(k)
				return
			}
		}
	}
}

func (p *Prefetcher) markDone(k CacheKey) {
	p.mu.Lock()
	delete(p.pending, k)
	p.mu.Unlock()
}
