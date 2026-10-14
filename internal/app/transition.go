package app

import (
	"github.com/hajimehoshi/ebiten/v2"
)

type transStyle int

const (
	transNone  transStyle = iota
	transSlide            // push left/right
	transFade             // cross-dissolve
)

const transFrames = 18 // ~300 ms at 60 fps

// transition holds the outgoing slide while a page-change animation runs.
// The outgoing image stays pinned in the cache — no copy is made. The
// animation only advances once the incoming slide is on screen (see
// Game.Update), so slow renders delay rather than truncate it.
type transition struct {
	style    transStyle // configured default
	curStyle transStyle // style for the currently active transition
	active   bool
	frame    int
	frames   int
	wait     int // frames spent waiting for the incoming slide
	dir      int // +1 forward, -1 backward, 0 non-directional

	prev       pinnedSlide
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

func (tr *transition) clear() {
	tr.prev.release()
	tr.active = false
}

// beginTransition captures the current slide and arms the transition.
// dir: +1 = forward, -1 = backward, 0 = non-directional jump (always fades).
func (g *Game) beginTransition(dir int) {
	if g.disp.img == nil || g.trans.style == transNone {
		return
	}
	g.trans.clear()
	g.trans.prev.set(g.disp.cache, g.disp.img, g.disp.key)
	g.trans.prevBounds = g.dispBounds
	g.trans.active = true
	g.trans.frame = 0
	g.trans.wait = 0
	g.trans.dir = dir
	if dir == 0 {
		g.trans.curStyle = transFade
	} else {
		g.trans.curStyle = g.trans.style
	}
}

// drawTransition renders both old and new slide with the active animation.
// Called from Draw() instead of the normal slide draw when a transition is active.
func (g *Game) drawTransition(screen *ebiten.Image) {
	t := easeInOut(g.trans.progress())
	tr := &g.trans

	switch tr.curStyle {
	case transSlide:
		// Forward navigation pushes left: the new slide enters from the right.
		w := float64(g.bufW)
		drawSlide(screen, g.disp.img, g.dispBounds, float64(tr.dir)*w*(1-t), 1)
		drawSlide(screen, tr.prev.img, tr.prevBounds, -float64(tr.dir)*w*t, 1)

	case transFade:
		// Cross-dissolve: the old slide stays opaque underneath while the
		// new one fades in on top, so the background never shows through.
		drawSlide(screen, tr.prev.img, tr.prevBounds, 0, 1)
		drawSlide(screen, g.disp.img, g.dispBounds, 0, float32(t))
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
