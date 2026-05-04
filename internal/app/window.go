package app

import (
	"github.com/hajimehoshi/ebiten/v2"

	"github.com/gethash/boozle/internal/display"
)

// fullscreenOnMonitor applies fullscreen after the Ebiten window exists.
// On Linux, applying fullscreen during startup can cause Ebiten/GLFW to
// re-detect the just-created window's monitor and fall back to the primary
// display. Moving the live window first makes --monitor reliable.
type fullscreenOnMonitor struct {
	game       ebiten.Game
	monitorIdx int
	applied    bool
}

func (g *fullscreenOnMonitor) Update() error {
	if !g.applied {
		if err := setFullscreenOnMonitor(g.monitorIdx, true); err != nil {
			return err
		}
		g.applied = true
	}
	return g.game.Update()
}

func (g *fullscreenOnMonitor) Draw(screen *ebiten.Image) {
	g.game.Draw(screen)
}

func (g *fullscreenOnMonitor) Layout(outsideWidth, outsideHeight int) (int, int) {
	return g.game.Layout(outsideWidth, outsideHeight)
}

func setFullscreenOnMonitor(monitorIdx int, fullscreen bool) error {
	if fullscreen {
		if err := display.PickMonitor(monitorIdx); err != nil {
			return err
		}
	}
	ebiten.SetFullscreen(fullscreen)
	return nil
}
