// Package app drives the boozle presenter: it owns the Ebiten Game,
// the render pipeline, the auto-advance timer, and input handling.
package app

import (
	"errors"
	"fmt"
	"image/color"
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
	// pipeline.resized. The constants here are floor/ceiling and the
	// initial value used before Layout has run. The user can override the
	// auto sizing via --cache-mb (cfg.CacheMB > 0).
	cacheBudgetMin     = 32 << 20  // 32 MB floor (small windows)
	cacheBudgetMax     = 192 << 20 // 192 MB ceiling of real GPU memory (3 pages at 4K)
	cacheBudgetPages   = 4         // target ~4 pages worth at current bufW×bufH
	cacheBudgetInitial = cacheBudgetMin

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
	presenterCmdOverviewGo = "overview-go"

	// transitionMaxWaitFrames bounds how long a transition waits for the
	// target slide's render before animating anyway (e.g. a broken page).
	transitionMaxWaitFrames = 60
)

// Run opens the presentation window and blocks until the user quits.
func Run(cfg config.Config) error {
	opened := time.Now()
	doc, err := pdf.Open(cfg.PDFPath)
	if err != nil {
		return err
	}
	defer doc.Close()
	if cfg.DebugStats {
		fmt.Fprintf(os.Stderr, "[boozle audience] pdf open %v (%d pages)\n", time.Since(opened).Round(time.Millisecond), doc.PageCount())
	}

	pageList := buildPageList(doc.PageCount(), cfg)
	if len(pageList) == 0 {
		return fmt.Errorf("no pages to play (page count = %d, --pages = %v)", doc.PageCount(), cfg.PageRange)
	}

	pipe := newPipeline(doc, cfg.PDFPath, cfg.CacheMB)
	defer pipe.close()

	auto := timer.New(cfg.Auto, cfg.PerPage)
	startIdx := initialIndex(pageList, cfg.StartPage)
	auto.Reset(pageList[startIdx] + 1)

	g := &Game{
		cfg:       cfg,
		bg:        color.RGBA{cfg.Background.R, cfg.Background.G, cfg.Background.B, cfg.Background.A},
		pipe:      pipe,
		auto:      auto,
		pageList:  pageList,
		listIdx:   startIdx,
		startedAt: time.Now(),
	}
	if cfg.DebugStats {
		g.stats = &statsReporter{role: "audience"}
	}

	g.trans.style = parseTransStyle(cfg.Transition)
	g.trans.frames = transFrames

	audienceMon, err := display.Resolve(cfg.Monitor)
	if err != nil {
		return fmt.Errorf("--monitor: %w", err)
	}
	g.monitor = audienceMon

	// ── Presenter view subprocess ─────────────────────────────────────────
	// The presenter process is started once the audience window has been
	// placed (see Update), so the two windows don't race for the window
	// manager's attention and the presenter window ends up focused.
	if cfg.PresenterEnabled() {
		presenterMon, err := display.Resolve(cfg.PresenterMonitor)
		if err != nil {
			return fmt.Errorf("--presenter-monitor: %w", err)
		}
		if err := validatePresenterMonitor(audienceMon, presenterMon, cfg.NoFullscreen); err != nil {
			return err
		}
		srv, err := ipc.Listen(ipc.Hello{PageList: pageList, Notes: cfg.Notes, StartedAt: g.startedAt, Loop: cfg.Loop})
		if err != nil {
			return fmt.Errorf("presenter IPC: %w", err)
		}
		defer srv.Close()
		go srv.Accept()

		self, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve executable: %w", err)
		}
		var child *exec.Cmd
		g.spawnPresenter = func() error {
			child = exec.Command(self, presenterArgs(cfg, presenterMon, srv.Path())...)
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
			if err := child.Start(); err != nil {
				return fmt.Errorf("spawn presenter: %w", err)
			}
			return nil
		}
		defer func() {
			if child != nil && child.Process != nil {
				_ = child.Process.Kill()
				_ = child.Wait()
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
	ebiten.SetScreenClearedEveryFrame(false)
	if err := display.PrepareWindow(audienceMon, 1280, 800); err != nil {
		return err
	}
	if !cfg.NoFullscreen {
		g.place = newMonitorPlacer(audienceMon)
		g.place.debug = cfg.DebugStats
		g.place.start()
	}

	err = ebiten.RunGameWithOptions(g, &ebiten.RunGameOptions{X11ClassName: "boozle", X11InstanceName: "audience"})
	if err != nil && !errors.Is(err, ErrQuit) {
		return err
	}
	return nil
}

// presenterArgs builds the command line for the presenter subprocess. The
// monitor is passed as the index resolved here, so both processes agree on
// it even when names are ambiguous.
func presenterArgs(cfg config.Config, monitor int, socketPath string) []string {
	args := []string{
		"--monitor", strconv.Itoa(monitor),
		"--_presenter-socket", socketPath,
	}
	if cfg.NoFullscreen {
		args = append(args, "--no-fullscreen")
	}
	if cfg.CacheMB > 0 {
		args = append(args, "--cache-mb", strconv.Itoa(cfg.CacheMB))
	}
	if cfg.RenderScale > 0 {
		args = append(args, "--render-scale", strconv.FormatFloat(cfg.RenderScale, 'f', -1, 64))
	}
	if cfg.DebugStats {
		args = append(args, "--debug-stats")
	}
	return append(args, cfg.PDFPath)
}

// buildPageList resolves the configured playback pages or PageRange against
// the actual page count and returns 0-indexed page numbers in playback order.
func buildPageList(total int, cfg config.Config) []int {
	pages := cfg.PlaybackPages
	if !cfg.UsePlaybackPages {
		pages = cfg.PageRange.Filter(total) // 1-indexed
	}
	out := make([]int, 0, len(pages))
	for _, n := range pages {
		if n >= 1 && n <= total {
			out = append(out, n-1)
		}
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

func validatePresenterMonitor(audience, presenter int, windowed bool) error {
	if audience == presenter && !windowed {
		return fmt.Errorf(
			"presenter view and audience both resolve to monitor %d: use different monitors or pass --no-fullscreen",
			audience,
		)
	}
	return nil
}

// presenterSpawnTimeout starts the presenter even if fullscreen placement
// of the audience window hasn't finished (2 s at 60 TPS).
const presenterSpawnTimeout = 120

// Game implements ebiten.Game for the audience window.
type Game struct {
	cfg  config.Config
	bg   color.RGBA
	pipe *pipeline
	auto *timer.Auto

	pageList  []int // 0-indexed page numbers, in playback order
	listIdx   int   // index into pageList
	startedAt time.Time

	digitBuf string // numeric-jump input buffer, e.g. "12" → Enter → page 12

	disp        pinnedSlide  // image on screen; may lag listIdx until its render lands
	dispBounds  renderBounds // where disp is drawn
	dispWanted  pdf.CacheKey // exact render wanted for the current page
	prefetchFor prefetchSig  // inputs the prefetch queue was last built from

	blackout, whiteout bool // visual blank-out states (mutually exclusive)

	bufW, bufH int // pixel-resolution buffer dimensions

	prevListIdx      int // last position before navigation, for L key
	lastNavDir       int // +1 / -1 / 0 — biases prefetch order
	lastCursorX      int
	lastCursorY      int
	cursorIdleFrames int
	quit             bool // set by advance() when --autoquit fires

	pageLabel pageLabelCache // cached fmt.Sprintf for the bottom-right counter

	trans transition
	ov    overview

	monitor int            // resolved audience monitor index
	place   *monitorPlacer // fullscreen placement; nil when windowed

	stateCh        chan ipc.PresenterState // nil when presenter view is disabled
	presenterCmds  <-chan ipc.PresenterCommand
	spawnPresenter func() error // starts the presenter process once; then nil
	ticks          int
	lastSent       ipc.PresenterState
	lastSentAt     time.Time

	redraw redrawGate[audienceFrame]
	stats  *statsReporter
}

// renderBounds is where a slide is drawn on screen, in buffer pixels. The
// image is scaled to fit when its size differs (stand-in renders).
type renderBounds struct {
	dstX, dstY, dstW, dstH int
}

func (b renderBounds) rect() rectF {
	return rectF{x: float64(b.dstX), y: float64(b.dstY), w: float64(b.dstW), h: float64(b.dstH)}
}

type prefetchSig struct {
	listIdx, bufW, bufH, dir int
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

// Update processes input and keeps the displayed slide in step with listIdx.
func (g *Game) Update() error {
	if g.quit {
		return ErrQuit
	}
	g.ticks++
	g.place.tick(ebitenWindow{})
	if g.spawnPresenter != nil && (g.place.settled() || g.ticks >= presenterSpawnTimeout) {
		spawn := g.spawnPresenter
		g.spawnPresenter = nil
		if err := spawn(); err != nil {
			return err
		}
	}
	if g.pipe != nil {
		g.pipe.drain()
	}
	presenterCmds := g.drainPresenterCommands()
	g.updateCursor()
	defer func() { g.stats.tick(g.pipe, g.listIdx) }()

	// Overview intercepts Escape and all navigation while active.
	if g.ov.phase != ovOff {
		err := g.updateOverview(presenterCmds)
		g.refreshDisplay()
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
		g.broadcastState()
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

	g.refreshDisplay()

	// A transition starts animating once the target slide is on screen,
	// so a slow render never eats into the animation.
	if g.trans.active {
		if g.displayIsCurrent() || g.trans.wait >= transitionMaxWaitFrames {
			g.trans.frame++
			if g.trans.frame >= g.trans.frames {
				g.trans.clear()
			}
		} else {
			g.trans.wait++
		}
	}

	g.broadcastState()
	return nil
}

func (g *Game) updateCursor() {
	// Auto-hide cursor after ~3 s of inactivity.
	cx, cy := ebiten.CursorPosition()
	if cx != g.lastCursorX || cy != g.lastCursorY {
		g.lastCursorX = cx
		g.lastCursorY = cy
		g.cursorIdleFrames = 0
		ebiten.SetCursorMode(ebiten.CursorModeVisible)
	} else {
		g.cursorIdleFrames++
		if g.cursorIdleFrames == 180 {
			ebiten.SetCursorMode(ebiten.CursorModeHidden)
		}
	}
}

// audienceFrame captures everything Draw depends on, so unchanged frames
// can be skipped.
type audienceFrame struct {
	img                *ebiten.Image
	bounds             renderBounds
	transActive        bool
	transFrame         int
	blackout, whiteout bool
	listIdx            int
	fraction           float64
	paused             bool
	ovPhase            ovPhase
	ovAnim             float64
	ovSel, ovHover     int
	ovVersion          int
	ovPreview          *ebiten.Image
	bufW, bufH         int
}

func (g *Game) frame() audienceFrame {
	f := audienceFrame{
		img:         g.disp.img,
		bounds:      g.dispBounds,
		transActive: g.trans.active,
		transFrame:  g.trans.frame,
		blackout:    g.blackout,
		whiteout:    g.whiteout,
		listIdx:     g.listIdx,
		bufW:        g.bufW,
		bufH:        g.bufH,
	}
	if g.cfg.Progress {
		f.fraction = g.autoProgressFraction()
		f.paused = g.auto.Paused()
	}
	if g.shouldDrawOverviewOnAudience() {
		f.ovPhase, f.ovAnim = g.ov.phase, g.ov.anim
		f.ovSel, f.ovHover, f.ovVersion = g.ov.selIdx, g.ov.hoverIdx, g.ov.version
		f.ovPreview = g.ov.previewImage(g.pipe, g.disp.img)
	}
	return f
}

// Draw renders the current frame, or nothing when the previous frame is
// still accurate (the screen is not cleared between frames).
func (g *Game) Draw(screen *ebiten.Image) {
	if !g.redraw.needed(g.frame(), time.Now()) {
		return
	}
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
	if g.disp.img != nil {
		if g.trans.active && g.trans.prev.img != nil {
			g.drawTransition(screen)
		} else {
			drawSlide(screen, g.disp.img, g.dispBounds, 0, 1)
		}
	}
	g.drawProgressOverlay(screen)
	if g.shouldDrawOverviewOnAudience() {
		g.ov.draw(screen, g.pipe, g.disp.img, g.dispBounds.rect())
	}
}

// drawSlide draws img scaled into b, shifted by dx pixels, with alpha.
func drawSlide(screen, img *ebiten.Image, b renderBounds, dx float64, alpha float32) {
	iw, ih := img.Bounds().Dx(), img.Bounds().Dy()
	if iw <= 0 || ih <= 0 {
		return
	}
	op := &ebiten.DrawImageOptions{}
	if iw != b.dstW || ih != b.dstH {
		op.GeoM.Scale(float64(b.dstW)/float64(iw), float64(b.dstH)/float64(ih))
	}
	op.GeoM.Translate(float64(b.dstX)+dx, float64(b.dstY))
	op.ColorScale.ScaleAlpha(alpha)
	op.Filter = ebiten.FilterLinear
	screen.DrawImage(img, op)
}

func (g *Game) shouldDrawOverviewOnAudience() bool {
	return g.ov.phase != ovOff && g.stateCh == nil
}

// Layout returns the buffer size in physical pixels.
//
// outsideWidth/outsideHeight come from Ebiten in device-independent units;
// multiplying by the active monitor's scale factor produces a pixel-sized
// buffer that PDFium then rasterises into — pixel-perfect on Retina and 4K.
// When the buffer changes (resize, monitor swap) the cache key changes and
// slides are re-rasterised; the old render stands in meanwhile.
func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	pxW, pxH := pixelBuffer(outsideWidth, outsideHeight, g.cfg.RenderScale)
	if pxW != g.bufW || pxH != g.bufH {
		g.bufW = pxW
		g.bufH = pxH
		if g.pipe != nil {
			g.pipe.resized(autoBudget(pxW, pxH), g.cfg.CacheMB)
		}
	}
	return pxW, pxH
}

// autoBudget sizes the GPU image cache to hold ~cacheBudgetPages full-buffer
// pages (counted at their real texture size), with a floor for small
// windows and a ceiling for high-DPI setups. The ceiling never drops below
// three pages (current, next, previous), or 5K+ displays could not even
// keep the next slide. The user can override entirely via --cache-mb.
func autoBudget(bufW, bufH int) int {
	if bufW <= 0 || bufH <= 0 {
		return cacheBudgetMin
	}
	page := pdf.TextureBytes(bufW, bufH)
	return min(max(cacheBudgetMax, 3*page), max(cacheBudgetMin, page*cacheBudgetPages))
}

func (g *Game) currentPage() int { return g.pageList[g.listIdx] }

// displayIsCurrent reports whether the image on screen shows the current
// page (possibly as a scaled stand-in).
func (g *Game) displayIsCurrent() bool {
	return g.disp.img != nil && g.disp.key.Page == g.currentPage()
}

// refreshDisplay keeps the on-screen slide in step with listIdx without
// ever rendering on this goroutine: an exact cache hit is a pointer swap;
// otherwise the page is requested urgently and the best cached render of
// it (any size) stands in. If the page isn't cached at all, the previous
// slide stays up until the render lands.
func (g *Game) refreshDisplay() {
	if g.pipe == nil || g.bufW <= 0 || g.bufH <= 0 {
		return
	}
	want, offX, offY, ok := g.pipe.fit(g.currentPage(), g.bufW, g.bufH)
	if !ok {
		return
	}
	if want != g.dispWanted {
		g.dispWanted = want
		g.pipe.requestNow(want)
	}
	if g.disp.key != want {
		if img, key := g.pipe.resolve(want); img != nil {
			g.disp.set(g.pipe.slides, img, key)
		}
	}
	if g.displayIsCurrent() {
		g.dispBounds = renderBounds{dstX: offX, dstY: offY, dstW: want.W, dstH: want.H}
	}
	g.updatePrefetch()
}

// updatePrefetch queues the slides the next navigation will most likely
// need, ordered by the direction of travel. The queue is replaced (not
// appended to) whenever position or size changes, so stale work is dropped.
func (g *Game) updatePrefetch() {
	sig := prefetchSig{listIdx: g.listIdx, bufW: g.bufW, bufH: g.bufH, dir: g.lastNavDir}
	if sig == g.prefetchFor {
		return
	}
	g.prefetchFor = sig
	deltas := []int{1, -1}
	if g.lastNavDir != 0 {
		// Look further ahead in the direction of travel, as far as the
		// cache can hold without evicting what we prefetched.
		deltas = deltas[:0]
		ahead := g.pipe.prefetchAhead(pdf.TextureBytes(g.bufW, g.bufH))
		for d := 1; d <= ahead; d++ {
			deltas = append(deltas, d*g.lastNavDir)
		}
		deltas = append(deltas, -g.lastNavDir)
	}
	var keys []pdf.CacheKey
	for _, d := range deltas {
		idx, ok := g.wrapIndex(g.listIdx + d)
		if !ok {
			continue
		}
		if key, _, _, ok := g.pipe.fit(g.pageList[idx], g.bufW, g.bufH); ok {
			keys = append(keys, key)
		}
	}
	g.pipe.sched.SetPrefetch(g.pipe.missing(keys...))
}

// wrapIndex maps a list index that may run off either end onto the list,
// honouring --loop.
func (g *Game) wrapIndex(idx int) (int, bool) {
	n := len(g.pageList)
	if idx >= 0 && idx < n {
		return idx, true
	}
	if !g.cfg.Loop || n == 0 {
		return 0, false
	}
	return (idx%n + n) % n, true
}

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

// presenterState snapshots what the presenter window needs to know.
func (g *Game) presenterState() ipc.PresenterState {
	nextPage := -1
	if idx, ok := g.wrapIndex(g.listIdx + 1); ok {
		nextPage = g.pageList[idx]
	}
	st := ipc.PresenterState{
		Page:           g.currentPage(),
		ListIndex:      g.listIdx,
		NextPage:       nextPage,
		Paused:         g.auto.Paused(),
		Fraction:       g.autoProgressFraction(),
		FractionPerSec: g.auto.Rate(),
	}
	if g.ov.phase != ovOff {
		st.Overview = ipc.PresenterOverviewState{
			Active:        true,
			Phase:         int(g.ov.phase),
			Anim:          g.ov.anim,
			FromIndex:     g.ov.fromIdx,
			SelectedIndex: g.ov.selIdx,
			ExitToIndex:   g.ov.exitToIdx,
		}
	}
	return st
}

// broadcastState sends the presentation state to the presenter process when
// it changed (the auto-advance fraction is extrapolated on the other side,
// with a once-a-second refresh to correct drift). Non-blocking: if the IPC
// goroutine is behind, the stale queued state is replaced.
func (g *Game) broadcastState() {
	if g.stateCh == nil {
		return
	}
	st := g.presenterState()
	cur, last := st, g.lastSent
	cur.Fraction, last.Fraction = 0, 0
	now := time.Now()
	if !g.lastSentAt.IsZero() && cur == last && now.Sub(g.lastSentAt) < time.Second {
		return
	}
	g.lastSent, g.lastSentAt = st, now
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
	// Digits (top row or numpad) accumulate into the jump buffer.
	for k := ebiten.KeyDigit0; k <= ebiten.KeyDigit9; k++ {
		if inpututil.IsKeyJustPressed(k) {
			g.appendDigit(int(k - ebiten.KeyDigit0))
		}
	}
	for k := ebiten.KeyNumpad0; k <= ebiten.KeyNumpad9; k++ {
		if inpututil.IsKeyJustPressed(k) {
			g.appendDigit(int(k - ebiten.KeyNumpad0))
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
		g.goTo(0, 0)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEnd) || presenterCmdPressed(presenterCmds, presenterCmdEnd) {
		g.goTo(len(g.pageList)-1, 0)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF) || presenterCmdPressed(presenterCmds, presenterCmdFullscreen) {
		g.place = toggleFullscreen(g.place, g.monitor)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyL) || presenterCmdPressed(presenterCmds, presenterCmdReturnLast) {
		g.goTo(g.prevListIdx, 0)
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

// advance moves listIdx by delta, looping if --loop is set, else stopping.
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
			return
		}
	case next >= len(g.pageList):
		if g.cfg.Loop {
			next = 0
		} else if g.cfg.AutoQuit {
			g.quit = true
			return
		} else {
			return
		}
	}
	if next == g.listIdx {
		return
	}
	g.lastNavDir = sign(delta)
	g.goTo(next, g.lastNavDir)
}

// goTo moves to list index idx, animating in direction dir (0 = jump,
// which fades), and remembers the previous position for "l". It does
// nothing when idx is already current.
func (g *Game) goTo(idx, dir int) {
	if idx < 0 || idx >= len(g.pageList) || idx == g.listIdx {
		return
	}
	g.beginTransition(dir)
	g.prevListIdx = g.listIdx
	g.listIdx = idx
}

// jumpTo1Indexed seeks to a doc page (1-indexed). If it's been filtered out by
// --pages, the nearest later page is selected.
func (g *Game) jumpTo1Indexed(n int) {
	g.goTo(listIndexForPage(g.pageList, n-1), 0)
}

// listIndexForPage returns the play-list position showing page (0-indexed),
// or the next later page if it was filtered out, or the last position.
func listIndexForPage(pageList []int, page int) int {
	for i, p := range pageList {
		if p == page {
			return i
		}
	}
	for i, p := range pageList {
		if p > page {
			return i
		}
	}
	return len(pageList) - 1
}
