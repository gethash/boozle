package app

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strings"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"

	"github.com/gethash/boozle/internal/display"
	"github.com/gethash/boozle/internal/ipc"
	"github.com/gethash/boozle/internal/pdf"
)

// presenterPrefetchQueue is sized to 1 because the presenter only ever
// requests one neighbour (the next slide) at a time — a deeper queue is
// pure waste here.
const presenterPrefetchQueue = 1

// PresenterGame is an ebiten.Game that renders the presenter view: current
// slide on the left, next-slide preview + timer + clock on the right.
type PresenterGame struct {
	doc      *pdf.Doc
	cache    *pdf.Cache
	prefetch *pdf.Prefetcher
	receiver *ipc.Receiver

	cacheMB     int     // 0 = auto; >0 = hard cap in MB
	renderScale float64 // 0 = native; 0.5..1.0 fraction of native pixels

	bufW, bufH int

	curImg     *ebiten.Image
	curKey     pdf.CacheKey
	curPinned  bool
	nextImg    *ebiten.Image
	nextKey    pdf.CacheKey
	nextPinned bool

	counterCache pageLabelCache
	elapsedCache elapsedCache
	clockCache   clockCache
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
	if seconds < 0 {
		seconds = 0
	}
	d := time.Duration(seconds) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
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
// presenter-slave subprocess. cacheMB and renderScale match the master's
// --cache-mb and --render-scale flags; pass 0 to keep the auto/native default.
func RunPresenter(socketPath, pdfPath string, monitorIdx, cacheMB int, renderScale float64) error {
	doc, err := pdf.Open(pdfPath)
	if err != nil {
		return err
	}
	defer doc.Close()

	cache := pdf.NewCache(cacheBudgetInitial)
	defer cache.Clear()
	if cacheMB > 0 {
		cache.Resize(cacheMB << 20)
	}

	pf := pdf.NewPrefetcher(doc, cache, presenterPrefetchQueue)
	pf.Start()
	defer pf.Stop()

	receiver, err := ipc.Connect(socketPath, func() { os.Exit(0) })
	if err != nil {
		return fmt.Errorf("presenter: connect to master: %w", err)
	}

	g := &PresenterGame{
		doc:         doc,
		cache:       cache,
		prefetch:    pf,
		receiver:    receiver,
		cacheMB:     cacheMB,
		renderScale: renderScale,
	}

	ebiten.SetWindowTitle("boozle — Presenter View")
	setWindowIcon()
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	if err := display.PickMonitor(monitorIdx); err != nil {
		return err
	}
	ebiten.SetWindowSize(1280, 800)

	runGame := &fullscreenOnMonitor{game: g, monitorIdx: monitorIdx}
	if err := ebiten.RunGame(runGame); err != nil && !errors.Is(err, ErrQuit) {
		return err
	}
	return nil
}

func (g *PresenterGame) Update() error {
	g.drainPendingUploads()
	g.forwardInput()
	st := g.receiver.Latest()
	if g.bufW > 0 && g.bufH > 0 {
		g.maybeRefreshPanes(st)
	}
	return nil
}

// drainPendingUploads moves prefetcher RGBA into GPU images on the main
// goroutine, where ebiten allows GPU operations.
func (g *PresenterGame) drainPendingUploads() {
	if g.prefetch == nil {
		return
	}
	uploads := g.prefetch.Uploads()
	for i := 0; i < maxUploadsPerFrame; i++ {
		select {
		case job := <-uploads:
			if g.cache.Has(job.Key) {
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

func (g *PresenterGame) Draw(screen *ebiten.Image) {
	resetTextPool()
	st := g.receiver.Latest()

	screen.Fill(color.RGBA{13, 15, 26, 255}) // #0D0F1A

	if g.bufW <= 0 || g.bufH <= 0 {
		return
	}

	lo := g.presenterLayout()
	drawPresenterPanel(screen, lo.currentPanel)
	drawPresenterPanel(screen, lo.nextPanel)
	drawPresenterPanel(screen, lo.statusPanel)

	drawPresenterText(screen, "CURRENT", lo.currentPanel.x+18, lo.currentPanel.y+14, 2, color.RGBA{148, 163, 184, 255})
	drawPresenterText(screen, "NEXT", lo.nextPanel.x+18, lo.nextPanel.y+14, 2, color.RGBA{148, 163, 184, 255})

	drawImageInPresenterRect(screen, g.curImg, lo.currentContent)
	drawImageInPresenterRect(screen, g.nextImg, lo.nextContent)
	g.drawPresenterStatus(screen, lo.statusPanel, st)
}

func (g *PresenterGame) Layout(outsideW, outsideH int) (int, int) {
	sf := ebiten.Monitor().DeviceScaleFactor()
	if sf <= 0 {
		sf = 1
	}
	pxW := int(math.Round(float64(outsideW) * sf))
	pxH := int(math.Round(float64(outsideH) * sf))
	if rs := g.renderScale; rs > 0 && rs < 1 {
		pxW = max(1, int(math.Round(float64(pxW)*rs)))
		pxH = max(1, int(math.Round(float64(pxH)*rs)))
	}
	if pxW != g.bufW || pxH != g.bufH {
		g.bufW = pxW
		g.bufH = pxH
		if g.cache != nil && pxW > 0 && pxH > 0 {
			if g.cacheMB <= 0 {
				g.cache.Resize(autoBudget(pxW, pxH))
			}
			g.cache.PurgeNotMatching(pxW, pxH)
		}
	}
	return pxW, pxH
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
	leftW := g.bufW - margin*2 - gap - rightW
	if leftW < 1 {
		leftW = 1
	}

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
	w := r.w - left - right
	h := r.h - top - bottom
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return presenterRect{x: r.x + left, y: r.y + top, w: w, h: h}
}

// maybeRefreshPanes loads the current and next slides as cache-owned GPU
// images, pinning them so they aren't evicted while displayed.
func (g *PresenterGame) maybeRefreshPanes(st ipc.PresenterState) {
	lo := g.presenterLayout()

	// Current slide
	if st.Total > 0 && st.Page >= 0 {
		if page, err := g.doc.PageSize(st.Page); err == nil {
			w, h, _, _ := presenterAspectFit(page.WidthPoints, page.HeightPoints, lo.currentContent.w, lo.currentContent.h)
			if w > 0 && h > 0 {
				key := pdf.CacheKey{Page: st.Page, W: w, H: h}
				if g.curImg == nil || g.curKey != key {
					if img, ok := g.loadSlide(key); ok {
						g.unpinCur()
						g.curImg = img
						g.curKey = key
						g.cache.Pin(key)
						g.curPinned = true
					}
				}
			}
		}
	}

	// Next slide
	if st.NextPage >= 0 {
		if page, err := g.doc.PageSize(st.NextPage); err == nil {
			w, h, _, _ := presenterAspectFit(page.WidthPoints, page.HeightPoints, lo.nextContent.w, lo.nextContent.h)
			if w > 0 && h > 0 {
				key := pdf.CacheKey{Page: st.NextPage, W: w, H: h}
				if g.nextImg == nil || g.nextKey != key {
					if img, ok := g.loadSlide(key); ok {
						g.unpinNext()
						g.nextImg = img
						g.nextKey = key
						g.cache.Pin(key)
						g.nextPinned = true
					}
				}
				g.prefetch.Request(key)
			}
		}
	} else if g.nextImg != nil {
		g.unpinNext()
		g.nextImg = nil
		g.nextKey = pdf.CacheKey{}
	}
}

// loadSlide returns the GPU image for key — from cache if present, else by
// rendering synchronously and inserting. The cache owns the image; the
// caller must Pin it to prevent eviction while displayed.
func (g *PresenterGame) loadSlide(key pdf.CacheKey) (*ebiten.Image, bool) {
	if cached, ok := g.cache.Get(key); ok {
		return cached.(*ebiten.Image), true
	}
	img, cleanup, err := g.doc.RenderPage(key.Page, key.W, key.H)
	if err != nil {
		return nil, false
	}
	eimg := uploadRGBA(img)
	if cleanup != nil {
		cleanup()
	}
	g.cache.Put(key, eimg)
	return eimg, true
}

func (g *PresenterGame) unpinCur() {
	if g.curPinned {
		g.cache.Unpin(g.curKey)
		g.curPinned = false
	}
}

func (g *PresenterGame) unpinNext() {
	if g.nextPinned {
		g.cache.Unpin(g.nextKey)
		g.nextPinned = false
	}
}

func (g *PresenterGame) forwardInput() {
	if inpututil.IsKeyJustPressed(ebiten.KeyQ) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdQuit})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEscape) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdEscape})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyTab) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdTab})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyP) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdPause})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyB) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdBlackout})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyW) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdWhiteout})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyF) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdFullscreen})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyL) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdReturnLast})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyHome) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdHome})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEnd) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdEnd})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyEnter) || inpututil.IsKeyJustPressed(ebiten.KeyNumpadEnter) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdEnter})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyBackspace) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdBackspace})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowRight) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdRight})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowLeft) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdLeft})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowDown) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdDown})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyArrowUp) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdUp})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdSpace})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyPageDown) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdPageDown})
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyPageUp) {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdPageUp})
	}
	for k := ebiten.KeyDigit0; k <= ebiten.KeyDigit9; k++ {
		if inpututil.IsKeyJustPressed(k) {
			g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdDigit, Arg: int(k - ebiten.KeyDigit0)})
		}
	}
	for k := ebiten.KeyNumpad0; k <= ebiten.KeyNumpad9; k++ {
		if inpututil.IsKeyJustPressed(k) {
			g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdDigit, Arg: int(k - ebiten.KeyNumpad0)})
		}
	}

	_, wy := ebiten.Wheel()
	if wy < 0 {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdSpace})
	} else if wy > 0 {
		g.receiver.SendCommand(ipc.PresenterCommand{Name: presenterCmdPageUp})
	}
}

// presenterAspectFit is a local copy of aspectFit for use in the presenter
// package without exporting from app.go.
func presenterAspectFit(srcW, srcH float64, dstW, dstH int) (w, h, offX, offY int) {
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
	w, h, offX, offY := presenterAspectFit(float64(iw), float64(ih), r.w, r.h)
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(float64(w)/float64(iw), float64(h)/float64(ih))
	op.GeoM.Translate(float64(r.x+offX), float64(r.y+offY))
	op.Filter = ebiten.FilterLinear
	screen.DrawImage(img, op)
}

func (g *PresenterGame) drawPresenterStatus(screen *ebiten.Image, r presenterRect, st ipc.PresenterState) {
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

	clock := g.clockCache.String(time.Now())
	drawPresenterText(screen, "CLOCK", clockCard.x+16, clockCard.y+14, 2, color.RGBA{148, 163, 184, 255})
	drawPresenterText(screen, clock, clockCard.x+16, clockCard.y+42, 3, color.RGBA{248, 250, 252, 255})

	counter := "-- / --"
	if st.Total > 0 {
		counter = g.counterCache.String(st.ListIndex, st.Total)
	}
	drawPresenterText(screen, "SLIDE", slideCard.x+16, slideCard.y+14, 2, color.RGBA{148, 163, 184, 255})
	drawPresenterText(screen, counter, slideCard.x+16, slideCard.y+42, 3, color.RGBA{248, 250, 252, 255})

	drawPresenterText(screen, "ELAPSED", timerCard.x+18, timerCard.y+16, 2, color.RGBA{148, 163, 184, 255})
	drawPresenterGradientText(screen, g.elapsedCache.String(st.ElapsedSeconds), timerCard.x+18, timerCard.y+50, 5)

	status := "TIMER"
	if st.Paused {
		status = "PAUSED"
	}
	barLabelY := timerCard.y + timerCard.h + gap + 8
	drawPresenterText(screen, status, innerX, barLabelY, 2, color.RGBA{148, 163, 184, 255})
	barY := float32(barLabelY + 32)
	barH := float32(max(10, min(18, r.h/34)))
	drawSegmentedProgress(screen, float32(innerX), barY, float32(innerW), barH, st.Total, st.ListIndex, st.Fraction, st.Paused)

	notesY := int(barY+barH) + gap + 18
	if st.Notes != "" && notesY < r.y+r.h-28 {
		drawPresenterText(screen, "NOTES", innerX, notesY, 2, color.RGBA{148, 163, 184, 255})
		drawWrappedPresenterText(screen, st.Notes, innerX, notesY+30, innerW, r.y+r.h-pad, 2, color.RGBA{226, 232, 240, 255})
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

const textBufWidth = 1024
const textBufHeight = 13

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
	width := len(s)*7 + 2
	if width > textBufWidth {
		width = textBufWidth
	}
	buf := acquireTextBuf()
	ebitenutil.DebugPrintAt(buf, s, 0, 0)
	src := buf.SubImage(image.Rect(0, 0, width, textBufHeight)).(*ebiten.Image)
	op := &ebiten.DrawImageOptions{}
	op.GeoM.Scale(float64(scale), float64(scale))
	op.GeoM.Translate(float64(x), float64(y))
	op.ColorScale.ScaleWithColor(clr)
	screen.DrawImage(src, op)
}

func drawWrappedPresenterText(screen *ebiten.Image, s string, x, y, maxW, maxY, scale int, clr color.Color) {
	if s == "" || maxW <= 0 || y >= maxY {
		return
	}
	charW := 7 * scale
	lineH := 15 * scale
	maxChars := max(1, maxW/charW)
	lines := wrapPresenterLines(s, maxChars)
	for _, line := range lines {
		if y+lineH > maxY {
			drawPresenterText(screen, "...", x, y, scale, clr)
			return
		}
		drawPresenterText(screen, line, x, y, scale, clr)
		y += lineH
	}
}

func wrapPresenterLines(s string, maxChars int) []string {
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := ""
		for _, word := range words {
			for len(word) > maxChars {
				if line != "" {
					lines = append(lines, line)
					line = ""
				}
				lines = append(lines, word[:maxChars])
				word = word[maxChars:]
			}
			if line == "" {
				line = word
				continue
			}
			if len(line)+1+len(word) <= maxChars {
				line += " " + word
			} else {
				lines = append(lines, line)
				line = word
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
			clr = color.RGBA{148, 163, 184, 255}
		}
		drawPresenterText(screen, string(ch), cursor, y, scale, clr)
		cursor += step
	}
}
