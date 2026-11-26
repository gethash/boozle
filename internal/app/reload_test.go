package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gethash/boozle/internal/config"
	"github.com/gethash/boozle/internal/pdf"
	"github.com/gethash/boozle/internal/pdf/pdftest"
	"github.com/gethash/boozle/internal/timer"
)

// writeDeck writes an n-page landscape PDF; shade tells versions apart.
func writeDeck(t *testing.T, path string, n int, shade float64) {
	t.Helper()
	var spec []pdftest.Page
	for i := range n {
		spec = append(spec, pdftest.Page{W: 960, H: 540, R: shade, G: float64(i) / float64(n)})
	}
	if err := pdftest.Write(path, spec); err != nil {
		t.Fatal(err)
	}
}

// newReloadGame opens an n-page deck for an audience Game on listIdx, with
// reloading wired up but no window. loadConfig may be nil.
func newReloadGame(t *testing.T, n, listIdx int, loadConfig func() (config.Config, error)) (*Game, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deck.pdf")
	writeDeck(t, path, n, 0)
	// Warm the shared compiled-module cache before going hermetic (see
	// newTestPipeline).
	warm, err := pdf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = warm.Close()
	t.Setenv("BOOZLE_CACHE_DIR", t.TempDir())

	d, err := openDeck(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{PDFPath: path, PageRange: config.PageRange{All: true}, Transition: "none"}
	g := &Game{
		cfg:      cfg,
		doc:      d.doc,
		pipe:     d.pipe,
		auto:     timer.New(0, nil),
		pageList: buildPageList(n, cfg),
		listIdx:  listIdx,
		reload:   newReloader(path, 0, loadConfig),
	}
	t.Cleanup(func() {
		g.reload.close()
		(&deck{doc: g.doc, pipe: g.pipe}).close()
	})
	g.Layout(640, 360)
	return g, path
}

// updateUntil runs frames until done reports true, failing after a while.
// check, if set, runs after every frame.
func updateUntil(t *testing.T, g *Game, what string, done func() bool, check func()) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		if err := g.Update(); err != nil {
			t.Fatal(err)
		}
		if check != nil {
			check()
		}
		time.Sleep(time.Millisecond)
	}
}

func (g *Game) showsExactCurrent() bool {
	return g.displayIsCurrent() && g.disp.key == g.dispWanted
}

func TestReloadSwapsDeckWithoutGapAndClampsPage(t *testing.T) {
	g, path := newReloadGame(t, 5, 4, nil)
	updateUntil(t, g, "first slide", g.showsExactCurrent, nil)
	oldDoc, oldSlides := g.doc, g.pipe.slides

	writeDeck(t, path, 3, 1)
	g.reload.request()
	updateUntil(t, g, "reloaded slide", func() bool {
		return g.doc != oldDoc && g.showsExactCurrent() && len(g.reload.retired) == 0
	}, func() {
		if g.disp.img == nil {
			t.Fatal("nothing on screen during the reload")
		}
	})

	if g.doc.PageCount() != 3 || !reflect.DeepEqual(g.pageList, []int{0, 1, 2}) {
		t.Fatalf("after reload: %d pages, play list %v", g.doc.PageCount(), g.pageList)
	}
	if g.listIdx != 2 {
		t.Fatalf("listIdx = %d, want 2 (page 5 is gone, so the last page)", g.listIdx)
	}
	if g.generation != 1 {
		t.Fatalf("generation = %d, want 1", g.generation)
	}
	if oldSlides.Len() != 0 {
		t.Fatalf("retired slide cache still holds %d slides", oldSlides.Len())
	}
	if _, _, err := oldDoc.RenderPage(0, 10, 10); err == nil {
		t.Fatal("old document should be closed")
	}
}

func TestReloadKeepsPageWhenPagesAreAppended(t *testing.T) {
	g, path := newReloadGame(t, 3, 1, nil)
	updateUntil(t, g, "first slide", g.showsExactCurrent, nil)
	oldDoc := g.doc

	writeDeck(t, path, 6, 1)
	g.reload.request()
	updateUntil(t, g, "reload", func() bool { return g.doc != oldDoc && g.showsExactCurrent() }, nil)
	if g.listIdx != 1 || g.currentPage() != 1 || len(g.pageList) != 6 {
		t.Fatalf("listIdx=%d page=%d list=%v, want page 2 of 6", g.listIdx, g.currentPage(), g.pageList)
	}
}

func TestReloadOfBrokenFileKeepsCurrentDeck(t *testing.T) {
	g, path := newReloadGame(t, 3, 1, nil)
	updateUntil(t, g, "first slide", g.showsExactCurrent, nil)
	oldDoc, oldImg := g.doc, g.disp.img

	if err := os.WriteFile(path, []byte("%PDF-1.7\nhalf-written"), 0o644); err != nil {
		t.Fatal(err)
	}
	g.reload.request()
	updateUntil(t, g, "failed reload", func() bool { return !g.reload.busy }, nil)
	if err := g.Update(); err != nil {
		t.Fatal(err)
	}
	if g.doc != oldDoc || g.disp.img != oldImg || g.generation != 0 {
		t.Fatal("a broken file must leave the current deck on screen")
	}
}

func TestReloadAppliesSidecarChanges(t *testing.T) {
	next := config.Config{
		PageRange:  config.PageRange{Pages: []int{1, 3}},
		Notes:      map[int]string{3: "new note"},
		Loop:       true,
		Transition: "fade",
		Monitor:    "7", // not reloadable
	}
	g, _ := newReloadGame(t, 4, 2, func() (config.Config, error) { return next, nil })
	updateUntil(t, g, "first slide", g.showsExactCurrent, nil)
	oldDoc := g.doc

	g.reload.request()
	updateUntil(t, g, "reload", func() bool { return g.doc != oldDoc }, nil)
	if !reflect.DeepEqual(g.pageList, []int{0, 2}) || g.listIdx != 1 {
		t.Fatalf("play list %v at %d, want [0 2] at 1 (page 3 kept)", g.pageList, g.listIdx)
	}
	if !g.cfg.Loop || g.cfg.Notes[3] != "new note" || g.trans.style != transFade {
		t.Fatalf("reloadable settings not applied: %+v", g.cfg)
	}
	if g.cfg.Monitor != "" {
		t.Fatalf("monitor changed on reload to %q", g.cfg.Monitor)
	}
	if h := g.hello(); h.Generation != 1 || !reflect.DeepEqual(h.PageList, []int{0, 2}) || h.Notes[3] != "new note" {
		t.Fatalf("presenter hello = %+v", h)
	}
}

func TestReloadWithBadSidecarStillReloadsPDF(t *testing.T) {
	g, path := newReloadGame(t, 2, 0, func() (config.Config, error) { return config.Config{}, errors.New("bad toml") })
	updateUntil(t, g, "first slide", g.showsExactCurrent, nil)
	oldDoc, oldCfg := g.doc, g.cfg

	writeDeck(t, path, 4, 1)
	g.reload.request()
	updateUntil(t, g, "reload", func() bool { return g.doc != oldDoc }, nil)
	if len(g.pageList) != 4 || !reflect.DeepEqual(g.cfg, oldCfg) {
		t.Fatalf("pages=%d cfg changed=%v, want 4 pages and the old config", len(g.pageList), !reflect.DeepEqual(g.cfg, oldCfg))
	}
}

func TestReloadRebuildsOpenOverview(t *testing.T) {
	g, path := newReloadGame(t, 4, 1, nil)
	updateUntil(t, g, "first slide", g.showsExactCurrent, nil)
	g.openOverview()
	if g.ov.store == nil {
		t.Fatal("audience-only overview should load thumbnails")
	}
	oldDoc := g.doc

	writeDeck(t, path, 6, 1)
	g.reload.request()
	updateUntil(t, g, "reload", func() bool { return g.doc != oldDoc }, nil)
	if g.ov.phase == ovOff || len(g.ov.pageList) != 6 || g.ov.store != g.pipe.thumbs {
		t.Fatalf("overview after reload: phase=%v pages=%d attached to new pipeline=%v",
			g.ov.phase, len(g.ov.pageList), g.ov.store == g.pipe.thumbs)
	}
	if g.ov.selIdx != 1 {
		t.Fatalf("overview selection = %d, want 1", g.ov.selIdx)
	}
}

func TestReloaderCoalescesRequests(t *testing.T) {
	var opens atomic.Int32
	gate := make(chan struct{})
	r := &reloader{
		open: func() (*deck, error) {
			<-gate
			opens.Add(1)
			return &deck{}, nil
		},
		results: make(chan reloadResult, 1),
	}
	r.request()
	r.request() // while busy: queued once
	r.request()
	gate <- struct{}{}

	waitResult := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, ok := r.poll(); ok {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("no reload result")
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitResult()
	if !r.busy {
		t.Fatal("the queued request should start once the first finishes")
	}
	gate <- struct{}{}
	waitResult()
	if r.busy || opens.Load() != 2 {
		t.Fatalf("busy=%v opens=%d, want idle after 2 opens", r.busy, opens.Load())
	}
}

func TestReloadableKeepsWindowSettings(t *testing.T) {
	cur := config.Config{PDFPath: "a.pdf", Monitor: "1", PresenterMonitor: "0", NoFullscreen: true, CacheMB: 64, RenderScale: 0.5, Watch: true}
	next := config.Config{PDFPath: "b.pdf", Monitor: "2", Auto: time.Second, Loop: true, Progress: true, AutoQuit: true, Transition: "fade"}
	got := reloadable(cur, next)
	if got.PDFPath != "a.pdf" || got.Monitor != "1" || got.PresenterMonitor != "0" || !got.NoFullscreen ||
		got.CacheMB != 64 || got.RenderScale != 0.5 || !got.Watch {
		t.Fatalf("window/process settings changed: %+v", got)
	}
	if got.Auto != time.Second || !got.Loop || !got.Progress || !got.AutoQuit || got.Transition != "fade" {
		t.Fatalf("content settings not taken over: %+v", got)
	}
}

// TestPresenterReloadKeepsPaneUntilNewRender swaps the presenter's deck
// while its current pane shows a slide of the old one: the old slide stays
// drawable until the pane moves on, then the retired cache is freed.
func TestPresenterReloadKeepsPaneUntilNewRender(t *testing.T) {
	g0, path := newReloadGame(t, 3, 0, nil)
	updateUntil(t, g0, "first slide", g0.showsExactCurrent, nil)

	d, err := openDeck(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := &PresenterGame{doc: d.doc, pipe: d.pipe, reload: newReloader(path, 0, nil), bufW: 1280, bufH: 800}
	t.Cleanup(func() {
		g.cur.release()
		g.reload.close()
		(&deck{doc: g.doc, pipe: g.pipe}).close()
	})
	key, _, _, _ := g.pipe.fit(0, 320, 180)
	g.pipe.requestNow(key)
	deadline := time.Now().Add(10 * time.Second)
	for !g.pipe.slides.Has(key) && time.Now().Before(deadline) {
		g.pipe.drain()
		time.Sleep(time.Millisecond)
	}
	img, got := g.pipe.resolve(key)
	g.cur.set(g.pipe.slides, img, got)
	oldSlides := g.pipe.slides

	writeDeck(t, path, 2, 1)
	g.reload.request()
	for g.doc == d.doc && time.Now().Before(deadline) {
		g.updateReload()
		time.Sleep(time.Millisecond)
	}
	if g.doc == d.doc {
		t.Fatal("presenter never swapped in the reloaded deck")
	}
	if g.cur.img != img || oldSlides.Len() != 1 || len(g.reload.retired) != 1 {
		t.Fatalf("on-screen slide not kept: same img=%v old cache len=%d retired=%d", g.cur.img == img, oldSlides.Len(), len(g.reload.retired))
	}

	g.cur.release() // the pane moves on to a new render
	g.updateReload()
	if oldSlides.Len() != 0 || len(g.reload.retired) != 0 {
		t.Fatalf("retired cache not freed: len=%d retired=%d", oldSlides.Len(), len(g.reload.retired))
	}
}
