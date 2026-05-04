package app

import (
	"github.com/hajimehoshi/ebiten/v2"

	"github.com/gethash/boozle/internal/pdf"
)

type transStyle int

const (
	transNone  transStyle = iota
	transSlide            // push left/right
	transFade             // cross-dissolve
)

const transFrames = 18 // ~300 ms at 60 fps

// transition holds the outgoing slide while a page-change animation runs.
// The outgoing image is a *pinned* cache entry — no copy is made.
type transition struct {
	style    transStyle // configured default
	curStyle transStyle // style for the currently active transition
	active   bool
	frame    int
	frames   int
	dir      int // +1 forward, -1 backward, 0 non-directional

	prevImg    *ebiten.Image
	prevKey    pdf.CacheKey
	prevBounds renderBounds
	cache      *pdf.Cache
	pinned     bool
}

func parseTransStyle(s string) transStyle {
	switch s {
	case "slide":
		return transSlide
	case "fade":
		return transFade
	default:
		return transNone
	}
}

func (tr *transition) progress() float64 {
	if tr.frames <= 0 {
		return 1
	}
	return clamp01(float64(tr.frame) / float64(tr.frames))
}

// capture pins the previous slide's cache entry so it is safe to draw while
// the transition runs. Pin is a no-op if prevKey is not in the cache (e.g.
// the previous frame used mipmap reuse) — in that case we just hold the
// pointer; the cache is unlikely to evict it within the ~300 ms animation.
func (tr *transition) capture(prev *ebiten.Image, prevKey pdf.CacheKey, b renderBounds, cache *pdf.Cache) {
	tr.releasePin()
	tr.prevImg = prev
	tr.prevKey = prevKey
	tr.prevBounds = b
	tr.cache = cache
	if cache != nil {
		cache.Pin(prevKey)
		tr.pinned = true
	}
}

func (tr *transition) releasePin() {
	if tr.pinned && tr.cache != nil {
		tr.cache.Unpin(tr.prevKey)
	}
	tr.pinned = false
}

func (tr *transition) clear() {
	tr.releasePin()
	tr.prevImg = nil
	tr.active = false
}

// beginTransition captures the current slide and arms the transition.
// dir: +1 = forward, -1 = backward, 0 = non-directional jump (always fades).
func (g *Game) beginTransition(dir int) {
	if g.display == nil || g.trans.style == transNone {
		return
	}
	if g.trans.active {
		g.trans.clear()
	}
	g.trans.capture(g.display, g.displayKey, g.displayBounds, g.cache)
	g.trans.active = true
	g.trans.frame = 0
	g.trans.dir = dir
	if dir == 0 {
		g.trans.curStyle = transFade
	} else {
		g.trans.curStyle = g.trans.style
	}
}

// drawTransition renders both old and new slide with the active animation.
// Called from Draw() instead of the normal g.display draw when a transition is active.
func (g *Game) drawTransition(screen *ebiten.Image) {
	t := easeInOut(g.trans.progress()) // easeInOut defined in overview.go
	tr := &g.trans

	switch tr.curStyle {
	case transSlide:
		W := float64(g.bufW)
		// Forward navigation pushes left: the new slide enters from the right.
		newOp := &ebiten.DrawImageOptions{}
		applyDisplayScale(&newOp.GeoM, g.displayBounds)
		newOp.GeoM.Translate(
			float64(g.displayBounds.dstX)+float64(tr.dir)*W*(1-t),
			float64(g.displayBounds.dstY),
		)
		newOp.Filter = ebiten.FilterLinear
		screen.DrawImage(g.display, newOp)
		oldOp := &ebiten.DrawImageOptions{}
		applyDisplayScale(&oldOp.GeoM, tr.prevBounds)
		oldOp.GeoM.Translate(
			float64(tr.prevBounds.dstX)-float64(tr.dir)*W*t,
			float64(tr.prevBounds.dstY),
		)
		oldOp.Filter = ebiten.FilterLinear
		screen.DrawImage(tr.prevImg, oldOp)

	case transFade:
		newOp := &ebiten.DrawImageOptions{}
		applyDisplayScale(&newOp.GeoM, g.displayBounds)
		newOp.GeoM.Translate(float64(g.displayBounds.dstX), float64(g.displayBounds.dstY))
		newOp.ColorScale.ScaleAlpha(float32(t))
		newOp.Filter = ebiten.FilterLinear
		screen.DrawImage(g.display, newOp)

		oldOp := &ebiten.DrawImageOptions{}
		applyDisplayScale(&oldOp.GeoM, tr.prevBounds)
		oldOp.GeoM.Translate(float64(tr.prevBounds.dstX), float64(tr.prevBounds.dstY))
		oldOp.ColorScale.ScaleAlpha(float32(1 - t))
		oldOp.Filter = ebiten.FilterLinear
		screen.DrawImage(tr.prevImg, oldOp)
	}
}

func sign(x int) int {
	if x > 0 {
		return 1
	}
	if x < 0 {
		return -1
	}
	return 0
}
