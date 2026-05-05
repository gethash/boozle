// Package app drives the boozle presenter: it owns the Ebiten Game,
// the PDF renderer, the page cache, the auto-advance timer, and input handling.
package app

import (
	"errors"
	"fmt"
	"image"
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
	// Cache budgets are sized at runtime by autoBudget(bufW, bufH) — see
	// onLayoutChanged. The constants here are floor/ceiling and the initial
	// value used before Layout has run. The user can override the auto sizing
	// via --cache-mb (cfg.CacheMB > 0).
	cacheBudgetMin     = 32 << 20  // 32 MB floor (small windows)
	cacheBudgetMax     = 192 << 20 // 192 MB ceiling — past this, more pages don't help typical nav
	cacheBudgetPages   = 4         // target ~4 pages worth at current bufW×bufH
	cacheBudgetInitial = cacheBudgetMin

	// prefetchQueue is bounded so input spam doesn't pile up work.
	prefetchQueue = 8

	// maxUploadsPerFrame caps GPU upload work done on the main goroutine
	// each tick to keep frame budget. Each upload is a NewImage+WritePixels
	// pair (~1–3 ms on 4K).
	maxUploadsPerFrame = 2

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

	cache := pdf.NewCache(cacheBudgetInitial)
	defer cache.Clear()
	if cfg.CacheMB > 0 {
		cache.Resize(cfg.CacheMB << 20)
	}

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
		args := []string{
			"--monitor", strconv.Itoa(cfg.PresenterMonitor),
			"--_presenter-socket", socketPath,
		}
		if cfg.CacheMB > 0 {
			args = append(args, "--cache-mb", strconv.Itoa(cfg.CacheMB))
		}
		if cfg.RenderScale > 0 {
			args = append(args, "--render-scale", strconv.FormatFloat(cfg.RenderScale, 'f', -1, 64))
		}
		args = append(args, cfg.PDFPath)
		cmd := exec.Command(self, args...)
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
	setWindowIcon()
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
	displayKey    pdf.CacheKey // exact key currently backing g.display (zero if mipmap-reused)
	displayWanted pdf.CacheKey // last requested key — distinguishes "exact" from "stand-in"
	displayPinned bool         // true while the cache entry at displayKey is Pin'd
	displayBounds renderBounds

	blackout, whiteout bool // visual blank-out states (mutually exclusive)

	bufW, bufH int // pixel-resolution buffer dimensions

	prevListIdx      int // last position before navigation, for L key
	lastNavDir       int // +1 / -1 / 0 — biases prefetch order
	lastCursorX      int
	lastCursorY      int
	cursorIdleFrames int
	quit             bool // set by advance() when --autoquit fires

	pageLabel pageLabelCache // cached fmt.Sprintf for the bottom-right counter

	trans         transition
	ov            overview
	stateCh       chan ipc.PresenterState // nil when presenter view is disabled
	presenterCmds <-chan ipc.PresenterCommand
}

// renderBounds describes how to position and scale a rendered slide on the
// screen. dstX/dstY are pixel offsets; dstW/dstH are the on-screen size;
// srcW/srcH are the actual source-image dimensions (may differ from dst*
// when serving a larger cached image during a resize, which is then
// linearly downsampled by Ebiten).
type renderBounds struct {
	dstX, dstY, dstW, dstH int
	srcW, srcH             int
}

// pageLabelCache memoises the "12 / 84"-style page counter so we don't
// rebuild the string in fmt.Sprintf on every Draw call.
type pageLabelCache struct {
	listIdx int
	total   int
	s       string
}

func (p *pageLabelCache) String(listIdx, total int) string {
	if p.s == "" || p.listIdx != listIdx || p.total != total {
		p.listIdx = listIdx
		p.total = total
		p.s = fmt.Sprintf("%d / %d", listIdx+1, total)
	}
	return p.s
}

// Update processes input and re-rasterizes when the page or buffer changes.
func (g *Game) Update() error {
	if g.quit {
		return ErrQuit
	}
	g.drainPendingUploads()
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
	resetTextPool()
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
			applyDisplayScale(&op.GeoM, g.displayBounds)
			op.GeoM.Translate(float64(g.displayBounds.dstX), float64(g.displayBounds.dstY))
			op.Filter = ebiten.FilterLinear
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
	if rs := g.cfg.RenderScale; rs > 0 && rs < 1 {
		pxW = max(1, int(math.Round(float64(pxW)*rs)))
		pxH = max(1, int(math.Round(float64(pxH)*rs)))
	}
	if pxW != g.bufW || pxH != g.bufH {
		g.bufW = pxW
		g.bufH = pxH
		g.onLayoutChanged()
	}
	return pxW, pxH
}

// onLayoutChanged is called when the pixel buffer dimensions change (window
// resize or DPI/monitor swap). It auto-sizes the cache budget and proactively
// purges entries whose dimensions exceed the new buffer — those are the
// ones most likely to become stale and waste GPU memory.
func (g *Game) onLayoutChanged() {
	if g.cache == nil || g.bufW <= 0 || g.bufH <= 0 {
		return
	}
	if g.cfg.CacheMB <= 0 {
		g.cache.Resize(autoBudget(g.bufW, g.bufH))
	}
	// PurgeNotMatching keeps smaller-or-equal entries: those serve as
	// linearly-downsampled stand-ins via the mipmap-reuse path until the
	// prefetcher catches up at the new resolution.
	g.cache.PurgeNotMatching(g.bufW, g.bufH)
}

// autoBudget sizes the GPU image cache to hold ~cacheBudgetPages full-buffer
// pages, with a sensible floor for small windows and a ceiling for high-DPI
// setups. The user can override entirely via --cache-mb.
func autoBudget(bufW, bufH int) int {
	if bufW <= 0 || bufH <= 0 {
		return cacheBudgetMin
	}
	perPage := bufW * bufH * 4
	budget := perPage * cacheBudgetPages
	if budget < cacheBudgetMin {
		return cacheBudgetMin
	}
	if budget > cacheBudgetMax {
		return cacheBudgetMax
	}
	return budget
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
	g.lastNavDir = sign(delta)
	g.beginTransition(g.lastNavDir)
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

// maybeRefreshDisplay swaps in the current page's rendered image when the
// page or pixel buffer changes. Cache hits are pointer swaps (no upload);
// misses fall back to a synchronous render + WritePixels on the main thread.
//
// During a resize/DPI change the cache may not have an entry at the new
// dimensions yet; rather than block on a fresh PDFium render, we look for a
// larger same-page entry and let Ebiten linearly downsample it. The prefetcher
// then renders the exact-size version which lands a frame or two later.
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
	requestKey := pdf.CacheKey{Page: pageIdx, W: w, H: h}
	g.displayWanted = requestKey

	// Fast path: g.display already shows the exact requested key.
	if g.display != nil && g.displayKey == requestKey {
		return nil
	}

	// Exact cache hit — pointer swap, no upload.
	if cached, ok := g.cache.Get(requestKey); ok {
		g.adoptDisplay(cached.(*ebiten.Image), requestKey,
			renderBounds{dstX: offX, dstY: offY, dstW: w, dstH: h, srcW: w, srcH: h})
		return nil
	}

	// Mipmap reuse: any cached entry for the same page at ≥requested size and
	// matching aspect can be downsampled by Ebiten while we wait for the
	// prefetcher to render the exact-size version.
	if mipKey, ok := g.findScalable(requestKey); ok {
		if cached, ok := g.cache.Get(mipKey); ok {
			g.adoptDisplay(cached.(*ebiten.Image), mipKey,
				renderBounds{dstX: offX, dstY: offY, dstW: w, dstH: h, srcW: mipKey.W, srcH: mipKey.H})
			g.prefetcher.Request(requestKey)
			return nil
		}
	}

	// Cache miss: render synchronously, upload, insert.
	img, cleanup, err := g.doc.RenderPage(requestKey.Page, requestKey.W, requestKey.H)
	if err != nil {
		return err
	}
	eimg := uploadRGBA(img)
	if cleanup != nil {
		cleanup()
	}
	g.cache.Put(requestKey, eimg)
	b := img.Bounds()
	g.adoptDisplay(eimg, requestKey,
		renderBounds{dstX: offX, dstY: offY, dstW: w, dstH: h, srcW: b.Dx(), srcH: b.Dy()})
	return nil
}

// adoptDisplay swaps g.display to a new cache-owned image, pinning the new
// entry so it cannot be evicted while displayed (and unpinning the old one).
// All cleanup of the previous image is the cache's responsibility.
func (g *Game) adoptDisplay(eimg *ebiten.Image, key pdf.CacheKey, bounds renderBounds) {
	if g.displayPinned && g.cache != nil {
		g.cache.Unpin(g.displayKey)
		g.displayPinned = false
	}
	g.display = eimg
	g.displayKey = key
	g.displayBounds = bounds
	if g.cache != nil && key != (pdf.CacheKey{}) {
		g.cache.Pin(key)
		g.displayPinned = true
	}
}

// findScalable scans the cache for an entry of the same page with both
// dimensions ≥ the requested size and a near-identical aspect ratio. Used
// only during a transient resize to avoid blocking on PDFium for one frame.
func (g *Game) findScalable(req pdf.CacheKey) (pdf.CacheKey, bool) {
	if req.W <= 0 || req.H <= 0 {
		return pdf.CacheKey{}, false
	}
	wantAspect := float64(req.W) / float64(req.H)
	var best pdf.CacheKey
	bestArea := 0
	g.cache.Range(func(k pdf.CacheKey) {
		if k.Page != req.Page || k.W < req.W || k.H < req.H {
			return
		}
		gotAspect := float64(k.W) / float64(k.H)
		if math.Abs(gotAspect-wantAspect)/wantAspect > 0.005 {
			return
		}
		// Prefer the smallest-area scalable entry — closest to native res.
		area := k.W * k.H
		if best.W == 0 || area < bestArea {
			best = k
			bestArea = area
		}
	})
	return best, best.W != 0
}

// uploadRGBA creates a fresh GPU image at rgba's actual bounds and uploads
// the pixel data via WritePixels (faster than NewImageFromImage on big buffers).
func uploadRGBA(rgba *image.RGBA) *ebiten.Image {
	b := rgba.Bounds()
	eimg := ebiten.NewImage(b.Dx(), b.Dy())
	eimg.WritePixels(rgba.Pix)
	return eimg
}

// drainPendingUploads moves prefetcher-rendered RGBA pixels onto the GPU
// (this must happen on the main goroutine for Ebiten) and inserts them
// into the cache. Bounded per frame to keep latency in check.
func (g *Game) drainPendingUploads() {
	if g.prefetcher == nil {
		return
	}
	uploads := g.prefetcher.Uploads()
	for i := 0; i < maxUploadsPerFrame; i++ {
		select {
		case job := <-uploads:
			if g.cache.Has(job.Key) {
				// A synchronous render on the main thread already cached this
				// key while the prefetcher had it in flight — drop the dupe.
				if job.Cleanup != nil {
					job.Cleanup()
				}
				if job.Done != nil {
					job.Done()
				}
				continue
			}
			eimg := uploadRGBA(job.RGBA)
			if job.Cleanup != nil {
				job.Cleanup()
			}
			g.cache.Put(job.Key, eimg)
			if job.Done != nil {
				job.Done()
			}
		default:
			return
		}
	}
}

// applyDisplayScale multiplies the GeoM by the source-to-destination ratio
// when serving a cached image at a different size than the requested layout
// (the mipmap-reuse path). When source == destination, this is a no-op.
func applyDisplayScale(g *ebiten.GeoM, b renderBounds) {
	if b.srcW <= 0 || b.srcH <= 0 || (b.srcW == b.dstW && b.srcH == b.dstH) {
		return
	}
	sx := float64(b.dstW) / float64(b.srcW)
	sy := float64(b.dstH) / float64(b.srcH)
	g.Scale(sx, sy)
}

// prefetchNeighbors pushes render hints for the next few neighbors, ordered
// by recent navigation direction so forward marches keep the queue ahead of
// the user. Drops are silent: the prefetcher's queue is bounded.
func (g *Game) prefetchNeighbors() {
	if len(g.pageList) <= 1 {
		return
	}
	var deltas []int
	switch {
	case g.lastNavDir > 0:
		deltas = []int{1, 2, 3, -1}
	case g.lastNavDir < 0:
		deltas = []int{-1, -2, -3, 1}
	default:
		deltas = []int{1, -1}
	}
	for _, delta := range deltas {
		idx := g.listIdx + delta
		switch {
		case idx < 0:
			if !g.cfg.Loop {
				continue
			}
			idx = (idx%len(g.pageList) + len(g.pageList)) % len(g.pageList)
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
