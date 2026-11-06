package app

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/gethash/boozle/internal/display"
	"github.com/gethash/boozle/internal/ipc"
	"github.com/gethash/boozle/internal/pdf"
)

var (
	colorLabel = color.RGBA{148, 163, 184, 255}
	colorValue = color.RGBA{248, 250, 252, 255}
	colorDim   = color.RGBA{100, 116, 139, 255}
	colorNotes = color.RGBA{226, 232, 240, 255}
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

	counterCache pageLabelCache
	elapsedCache elapsedCache
	clockCache   clockCache
	notes        notesLayout

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
	resetTextPool()
	screen.Fill(color.RGBA{13, 15, 26, 255}) // #0D0F1A

	if g.bufW <= 0 || g.bufH <= 0 {
		return
	}

	lo := g.presenterLayout()
	drawPresenterPanel(screen, lo.currentPanel)
	drawPresenterPanel(screen, lo.nextPanel)
	drawPresenterPanel(screen, lo.notesPanel)
	drawPresenterPanel(screen, lo.statusPanel)

	drawPresenterText(screen, "CURRENT", lo.currentPanel.x+18, lo.currentPanel.y+14, 2, colorLabel)
	drawPresenterText(screen, "NEXT", lo.nextPanel.x+18, lo.nextPanel.y+14, 2, colorLabel)
	drawPresenterText(screen, "NOTES", lo.notesPanel.x+18, lo.notesPanel.y+14, 2, colorLabel)

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

	return presenterLayout{
		currentPanel:   currentPanel,
		currentContent: insetPresenterRect(currentPanel, 16, 44, 16, 16),
		nextPanel:      nextPanel,
		nextContent:    insetPresenterRect(nextPanel, 14, 44, 14, 14),
		notesPanel:     notesPanel,
		notesContent:   insetPresenterRect(notesPanel, 18, 44, 18, 14),
		statusPanel:    statusPanel,
	}
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

func drawPresenterPanel(screen *ebiten.Image, r presenterRect) {
	if r.w <= 0 || r.h <= 0 {
		return
	}
	vector.FillRect(screen, float32(r.x), float32(r.y), float32(r.w), float32(r.h),
		color.RGBA{15, 19, 32, 255}, false)
	c := color.RGBA{63, 72, 95, 180}
	vector.FillRect(screen, float32(r.x), float32(r.y), float32(r.w), 1, c, false)
	vector.FillRect(screen, float32(r.x), float32(r.y+r.h-1), float32(r.w), 1, c, false)
	vector.FillRect(screen, float32(r.x), float32(r.y), 1, float32(r.h), c, false)
	vector.FillRect(screen, float32(r.x+r.w-1), float32(r.y), 1, float32(r.h), c, false)
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

func (g *PresenterGame) drawPresenterStatus(screen *ebiten.Image, r presenterRect, st ipc.PresenterState, recvAt, now time.Time) {
	if r.w <= 0 || r.h <= 0 {
		return
	}
	lo := layoutPresenterStatus(r)

	drawPresenterPanel(screen, lo.clock)
	drawPresenterPanel(screen, lo.slide)
	drawPresenterPanel(screen, lo.elapsed)

	total := len(g.hello.PageList)
	counter := "-- / --"
	counterChars := len(counter)
	if g.hasHello && total > 0 {
		counter = g.counterCache.String(st.ListIndex, total)
		// Size for the widest counter so the text doesn't jump on 9 → 10.
		counterChars = 2*len(strconv.Itoa(total)) + 3
	}
	elapsed := int64(0)
	if g.hasHello {
		elapsed = int64(now.Sub(g.hello.StartedAt).Seconds())
	}
	clock := g.clockCache.String(now)
	elapsedStr := g.elapsedCache.String(elapsed)

	drawStatusCard(screen, lo.clock, "CLOCK", clock, statusValueScale(lo.clock, len(clock), debugGlyphW, 3), colorValue)
	drawStatusCard(screen, lo.slide, "SLIDE", counter, statusValueScale(lo.slide, counterChars, debugGlyphW, 3), colorValue)
	drawPresenterText(screen, "ELAPSED", lo.elapsed.x+statusCardPadX, lo.elapsed.y+statusLabelY, 2, colorLabel)
	if s := statusValueScale(lo.elapsed, len(elapsedStr), gradientGlyphW, 5); s > 0 {
		drawPresenterGradientText(screen, elapsedStr, lo.elapsed.x+statusCardPadX, lo.elapsed.y+statusValueY, s)
	}

	status := "TIMER"
	if st.Paused {
		status = "PAUSED"
	}
	drawPresenterText(screen, status, lo.bar.x, lo.barLabelY, 2, colorLabel)
	drawSegmentedProgress(screen, float32(lo.bar.x), float32(lo.bar.y), float32(lo.bar.w), float32(lo.bar.h),
		total, st.ListIndex, st.FractionAt(recvAt, now), st.Paused)
}

// Status card text placement: label at the top, value below it.
const (
	statusCardPadX   = 16
	statusLabelY     = 14
	statusValueY     = 42
	statusValueBotPd = 8  // space below the value's glyphs
	statusGlyphH     = 12 // visible debug-font glyph height at scale 1
	gradientGlyphW   = 7  // drawPresenterGradientText advance at scale 1
	statusBarLabelH  = 32 // progress label (scale 2) plus spacing above the bar
)

// presenterStatusLayout places the clock, slide and elapsed cards and the
// progress bar inside the status panel.
type presenterStatusLayout struct {
	clock, slide, elapsed presenterRect
	barLabelY             int
	bar                   presenterRect
}

// layoutPresenterStatus lays out the status panel: CLOCK | SLIDE cards over
// a wide ELAPSED card, then the progress bar. When the panel is too short
// for two rows of cards (small windows, --render-scale), the three cards
// share one row.
func layoutPresenterStatus(r presenterRect) presenterStatusLayout {
	pad := max(14, min(r.w, r.h)/18)
	gap := max(10, pad/2)
	innerX, innerY := r.x+pad, r.y+pad
	innerW := max(1, r.w-pad*2)
	innerH := max(1, r.h-pad*2)

	barH := max(10, min(18, r.h/24))
	progressH := statusBarLabelH + barH
	avail := max(1, innerH-progressH-gap)

	var lo presenterStatusLayout
	twoRowMin := 2*statusCardMinH(2) + gap
	if avail >= twoRowMin {
		cardH := max(statusCardMinH(2), (avail-gap)*2/5)
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
	lo.bar = presenterRect{x: innerX, y: lo.barLabelY + statusBarLabelH, w: innerW, h: barH}
	return lo
}

// statusCardMinH is the card height needed to show a value at scale s.
func statusCardMinH(s int) int {
	return statusValueY + statusGlyphH*s + statusValueBotPd
}

// statusValueScale returns the largest scale ≤ maxScale at which a value of
// chars glyphs (advance px each at scale 1) fits card r, or 0 if none does.
func statusValueScale(r presenterRect, chars, advance, maxScale int) int {
	for s := maxScale; s >= 1; s-- {
		if r.h >= statusCardMinH(s) && 2*statusCardPadX+chars*advance*s <= r.w {
			return s
		}
	}
	return 0
}

func drawStatusCard(screen *ebiten.Image, r presenterRect, label, value string, scale int, clr color.Color) {
	drawPresenterText(screen, label, r.x+statusCardPadX, r.y+statusLabelY, 2, colorLabel)
	if scale > 0 {
		drawPresenterText(screen, value, r.x+statusCardPadX, r.y+statusValueY, scale, clr)
	}
}

// drawNotesPanel draws the current slide's speaker notes into r, or a dim
// placeholder when the slide has none.
func (g *PresenterGame) drawNotesPanel(screen *ebiten.Image, r presenterRect, st ipc.PresenterState) {
	notes := g.hello.Notes[st.Page+1]
	if strings.TrimSpace(notes) == "" {
		if g.hasHello {
			drawPresenterText(screen, "No notes for this slide", r.x, r.y, 2, colorDim)
		}
		return
	}
	g.drawNotes(screen, notes, r, colorNotes)
}

// ── text rendering ─────────────────────────────────────────────────────
//
// Per-frame pool of small DebugPrintAt scratch images. resetTextPool()
// is called at the top of Draw; each drawPresenterText acquires a fresh
// buffer so sequential calls can't trample each other's pixels before
// Ebiten flushes (subsequent writes to a source image after a queued
// DrawImage can otherwise be reordered visibly).
var textPoolBufs []*ebiten.Image
var textPoolNext int

const (
	textBufWidth  = 1024
	textBufHeight = 13
	debugGlyphW   = 6 // ebitenutil debug font advance
)

func resetTextPool() { textPoolNext = 0 }

func acquireTextBuf() *ebiten.Image {
	for textPoolNext >= len(textPoolBufs) {
		textPoolBufs = append(textPoolBufs, ebiten.NewImage(textBufWidth, textBufHeight))
	}
	b := textPoolBufs[textPoolNext]
	textPoolNext++
	b.Clear()
	return b
}

// drawPresenterText renders s into a pooled scratch image using DebugPrintAt,
// then blits it to screen at (x, y) scaled by scale× and tinted.
func drawPresenterText(screen *ebiten.Image, s string, x, y, scale int, clr color.Color) {
	if s == "" || scale <= 0 {
		return
	}
	width := min(textBufWidth, utf8.RuneCountInString(s)*debugGlyphW+2)
	buf := acquireTextBuf()
	ebitenutil.DebugPrintAt(buf, s, 0, 0)
	src := buf.SubImage(image.Rect(0, 0, width, textBufHeight)).(*ebiten.Image)
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(float64(scale), float64(scale))
	op.GeoM.Translate(float64(x), float64(y))
	op.ColorScale.ScaleWithColor(clr)
	screen.DrawImage(src, op)
}

const (
	notesMinScale = 2
	notesLineH    = 15 // line height per scale unit
)

// notesMaxScale is the largest text scale tried for notes: 3 on a 1080p
// buffer, growing with the buffer height so HiDPI and 4K panels get text
// of similar physical size.
func notesMaxScale(bufH int) int {
	return max(3, bufH/360)
}

// notesLayout caches the wrapped lines and chosen text scale of the current
// speaker notes, so fitting only reruns when the note or the area changes.
type notesLayout struct {
	text           string
	w, h, maxScale int
	scale          int
	lines          []string
}

// fit picks the largest scale in [notesMinScale, maxScale] at which text,
// wrapped to width w, fits height h, and returns the wrapped lines. When
// it doesn't fit even at notesMinScale, the lines at that scale are
// returned and the caller truncates.
func (n *notesLayout) fit(text string, w, h, maxScale int) ([]string, int) {
	if n.lines != nil && n.text == text && n.w == w && n.h == h && n.maxScale == maxScale {
		return n.lines, n.scale
	}
	n.text, n.w, n.h, n.maxScale = text, w, h, maxScale
	clean := debugFontText(text)
	for s := max(maxScale, notesMinScale); s >= notesMinScale; s-- {
		// Lines are drawn through the textBufWidth scratch buffer.
		maxChars := max(1, min(w/(debugGlyphW*s), textBufWidth/debugGlyphW-1))
		n.lines, n.scale = wrapPresenterLines(clean, maxChars), s
		if len(n.lines)*notesLineH*s <= h {
			break
		}
	}
	return n.lines, n.scale
}

func (g *PresenterGame) drawNotes(screen *ebiten.Image, s string, r presenterRect, clr color.Color) {
	if s == "" || r.w <= 0 || r.h <= 0 {
		return
	}
	lines, scale := g.notes.fit(s, r.w, r.h, notesMaxScale(g.bufH))
	lineH := notesLineH * scale
	y, maxY := r.y, r.y+r.h
	for i, line := range lines {
		if y+lineH > maxY {
			return
		}
		if i < len(lines)-1 && y+2*lineH > maxY {
			// Last line that fits, more to come: mark the cut.
			drawPresenterText(screen, "...", r.x, y, scale, clr)
			return
		}
		drawPresenterText(screen, line, r.x, y, scale, clr)
		y += lineH
	}
}

// debugFontReplacements maps common typographic characters outside the
// debug font's U+0000–U+00FF range (frequent in imported PowerPoint notes)
// to ASCII look-alikes.
var debugFontReplacements = map[rune]string{
	'\u2018': "'", '\u2019': "'", '\u201a': ",", '\u201b': "'",
	'\u201c': `"`, '\u201d': `"`, '\u201e': `"`, '\u201f': `"`,
	'\u2010': "-", '\u2011': "-", '\u2012': "-", '\u2013': "-",
	'\u2014': "-", '\u2015': "-", '\u2212': "-", '\u2026': "...",
	'\u2022': "*", '\u2023': ">", '\u25e6': "o", '\u25aa': "*",
	'\u25cf': "*", '\u20ac': "EUR", '\u2122': "TM", '\u2192': "->",
	'\u2190': "<-", '\u21d2': "=>", '\u2009': " ", '\u200a': " ",
	'\u202f': " ", '\u2007': " ", '\u200b': "", '\ufeff': "",
}

// debugFontText makes s drawable with the debug font: characters in
// U+0000–U+00FF (which includes German umlauts and most Western European
// accents) pass through, known typographic characters are replaced, and
// anything else becomes '?'.
func debugFontText(s string) string {
	ok := true
	for _, r := range s {
		if r > 0xFF {
			ok = false
			break
		}
	}
	if ok {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch rep, known := debugFontReplacements[r]; {
		case r <= 0xFF:
			b.WriteRune(r)
		case known:
			b.WriteString(rep)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// wrapPresenterLines word-wraps s to at most maxChars characters (runes)
// per line, hard-splitting words that are longer than a line.
func wrapPresenterLines(s string, maxChars int) []string {
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line, lineLen := "", 0
		for _, word := range words {
			runes := []rune(word)
			for len(runes) > maxChars {
				if line != "" {
					lines = append(lines, line)
					line, lineLen = "", 0
				}
				lines = append(lines, string(runes[:maxChars]))
				runes = runes[maxChars:]
			}
			word = string(runes)
			switch {
			case line == "":
				line, lineLen = word, len(runes)
			case lineLen+1+len(runes) <= maxChars:
				line += " " + word
				lineLen += 1 + len(runes)
			default:
				lines = append(lines, line)
				line, lineLen = word, len(runes)
			}
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func drawPresenterGradientText(screen *ebiten.Image, s string, x, y, scale int) {
	if s == "" || scale <= 0 {
		return
	}
	cursor := x
	step := 7 * scale
	denom := max(1, len(s)-1)
	for i, ch := range s {
		t := float32(i) / float32(denom)
		clr := rainbowAt(t, 255)
		if ch == ':' {
			clr = colorLabel
		}
		drawPresenterText(screen, string(ch), cursor, y, scale, clr)
		cursor += step
	}
}
