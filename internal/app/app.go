// Package app drives the boozle presenter: it owns the Ebiten Game,
// the PDF renderer, the page cache, the auto-advance timer, and input handling.
package app

import (
	"errors"
	"fmt"
	"image/color"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"github.com/gethash/boozle/internal/config"
	"github.com/gethash/boozle/internal/display"
	"github.com/gethash/boozle/internal/ipc"
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

	presenterCmdQuit       = "quit"
	presenterCmdEscape     = "escape"
	presenterCmdTab        = "tab"
	presenterCmdPause      = "pause"
	presenterCmdBlackout   = "blackout"
	presenterCmdWhiteout   = "whiteout"
	presenterCmdFullscreen = "fullscreen"
	presenterCmdReturnLast = "return-last"
	presenterCmdHome       = "home"
	presenterCmdEnd        = "end"
	presenterCmdEnter      = "enter"
	presenterCmdBackspace  = "backspace"
	presenterCmdRight      = "right"
	presenterCmdLeft       = "left"
	presenterCmdDown       = "down"
	presenterCmdUp         = "up"
	presenterCmdSpace      = "space"
	presenterCmdPageDown   = "page-down"
	presenterCmdPageUp     = "page-up"
	presenterCmdDigit      = "digit"
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
		startedAt:  time.Now(),
	}

	g.trans.style = parseTransStyle(cfg.Transition)
	g.trans.frames = transFrames

	// ── Presenter view subprocess ─────────────────────────────────────────
	if cfg.PresenterMonitor >= 0 {
		if err := validatePresenterMonitor(cfg); err != nil {
			return err
		}
		socketPath := ipc.SocketPath(os.Getpid())
		srv, err := ipc.Listen(socketPath)
		if err != nil {
			return fmt.Errorf("presenter IPC: %w", err)
		}
		defer srv.Close()
		go srv.AcceptLoop()

		self, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve executable: %w", err)
		}
		cmd := exec.Command(self,
			"--monitor", strconv.Itoa(cfg.PresenterMonitor),
			"--_presenter-socket", socketPath,
			cfg.PDFPath,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("spawn presenter: %w", err)
		}
		defer func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()

		g.stateCh = make(chan ipc.PresenterState, 1)
		g.presenterCmds = srv.Commands()
		go func() {
			for st := range g.stateCh {
				srv.Send(st)
			}
		}()
	}

	ebiten.SetWindowTitle(fmt.Sprintf("boozle — %s", filepath.Base(cfg.PDFPath)))
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	if err := display.PickMonitor(cfg.MonitorIdx); err != nil {
		return err
	}
	var runGame ebiten.Game = g
	if cfg.NoFullscreen {
		ebiten.SetWindowSize(1280, 800)
	} else {
		ebiten.SetWindowSize(1280, 800)
		runGame = &fullscreenOnMonitor{game: g, monitorIdx: cfg.MonitorIdx}
	}

	if err := ebiten.RunGame(runGame); err != nil && !errors.Is(err, ErrQuit) {
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

func validatePresenterMonitor(cfg config.Config) error {
	if cfg.PresenterMonitor == cfg.MonitorIdx && !cfg.NoFullscreen {
		return fmt.Errorf(
			"--presenter-monitor %d conflicts with --monitor %d: use different monitors or pass --no-fullscreen",
			cfg.PresenterMonitor, cfg.MonitorIdx,
		)
	}
	return nil
}

// Game implements ebiten.Game.
type Game struct {
	cfg        config.Config
	bg         color.RGBA
	doc        *pdf.Doc
	cache      *pdf.Cache
	prefetcher *pdf.Prefetcher
	auto       *timer.Auto

	pageList  []int // 0-indexed page numbers, in playback order
	listIdx   int   // index into pageList
	startedAt time.Time

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

	trans         transition
	ov            overview
	stateCh       chan ipc.PresenterState // nil when presenter view is disabled
	presenterCmds <-chan ipc.PresenterCommand
}

type renderBounds struct{ dstX, dstY, dstW, dstH int }

// Update processes input and re-rasterizes when the page or buffer changes.
func (g *Game) Update() error {
	if g.quit {
		return ErrQuit
	}
	presenterCmds := g.drainPresenterCommands()

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
		err := g.updateOverview(presenterCmds)
		g.broadcastState()
		return err
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) ||
		inpututil.IsKeyJustPressed(ebiten.KeyQ) ||
		presenterCmdPressed(presenterCmds, presenterCmdEscape) ||
		presenterCmdPressed(presenterCmds, presenterCmdQuit) {
		return ErrQuit
	}
	// Tab enters overview (not during blank screens).
	if (inpututil.IsKeyJustPressed(ebiten.KeyTab) || presenterCmdPressed(presenterCmds, presenterCmdTab)) &&
		g.bufW > 0 && g.bufH > 0 && !g.blackout && !g.whiteout {
		g.openOverview()
		return nil
	}

	prevIdx := g.listIdx

	if inpututil.IsKeyJustPressed(ebiten.KeyP) || presenterCmdPressed(presenterCmds, presenterCmdPause) {
		g.auto.TogglePause(g.currentPage() + 1)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyB) || presenterCmdPressed(presenterCmds, presenterCmdBlackout) {
		g.blackout = !g.blackout
		if g.blackout {
			g.whiteout = false
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyW) || presenterCmdPressed(presenterCmds, presenterCmdWhiteout) {
		g.whiteout = !g.whiteout
		if g.whiteout {
			g.blackout = false
		}
	}
	if g.auto.ShouldAdvance() {
		g.advance(+1)
	}

	if err := g.handleNavigation(presenterCmds); err != nil {
		return err
	}

	if g.listIdx != prevIdx {
		g.auto.Reset(g.currentPage() + 1)
	}

	if g.trans.active {
		g.trans.frame++
		if g.trans.frame >= g.trans.frames {
			g.trans.clear()
		}
	}

	if g.bufW > 0 && g.bufH > 0 {
		if err := g.maybeRefreshDisplay(); err != nil {
			return err
		}
		g.prefetchNeighbors()
	}
	g.broadcastState()
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
		if g.trans.active && g.trans.prevImg != nil {
			g.drawTransition(screen)
		} else {
			op := &ebiten.DrawImageOptions{}
			op.GeoM.Translate(float64(g.displayBounds.dstX), float64(g.displayBounds.dstY))
			screen.DrawImage(g.display, op)
		}
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

func (g *Game) drainPresenterCommands() []ipc.PresenterCommand {
	if g.presenterCmds == nil {
		return nil
	}
	var cmds []ipc.PresenterCommand
	for range 64 {
		select {
		case cmd := <-g.presenterCmds:
			cmds = append(cmds, cmd)
		default:
			return cmds
		}
	}
	return cmds
}

func presenterCmdPressed(cmds []ipc.PresenterCommand, name string) bool {
	for _, cmd := range cmds {
		if cmd.Name == name {
			return true
		}
	}
	return false
}

// broadcastState sends the current presentation state to the presenter slave.
// Non-blocking: drops stale state if the IPC goroutine hasn't caught up.
func (g *Game) broadcastState() {
	if g.stateCh == nil {
		return
	}
	nextPage := -1
	if g.listIdx+1 < len(g.pageList) {
		nextPage = g.pageList[g.listIdx+1]
	} else if g.cfg.Loop && len(g.pageList) > 0 {
		nextPage = g.pageList[0]
	}
	st := ipc.PresenterState{
		Page:           g.currentPage(),
		ListIndex:      g.listIdx,
		Total:          len(g.pageList),
		Fraction:       g.autoProgressFraction(),
		Paused:         g.auto.Paused(),
		NextPage:       nextPage,
		ElapsedSeconds: int64(time.Since(g.startedAt).Seconds()),
		Notes:          g.cfg.Notes[g.currentPage()+1],
	}
	// Drain-then-send so the slave always gets the latest frame, never a stale one.
	select {
	case g.stateCh <- st:
	default:
		select {
		case <-g.stateCh:
		default:
		}
		select {
		case g.stateCh <- st:
		default:
		}
	}
}

func (g *Game) autoProgressFraction() float64 {
	if !g.auto.IsActive() {
		return 0
	}
	if g.auto.Paused() {
		return g.auto.FractionAtPause()
	}
	return g.auto.Fraction()
}

// handleNavigation reads local and presenter-window input and updates
// listIdx / digitBuf.
func (g *Game) handleNavigation(presenterCmds []ipc.PresenterCommand) error {
	// Digits accumulate into the jump buffer.
	for k := ebiten.KeyDigit0; k <= ebiten.KeyDigit9; k++ {
		if inpututil.IsKeyJustPressed(k) {
			g.appendDigit(int(k - ebiten.KeyDigit0))
		}
	}
	for _, cmd := range presenterCmds {
		if cmd.Name == presenterCmdDigit {
			g.appendDigit(cmd.Arg)
		}
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyEnter) ||
		inpututil.IsKeyJustPressed(ebiten.KeyNumpadEnter) ||
		presenterCmdPressed(presenterCmds, presenterCmdEnter) {
		if g.digitBuf != "" {
			n, err := strconv.Atoi(g.digitBuf)
			g.digitBuf = ""
			if err == nil {
				g.jumpTo1Indexed(n)
			}
		}
		return nil
	}

	// Backspace: chip the digit buffer; if empty, go back one page.
	if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) ||
		presenterCmdPressed(presenterCmds, presenterCmdBackspace) {
		if len(g.digitBuf) > 0 {
			g.digitBuf = g.digitBuf[:len(g.digitBuf)-1]
		} else {
			g.advance(-1)
		}
		return nil
	}

	if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) ||
		inpututil.IsKeyJustPressed(ebiten.KeyPageDown) ||
		inpututil.IsKeyJustPressed(ebiten.KeySpace) ||
		presenterCmdPressed(presenterCmds, presenterCmdRight) ||
		presenterCmdPressed(presenterCmds, presenterCmdPageDown) ||
		presenterCmdPressed(presenterCmds, presenterCmdSpace) {
		g.advance(+1)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) ||
		inpututil.IsKeyJustPressed(ebiten.KeyPageUp) ||
		presenterCmdPressed(presenterCmds, presenterCmdLeft) ||
		presenterCmdPressed(presenterCmds, presenterCmdPageUp) {
		g.advance(-1)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyHome) || presenterCmdPressed(presenterCmds, presenterCmdHome) {
		g.beginTransition(0)
		g.listIdx = 0
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEnd) || presenterCmdPressed(presenterCmds, presenterCmdEnd) {
		g.beginTransition(0)
		g.listIdx = len(g.pageList) - 1
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF) || presenterCmdPressed(presenterCmds, presenterCmdFullscreen) {
		if err := setFullscreenOnMonitor(g.cfg.MonitorIdx, !ebiten.IsFullscreen()); err != nil {
			return err
		}
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyL) || presenterCmdPressed(presenterCmds, presenterCmdReturnLast) {
		g.beginTransition(0)
		g.listIdx, g.prevListIdx = g.prevListIdx, g.listIdx
	}

	// Mouse wheel: scroll down = next page, up = previous.
	_, wy := ebiten.Wheel()
	if wy < 0 {
		g.advance(+1)
	} else if wy > 0 {
		g.advance(-1)
	}
	return nil
}

func (g *Game) appendDigit(n int) {
	if n < 0 || n > 9 {
		return
	}
	g.digitBuf += string(rune('0' + n))
	if len(g.digitBuf) > 6 {
		g.digitBuf = g.digitBuf[len(g.digitBuf)-6:]
	}
}

// advance moves listIdx by delta, looping if --loop is set, else clamping.
// Sets g.quit when --autoquit fires at the end of the deck.
func (g *Game) advance(delta int) {
	if len(g.pageList) == 0 {
		return
	}
	g.beginTransition(sign(delta))
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
	g.beginTransition(0)
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
