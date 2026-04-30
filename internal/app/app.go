// Package app drives the boozle presenter: it owns the Ebiten Game,
// the PDF renderer, the page cache, the auto-advance timer, and input handling.
package app

import (
	"errors"
	"fmt"
	"image/color"
	"math"
	"path/filepath"
	"strconv"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"github.com/gethash/boozle/internal/config"
	"github.com/gethash/boozle/internal/display"
	"github.com/gethash/boozle/internal/pdf"
	"github.com/gethash/boozle/internal/timer"
)

// ErrQuit is returned from Update to cleanly exit the Ebiten run loop.
var ErrQuit = errors.New("boozle: quit")

const (
	// cacheCap holds current ± 2 plus a few for monitor / resize variation.
	cacheCap = 8
	// prefetchQueue is bounded so input spam doesn't pile up work.
	prefetchQueue = 4
)

// Run opens the presentation window and blocks until the user quits.
func Run(cfg config.Config) error {
	doc, err := pdf.Open(cfg.PDFPath)
	if err != nil {
		return err
	}
	defer doc.Close()

	pageList := buildPageList(doc.PageCount(), cfg.PageRange)
	if len(pageList) == 0 {
		return fmt.Errorf("no pages to play (page count = %d, --pages = %v)", doc.PageCount(), cfg.PageRange)
	}

	cache := pdf.NewCache(cacheCap)
	defer cache.Clear()

	pf := pdf.NewPrefetcher(doc, cache, prefetchQueue)
	pf.Start()
	defer pf.Stop()

	auto := timer.New(cfg.Auto, cfg.PerPage)
	startIdx := initialIndex(pageList, cfg.StartPage)
	auto.Reset(pageList[startIdx] + 1)

	g := &Game{
		cfg:        cfg,
		bg:         color.RGBA{cfg.Background.R, cfg.Background.G, cfg.Background.B, cfg.Background.A},
		doc:        doc,
		cache:      cache,
		prefetcher: pf,
		auto:       auto,
		pageList:   pageList,
		listIdx:    startIdx,
	}

	ebiten.SetWindowTitle(fmt.Sprintf("boozle — %s", filepath.Base(cfg.PDFPath)))
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	if err := display.PickMonitor(cfg.MonitorIdx); err != nil {
		return err
	}
	if cfg.NoFullscreen {
		ebiten.SetWindowSize(1280, 800)
	} else {
		ebiten.SetFullscreen(true)
	}

	if err := ebiten.RunGame(g); err != nil && !errors.Is(err, ErrQuit) {
		return err
	}
	return nil
}

// buildPageList resolves the configured PageRange against the actual page
// count and returns a slice of 0-indexed page numbers in playback order.
func buildPageList(total int, pr config.PageRange) []int {
	pages := pr.Filter(total) // 1-indexed
	out := make([]int, 0, len(pages))
	for _, n := range pages {
		out = append(out, n-1)
	}
	return out
}

// initialIndex finds the playback-list index for --start (1-indexed),
// clamping to a valid range and falling back to the first page if --start
// is filtered out by --pages.
func initialIndex(pageList []int, startPage1Based int) int {
	target := startPage1Based - 1
	for i, p := range pageList {
		if p == target {
			return i
		}
	}
	for i, p := range pageList {
		if p >= target {
			return i
		}
	}
	return 0
}

// Game implements ebiten.Game.
type Game struct {
	cfg        config.Config
	bg         color.RGBA
	doc        *pdf.Doc
	cache      *pdf.Cache
	prefetcher *pdf.Prefetcher
	auto       *timer.Auto

	pageList []int // 0-indexed page numbers, in playback order
	listIdx  int   // index into pageList

	digitBuf string // numeric-jump input buffer, e.g. "12" → Enter → page 12

	display       *ebiten.Image
	displayKey    pdf.CacheKey
	displayBounds renderBounds

	blackout, whiteout bool // visual blank-out states (mutually exclusive)

	bufW, bufH int // pixel-resolution buffer dimensions

	prevListIdx      int // last position before navigation, for L key
	lastCursorX      int
	lastCursorY      int
	cursorIdleFrames int
	quit             bool // set by advance() when --autoquit fires

	ov overview
}

type renderBounds struct{ dstX, dstY, dstW, dstH int }

// Update processes input and re-rasterizes when the page or buffer changes.
func (g *Game) Update() error {
	if g.quit {
		return ErrQuit
	}

	// Auto-hide cursor after ~3 s of inactivity.
	cx, cy := ebiten.CursorPosition()
	if cx != g.lastCursorX || cy != g.lastCursorY {
		g.lastCursorX = cx
		g.lastCursorY = cy
		g.cursorIdleFrames = 0
		ebiten.SetCursorMode(ebiten.CursorModeVisible)
	} else {
		g.cursorIdleFrames++
		if g.cursorIdleFrames > 180 {
			ebiten.SetCursorMode(ebiten.CursorModeHidden)
		}
	}

	// Overview intercepts Escape and all navigation while active.
	if g.ov.phase != ovOff {
		return g.updateOverview()
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) || inpututil.IsKeyJustPressed(ebiten.KeyQ) {
		return ErrQuit
	}
	// Tab enters overview (not during blank screens).
	if inpututil.IsKeyJustPressed(ebiten.KeyTab) && g.bufW > 0 && g.bufH > 0 && !g.blackout && !g.whiteout {
		g.openOverview()
		return nil
	}

	prevIdx := g.listIdx

	if inpututil.IsKeyJustPressed(ebiten.KeyP) {
		g.auto.TogglePause(g.currentPage() + 1)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyB) {
		g.blackout = !g.blackout
		if g.blackout {
			g.whiteout = false
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyW) {
		g.whiteout = !g.whiteout
		if g.whiteout {
			g.blackout = false
		}
	}
	if g.auto.ShouldAdvance() {
		g.advance(+1)
	}

	g.handleNavigation()

	if g.listIdx != prevIdx {
		g.auto.Reset(g.currentPage() + 1)
	}

	if g.bufW > 0 && g.bufH > 0 {
		if err := g.maybeRefreshDisplay(); err != nil {
			return err
		}
		g.prefetchNeighbors()
	}
	return nil
}

// Draw renders the current frame.
func (g *Game) Draw(screen *ebiten.Image) {
	switch {
	case g.blackout:
		screen.Fill(color.RGBA{0, 0, 0, 255})
		return
	case g.whiteout:
		screen.Fill(color.RGBA{255, 255, 255, 255})
		return
	}
	screen.Fill(g.bg)
	if g.display != nil {
		op := &ebiten.DrawImageOptions{}
		op.GeoM.Translate(float64(g.displayBounds.dstX), float64(g.displayBounds.dstY))
		screen.DrawImage(g.display, op)
	}
	g.drawProgressOverlay(screen)
	if g.ov.phase != ovOff {
		g.drawOverview(screen)
	}
}

// Layout returns the buffer size in physical pixels.
//
// outsideWidth/outsideHeight come from Ebiten in device-independent units;
// multiplying by the active monitor's scale factor produces a pixel-sized
// buffer that PDFium then rasterises into — pixel-perfect on Retina, 4K,
// and mixed-DPI multi-monitor setups. When the user drags the window to a
// monitor with a different DPI, Layout sees new outside dims, the cache
// key changes, and we re-rasterise.
func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	sf := ebiten.Monitor().DeviceScaleFactor()
	if sf <= 0 {
		sf = 1
	}
	pxW := int(math.Round(float64(outsideWidth) * sf))
	pxH := int(math.Round(float64(outsideHeight) * sf))
	g.bufW = pxW
	g.bufH = pxH
	return pxW, pxH
}

func (g *Game) currentPage() int { return g.pageList[g.listIdx] }

// handleNavigation reads the keyboard and updates listIdx / digitBuf.
func (g *Game) handleNavigation() {
	// Digits accumulate into the jump buffer.
	for k := ebiten.KeyDigit0; k <= ebiten.KeyDigit9; k++ {
		if inpututil.IsKeyJustPressed(k) {
			g.digitBuf += string(rune('0' + (k - ebiten.KeyDigit0)))
			if len(g.digitBuf) > 6 {
				g.digitBuf = g.digitBuf[len(g.digitBuf)-6:]
			}
		}
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyNumpadEnter) {
		if g.digitBuf != "" {
			n, err := strconv.Atoi(g.digitBuf)
			g.digitBuf = ""
			if err == nil {
				g.jumpTo1Indexed(n)
			}
		}
		return
	}

	// Backspace: chip the digit buffer; if empty, go back one page.
	if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
		if len(g.digitBuf) > 0 {
			g.digitBuf = g.digitBuf[:len(g.digitBuf)-1]
		} else {
			g.advance(-1)
		}
		return
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) ||
		inpututil.IsKeyJustPressed(ebiten.KeyPageDown) ||
		inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		g.advance(+1)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) ||
		inpututil.IsKeyJustPressed(ebiten.KeyPageUp) {
		g.advance(-1)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyHome) {
		g.listIdx = 0
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEnd) {
		g.listIdx = len(g.pageList) - 1
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF) {
		ebiten.SetFullscreen(!ebiten.IsFullscreen())
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyL) {
		g.listIdx, g.prevListIdx = g.prevListIdx, g.listIdx
	}

	// Mouse wheel: scroll down = next page, up = previous.
	_, wy := ebiten.Wheel()
	if wy < 0 {
		g.advance(+1)
	} else if wy > 0 {
		g.advance(-1)
	}
}

// advance moves listIdx by delta, looping if --loop is set, else clamping.
// Sets g.quit when --autoquit fires at the end of the deck.
func (g *Game) advance(delta int) {
	if len(g.pageList) == 0 {
		return
	}
	next := g.listIdx + delta
	switch {
	case next < 0:
		if g.cfg.Loop {
			next = len(g.pageList) - 1
		} else {
			next = 0
		}
	case next >= len(g.pageList):
		if g.cfg.Loop {
			next = 0
		} else if g.cfg.AutoQuit {
			g.quit = true
			return
		} else {
			next = len(g.pageList) - 1
		}
	}
	g.prevListIdx = g.listIdx
	g.listIdx = next
}

// jumpTo1Indexed seeks to a doc page (1-indexed). If it's been filtered out by
// --pages, the nearest later page is selected.
func (g *Game) jumpTo1Indexed(n int) {
	target := n - 1
	for i, p := range g.pageList {
		if p == target {
			g.listIdx = i
			return
		}
	}
	for i, p := range g.pageList {
		if p > target {
			g.listIdx = i
			return
		}
	}
	g.listIdx = len(g.pageList) - 1
}

// maybeRefreshDisplay swaps in the current page's rendered image when
// the page or pixel buffer changes. On miss it renders synchronously.
func (g *Game) maybeRefreshDisplay() error {
	pageIdx := g.currentPage()
	page, err := g.doc.PageSize(pageIdx)
	if err != nil {
		return err
	}
	w, h, offX, offY := aspectFit(page.WidthPoints, page.HeightPoints, g.bufW, g.bufH)
	if w <= 0 || h <= 0 {
		return nil
	}
	key := pdf.CacheKey{Page: pageIdx, W: w, H: h}
	if g.display != nil && g.displayKey == key {
		return nil
	}

	rgba, ok := g.cache.Get(key)
	if !ok {
		img, cleanup, err := g.doc.RenderPage(key.Page, key.W, key.H)
		if err != nil {
			return err
		}
		g.cache.Put(key, img, cleanup)
		rgba = img
	}

	eimg := ebiten.NewImageFromImage(rgba)
	if g.display != nil {
		g.display.Deallocate()
	}
	g.display = eimg
	g.displayKey = key
	g.displayBounds = renderBounds{dstX: offX, dstY: offY, dstW: w, dstH: h}
	return nil
}

// prefetchNeighbors pushes render hints for the +1 / -1 / +2 neighbors.
// Drops are silent: the prefetcher's queue is bounded.
func (g *Game) prefetchNeighbors() {
	if len(g.pageList) <= 1 {
		return
	}
	for _, delta := range []int{1, -1, 2} {
		idx := g.listIdx + delta
		switch {
		case idx < 0:
			if !g.cfg.Loop {
				continue
			}
			idx = len(g.pageList) - 1
		case idx >= len(g.pageList):
			if !g.cfg.Loop {
				continue
			}
			idx = idx % len(g.pageList)
		}
		pageIdx := g.pageList[idx]
		page, err := g.doc.PageSize(pageIdx)
		if err != nil {
			continue
		}
		w, h, _, _ := aspectFit(page.WidthPoints, page.HeightPoints, g.bufW, g.bufH)
		if w > 0 && h > 0 {
			g.prefetcher.Request(pdf.CacheKey{Page: pageIdx, W: w, H: h})
		}
	}
}

// aspectFit returns the largest (w, h) that fits inside (dstW, dstH) while
// preserving srcW:srcH, plus the centering offset.
func aspectFit(srcW, srcH float64, dstW, dstH int) (w, h, offX, offY int) {
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return 0, 0, 0, 0
	}
	sx := float64(dstW) / srcW
	sy := float64(dstH) / srcH
	s := math.Min(sx, sy)
	w = int(math.Round(srcW * s))
	h = int(math.Round(srcH * s))
	if w > dstW {
		w = dstW
	}
	if h > dstH {
		h = dstH
	}
	offX = (dstW - w) / 2
	offY = (dstH - h) / 2
	return
}
