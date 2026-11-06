package app

import (
	"image/color"
	"math"
)

// Design tokens from the Boozle design system (tokens.json, dark theme — the
// default theme and the only one the app draws). Keep the names and values
// in step with the design system; TestThemeTokensMatchDesignSystem pins them.
var (
	tokGround        = color.RGBA{0x0b, 0x0b, 0x12, 0xff} // ground: window background
	tokSurface       = color.RGBA{0x12, 0x12, 0x1b, 0xff} // surface: panels
	tokSurfaceRaised = color.RGBA{0x1a, 0x1a, 0x26, 0xff} // surface-raised: status cards, placeholders
	tokLine          = color.RGBA{0x26, 0x26, 0x36, 0xff} // line: panel hairlines
	tokLineControl   = color.RGBA{0x66, 0x66, 0x8a, 0xff} // line-control: "opened from" tile
	tokInk           = color.RGBA{0xf4, 0xf4, 0xfa, 0xff} // ink: values, headings
	tokInkMuted      = color.RGBA{0xa9, 0xa9, 0xc0, 0xff} // ink-muted: speaker notes
	tokInkFaint      = color.RGBA{0x85, 0x85, 0x9f, 0xff} // ink-faint: labels, captions
	tokTrack         = color.RGBA{0x22, 0x22, 0x2f, 0xff} // track: unfilled progress
	tokFocus         = color.RGBA{0x00, 0xd4, 0xff, 0xff} // focus: selected overview tile
	tokWarning       = color.RGBA{0xff, 0xc9, 0x4d, 0xff} // warning: PAUSED

	// glassTint is white for the HUD drawn on the slide itself, used only
	// translucent (.bz-hud: track at 16 %, countdown chip at 10 %).
	glassTint = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

// spectrumStops is the eight-stop brand spectrum (spectrum-rose … -pink).
// The spectrum means time: use it only for progress segments and the
// countdown ring, never for text, borders or fills.
var spectrumStops = [8]color.RGBA{
	{0xff, 0x4d, 0x6d, 0xff}, // spectrum-rose
	{0xff, 0x9f, 0x1c, 0xff}, // spectrum-amber
	{0xf9, 0xe9, 0x00, 0xff}, // spectrum-yellow
	{0x2e, 0xe5, 0x9d, 0xff}, // spectrum-mint
	{0x00, 0xd4, 0xff, 0xff}, // spectrum-cyan
	{0x5b, 0x7c, 0xfa, 0xff}, // spectrum-blue
	{0xb9, 0x67, 0xff, 0xff}, // spectrum-violet
	{0xff, 0x61, 0xd8, 0xff}, // spectrum-pink
}

// spectrumAt returns the linear spectrum gradient (rose → pink) at t ∈ [0,1].
func spectrumAt(t float64) color.RGBA {
	return gradientAt(spectrumStops[:], t)
}

// spectrumConicAt returns the conic spectrum (rose → pink → rose) at
// t ∈ [0,1], as used by the countdown ring.
func spectrumConicAt(t float64) color.RGBA {
	var stops [len(spectrumStops) + 1]color.RGBA
	copy(stops[:], spectrumStops[:])
	stops[len(stops)-1] = spectrumStops[0]
	return gradientAt(stops[:], t)
}

func gradientAt(stops []color.RGBA, t float64) color.RGBA {
	t = clamp01(t)
	scaled := t * float64(len(stops)-1)
	i := min(int(scaled), len(stops)-2)
	f := scaled - float64(i)
	a, b := stops[i], stops[i+1]
	lerp := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*f)) }
	return color.RGBA{lerp(a.R, b.R), lerp(a.G, b.G), lerp(a.B, b.B), 0xff}
}

// withAlpha returns c at opacity a ∈ [0,1], premultiplied as color.RGBA
// requires.
func withAlpha(c color.RGBA, a float64) color.RGBA {
	a = clamp01(a) * float64(c.A) / 255
	return color.RGBA{
		R: uint8(math.Round(float64(c.R) * a)),
		G: uint8(math.Round(float64(c.G) * a)),
		B: uint8(math.Round(float64(c.B) * a)),
		A: uint8(math.Round(255 * a)),
	}
}

// Spacing and radius tokens in design pixels. Multiply by the window's UI
// unit (uiUnit) to get buffer pixels.
const (
	space1 = 4
	space2 = 8
	space3 = 12
	space4 = 16

	radiusXS = 5
	radiusSM = 9
	radiusMD = 14
)

// uiUnit converts design pixels to buffer pixels for a bufW×bufH window.
// The buffer is already in physical pixels (see pixelBuffer), so scaling by
// the window's short side keeps the chrome the same size relative to the
// window on every display, HiDPI and --render-scale included: 1 at 720p,
// 1.5 at 1080p, 3 at 4K.
func uiUnit(bufW, bufH int) float64 {
	return max(1, float64(min(bufW, bufH))/720)
}

// Motion tokens (Motion.md): ease-out for things entering or responding,
// ease-in-out for things already on screen moving to a new place.
var (
	curveEaseOut   = cubicBezier{0.23, 1, 0.32, 1}
	curveEaseInOut = cubicBezier{0.77, 0, 0.175, 1}
)

func easeOut(t float64) float64   { return curveEaseOut.at(t) }
func easeInOut(t float64) float64 { return curveEaseInOut.at(t) }

// cubicBezier is a CSS cubic-bezier(x1, y1, x2, y2) timing function.
type cubicBezier struct{ x1, y1, x2, y2 float64 }

// at returns the eased progress for linear progress t ∈ [0,1].
func (c cubicBezier) at(t float64) float64 {
	t = clamp01(t)
	if t == 0 || t == 1 {
		return t
	}
	bez := func(p1, p2, s float64) float64 {
		u := 1 - s
		return 3*u*u*s*p1 + 3*u*s*s*p2 + s*s*s
	}
	// Solve x(s) = t for s: Newton steps, falling back to bisection.
	s := t
	for range 8 {
		x := bez(c.x1, c.x2, s) - t
		if math.Abs(x) < 1e-6 {
			return bez(c.y1, c.y2, s)
		}
		u := 1 - s
		dx := 3*u*u*c.x1 + 6*u*s*(c.x2-c.x1) + 3*s*s*(1-c.x2)
		if math.Abs(dx) < 1e-6 {
			break
		}
		s = clamp01(s - x/dx)
	}
	lo, hi := 0.0, 1.0
	s = t
	for range 40 {
		x := bez(c.x1, c.x2, s)
		if math.Abs(x-t) < 1e-7 {
			break
		}
		if x < t {
			lo = s
		} else {
			hi = s
		}
		s = (lo + hi) / 2
	}
	return bez(c.y1, c.y2, s)
}
