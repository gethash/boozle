package app

import (
	"errors"
	"fmt"
	"image/color"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/text/v2"

	"github.com/gethash/boozle/internal/display"
	"github.com/gethash/boozle/internal/ipc"
	"github.com/gethash/boozle/internal/pdf"
)

// PresenterOptions configures the presenter-view subprocess. CacheMB and
// RenderScale mirror the master's --cache-mb and --render-scale.
type PresenterOptions struct {
	SocketPath  string
	PDFPath     string
	Monitor     string  // index or name
	Windowed    bool    // --no-fullscreen: open as a normal window
	CacheMB     int     // 0 = auto
	RenderScale float64 // 0 = native
	DebugStats  bool
}

// PresenterGame is an ebiten.Game that renders the presenter view: current
// slide and next-slide preview on top, speaker notes and clock, slide
// counter, elapsed time and progress in a bottom row (see presenterLayout).
type PresenterGame struct {
	pipe     *pipeline
	receiver *ipc.Receiver
	place    *monitorPlacer
	hello    ipc.Hello
	hasHello bool
	opts     PresenterOptions

	bufW, bufH int

	cur, next   pinnedSlide
	curWanted   pdf.CacheKey
	prefetchFor presenterPrefetchSig

	counterCache   pageLabelCache
	elapsedCache   elapsedCache
	clockCache     clockCache
	countdownCache countdownCache
	notes          notesLayout
	statusBar      segmentedBar

	ov     overview
	redraw redrawGate[presenterFrame]
	stats  *statsReporter
}

type presenterPrefetchSig struct {
	listIdx      int
	curW, curH   int
	nextW, nextH int
}

// elapsedCache memoises the HH:MM:SS string by integer seconds.
type elapsedCache struct {
	seconds int64
	s       string
}

func (e *elapsedCache) String(seconds int64) string {
	if e.s == "" || e.seconds != seconds {
		e.seconds = seconds
		e.s = formatElapsed(seconds)
	}
	return e.s
}

// formatElapsed renders seconds as HH:MM:SS.
func formatElapsed(seconds int64) string {
	seconds = max(0, seconds)
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
}

// clockCache memoises the wall-clock string at integer-second resolution.
type clockCache struct {
	unix int64
	s    string
}

func (c *clockCache) String(now time.Time) string {
	sec := now.Unix()
	if c.s == "" || c.unix != sec {
		c.unix = sec
		c.s = now.Format("15:04:05")
	}
	return c.s
}

// RunPresenter opens the presenter window and blocks until the window closes
// or the master process disconnects. It is the entry point for the
// presenter-slave subprocess.
func RunPresenter(opts PresenterOptions) error {
	opened := time.Now()
	doc, err := pdf.Open(opts.PDFPath)
	if err != nil {
		return err
	}
	defer doc.Close()
	if opts.DebugStats {
		fmt.Fprintf(os.Stderr, "[boozle presenter] pdf open %v\n", time.Since(opened).Round(time.Millisecond))
	}

	pipe := newPipeline(doc, opts.PDFPath, opts.CacheMB)
	defer pipe.close()

	receiver, err := ipc.Connect(opts.SocketPath, func() { os.Exit(0) })
	if err != nil {
		return fmt.Errorf("presenter: connect to master: %w", err)
	}

	g := &PresenterGame{pipe: pipe, receiver: receiver, opts: opts}
	if opts.DebugStats {
		g.stats = &statsReporter{role: "presenter"}
	}

	ebiten.SetWindowTitle("boozle — Presenter View")
	setWindowIcon()
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetScreenClearedEveryFrame(false)
	monitor, err := display.Resolve(opts.Monitor)
	if err != nil {
		return fmt.Errorf("presenter --monitor: %w", err)
	}
	if err := display.PrepareWindow(monitor, 1280, 800); err != nil {
		return err
	}
	if !opts.Windowed {
		g.place = newMonitorPlacer(monitor)
		g.place.debug = opts.DebugStats
		g.place.start()
	}

	err = ebiten.RunGameWithOptions(g, &ebiten.RunGameOptions{X11ClassName: "boozle", X11InstanceName: "presenter"})
	if err != nil && !errors.Is(err, ErrQuit) {
		return err
	}
	return nil
}

func (g *PresenterGame) Update() error {
	g.place.tick(ebitenWindow{})
	g.pipe.drain()
	if !g.hasHello {
		g.hello, g.hasHello = g.receiver.Hello()
	}
	st, _, _ := g.receiver.Latest()
	if g.hasHello && g.receiver.Ready() && len(g.hello.PageList) > 0 && g.bufW > 0 && g.bufH > 0 {
		g.syncOverview(st)
		g.refreshPanes(st)
	}
	g.forwardInput()
	st2, _, _ := g.receiver.Latest()
	g.stats.tick(g.pipe, st2.ListIndex)
	return nil
}

// syncOverview mirrors the master's overview session.
func (g *PresenterGame) syncOverview(st ipc.PresenterState) {
	if !st.Overview.Active {
		if g.ov.phase != ovOff {
			g.ov.release(g.pipe)
		}
		return
	}
	n := len(g.hello.PageList)
	if g.ov.phase == ovOff || g.ov.bufW != g.bufW || g.ov.bufH != g.bufH {
		g.ov.release(g.pipe)
		g.ov = newOverview(g.hello.PageList, g.bufW, g.bufH, st.Overview.FromIndex)
		g.ov.attach(g.pipe)
	}
	g.ov.phase = ovPhase(st.Overview.Phase)
	g.ov.anim = st.Overview.Anim
	g.ov.fromIdx = clampListIndex(st.Overview.FromIndex, n)
	g.ov.selIdx = clampListIndex(st.Overview.SelectedIndex, n)
	g.ov.exitToIdx = clampListIndex(st.Overview.ExitToIndex, n)
	if g.ov.phase == ovExiting {
		g.pipe.sched.ClearBackground()
	}
}

// refreshPanes keeps the current and next panes in step with the master
// without rendering on this goroutine. Requests are prioritised: current
// pane (urgent) → next pane and overview (visible) → what the next advance
// will need (prefetch). While a pane's exact render is pending, the best
// cached render of the same page stands in — after an advance that is the
// old next-pane image, upscaled for a moment.
func (g *PresenterGame) refreshPanes(st ipc.PresenterState) {
	lo := g.presenterLayout()

	if key, _, _, ok := g.pipe.fit(st.Page, lo.currentContent.w, lo.currentContent.h); ok {
		if key != g.curWanted {
			g.curWanted = key
			g.pipe.requestNow(key)
		}
		if g.cur.key != key {
			if img, got := g.pipe.resolve(key); img != nil {
				g.cur.set(g.pipe.slides, img, got)
			}
		}
	}

	var visible []pdf.Job
	if st.NextPage >= 0 {
		if key, _, _, ok := g.pipe.fit(st.NextPage, lo.nextContent.w, lo.nextContent.h); ok {
			visible = append(visible, g.pipe.missing(key)...)
			if g.next.key != key {
				if img, got := g.pipe.resolve(key); img != nil {
					g.next.set(g.pipe.slides, img, got)
				}
			}
		}
	} else {
		g.next.release()
	}
	visible = append(visible, g.ov.visibleJobs(g.pipe, time.Now())...)
	g.pipe.sched.SetVisible(visible)

	g.updatePrefetch(st, lo)
}

// updatePrefetch queues what advancing (or stepping back) will need: the
// next slide at current-pane size, the slide after it at next-pane size,
// and the previous slide at current-pane size.
func (g *PresenterGame) updatePrefetch(st ipc.PresenterState, lo presenterLayout) {
	sig := presenterPrefetchSig{
		listIdx: st.ListIndex,
		curW:    lo.currentContent.w, curH: lo.currentContent.h,
		nextW: lo.nextContent.w, nextH: lo.nextContent.h,
	}
	if sig == g.prefetchFor {
		return
	}
	g.prefetchFor = sig
	var keys []pdf.CacheKey
	add := func(delta int, box presenterRect) {
		idx, ok := g.listIndex(st.ListIndex + delta)
		if !ok {
			return
		}
		if key, _, _, ok := g.pipe.fit(g.hello.PageList[idx], box.w, box.h); ok {
			keys = append(keys, key)
		}
	}
	add(+1, lo.currentContent)
	add(+2, lo.nextContent)
	add(-1, lo.currentContent)
	g.pipe.sched.SetPrefetch(g.pipe.missing(keys...))
}

func (g *PresenterGame) listIndex(idx int) (int, bool) {
	n := len(g.hello.PageList)
	if idx >= 0 && idx < n {
		return idx, true
	}
	if !g.hello.Loop || n == 0 {
		return 0, false
	}
	return (idx%n + n) % n, true
}

// presenterFrame captures everything Draw depends on, so unchanged frames
// can be skipped.
type presenterFrame struct {
	seq            uint64
	cur, next      *ebiten.Image
	second         int64
	fraction       int // per mille
	ovPhase        ovPhase
	ovAnim         float64
	ovSel, ovHover int
	ovVersion      int
	ovPreview      *ebiten.Image
	bufW, bufH     int
}

func (g *PresenterGame) frame(st ipc.PresenterState, recvAt time.Time, seq uint64, now time.Time) presenterFrame {
	f := presenterFrame{
		seq:      seq,
		cur:      g.cur.img,
		next:     g.next.img,
		second:   now.Unix(),
		fraction: int(st.FractionAt(recvAt, now) * 1000),
		bufW:     g.bufW,
		bufH:     g.bufH,
	}
	if g.ov.phase != ovOff {
		f.ovPhase, f.ovAnim = g.ov.phase, g.ov.anim
		f.ovSel, f.ovHover, f.ovVersion = g.ov.selIdx, g.ov.hoverIdx, g.ov.version
		f.ovPreview = g.ov.previewImage(g.pipe, g.cur.img)
	}
	return f
}

func (g *PresenterGame) Draw(screen *ebiten.Image) {
	st, recvAt, seq := g.receiver.Latest()
	now := time.Now()
	if !g.redraw.needed(g.frame(st, recvAt, seq, now), now) {
		return
	}
	screen.Fill(tokGround)

	if g.bufW <= 0 || g.bufH <= 0 {
		return
	}

	lo := g.presenterLayout()
	u := uiUnit(g.bufW, g.bufH)
	drawLabelledPanel(screen, lo.currentPanel, "Current", u)
	drawLabelledPanel(screen, lo.nextPanel, "Next", u)
	drawLabelledPanel(screen, lo.notesPanel, "Notes", u)
	drawPanel(screen, lo.statusPanel, u)

	drawImageInPresenterRect(screen, g.cur.img, lo.currentContent)
	drawImageInPresenterRect(screen, g.next.img, lo.nextContent)
	g.drawNotesPanel(screen, lo.notesContent, st)
	g.drawPresenterStatus(screen, lo.statusPanel, st, recvAt, now)
	if g.ov.phase != ovOff {
		var curRect rectF
		if g.cur.img != nil {
			curRect = imageFitRect(g.cur.img, lo.currentContent)
		}
		g.ov.draw(screen, g.pipe, g.cur.img, curRect)
	}
}

func (g *PresenterGame) Layout(outsideW, outsideH int) (int, int) {
	pxW, pxH := pixelBuffer(outsideW, outsideH, g.opts.RenderScale)
	if pxW != g.bufW || pxH != g.bufH {
		g.bufW = pxW
		g.bufH = pxH
		g.pipe.resized(presenterBudget(g.presenterLayout()), g.opts.CacheMB)
	}
	return pxW, pxH
}

// presenterBudget sizes the presenter's slide cache from its pane sizes
// rather than the full window: the current and next panes, what the next
// advance needs (next at current size, the one after at next size), the
// previous slide, and room for stand-ins and the overview preview.
func presenterBudget(lo presenterLayout) int {
	cur := pdf.TextureBytes(lo.currentContent.w, lo.currentContent.h)
	next := pdf.TextureBytes(lo.nextContent.w, lo.nextContent.h)
	return min(max(cacheBudgetMax, 2*cur+2*next), max(cacheBudgetMin, 3*cur+3*next))
}

type presenterRect struct {
	x, y, w, h int
}

// presenterLayout is the presenter window's grid: current and next slide
// side by side on top, speaker notes and the status panel (clock, slide
// counter, elapsed time, progress) below. The bottom row always gets at
// least a third of the window so notes stay readable.
type presenterLayout struct {
	currentPanel   presenterRect
	currentContent presenterRect
	nextPanel      presenterRect
	nextContent    presenterRect
	notesPanel     presenterRect
	notesContent   presenterRect
	statusPanel    presenterRect
}

const (
	presenterBottomMinH = 240 // notes/status row floor in pixels
	presenterRightMinW  = 360 // next/status column floor in pixels
)

func (g *PresenterGame) presenterLayout() presenterLayout {
	margin := max(18, min(g.bufW, g.bufH)/52)
	gap := max(14, margin/2)
	fullW := max(1, g.bufW-margin*2)
	fullH := max(1, g.bufH-margin*2)

	rightW := int(float64(fullW) * 0.35)
	rightW = max(rightW, presenterRightMinW)
	rightW = max(1, min(rightW, fullW/2))
	leftW := max(1, fullW-gap-rightW)

	bottomH := max(g.bufH/3, presenterBottomMinH)
	bottomH = max(1, min(bottomH, fullH/2))
	topH := max(1, fullH-gap-bottomH)

	currentPanel := presenterRect{x: margin, y: margin, w: leftW, h: topH}
	nextPanel := presenterRect{x: margin + leftW + gap, y: margin, w: rightW, h: topH}
	bottomY := margin + topH + gap
	notesPanel := presenterRect{x: margin, y: bottomY, w: leftW, h: bottomH}
	statusPanel := presenterRect{x: nextPanel.x, y: bottomY, w: rightW, h: bottomH}

	pad, head := panelInsets(uiUnit(g.bufW, g.bufH))
	return presenterLayout{
		currentPanel:   currentPanel,
		currentContent: insetPresenterRect(currentPanel, pad, head, pad, pad),
		nextPanel:      nextPanel,
		nextContent:    insetPresenterRect(nextPanel, pad, head, pad, pad),
		notesPanel:     notesPanel,
		notesContent:   insetPresenterRect(notesPanel, pad, head, pad, pad),
		statusPanel:    statusPanel,
	}
}

// panelInsets returns a labelled panel's content padding and the top inset
// that leaves room for its eyebrow label, in buffer pixels.
func panelInsets(u float64) (pad, head int) {
	pad = int(math.Round(space3 * u))
	head = int(math.Round((space3 + eyebrowLineH + space2) * u))
	return pad, head
}

func insetPresenterRect(r presenterRect, left, top, right, bottom int) presenterRect {
	return presenterRect{
		x: r.x + left,
		y: r.y + top,
		w: max(1, r.w-left-right),
		h: max(1, r.h-top-bottom),
	}
}

func (g *PresenterGame) send(name string, arg int) {
	g.receiver.SendCommand(ipc.PresenterCommand{Name: name, Arg: arg})
}

// presenterKeys maps keys pressed in the presenter window to the commands
// forwarded to the master, which owns all presentation state.
var presenterKeys = []struct {
	key ebiten.Key
	cmd string
}{
	{ebiten.KeyQ, presenterCmdQuit},
	{ebiten.KeyEscape, presenterCmdEscape},
	{ebiten.KeyTab, presenterCmdTab},
	{ebiten.KeyP, presenterCmdPause},
	{ebiten.KeyB, presenterCmdBlackout},
	{ebiten.KeyW, presenterCmdWhiteout},
	{ebiten.KeyF, presenterCmdFullscreen},
	{ebiten.KeyL, presenterCmdReturnLast},
	{ebiten.KeyHome, presenterCmdHome},
	{ebiten.KeyEnd, presenterCmdEnd},
	{ebiten.KeyEnter, presenterCmdEnter},
	{ebiten.KeyNumpadEnter, presenterCmdEnter},
	{ebiten.KeyBackspace, presenterCmdBackspace},
	{ebiten.KeyArrowRight, presenterCmdRight},
	{ebiten.KeyArrowLeft, presenterCmdLeft},
	{ebiten.KeyArrowDown, presenterCmdDown},
	{ebiten.KeyArrowUp, presenterCmdUp},
	{ebiten.KeySpace, presenterCmdSpace},
	{ebiten.KeyPageDown, presenterCmdPageDown},
	{ebiten.KeyPageUp, presenterCmdPageUp},
}

func (g *PresenterGame) forwardInput() {
	if g.ov.phase == ovActive {
		if clicked := g.ov.updateHover(); clicked >= 0 {
			g.send(presenterCmdOverviewGo, clicked)
		}
	}
	for _, k := range presenterKeys {
		if inpututil.IsKeyJustPressed(k.key) {
			g.send(k.cmd, 0)
		}
	}
	for k := ebiten.KeyDigit0; k <= ebiten.KeyDigit9; k++ {
		if inpututil.IsKeyJustPressed(k) {
			g.send(presenterCmdDigit, int(k-ebiten.KeyDigit0))
		}
	}
	for k := ebiten.KeyNumpad0; k <= ebiten.KeyNumpad9; k++ {
		if inpututil.IsKeyJustPressed(k) {
			g.send(presenterCmdDigit, int(k-ebiten.KeyNumpad0))
		}
	}

	_, wy := ebiten.Wheel()
	if wy < 0 {
		g.send(presenterCmdSpace, 0)
	} else if wy > 0 {
		g.send(presenterCmdPageUp, 0)
	}
}

// eyebrowLineH is the eyebrow style's line height in design pixels.
const eyebrowLineH = 16

// drawEyebrow draws an uppercase eyebrow label with its top-left at (x, y).
func drawEyebrow(dst *ebiten.Image, s string, x, y, u float64, clr color.Color) {
	drawStyled(dst, strings.ToUpper(s), styleEyebrow, u, x, y, text.AlignStart, text.AlignStart, clr)
}

// drawLabelledPanel draws a panel with an eyebrow label in its top-left
// corner, aligned with the panel's content inset.
func drawLabelledPanel(dst *ebiten.Image, r presenterRect, label string, u float64) {
	drawPanel(dst, r, u)
	pad, _ := panelInsets(u)
	drawEyebrow(dst, label, float64(r.x+pad), float64(r.y+pad), u, tokInkFaint)
}

func drawImageInPresenterRect(screen, img *ebiten.Image, r presenterRect) {
	if img == nil || r.w <= 0 || r.h <= 0 {
		return
	}
	iw := img.Bounds().Dx()
	ih := img.Bounds().Dy()
	if iw <= 0 || ih <= 0 {
		return
	}
	w, h, offX, offY := aspectFit(float64(iw), float64(ih), r.w, r.h)
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(float64(w)/float64(iw), float64(h)/float64(ih))
	op.GeoM.Translate(float64(r.x+offX), float64(r.y+offY))
	op.Filter = ebiten.FilterLinear
	screen.DrawImage(img, op)
}

// Largest value sizes in design pixels: the clock and slide counter, and
// the elapsed time (the presenter's main number).
const (
	statusClockMaxPx   = 30
	statusElapsedMaxPx = 52
	statusValueMinPx   = 18 // two card rows need room for at least this
)

func (g *PresenterGame) drawPresenterStatus(screen *ebiten.Image, r presenterRect, st ipc.PresenterState, recvAt, now time.Time) {
	if r.w <= 0 || r.h <= 0 {
		return
	}
	u := uiUnit(g.bufW, g.bufH)
	lo := layoutPresenterStatus(r, u)

	total := len(g.hello.PageList)
	counter := "-- / --"
	if g.hasHello && total > 0 {
		counter = g.counterCache.String(st.ListIndex, total)
	}
	elapsed := int64(0)
	if g.hasHello {
		elapsed = int64(now.Sub(g.hello.StartedAt).Seconds())
	}
	clock := g.clockCache.String(now)
	elapsedStr := g.elapsedCache.String(elapsed)

	drawStatusCard(screen, lo.clock, "Clock", clock, statusClockMaxPx*u, u)
	drawStatusCard(screen, lo.slide, "Slide", counter, statusClockMaxPx*u, u)

	// Elapsed time, with the current slide's countdown ring on the right
	// while auto-advance runs.
	e := lo.elapsed
	fillRoundRect(screen, float32(e.x), float32(e.y), float32(e.w), float32(e.h), float32(radiusSM*u), tokSurfaceRaised)
	if st.SlideSeconds > 0 {
		if ring, ok := statusRingBox(lo.elapsed, u); ok {
			remaining := st.RemainingAt(recvAt, now)
			drawStatusRing(screen, ring, g.countdownCache.String(remaining), remaining.Seconds()/st.SlideSeconds, st.Paused)
			e.w = max(1, int(ring.x-space3*u)-e.x)
		}
	}
	drawStatusCardContent(screen, e, "Elapsed", elapsedStr, statusElapsedMaxPx*u, u)

	label, clr := "Progress", color.Color(tokInkFaint)
	switch {
	case st.Paused:
		label, clr = "Paused", tokWarning
	case st.SlideSeconds > 0:
		label = "Timer"
	}
	drawEyebrow(screen, label, float64(lo.bar.x), float64(lo.barLabelY), u, clr)
	g.statusBar.draw(screen, lo.bar.x, lo.bar.y, lo.bar.w, lo.bar.h,
		total, st.ListIndex, st.FractionAt(recvAt, now), st.Paused, tokTrack, float32(max(2, math.Round(space1*u))))
}

// presenterStatusLayout places the clock, slide and elapsed cards and the
// progress bar inside the status panel.
type presenterStatusLayout struct {
	clock, slide, elapsed presenterRect
	barLabelY             int
	bar                   presenterRect
}

// statusCardMinH is the card height that fits a label and a value of
// statusValueMinPx.
func statusCardMinH(u float64) int {
	return int(math.Ceil((2*space3 + eyebrowLineH + space1 + statusValueMinPx*0.75*capsHeightRatio) * u))
}

// layoutPresenterStatus lays out the status panel: CLOCK | SLIDE cards over
// a wide ELAPSED card, then the progress bar. When the panel is too short
// for two rows of cards (small windows, --render-scale), the three cards
// share one row.
func layoutPresenterStatus(r presenterRect, u float64) presenterStatusLayout {
	px := func(v float64) int { return int(math.Round(v * u)) }
	pad, gap := px(space4), px(space3)
	innerX, innerY := r.x+pad, r.y+pad
	innerW := max(1, r.w-pad*2)
	innerH := max(1, r.h-pad*2)

	barH := max(4, px(6))
	labelH, labelGap := px(eyebrowLineH), px(space2)
	avail := max(1, innerH-labelH-labelGap-barH-gap)

	var lo presenterStatusLayout
	cardMin := statusCardMinH(u)
	if avail >= 2*cardMin+gap {
		cardH := max(cardMin, (avail-gap)*9/20)
		halfW := max(1, (innerW-gap)/2)
		lo.clock = presenterRect{x: innerX, y: innerY, w: halfW, h: cardH}
		lo.slide = presenterRect{x: innerX + halfW + gap, y: innerY, w: max(1, innerW-halfW-gap), h: cardH}
		lo.elapsed = presenterRect{x: innerX, y: innerY + cardH + gap, w: innerW, h: max(1, avail-cardH-gap)}
	} else {
		w := max(1, (innerW-2*gap)/3)
		lo.clock = presenterRect{x: innerX, y: innerY, w: w, h: avail}
		lo.slide = presenterRect{x: innerX + w + gap, y: innerY, w: w, h: avail}
		lo.elapsed = presenterRect{x: innerX + 2*(w+gap), y: innerY, w: max(1, innerW-2*(w+gap)), h: avail}
	}
	lo.barLabelY = innerY + avail + gap
	lo.bar = presenterRect{x: innerX, y: lo.barLabelY + labelH + labelGap, w: innerW, h: barH}
	return lo
}

// statusValueBox is the area below a card's label that its value fills.
func statusValueBox(r presenterRect, u float64) rectF {
	padX, padY := space4*u, space3*u
	top := padY + (eyebrowLineH+space1)*u
	return rectF{
		x: float64(r.x) + padX,
		y: float64(r.y) + top,
		w: max(0, float64(r.w)-2*padX),
		h: max(0, float64(r.h)-top-padY),
	}
}

// statusValueSize is the timer-style text size, at most maxPx, at which
// value fills card r's value box.
func statusValueSize(r presenterRect, value string, maxPx, u float64) float64 {
	b := statusValueBox(r, u)
	return fitTextSize(value, styleTimer, b.w, b.h, maxPx)
}

func drawStatusCard(dst *ebiten.Image, r presenterRect, label, value string, maxPx, u float64) {
	fillRoundRect(dst, float32(r.x), float32(r.y), float32(r.w), float32(r.h), float32(radiusSM*u), tokSurfaceRaised)
	drawStatusCardContent(dst, r, label, value, maxPx, u)
}

// drawStatusCardContent draws a card's eyebrow label and its value, sized
// to fit and centred vertically in the value box.
func drawStatusCardContent(dst *ebiten.Image, r presenterRect, label, value string, maxPx, u float64) {
	drawEyebrow(dst, label, float64(r.x)+space4*u, float64(r.y)+space3*u, u, tokInkFaint)
	size := statusValueSize(r, value, maxPx, u)
	if size < 6 {
		return
	}
	b := statusValueBox(r, u)
	f := styleTimer.faceAt(size)
	drawCapsCentred(dst, value, f, b.x, b.y+b.h/2, text.AlignStart, styleTimer.trackingPx(f), tokInk)
}

// statusRingBox is where the countdown ring sits in the elapsed card: a
// square at its right edge, or false when the card is too small for one.
func statusRingBox(card presenterRect, u float64) (rectF, bool) {
	pad := space3 * u
	d := math.Floor(min(float64(card.h)-2*pad, float64(card.w)*0.4))
	if d < 40*u {
		return rectF{}, false
	}
	return rectF{
		x: float64(card.x+card.w) - space4*u - d,
		y: float64(card.y) + (float64(card.h)-d)/2,
		w: d, h: d,
	}, true
}

// drawStatusRing draws the countdown ring in box with the remaining time in
// its centre.
func drawStatusRing(dst *ebiten.Image, box rectF, label string, remaining float64, paused bool) {
	d := box.w
	width := max(3, math.Round(d*0.07))
	cx, cy := box.x+d/2, box.y+d/2
	drawCountdownRing(dst, float32(cx), float32(cy), float32(d), float32(width), remaining)
	inner := d - 2*width
	size := fitTextSize(label, styleTimer, inner*0.72, inner*0.4, d*0.24)
	if size < 6 {
		return
	}
	clr := color.Color(tokInk)
	if paused {
		clr = tokWarning
	}
	f := styleTimer.faceAt(size)
	drawCapsCentred(dst, label, f, cx, cy, text.AlignCenter, styleTimer.trackingPx(f), clr)
}

// drawNotesPanel draws the current slide's speaker notes into r, or a dim
// placeholder when the slide has none.
func (g *PresenterGame) drawNotesPanel(screen *ebiten.Image, r presenterRect, st ipc.PresenterState) {
	u := uiUnit(g.bufW, g.bufH)
	notes := g.hello.Notes[st.Page+1]
	if strings.TrimSpace(notes) == "" {
		if g.hasHello {
			minPx, _ := notesSizeRange(u)
			drawText(screen, "No notes for this slide", styleNotes.faceAt(minPx), float64(r.x), float64(r.y),
				text.AlignStart, text.AlignStart, 0, tokInkFaint)
		}
		return
	}
	g.drawNotes(screen, notes, r, u)
}

// notesLineSpacing is the notes' line height as a multiple of the text size.
const notesLineSpacing = 1.5

// notesSizeRange is the range of notes text sizes in buffer pixels: body-s
// as the floor, larger when the notes are short enough.
func notesSizeRange(u float64) (minPx, maxPx float64) {
	return math.Round(styleNotes.size * u), math.Round(26 * u)
}

// notesLayout caches the wrapped lines and chosen text size of the current
// speaker notes, so fitting only reruns when the note or the area changes.
type notesLayout struct {
	text         string
	w, h         int
	minPx, maxPx float64
	size         float64
	lines        []string
}

// fit picks the largest size in [minPx, maxPx] at which text, wrapped to
// width w, fits height h, and returns the wrapped lines. When it doesn't
// fit even at minPx, the lines at that size are returned and the caller
// truncates.
func (n *notesLayout) fit(s string, w, h int, minPx, maxPx float64) ([]string, float64) {
	if n.lines != nil && n.text == s && n.w == w && n.h == h && n.minPx == minPx && n.maxPx == maxPx {
		return n.lines, n.size
	}
	n.text, n.w, n.h, n.minPx, n.maxPx = s, w, h, minPx, maxPx
	step := max(1, math.Round(minPx/15))
	for size := max(maxPx, minPx); ; size = max(minPx, size-step) {
		f := styleNotes.faceAt(size)
		n.lines = wrapText(s, float64(w), func(t string) float64 { return text.Advance(t, f) })
		n.size = size
		if float64(len(n.lines))*math.Round(size*notesLineSpacing) <= float64(h) || size <= minPx {
			break
		}
	}
	return n.lines, n.size
}

func (g *PresenterGame) drawNotes(screen *ebiten.Image, s string, r presenterRect, u float64) {
	if s == "" || r.w <= 0 || r.h <= 0 {
		return
	}
	minPx, maxPx := notesSizeRange(u)
	lines, size := g.notes.fit(s, r.w, r.h, minPx, maxPx)
	f := styleNotes.faceAt(size)
	lineH := math.Round(size * notesLineSpacing)
	y, maxY := float64(r.y), float64(r.y+r.h)
	for i, line := range lines {
		if y+lineH > maxY {
			return
		}
		if i < len(lines)-1 && y+2*lineH > maxY {
			// Last line that fits, more to come: mark the cut.
			line = "…"
		}
		drawText(screen, line, f, float64(r.x), y, text.AlignStart, text.AlignStart, 0, tokInkMuted)
		y += lineH
	}
}

// wrapText word-wraps s so no line is wider than maxW as measured by adv,
// hard-splitting words that are wider than a line on their own.
func wrapText(s string, maxW float64, adv func(string) float64) []string {
	space := adv(" ")
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line, lineW := "", 0.0
		for _, word := range words {
			ww := adv(word)
			for ww > maxW && utf8.RuneCountInString(word) > 1 {
				if line != "" {
					lines = append(lines, line)
					line, lineW = "", 0
				}
				cut := prefixWithin(word, maxW, adv)
				lines = append(lines, word[:cut])
				word = word[cut:]
				ww = adv(word)
			}
			switch {
			case line == "":
				line, lineW = word, ww
			case lineW+space+ww <= maxW:
				line += " " + word
				lineW += space + ww
			default:
				lines = append(lines, line)
				line, lineW = word, ww
			}
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// prefixWithin returns the byte length of the longest prefix of word (at
// least one rune) no wider than maxW.
func prefixWithin(word string, maxW float64, adv func(string) float64) int {
	cut := 0
	for i, r := range word {
		end := i + utf8.RuneLen(r)
		if cut > 0 && adv(word[:end]) > maxW {
			break
		}
		cut = end
	}
	return cut
}
