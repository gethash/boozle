// Package display contains Ebiten-side helpers for monitor selection
// and HiDPI scaling.
package display

import (
	"fmt"

	"github.com/hajimehoshi/ebiten/v2"
)

// PickMonitor selects the n-th monitor (0-indexed) and applies it as the
// current window's target. It is safe to call before ebiten.RunGame so the
// presentation opens fullscreen on the right display.
//
// If n is 0 we don't override the system's default — that already maps to
// the primary, and explicitly calling SetMonitor before the window exists
// can race with platform initialisation.
func PickMonitor(n int) error {
	if n <= 0 {
		return nil
	}
	monitors := ebiten.AppendMonitors(nil)
	if n >= len(monitors) {
		return fmt.Errorf("--monitor %d: only %d monitor(s) detected", n, len(monitors))
	}
	ebiten.SetMonitor(monitors[n])
	return nil
}
