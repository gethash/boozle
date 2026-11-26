package app

import (
	"errors"
	"fmt"
	"image/color"
	"maps"
	"os"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"github.com/gethash/boozle/internal/config"
	"github.com/gethash/boozle/internal/ipc"
	"github.com/gethash/boozle/internal/pdf"
	"github.com/gethash/boozle/internal/timer"
)

// Reloading
//
// A reload opens the new version of the PDF in the background (never on
// the Ebiten goroutine), then swaps it in between two frames. The old
// deck's renderer stops at once, but the slides it has on screen stay
// pinned and keep being drawn as stand-ins until the new renders land, so
// a reload never flashes. The audience process reloads on --watch or the
// r key and sends the presenter a new Hello; the presenter reopens the
// file when the Hello's Generation grows.

const (
	// A PDF that changes while it is being copied is retried this often,
	// this far apart, before the reload gives up until the next change.
	openDeckAttempts   = 5
	openDeckRetryDelay = 200 * time.Millisecond
)

// deck is an opened PDF together with the pipeline that renders it.
type deck struct {
	doc  *pdf.Doc
	pipe *pipeline
}

// openDeck opens a private snapshot of the PDF at path (see
// pdf.OpenSnapshot) and a render pipeline for it.
func openDeck(path string, cacheMB int) (*deck, error) {
	var err error
	for range openDeckAttempts {
		var doc *pdf.Doc
		var info os.FileInfo
		doc, info, err = pdf.OpenSnapshot(path)
		if err == nil {
			return &deck{doc: doc, pipe: newPipeline(doc, path, info, cacheMB)}, nil
		}
		if !errors.Is(err, pdf.ErrChanged) {
			break
		}
		time.Sleep(openDeckRetryDelay)
	}
	return nil, err
}

// close frees everything the deck holds. Safe on a nil deck.
func (d *deck) close() {
	if d == nil {
		return
	}
	if d.pipe != nil {
		d.pipe.close()
	}
	if d.doc != nil {
		_ = d.doc.Close()
	}
}

// retire stops the deck's renderer and closes its document, keeping only
// the slides pinned on screen. They stay drawable until the returned cache
// is cleared.
func (d *deck) retire() *pdf.Cache {
	d.pipe.sched.Stop()
	d.pipe.thumbs.Clear()
	d.pipe.slides.PurgeExcept(func(pdf.CacheKey) bool { return false })
	if d.doc != nil {
		_ = d.doc.Close()
	}
	return d.pipe.slides
}

// reloadResult is a finished background reload.
type reloadResult struct {
	deck   *deck
	err    error
	cfg    *config.Config // reloaded config; nil when not reloaded or invalid
	cfgErr error
}

// reloader runs reloads one at a time and frees retired decks once nothing
// on screen shows them. All methods run on the Ebiten goroutine.
type reloader struct {
	open       func() (*deck, error)
	loadConfig func() (config.Config, error) // nil: reload the PDF only

	results chan reloadResult
	busy    bool // a reload is running
	again   bool // another reload was requested meanwhile
	retired []*pdf.Cache
}

func newReloader(path string, cacheMB int, loadConfig func() (config.Config, error)) *reloader {
	return &reloader{
		open:       func() (*deck, error) { return openDeck(path, cacheMB) },
		loadConfig: loadConfig,
		results:    make(chan reloadResult, 1),
	}
}

// request starts a reload, or queues one if a reload is already running
// (the file may have changed again since it was opened).
func (r *reloader) request() {
	if r.busy {
		r.again = true
		return
	}
	r.busy = true
	open, load := r.open, r.loadConfig
	go func() {
		var res reloadResult
		res.deck, res.err = open()
		if load != nil {
			if cfg, err := load(); err != nil {
				res.cfgErr = err
			} else {
				res.cfg = &cfg
			}
		}
		r.results <- res
	}()
}

// poll returns a finished reload, if there is one. The caller owns its deck.
func (r *reloader) poll() (reloadResult, bool) {
	select {
	case res := <-r.results:
		r.busy = false
		if r.again {
			r.again = false
			r.request()
		}
		return res, true
	default:
		return reloadResult{}, false
	}
}

// retire takes over the replaced deck d; see collect.
func (r *reloader) retire(d *deck) {
	if d != nil && d.pipe != nil {
		r.retired = append(r.retired, d.retire())
	}
}

// collect frees the slides of retired decks that inUse says are no longer
// on screen.
func (r *reloader) collect(inUse func(*pdf.Cache) bool) {
	keep := r.retired[:0]
	for _, c := range r.retired {
		if inUse(c) {
			keep = append(keep, c)
		} else {
			c.Clear()
		}
	}
	clear(r.retired[len(keep):])
	r.retired = keep
}

// close waits for a running reload and frees everything the reloader holds.
func (r *reloader) close() {
	if r == nil {
		return
	}
	if r.busy {
		res := <-r.results
		res.deck.close()
		r.busy = false
	}
	for _, c := range r.retired {
		c.Clear()
	}
	r.retired = nil
}

// reloadable returns cur with the settings a reload may change taken from
// next. Window and process settings (monitors, fullscreen, cache size,
// render scale, watching) keep their startup values.
func reloadable(cur, next config.Config) config.Config {
	cur.Auto = next.Auto
	cur.Loop = next.Loop
	cur.PageRange = next.PageRange
	cur.PlaybackPages = next.PlaybackPages
	cur.UsePlaybackPages = next.UsePlaybackPages
	cur.PerPage = next.PerPage
	cur.Notes = next.Notes
	cur.Progress = next.Progress
	cur.AutoQuit = next.AutoQuit
	cur.Transition = next.Transition
	cur.Background = next.Background
	return cur
}

// ── audience ─────────────────────────────────────────────────────────────

// updateReload starts reloads (r key, presenter command, file watcher),
// swaps in finished ones and frees retired slides.
func (g *Game) updateReload(cmds []ipc.PresenterCommand) {
	if g.reload == nil {
		return
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyR) || presenterCmdPressed(cmds, presenterCmdReload) {
		g.reload.request()
	}
	select {
	case <-g.watch: // nil channel without --watch: never ready
		g.reload.request()
	default:
	}
	if res, ok := g.reload.poll(); ok {
		g.applyReload(res)
	}
	g.reload.collect(func(c *pdf.Cache) bool {
		return g.disp.cache == c || g.trans.prev.cache == c
	})
}

// applyReload swaps in a reloaded deck and config, keeping the current page
// (by page number) and the overview, and tells the presenter.
func (g *Game) applyReload(res reloadResult) {
	if res.err != nil {
		fmt.Fprintf(os.Stderr, "boozle: reload failed, still showing the previous version: %v\n", res.err)
		return
	}
	cfg := g.cfg
	if res.cfgErr != nil {
		fmt.Fprintf(os.Stderr, "boozle: sidecar not reloaded: %v\n", res.cfgErr)
	} else if res.cfg != nil {
		cfg = reloadable(g.cfg, *res.cfg)
	}
	pages := res.deck.doc.PageCount()
	pageList := buildPageList(pages, cfg)
	if len(pageList) == 0 {
		fmt.Fprintf(os.Stderr, "boozle: reload skipped: no pages to play (page count = %d)\n", pages)
		res.deck.close()
		return
	}

	// Positions follow page numbers into the new play list.
	oldList := g.pageList
	remap := func(idx int) int {
		return listIndexForPage(pageList, oldList[clampListIndex(idx, len(oldList))])
	}
	listIdx, prevIdx := remap(g.listIdx), remap(g.prevListIdx)
	pageChanged := pageList[listIdx] != g.currentPage()
	autoChanged := cfg.Auto != g.cfg.Auto || !maps.Equal(cfg.PerPage, g.cfg.PerPage)

	// Detach everything that references the old pipeline.
	ov := g.ov
	g.ov.release(g.pipe)
	g.trans.clear()
	old := &deck{doc: g.doc, pipe: g.pipe}
	g.doc, g.pipe = res.deck.doc, res.deck.pipe
	if g.bufW > 0 && g.bufH > 0 {
		g.pipe.resized(autoBudget(g.bufW, g.bufH), g.cfg.CacheMB)
	}
	g.reload.retire(old)

	g.cfg = cfg
	g.bg = color.RGBA{cfg.Background.R, cfg.Background.G, cfg.Background.B, cfg.Background.A}
	g.trans.style = parseTransStyle(cfg.Transition)
	g.pageList, g.listIdx, g.prevListIdx = pageList, listIdx, prevIdx
	if autoChanged {
		paused := g.auto.Paused()
		g.auto = timer.New(cfg.Auto, cfg.PerPage)
		g.auto.Reset(g.currentPage() + 1)
		if paused {
			g.auto.TogglePause(g.currentPage() + 1)
		}
	} else if pageChanged {
		g.auto.Reset(g.currentPage() + 1)
	}
	if ov.phase != ovOff {
		g.ov = newOverview(pageList, g.bufW, g.bufH, remap(ov.fromIdx))
		g.ov.phase, g.ov.anim = ov.phase, ov.anim
		g.ov.selIdx, g.ov.exitToIdx = remap(ov.selIdx), remap(ov.exitToIdx)
		if g.shouldDrawOverviewOnAudience() && g.ov.phase != ovExiting {
			g.ov.attach(g.pipe)
		}
	}
	g.dispWanted, g.prefetchFor = pdf.CacheKey{}, prefetchSig{}

	g.generation++
	if g.srv != nil {
		g.srv.UpdateHello(g.hello())
		g.lastSentAt = time.Time{} // resend the state right away
	}
	if g.cfg.DebugStats {
		fmt.Fprintf(os.Stderr, "[boozle audience] reloaded %s (%d pages, %d in play list)\n", g.cfg.PDFPath, pages, len(pageList))
	}
}

// hello is what the presenter process needs to know about the deck.
func (g *Game) hello() ipc.Hello {
	return ipc.Hello{
		PageList:   g.pageList,
		Notes:      g.cfg.Notes,
		StartedAt:  g.startedAt,
		Loop:       g.cfg.Loop,
		Generation: g.generation,
	}
}

// ── presenter ────────────────────────────────────────────────────────────

// updateReload reloads the PDF when the audience process reports a new
// generation, swaps in finished reloads and frees retired slides.
func (g *PresenterGame) updateReload() {
	if g.reload == nil {
		return
	}
	if res, ok := g.reload.poll(); ok {
		g.applyReload(res)
	}
	g.reload.collect(func(c *pdf.Cache) bool {
		return g.cur.cache == c || g.next.cache == c
	})
}

func (g *PresenterGame) applyReload(res reloadResult) {
	if res.err != nil {
		fmt.Fprintf(os.Stderr, "boozle presenter: reload failed, still showing the previous version: %v\n", res.err)
		return
	}
	// The overview is rebuilt on the new pipeline by syncOverview.
	g.ov.release(g.pipe)
	old := &deck{doc: g.doc, pipe: g.pipe}
	g.doc, g.pipe = res.deck.doc, res.deck.pipe
	if g.bufW > 0 && g.bufH > 0 {
		g.pipe.resized(presenterBudget(g.presenterLayout()), g.opts.CacheMB)
	}
	g.reload.retire(old)
	g.curWanted, g.prefetchFor = pdf.CacheKey{}, presenterPrefetchSig{}
	if g.opts.DebugStats {
		fmt.Fprintf(os.Stderr, "[boozle presenter] reloaded (%d pages)\n", g.doc.PageCount())
	}
}
