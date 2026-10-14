package app

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
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
// slide on the left, next-slide preview + timer + clock + notes on the right.
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
	drawPresenterPanel(screen, lo.statusPanel)

	drawPresenterText(screen, "CURRENT", lo.currentPanel.x+18, lo.currentPanel.y+14, 2, colorLabel)
	drawPresenterText(screen, "NEXT", lo.nextPanel.x+18, lo.nextPanel.y+14, 2, colorLabel)

	drawImageInPresenterRect(screen, g.cur.img, lo.currentContent)
	drawImageInPresenterRect(screen, g.next.img, lo.nextContent)
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

type presenterLayout struct {
	currentPanel   presenterRect
	currentContent presenterRect
	nextPanel      presenterRect
	nextContent    presenterRect
	statusPanel    presenterRect
}

func (g *PresenterGame) presenterLayout() presenterLayout {
	margin := max(18, min(g.bufW, g.bufH)/52)
	gap := max(14, margin/2)
	rightW := int(float64(g.bufW) * 0.31)
	rightW = max(rightW, 360)
	rightW = min(rightW, g.bufW/2)
	leftW := max(1, g.bufW-margin*2-gap-rightW)

	fullH := g.bufH - margin*2
	nextH := int(float64(fullH) * 0.40)
	nextH = max(nextH, 250)
	nextH = min(nextH, fullH-gap-220)
	if nextH < 1 {
		nextH = max(1, fullH/2)
	}

	currentPanel := presenterRect{x: margin, y: margin, w: leftW, h: fullH}
	nextPanel := presenterRect{x: margin + leftW + gap, y: margin, w: rightW, h: nextH}
	statusPanel := presenterRect{
		x: nextPanel.x,
		y: nextPanel.y + nextPanel.h + gap,
		w: rightW,
		h: fullH - nextPanel.h - gap,
	}

	return presenterLayout{
		currentPanel:   currentPanel,
		currentContent: insetPresenterRect(currentPanel, 16, 44, 16, 16),
		nextPanel:      nextPanel,
		nextContent:    insetPresenterRect(nextPanel, 14, 44, 14, 14),
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
	pad := max(18, min(r.w, r.h)/18)
	gap := max(12, pad/2)
	innerX := r.x + pad
	innerY := r.y + pad
	innerW := r.w - pad*2

	cardH := max(78, r.h/6)
	halfW := (innerW - gap) / 2
	clockCard := presenterRect{x: innerX, y: innerY, w: halfW, h: cardH}
	slideCard := presenterRect{x: innerX + halfW + gap, y: innerY, w: innerW - halfW - gap, h: cardH}
	timerCard := presenterRect{x: innerX, y: innerY + cardH + gap, w: innerW, h: max(118, r.h/3)}

	drawPresenterPanel(screen, clockCard)
	drawPresenterPanel(screen, slideCard)
	drawPresenterPanel(screen, timerCard)

	drawPresenterText(screen, "CLOCK", clockCard.x+16, clockCard.y+14, 2, colorLabel)
	drawPresenterText(screen, g.clockCache.String(now), clockCard.x+16, clockCard.y+42, 3, colorValue)

	total := len(g.hello.PageList)
	counter := "-- / --"
	if g.hasHello && total > 0 {
		counter = g.counterCache.String(st.ListIndex, total)
	}
	drawPresenterText(screen, "SLIDE", slideCard.x+16, slideCard.y+14, 2, colorLabel)
	drawPresenterText(screen, counter, slideCard.x+16, slideCard.y+42, 3, colorValue)

	elapsed := int64(0)
	if g.hasHello {
		elapsed = int64(now.Sub(g.hello.StartedAt).Seconds())
	}
	drawPresenterText(screen, "ELAPSED", timerCard.x+18, timerCard.y+16, 2, colorLabel)
	drawPresenterGradientText(screen, g.elapsedCache.String(elapsed), timerCard.x+18, timerCard.y+50, 5)

	status := "TIMER"
	if st.Paused {
		status = "PAUSED"
	}
	barLabelY := timerCard.y + timerCard.h + gap + 8
	drawPresenterText(screen, status, innerX, barLabelY, 2, colorLabel)
	barY := float32(barLabelY + 32)
	barH := float32(max(10, min(18, r.h/34)))
	drawSegmentedProgress(screen, float32(innerX), barY, float32(innerW), barH, total, st.ListIndex, st.FractionAt(recvAt, now), st.Paused)

	notesY := int(barY+barH) + gap + 18
	if notes := g.hello.Notes[st.Page+1]; notes != "" && notesY < r.y+r.h-28 {
		drawPresenterText(screen, "NOTES", innerX, notesY, 2, colorLabel)
		g.drawNotes(screen, notes, innerX, notesY+30, innerW, r.y+r.h-pad, 2, color.RGBA{226, 232, 240, 255})
	}
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

// notesLayout caches the wrapped lines of the current speaker notes, so
// wrapping only reruns when the note or the available width changes.
type notesLayout struct {
	text     string
	maxChars int
	lines    []string
}

func (n *notesLayout) wrap(text string, maxChars int) []string {
	if n.lines == nil || n.text != text || n.maxChars != maxChars {
		n.text, n.maxChars = text, maxChars
		n.lines = wrapPresenterLines(debugFontText(text), maxChars)
	}
	return n.lines
}

func (g *PresenterGame) drawNotes(screen *ebiten.Image, s string, x, y, maxW, maxY, scale int, clr color.Color) {
	if s == "" || maxW <= 0 || y >= maxY {
		return
	}
	lineH := 15 * scale
	maxChars := max(1, maxW/(debugGlyphW*scale))
	for _, line := range g.notes.wrap(s, maxChars) {
		if y+lineH > maxY {
			drawPresenterText(screen, "...", x, y, scale, clr)
			return
		}
		drawPresenterText(screen, line, x, y, scale, clr)
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
