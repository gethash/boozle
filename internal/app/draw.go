package app

import (
	"image"
	"image/color"
	"math"
	"sync"
	"unicode/utf8"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

// ── text ───────────────────────────────────────────────────────────────

// textWidth is the advance of s in face with tracking px added between
// characters.
func textWidth(s string, face *text.GoTextFace, tracking float64) float64 {
	if tracking == 0 {
		return text.Advance(s, face)
	}
	w, n := 0.0, 0
	for _, r := range s {
		w += text.Advance(string(r), face)
		n++
	}
	if n > 1 {
		w += tracking * float64(n-1)
	}
	return w
}

// lineHeight is the height of one line of face (ascent + descent).
func lineHeight(face *text.GoTextFace) float64 {
	m := face.Metrics()
	return m.HAscent + m.HDescent
}

// drawText draws one line of s at (x, y). hAlign positions x at the start,
// centre or end of the line; vAlign positions y at the top, middle or bottom
// of the line box. tracking adds px between characters (design-system
// letter-spacing, which text/v2 doesn't support directly).
func drawText(dst *ebiten.Image, s string, face *text.GoTextFace, x, y float64, hAlign, vAlign text.Align, tracking float64, clr color.Color) {
	if s == "" {
		return
	}
	op := &text.DrawOptions{}
	op.ColorScale.ScaleWithColor(clr)
	op.LayoutOptions.SecondaryAlign = vAlign
	if tracking == 0 {
		op.LayoutOptions.PrimaryAlign = hAlign
		op.GeoM.Translate(x, y)
		text.Draw(dst, s, face, op)
		return
	}
	switch hAlign {
	case text.AlignCenter:
		x -= textWidth(s, face, tracking) / 2
	case text.AlignEnd:
		x -= textWidth(s, face, tracking)
	}
	var buf [utf8.UTFMax]byte
	for _, r := range s {
		ch := string(buf[:utf8.EncodeRune(buf[:], r)])
		op.GeoM.Reset()
		op.GeoM.Translate(x, y)
		text.Draw(dst, ch, face, op)
		x += text.Advance(ch, face) + tracking
	}
}

// drawStyled draws s in style st at UI unit u.
func drawStyled(dst *ebiten.Image, s string, st textStyle, u, x, y float64, hAlign, vAlign text.Align, clr color.Color) {
	f := st.face(u)
	drawText(dst, s, f, x, y, hAlign, vAlign, st.trackingPx(f), clr)
}

// capsHeightRatio is the vertical room fitted values get per pixel of cap
// height: digits and colons only reach cap height, so fitting the full line
// box (with descender) would make timers needlessly small.
const capsHeightRatio = 1.3

// fitTextSize returns the largest size in buffer pixels, at most maxPx, at
// which s in style st is no wider than w and its caps no taller than h
// (with capsHeightRatio room). Glyph metrics scale linearly with size, so
// one measurement suffices. Meant for numbers: clocks, counters, timers.
func fitTextSize(s string, st textStyle, w, h, maxPx float64) float64 {
	if s == "" || w <= 0 || h <= 0 {
		return 0
	}
	const ref = 100.0
	f := st.faceAt(ref)
	sw := textWidth(s, f, st.trackingPx(f))
	ch := f.Metrics().CapHeight * capsHeightRatio
	if sw <= 0 || ch <= 0 {
		return 0
	}
	return math.Floor(min(maxPx, w/sw*ref, h/ch*ref))
}

// drawCapsCentred draws a line of numbers or capitals with its cap height
// centred on cy, which centres digits optically (text/v2's AlignCenter
// centres the whole line box, descender included).
func drawCapsCentred(dst *ebiten.Image, s string, face *text.GoTextFace, x, cy float64, hAlign text.Align, tracking float64, clr color.Color) {
	m := face.Metrics()
	baseline := cy + m.CapHeight/2
	drawText(dst, s, face, x, baseline-m.HAscent, hAlign, text.AlignStart, tracking, clr)
}

// ── shapes ─────────────────────────────────────────────────────────────

// shapePath is reused by the shape helpers; drawing happens on the Ebiten
// goroutine only.
var shapePath vector.Path

// appendRoundRect adds a rounded rectangle to p. r is clamped to half the
// shorter side, so a large r gives a pill.
func appendRoundRect(p *vector.Path, x, y, w, h, r float32) {
	r = max(0, min(r, w/2, h/2))
	if r == 0 {
		p.MoveTo(x, y)
		p.LineTo(x+w, y)
		p.LineTo(x+w, y+h)
		p.LineTo(x, y+h)
		p.Close()
		return
	}
	p.MoveTo(x+r, y)
	p.LineTo(x+w-r, y)
	p.ArcTo(x+w, y, x+w, y+r, r)
	p.LineTo(x+w, y+h-r)
	p.ArcTo(x+w, y+h, x+w-r, y+h, r)
	p.LineTo(x+r, y+h)
	p.ArcTo(x, y+h, x, y+h-r, r)
	p.LineTo(x, y+r)
	p.ArcTo(x, y, x+r, y, r)
	p.Close()
}

// Rounded rectangles are drawn as nine-slices of a small cached sprite.
// Ebitengine's antialiased path fill rasterises through a stencil atlas
// twice the path's width and keeps those textures around, so filling
// window-sized panels with it costs tens of MB of GPU memory. A sprite is
// (2r+2)² px: its corners are drawn 1:1 and its 2 px middle is stretched.

type roundSpriteKey struct {
	r, stroke    int
	fill, border color.RGBA
}

// roundSprite is a cached rounded-rect sprite cut into its nine slices.
type roundSprite struct {
	r      int
	slices [3][3]*ebiten.Image
}

const maxRoundSprites = 64

var roundSprites = map[roundSpriteKey]*roundSprite{}

func getRoundSprite(r, stroke int, fill, border color.RGBA) *roundSprite {
	k := roundSpriteKey{r: r, stroke: stroke, fill: fill, border: border}
	if sp, ok := roundSprites[k]; ok {
		return sp
	}
	if len(roundSprites) >= maxRoundSprites {
		clear(roundSprites) // sizes follow the window; drop stale ones
	}
	size := 2*r + 2
	img := ebiten.NewImage(size, size)
	fs := float32(size)
	if fill.A > 0 {
		shapePath.Reset()
		appendRoundRect(&shapePath, 0, 0, fs, fs, float32(r))
		op := &vector.DrawPathOptions{AntiAlias: true}
		op.ColorScale.ScaleWithColor(fill)
		vector.FillPath(img, &shapePath, nil, op)
	}
	if stroke > 0 && border.A > 0 {
		sw := float32(stroke)
		shapePath.Reset()
		appendRoundRect(&shapePath, sw/2, sw/2, fs-sw, fs-sw, max(0, float32(r)-sw/2))
		op := &vector.DrawPathOptions{AntiAlias: true}
		op.ColorScale.ScaleWithColor(border)
		vector.StrokePath(img, &shapePath, &vector.StrokeOptions{Width: sw}, op)
	}
	sp := &roundSprite{r: r}
	cuts := [4]int{0, r, size - r, size}
	for i := range 3 {
		for j := range 3 {
			sp.slices[i][j] = img.SubImage(image.Rect(cuts[i], cuts[j], cuts[i+1], cuts[j+1])).(*ebiten.Image)
		}
	}
	roundSprites[k] = sp
	return sp
}

// drawRoundRect draws a rounded rectangle with an optional inner border of
// stroke px. Coordinates are rounded to whole pixels so edges stay crisp.
func drawRoundRect(dst *ebiten.Image, x, y, w, h, r float64, stroke int, fill, border color.Color) {
	x, y, w, h = math.Round(x), math.Round(y), math.Round(w), math.Round(h)
	if w <= 0 || h <= 0 {
		return
	}
	ri := int(math.Round(max(0, min(r, w/2-1, h/2-1))))
	ri = max(ri, stroke)
	toRGBA := func(c color.Color) color.RGBA {
		if c == nil {
			return color.RGBA{}
		}
		return color.RGBAModel.Convert(c).(color.RGBA)
	}
	sp := getRoundSprite(ri, stroke, toRGBA(fill), toRGBA(border))
	fr := float64(ri)
	dx := [4]float64{x, x + fr, x + w - fr, x + w}
	dy := [4]float64{y, y + fr, y + h - fr, y + h}
	op := &ebiten.DrawImageOptions{}
	for i := range 3 {
		for j := range 3 {
			src := sp.slices[i][j]
			sw, sh := float64(src.Bounds().Dx()), float64(src.Bounds().Dy())
			dw, dh := dx[i+1]-dx[i], dy[j+1]-dy[j]
			if sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 {
				continue
			}
			op.GeoM.Reset()
			op.GeoM.Scale(dw/sw, dh/sh)
			op.GeoM.Translate(dx[i], dy[j])
			dst.DrawImage(src, op)
		}
	}
}

// fillRoundRect fills a rounded rectangle.
func fillRoundRect(dst *ebiten.Image, x, y, w, h, r float32, clr color.Color) {
	drawRoundRect(dst, float64(x), float64(y), float64(w), float64(h), float64(r), 0, clr, nil)
}

// strokeRoundRect strokes a rounded rectangle with the line inside its
// bounds.
func strokeRoundRect(dst *ebiten.Image, x, y, w, h, r, width float32, clr color.Color) {
	if width <= 0 {
		return
	}
	drawRoundRect(dst, float64(x), float64(y), float64(w), float64(h), float64(r), int(math.Round(float64(width))), nil, clr)
}

// drawPanel draws a design-system panel: surface fill, line hairline,
// radius-md corners.
func drawPanel(dst *ebiten.Image, r presenterRect, u float64) {
	if r.w <= 0 || r.h <= 0 {
		return
	}
	drawRoundRect(dst, float64(r.x), float64(r.y), float64(r.w), float64(r.h), radiusMD*u,
		int(max(1, math.Round(u*2/3))), tokSurface, tokLine)
}

// ── gradients ──────────────────────────────────────────────────────────

var whitePixel = sync.OnceValue(func() *ebiten.Image {
	img := ebiten.NewImage(3, 3)
	img.Fill(color.White)
	return img.SubImage(image.Rect(1, 1, 2, 2)).(*ebiten.Image)
})

// fillGradientQuad fills the axis-aligned rectangle (x0,y0)–(x1,y1) with a
// linear gradient: c0 → c1 horizontally (vertical false) or vertically.
// Colours are straight (non-premultiplied); a scales their opacity.
func fillGradientQuad(dst *ebiten.Image, x0, y0, x1, y1 float32, c0, c1 color.RGBA, a0, a1 float32, vertical bool) {
	if x1 <= x0 || y1 <= y0 {
		return
	}
	v := func(x, y float32, c color.RGBA, a float32) ebiten.Vertex {
		return ebiten.Vertex{
			DstX: x, DstY: y, SrcX: 1, SrcY: 1,
			ColorR: float32(c.R) / 255, ColorG: float32(c.G) / 255, ColorB: float32(c.B) / 255, ColorA: a,
		}
	}
	var vs [4]ebiten.Vertex
	if vertical {
		vs = [4]ebiten.Vertex{v(x0, y0, c0, a0), v(x1, y0, c0, a0), v(x0, y1, c1, a1), v(x1, y1, c1, a1)}
	} else {
		vs = [4]ebiten.Vertex{v(x0, y0, c0, a0), v(x1, y0, c1, a1), v(x0, y1, c0, a0), v(x1, y1, c1, a1)}
	}
	dst.DrawTriangles(vs[:], []uint16{0, 1, 2, 1, 3, 2}, whitePixel(), nil)
}

// fillSpectrumBand fills the part [from, to] of a w-wide spectrum bar at
// (x, y) with the linear spectrum, so separate segments read as slices of
// one gradient (the design system's background-size: n×100% trick).
func fillSpectrumBand(dst *ebiten.Image, x, y, w, h, from, to, alpha float32) {
	from, to = max(0, from), min(w, to)
	if to <= from || w <= 0 || alpha <= 0 {
		return
	}
	n := float32(len(spectrumStops) - 1)
	for k := range len(spectrumStops) - 1 {
		a, b := w*float32(k)/n, w*float32(k+1)/n
		a, b = max(a, from), min(b, to)
		if b <= a {
			continue
		}
		ca, cb := spectrumAt(float64(a/w)), spectrumAt(float64(b/w))
		fillGradientQuad(dst, x+a, y, x+b, y+h, ca, cb, alpha, alpha, false)
	}
}

// ensureImage returns *img if it is exactly w×h, else a fresh image of that
// size (deallocating the old one).
func ensureImage(img **ebiten.Image, w, h int) *ebiten.Image {
	if *img != nil {
		if b := (*img).Bounds(); b.Dx() == w && b.Dy() == h {
			return *img
		}
		(*img).Deallocate()
	}
	*img = ebiten.NewImage(w, h)
	return *img
}
