package app

import "github.com/hajimehoshi/ebiten/v2"

type transStyle int

const (
	transNone  transStyle = iota
	transSlide             // push left/right
	transFade              // cross-dissolve
)

const transFrames = 18 // ~300 ms at 60 fps

type transition struct {
	style    transStyle // configured default
	curStyle transStyle // style for the currently active transition
	active   bool
	frame    int
	frames   int
	dir      int // +1 forward, -1 backward, 0 non-directional
	prevImg    *ebiten.Image
	prevBounds renderBounds
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

func (tr *transition) capture(src *ebiten.Image, b renderBounds) {
	if tr.prevImg != nil {
		tr.prevImg.Deallocate()
	}
	w := src.Bounds().Dx()
	h := src.Bounds().Dy()
	tr.prevImg = ebiten.NewImage(w, h)
	tr.prevImg.DrawImage(src, nil)
	tr.prevBounds = b
}

func (tr *transition) clear() {
	if tr.prevImg != nil {
		tr.prevImg.Deallocate()
		tr.prevImg = nil
	}
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
	g.trans.capture(g.display, g.displayBounds)
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
		// new slide enters from the opposite side
		newOp := &ebiten.DrawImageOptions{}
		newOp.GeoM.Translate(
			float64(g.displayBounds.dstX)-float64(tr.dir)*W*(1-t),
			float64(g.displayBounds.dstY),
		)
		screen.DrawImage(g.display, newOp)
		// old slide exits toward dir side
		oldOp := &ebiten.DrawImageOptions{}
		oldOp.GeoM.Translate(
			float64(tr.prevBounds.dstX)+float64(tr.dir)*W*t,
			float64(tr.prevBounds.dstY),
		)
		screen.DrawImage(tr.prevImg, oldOp)

	case transFade:
		newOp := &ebiten.DrawImageOptions{}
		newOp.GeoM.Translate(float64(g.displayBounds.dstX), float64(g.displayBounds.dstY))
		newOp.ColorScale.ScaleAlpha(float32(t))
		screen.DrawImage(g.display, newOp)

		oldOp := &ebiten.DrawImageOptions{}
		oldOp.GeoM.Translate(float64(tr.prevBounds.dstX), float64(tr.prevBounds.dstY))
		oldOp.ColorScale.ScaleAlpha(float32(1 - t))
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
