package app

import (
	"image"
	"math"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/gethash/boozle/internal/pdf"
)

// uploadBytesPerFrame bounds how many rendered pixel bytes are uploaded to
// the GPU per frame: roughly one 4K slide, or a screenful of thumbnails.
// At least one result is always uploaded so large slides still make
// progress.
const uploadBytesPerFrame = 24 << 20

// pipeline turns PDF pages into GPU images for one window. It owns the
// slide cache, the overview thumbnail store and the render scheduler that
// feeds both. All methods except load/has run on the Ebiten goroutine.
type pipeline struct {
	doc    *pdf.Doc
	slides *pdf.Cache
	thumbs *overviewThumbStore
	sched  *pdf.Scheduler

	// onThumb is called for every freshly uploaded thumbnail (set while an
	// overview is open).
	onThumb func(pdf.CacheKey, *ebiten.Image)
}

func newPipeline(doc *pdf.Doc, pdfPath string, cacheMB int) *pipeline {
	p := &pipeline{
		doc:    doc,
		slides: pdf.NewCache(cacheBudgetInitial),
		thumbs: newOverviewThumbStore(pdfPath),
	}
	if cacheMB > 0 {
		p.slides.Resize(cacheMB << 20)
	}
	p.sched = pdf.NewScheduler(p.load, p.has)
	p.sched.Start()
	return p
}

func (p *pipeline) close() {
	p.sched.Stop()
	p.slides.Clear()
	p.thumbs.Clear()
}

// has reports whether a job's output is already cached. Scheduler goroutine.
func (p *pipeline) has(j pdf.Job) bool {
	if j.Kind == pdf.KindThumb {
		return p.thumbs.Has(j.Key)
	}
	return p.slides.Has(j.Key)
}

// load renders a job. Scheduler goroutine.
func (p *pipeline) load(j pdf.Job) (*image.RGBA, func(), error) {
	if j.Kind == pdf.KindThumb {
		if rgba, ok := p.thumbs.LoadDisk(j.Key); ok {
			return rgba, nil, nil
		}
		rgba, release, err := p.doc.RenderPage(j.Key.Page, j.Key.W, j.Key.H)
		if err == nil {
			p.thumbs.SaveDisk(j.Key, rgba)
		}
		return rgba, release, err
	}
	return p.doc.RenderPage(j.Key.Page, j.Key.W, j.Key.H)
}

// drain uploads finished renders into their caches and reports whether
// anything arrived.
func (p *pipeline) drain() bool {
	budget := uploadBytesPerFrame
	n := p.sched.Drain(func(r pdf.Result) bool {
		if r.Err == nil && r.RGBA != nil {
			// Slides get their own textures so eviction really frees GPU
			// memory; thumbnails share atlases so a grid of hundreds of
			// them draws in a few batches.
			img := uploadRGBA(r.RGBA, r.Kind == pdf.KindSlide)
			budget -= len(r.RGBA.Pix)
			if r.Kind == pdf.KindThumb {
				p.thumbs.Put(r.Key, img)
				if p.onThumb != nil {
					p.onThumb(r.Key, img)
				}
			} else {
				p.slides.Put(r.Key, img)
			}
		}
		if r.Release != nil {
			r.Release()
		}
		return budget > 0
	})
	return n > 0
}

// fit returns the cache key for page aspect-fitted into a boxW×boxH pixel
// box, plus the offset that centres it in the box.
func (p *pipeline) fit(page, boxW, boxH int) (key pdf.CacheKey, offX, offY int, ok bool) {
	size, err := p.doc.PageSize(page)
	if err != nil {
		return pdf.CacheKey{}, 0, 0, false
	}
	w, h, offX, offY := aspectFit(size.WidthPoints, size.HeightPoints, boxW, boxH)
	if w <= 0 || h <= 0 {
		return pdf.CacheKey{}, 0, 0, false
	}
	return pdf.CacheKey{Page: page, W: w, H: h}, offX, offY, true
}

// resolve returns the cached slide for want or, failing that, the best
// stand-in: the same page at another size — the smallest one at least as
// large (downscaled crisply), else the largest smaller one (upscaled until
// the exact render lands). img is nil when the page isn't cached at all.
func (p *pipeline) resolve(want pdf.CacheKey) (img *ebiten.Image, key pdf.CacheKey) {
	if cached, ok := p.slides.Get(want); ok {
		return cached.(*ebiten.Image), want
	}
	var bigger, smaller pdf.CacheKey
	p.slides.Range(func(k pdf.CacheKey) {
		if k.Page != want.Page {
			return
		}
		if k.W >= want.W && k.H >= want.H {
			if bigger.W == 0 || k.W*k.H < bigger.W*bigger.H {
				bigger = k
			}
		} else if smaller.W == 0 || k.W*k.H > smaller.W*smaller.H {
			smaller = k
		}
	})
	for _, k := range []pdf.CacheKey{bigger, smaller} {
		if k.W == 0 {
			continue
		}
		if cached, ok := p.slides.Get(k); ok {
			return cached.(*ebiten.Image), k
		}
	}
	return nil, pdf.CacheKey{}
}

// missing returns the slide jobs for keys that are not cached yet.
func (p *pipeline) missing(keys ...pdf.CacheKey) []pdf.Job {
	var jobs []pdf.Job
	for _, k := range keys {
		if k.W > 0 && !p.slides.Has(k) {
			jobs = append(jobs, pdf.Job{Kind: pdf.KindSlide, Key: k})
		}
	}
	return jobs
}

// requestNow asks for key ahead of everything else unless it is cached.
func (p *pipeline) requestNow(key pdf.CacheKey) {
	if key.W > 0 && !p.slides.Has(key) {
		p.sched.Urgent(pdf.Job{Kind: pdf.KindSlide, Key: key})
	}
}

// resized applies a new slide-cache budget (unless --cache-mb fixed it)
// after the window's pixel buffer changed, and drops every render that
// isn't on screen: all cached sizes derive from the old buffer. On-screen
// entries are pinned and stand in until the new renders land.
func (p *pipeline) resized(budget, cacheMB int) {
	if cacheMB <= 0 {
		p.slides.Resize(budget)
	}
	p.slides.PurgeExcept(func(pdf.CacheKey) bool { return false })
}

// prefetchAhead returns how many slides ahead to prefetch so that the
// working set — current slide, one behind, the ones ahead — fits the
// slide cache. Prefetching more than fits would evict renders before they
// are shown.
func (p *pipeline) prefetchAhead(slideBytes int) int {
	if slideBytes <= 0 {
		return 1
	}
	return min(3, max(1, p.slides.Budget()/slideBytes-2))
}

// pinnedSlide is a cache-owned image held on screen. The cache entry stays
// pinned (never evicted) for as long as it is displayed.
type pinnedSlide struct {
	img   *ebiten.Image
	key   pdf.CacheKey
	cache *pdf.Cache
}

func (s *pinnedSlide) set(cache *pdf.Cache, img *ebiten.Image, key pdf.CacheKey) {
	if s.img == img && s.key == key {
		return
	}
	s.release()
	s.img, s.key, s.cache = img, key, cache
	if cache != nil && img != nil {
		cache.Pin(key)
	}
}

func (s *pinnedSlide) release() {
	if s.cache != nil && s.img != nil {
		s.cache.Unpin(s.key)
	}
	*s = pinnedSlide{}
}

// uploadRGBA creates a GPU image at rgba's bounds and uploads the pixels via
// WritePixels (which copies synchronously, so rgba may be recycled after).
//
// Unmanaged images get a dedicated texture that Deallocate frees at once.
// Managed images are packed into shared atlases, which are only released
// when completely empty — fine for small images, but a pinned full-screen
// slide could keep a whole 4096² atlas (64 MB) alive.
func uploadRGBA(rgba *image.RGBA, unmanaged bool) *ebiten.Image {
	b := rgba.Bounds()
	eimg := ebiten.NewImageWithOptions(image.Rect(0, 0, b.Dx(), b.Dy()), &ebiten.NewImageOptions{Unmanaged: unmanaged})
	eimg.WritePixels(rgba.Pix)
	return eimg
}

// pixelBuffer converts Ebiten's device-independent outside size into the
// physical-pixel buffer we render into, optionally shrunk by --render-scale.
func pixelBuffer(outsideW, outsideH int, renderScale float64) (int, int) {
	sf := 1.0
	if m := ebiten.Monitor(); m != nil {
		if s := m.DeviceScaleFactor(); s > 0 {
			sf = s
		}
	}
	pxW := int(math.Round(float64(outsideW) * sf))
	pxH := int(math.Round(float64(outsideH) * sf))
	if renderScale > 0 && renderScale < 1 {
		pxW = max(1, int(math.Round(float64(pxW)*renderScale)))
		pxH = max(1, int(math.Round(float64(pxH)*renderScale)))
	}
	return pxW, pxH
}

// aspectFit returns the largest (w, h) that fits inside (dstW, dstH) while
// preserving srcW:srcH, plus the centering offset.
func aspectFit(srcW, srcH float64, dstW, dstH int) (w, h, offX, offY int) {
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return 0, 0, 0, 0
	}
	s := math.Min(float64(dstW)/srcW, float64(dstH)/srcH)
	w = min(dstW, int(math.Round(srcW*s)))
	h = min(dstH, int(math.Round(srcH*s)))
	offX = (dstW - w) / 2
	offY = (dstH - h) / 2
	return
}
