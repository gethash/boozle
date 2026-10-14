package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/gethash/boozle/internal/config"
	"github.com/gethash/boozle/internal/pdf"
	"github.com/gethash/boozle/internal/pdf/pdftest"
	"github.com/gethash/boozle/internal/timer"
)

// newTestPipeline opens a generated deck through the real PDFium pipeline.
func newTestPipeline(t *testing.T, pages int) *pipeline {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deck.pdf")
	var spec []pdftest.Page
	for i := range pages {
		w, h := 960.0, 540.0
		if i%4 == 3 {
			w, h = 540, 960 // mix in portrait pages
		}
		spec = append(spec, pdftest.Page{W: w, H: h, R: float64(i) / float64(pages)})
	}
	if err := pdftest.Write(path, spec); err != nil {
		t.Fatal(err)
	}
	doc, err := pdf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// After Open, so the compiled PDFium module still comes from the shared
	// cache (compiling under -race takes ~10s) while thumbnails stay
	// hermetic.
	t.Setenv("BOOZLE_CACHE_DIR", t.TempDir())
	p := newPipeline(doc, path, 0)
	t.Cleanup(func() {
		p.close()
		_ = doc.Close()
	})
	return p
}

// TestAutoAdvanceShowsEveryPage drives the audience Game through a looping
// auto-advance with fade transitions and checks that every page ends up on
// screen at its exact size — i.e. async renders, stand-ins and transition
// gating never stall navigation.
func TestAutoAdvanceShowsEveryPage(t *testing.T) {
	testAutoAdvanceShowsEveryPage(t, 0)
}

// TestTinyCacheStillShowsEveryPage guards against a fresh render being
// evicted by its own insertion when the pinned on-screen slide already
// fills the budget (e.g. a small --cache-mb on a 4K display).
func TestTinyCacheStillShowsEveryPage(t *testing.T) {
	testAutoAdvanceShowsEveryPage(t, 1)
}

func testAutoAdvanceShowsEveryPage(t *testing.T, cacheMB int) {
	const pages = 6
	p := newTestPipeline(t, pages)
	if cacheMB > 0 {
		p.slides.Resize(cacheMB << 20)
	}
	auto := timer.New(40*time.Millisecond, nil)
	auto.Reset(1)
	g := &Game{
		cfg:      config.Config{Loop: true, CacheMB: cacheMB},
		pipe:     p,
		auto:     auto,
		pageList: []int{0, 1, 2, 3, 4, 5},
	}
	g.trans.style = transFade
	g.trans.frames = 4
	g.Layout(640, 360)

	shown := map[int]bool{}
	deadline := time.Now().Add(20 * time.Second)
	for len(shown) < pages && time.Now().Before(deadline) {
		if err := g.Update(); err != nil {
			t.Fatal(err)
		}
		if g.disp.img != nil && g.disp.key == g.dispWanted {
			shown[g.disp.key.Page] = true
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(shown) != pages {
		t.Fatalf("pages shown at exact size = %v, want all %d", shown, pages)
	}
}

func TestResolvePrefersSamePageStandIn(t *testing.T) {
	p := newTestPipeline(t, 2)
	small, _, _, _ := p.fit(0, 320, 180)
	big, _, _, _ := p.fit(0, 1280, 720)
	other, _, _, _ := p.fit(1, 640, 360)

	p.sched.SetPrefetch(p.missing(small, other))
	deadline := time.Now().Add(10 * time.Second)
	for (!p.slides.Has(small) || !p.slides.Has(other)) && time.Now().Before(deadline) {
		p.drain()
		time.Sleep(time.Millisecond)
	}

	img, key := p.resolve(big)
	if img == nil || key != small {
		t.Fatalf("resolve(%v) = %v, want stand-in %v", big, key, small)
	}
	if img, _ := p.resolve(pdf.CacheKey{Page: 1, W: 9, H: 9}); img == nil {
		t.Fatal("a larger render of the page should stand in for a smaller request")
	}
}

// TestOverviewLoadsThumbnailsAndDraws opens the overview, lets the scheduler
// deliver every thumbnail, moves the selection (debounced preview render)
// and exercises both the per-tile and the cached-grid draw paths.
func TestOverviewLoadsThumbnailsAndDraws(t *testing.T) {
	const pages = 9
	p := newTestPipeline(t, pages)
	g := &Game{
		pipe:     p,
		auto:     timer.New(0, nil),
		pageList: []int{0, 1, 2, 3, 4, 5, 6, 7, 8},
	}
	g.Layout(800, 450)
	screen := ebiten.NewImage(g.bufW, g.bufH)
	defer screen.Deallocate()

	g.openOverview()
	if g.ov.store == nil {
		t.Fatal("single-window overview should load thumbnails")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := g.Update(); err != nil {
			t.Fatal(err)
		}
		g.Draw(screen)
		loaded := 0
		for _, th := range g.ov.thumbs {
			if th != nil {
				loaded++
			}
		}
		if loaded == pages && g.ov.phase == ovActive {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	for i, th := range g.ov.thumbs {
		if th == nil {
			t.Fatalf("thumbnail %d never arrived", i)
		}
	}

	// Select another slide and wait for its preview-size render.
	g.ov.selIdx = 4
	want, ok := g.ov.previewKey(p, 4)
	if !ok {
		t.Fatal("previewKey failed")
	}
	deadline = time.Now().Add(10 * time.Second)
	for !p.slides.Has(want) && time.Now().Before(deadline) {
		if err := g.Update(); err != nil {
			t.Fatal(err)
		}
		g.Draw(screen)
		time.Sleep(5 * time.Millisecond)
	}
	if !p.slides.Has(want) {
		t.Fatal("debounced preview render never arrived")
	}

	g.closeOverview(4)
	for i := 0; i < ovExitFrames+2; i++ {
		if err := g.Update(); err != nil {
			t.Fatal(err)
		}
		g.Draw(screen)
	}
	if g.ov.phase != ovOff || g.listIdx != 4 || g.prevListIdx != 0 {
		t.Fatalf("after exit: phase=%v listIdx=%d prev=%d, want off/4/0", g.ov.phase, g.listIdx, g.prevListIdx)
	}
	if p.onThumb != nil {
		t.Fatal("closing the overview should detach thumbnail delivery")
	}
}
