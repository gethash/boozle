package app

import (
	"fmt"
	"image/color"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

const (
	overlayPanelH   = 24 // frosted backdrop height (px)
	segBarH         = 10 // segment height (px)
	segBarBottomPad = 7  // distance from screen bottom to segment bar bottom
	segGap          = 2  // horizontal gap between segments (px)
)

// rainbowStops are the brand gradient colors:
// #FF4D6D → #FF9F1C → #F9E900 → #2EE59D → #00D4FF → #5B7CFA → #B967FF → #FF61D8
var rainbowStops = [8][3]uint8{
	{255, 77, 109},
	{255, 159, 28},
	{249, 233, 0},
	{46, 229, 157},
	{0, 212, 255},
	{91, 124, 250},
	{185, 103, 255},
	{255, 97, 216},
}

// rainbowAt returns the interpolated brand gradient color at position t ∈ [0,1].
func rainbowAt(t float32, alpha uint8) color.RGBA {
	if t <= 0 {
		s := rainbowStops[0]
		return color.RGBA{s[0], s[1], s[2], alpha}
	}
	if t >= 1 {
		s := rainbowStops[len(rainbowStops)-1]
		return color.RGBA{s[0], s[1], s[2], alpha}
	}
	n := float32(len(rainbowStops) - 1)
	scaled := t * n
	i := int(scaled)
	f := scaled - float32(i)
	a, b := rainbowStops[i], rainbowStops[i+1]
	return color.RGBA{
		R: lerpU8(a[0], b[0], f),
		G: lerpU8(a[1], b[1], f),
		B: lerpU8(a[2], b[2], f),
		A: alpha,
	}
}

func lerpU8(a, b uint8, t float32) uint8 {
	return uint8(float32(a) + (float32(b)-float32(a))*t)
}

// drawProgressOverlay renders the frosted backdrop and one segment per slide
// when --progress is active.
//
// Completed segments use the brand rainbow gradient keyed by slide position.
// The current segment shows a dark track with the rainbow fill advancing as
// the auto-advance timer counts down. Future segments use the neutral dark.
func (g *Game) drawProgressOverlay(screen *ebiten.Image) {
	if !g.cfg.Progress {
		return
	}
	n := len(g.pageList)
	if n == 0 {
		return
	}

	W := float32(screen.Bounds().Dx())
	H := float32(screen.Bounds().Dy())

	// ── Frosted backdrop (#080A12 brand dark) ──────────────────────────────
	vector.FillRect(screen, 0, H-overlayPanelH, W, overlayPanelH,
		color.RGBA{8, 10, 18, 175}, false)
	vector.FillRect(screen, 0, H-overlayPanelH, W, 1,
		color.RGBA{255, 255, 255, 20}, false)

	// ── Segmented bar ─────────────────────────────────────────────────────
	gap := float32(segGap)
	segW := (W - gap*float32(n-1)) / float32(n)
	if segW < 1 {
		gap = 0
		segW = W / float32(n)
	}
	barY := H - float32(segBarBottomPad) - float32(segBarH)

	// t maps a segment index to its position in the rainbow gradient [0,1].
	gradPos := func(i int) float32 {
		if n == 1 {
			return 0
		}
		return float32(i) / float32(n-1)
	}

	for i := range n {
		x := float32(i) * (segW + gap)

		switch {
		case i < g.listIdx:
			// Completed — full rainbow color at this segment's gradient position.
			vector.FillRect(screen, x, barY, segW, segBarH,
				rainbowAt(gradPos(i), 210), false)

		case i == g.listIdx:
			// Current — dark track (#1F2937) with rainbow timer fill.
			vector.FillRect(screen, x, barY, segW, segBarH,
				color.RGBA{31, 41, 55, 200}, false)

			var frac float32
			var fillAlpha uint8
			if g.auto.IsActive() {
				if g.auto.Paused() {
					frac = float32(g.auto.FractionAtPause())
					fillAlpha = 120 // muted while paused
				} else {
					frac = float32(g.auto.Fraction())
					fillAlpha = 215
				}
			}

			// Last slide fully elapsed → show full rainbow color (whole bar done).
			if i == n-1 && frac >= 1 {
				vector.FillRect(screen, x, barY, segW, segBarH,
					rainbowAt(1, 210), false)
			} else if fw := segW * frac; fw > 0 {
				vector.FillRect(screen, x, barY, fw, segBarH,
					rainbowAt(gradPos(i), fillAlpha), false)
			}

		default:
			// Upcoming — brand neutral #374151.
			vector.FillRect(screen, x, barY, segW, segBarH,
				color.RGBA{55, 65, 81, 130}, false)
		}
	}

	// Page counter in the bottom-right of the frosted panel.
	label := fmt.Sprintf("%d / %d", g.listIdx+1, n)
	ebitenutil.DebugPrintAt(screen, label, int(W)-len(label)*7-6, int(H)-19)
}
