// Package display contains Ebiten-side helpers for monitor selection
// and HiDPI scaling.
package display

import (
	"errors"
	"fmt"

	"github.com/hajimehoshi/ebiten/v2"
)

// PickMonitor selects the n-th monitor (0-indexed) and applies it as the
// current window's target. It is safe to call before ebiten.RunGame so the
// presentation opens fullscreen on the right display.
func PickMonitor(n int) error {
	monitors := ebiten.AppendMonitors(nil)
	if n < 0 || n >= len(monitors) {
		return fmt.Errorf("--monitor %d: only %d monitor(s) detected", n, len(monitors))
	}
	ebiten.SetMonitor(monitors[n])
	return nil
}

var errListDone = errors.New("list done")

type listGame struct{ printed bool }

func (g *listGame) Update() error {
	if g.printed {
		return errListDone
	}
	monitors := ebiten.AppendMonitors(nil)
	for i, m := range monitors {
		fmt.Printf("  %d: %s (scale %.1fx)\n", i, m.Name(), m.DeviceScaleFactor())
	}
	g.printed = true
	return errListDone
}

func (g *listGame) Draw(*ebiten.Image) {}

func (g *listGame) Layout(int, int) (int, int) { return 1, 1 }

// PrintMonitors runs a minimal one-frame Ebiten window to enumerate all
// connected monitors, then exits. The window is 1×1 and undecorated so the
// flash is imperceptible.
func PrintMonitors() error {
	ebiten.SetWindowDecorated(false)
	ebiten.SetWindowSize(1, 1)
	ebiten.SetWindowTitle("")
	if err := ebiten.RunGame(&listGame{}); err != nil && !errors.Is(err, errListDone) {
		return err
	}
	return nil
}
