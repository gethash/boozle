package app

import (
	"fmt"
	"image/color"
	"math"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// ── segmented progress bar (the design system's ProgressOverlay) ───────

// segmentedBar draws one pill per slide. Past segments show their slice of
// the spectrum, the current one fills left to right with the auto-advance
// timer, future ones show only the track. It draws into an offscreen layer
// masked by antialiased pills, so the spectrum runs continuously across the
// segments and the gaps stay clean.
type segmentedBar struct {
	content, mask *ebiten.Image
	maskSig       barSig
}

type barSig struct {
	w, h, n int
	gap     float32
}

// segmentGeometry returns the segment width and the gap actually used for
// total segments across w px: gaps are dropped once segments would get
// narrower than them.
func segmentGeometry(w float32, total int, gap float32) (segW, gapUsed float32) {
	if total <= 0 {
		return 0, 0
	}
	segW = (w - gap*float32(total-1)) / float32(total)
	if segW < gap {
		return w / float32(total), 0
	}
	return segW, gap
}

// draw renders the bar into the integer rectangle (x, y, w, h). fraction is
// the current slide's auto-advance progress; paused dims its fill.
func (b *segmentedBar) draw(dst *ebiten.Image, x, y, w, h int, total, current int, fraction float64, paused bool, track color.RGBA, gap float32) {
	if total <= 0 || w <= 0 || h <= 0 {
		return
	}
	current = min(max(current, 0), total-1)
	fraction = clamp01(fraction)

	content := ensureImage(&b.content, w, h)
	mask := ensureImage(&b.mask, w, h)
	fw, fh := float32(w), float32(h)
	segW, gap := segmentGeometry(fw, total, gap)

	if sig := (barSig{w: w, h: h, n: total, gap: gap}); sig != b.maskSig || b.maskSig.n == 0 {
		b.maskSig = sig
		mask.Clear()
		shapePath.Reset()
		r := min(fh, segW) / 2
		for i := range total {
			appendRoundRect(&shapePath, float32(i)*(segW+gap), 0, segW, fh, r)
		}
		op := &vector.DrawPathOptions{AntiAlias: true}
		vector.FillPath(mask, &shapePath, nil, op)
	}

	content.Clear()
	vector.FillRect(content, 0, 0, fw, fh, track, false)
	curStart := float32(current) * (segW + gap)
	fillTo := curStart + segW*float32(fraction)
	if current == total-1 && fraction >= 1 {
		fillTo = fw
	}
	fillSpectrumBand(content, 0, 0, fw, fh, 0, curStart, 1)
	curAlpha := float32(1)
	if paused {
		curAlpha = 0.5
	}
	fillSpectrumBand(content, 0, 0, fw, fh, curStart, fillTo, curAlpha)
	content.DrawImage(mask, &ebiten.DrawImageOptions{Blend: ebiten.BlendDestinationIn})

	op := &ebiten.DrawImageOptions{}
	op.GeoM.Translate(float64(x), float64(y))
	dst.DrawImage(content, op)
}

// ── countdown ring (the design system's CountdownRing) ─────────────────

const ringSegments = 72

// drawCountdownRing draws a ring of diameter d centred at (cx, cy): the
// conic spectrum from 12 o'clock clockwise over the remaining fraction,
// on a track ring.
func drawCountdownRing(dst *ebiten.Image, cx, cy, d, width float32, remaining float64) {
	if d <= 0 || width <= 0 {
		return
	}
	r := (d - width) / 2
	vector.StrokeCircle(dst, cx, cy, r, width, tokTrack, true)
	remaining = clamp01(remaining)
	if remaining <= 0 {
		return
	}
	const top = -math.Pi / 2
	end := remaining * 2 * math.Pi
	step := 2 * math.Pi / ringSegments
	sop := &vector.StrokeOptions{Width: width}
	for a := 0.0; a < end; a += step {
		b := min(a+step, end)
		// Overlap neighbours slightly so antialiased seams don't show.
		b2 := min(b+step*0.25, end)
		shapePath.Reset()
		shapePath.Arc(cx, cy, r, float32(top+a), float32(top+b2), vector.Clockwise)
		op := &vector.DrawPathOptions{AntiAlias: true}
		op.ColorScale.ScaleWithColor(spectrumConicAt((a + b) / 2 / (2 * math.Pi)))
		vector.StrokePath(dst, &shapePath, sop, op)
	}
}

// ── counters ───────────────────────────────────────────────────────────

// formatCounter renders a slide counter as "03 / 12": both numbers padded
// to the width of total (at least two digits), so it never changes width.
func formatCounter(idx1, total int) string {
	w := max(2, len(fmt.Sprint(total)))
	return fmt.Sprintf("%0*d / %0*d", w, idx1, w, total)
}

// formatCountdown renders remaining time as "00:14" (or "1:02:03" past an
// hour), rounding up so the display reaches 00:00 as the slide changes.
func formatCountdown(d time.Duration) string {
	s := max(0, int64(math.Ceil(d.Seconds())))
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// countdownCache memoises formatCountdown at whole-second resolution.
type countdownCache struct {
	secs int64
	s    string
}

func (c *countdownCache) String(d time.Duration) string {
	secs := max(0, int64(math.Ceil(d.Seconds())))
	if c.s == "" || c.secs != secs {
		c.secs, c.s = secs, formatCountdown(d)
	}
	return c.s
}

// ── audience HUD ───────────────────────────────────────────────────────

// hudLayout places the audience HUD (the design system's DeckScreen HUD):
// a row inset 4 % from the sides and 5 % from the bottom holding the
// segments, the slide counter and the countdown chip.
type hudLayout struct {
	fontPx       float64
	rowY, rowH   float64 // the row's top and height (= chip height)
	bar          presenterRect
	segGap       float32
	counterX     float64 // right edge of the counter
	chip         rectF   // zero-sized when there's no countdown
	countdownTxt float64 // centre of the countdown text inside the chip
	scrimY       float64 // the legibility scrim starts here
}

func layoutHUD(bufW, bufH int, counter, countdown string) hudLayout {
	W, H := float64(bufW), float64(bufH)
	var lo hudLayout
	lo.fontPx = math.Round(max(10, min(H*0.02, W*0.016)))
	f := styleCounter.faceAt(lo.fontPx)
	padY, padX := math.Round(lo.fontPx*0.3), math.Round(lo.fontPx*0.55)
	lo.rowH = math.Round(lineHeight(f) + 2*padY)
	left, right := math.Round(W*0.04), math.Round(W*0.96)
	bottom := math.Round(H * 0.95)
	lo.rowY = bottom - lo.rowH
	gap := math.Round(lo.fontPx)

	x := right
	if countdown != "" {
		cw := math.Round(textWidth(countdown, f, 0) + 2*padX)
		lo.chip = rectF{x: x - cw, y: lo.rowY, w: cw, h: lo.rowH}
		lo.countdownTxt = x - cw/2
		x -= cw + gap
	}
	lo.counterX = x
	x -= textWidth(counter, f, 0) + gap

	barH := math.Round(max(3, H*0.0035))
	lo.bar = presenterRect{
		x: int(left),
		y: int(math.Round(lo.rowY + (lo.rowH-barH)/2)),
		w: max(1, int(x-left)),
		h: int(barH),
	}
	lo.segGap = float32(max(2, math.Round(barH*1.25)))
	lo.scrimY = lo.rowY - lo.rowH*1.5
	return lo
}

// drawProgressOverlay draws the audience HUD when --progress is on.
func (g *Game) drawProgressOverlay(screen *ebiten.Image) {
	if !g.cfg.Progress {
		return
	}
	n := len(g.pageList)
	if n == 0 {
		return
	}
	W, H := screen.Bounds().Dx(), screen.Bounds().Dy()
	counter := g.pageLabel.String(g.listIdx, n)
	countdown := ""
	if g.autoDuration() > 0 {
		countdown = g.countdown.String(g.autoRemaining())
	}
	lo := layoutHUD(W, H, counter, countdown)

	// A soft ground-coloured scrim keeps the HUD legible on light slides.
	fillGradientQuad(screen, 0, float32(lo.scrimY), float32(W), float32(H), tokGround, tokGround, 0, 0.55, true)

	g.hudBar.draw(screen, lo.bar.x, lo.bar.y, lo.bar.w, lo.bar.h, n, g.listIdx, g.autoProgressFraction(), g.auto.Paused(),
		withAlpha(glassTint, 0.16), lo.segGap)

	f := styleCounter.faceAt(lo.fontPx)
	midY := lo.rowY + lo.rowH/2
	drawCapsCentred(screen, counter, f, lo.counterX, midY, text.AlignEnd, 0, withAlpha(tokInk, 0.7))
	if countdown != "" {
		c := lo.chip
		fillRoundRect(screen, float32(c.x), float32(c.y), float32(c.w), float32(c.h), float32(lo.fontPx*0.45),
			withAlpha(glassTint, 0.1))
		drawCapsCentred(screen, countdown, f, lo.countdownTxt, midY, text.AlignCenter, 0, tokInk)
	}
}
