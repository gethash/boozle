package app

import (
	"fmt"
	"os"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/gethash/boozle/internal/display"
)

// Fullscreen placement on a specific monitor.
//
// Ebitengine goes fullscreen on whatever monitor it believes the window is
// on. On Linux (X11/XWayland) that belief comes from the window position,
// is cached for about a second, and window moves are asynchronous and may
// be overridden by the window manager. Calling SetMonitor and SetFullscreen
// back to back therefore often lands on the primary monitor. The placer
// instead waits for the window to be shown, moves it if needed, waits until
// Ebitengine reports the target monitor, and only then goes fullscreen —
// then checks the result once more and retries once.
const (
	placerSettleTicks  = 3  // let the window map before inspecting it
	placerMoveTimeout  = 90 // > Ebitengine's 60-tick current-monitor cache
	placerVerifyTicks  = 70 // re-check after the cache has refreshed
	placerLeavingTicks = 10 // after leaving fullscreen for a retry
)

type placerState int

const (
	placerIdle placerState = iota
	placerWaiting
	placerMoving
	placerVerifying
	placerLeaving
)

// windowSystem is the slice of Ebitengine the placer drives (faked in tests).
type windowSystem interface {
	currentMonitor() int // index, or -1 if unknown
	moveToMonitor(index int)
	setFullscreen(on bool)
	monitorName(index int) string
}

type ebitenWindow struct{}

func (ebitenWindow) currentMonitor() int { return display.Current() }

func (ebitenWindow) moveToMonitor(index int) {
	if m := display.Monitor(index); m != nil {
		ebiten.SetMonitor(m)
	}
}

func (ebitenWindow) setFullscreen(on bool)        { ebiten.SetFullscreen(on) }
func (ebitenWindow) monitorName(index int) string { return display.Name(index) }

type monitorPlacer struct {
	target  int
	state   placerState
	ticks   int
	retried bool
	debug   bool // --debug-stats: log every step
	logf    func(format string, args ...any)
}

func newMonitorPlacer(target int) *monitorPlacer {
	return &monitorPlacer{
		target: target,
		logf:   func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}
}

// start begins (or restarts) fullscreen placement.
func (p *monitorPlacer) start() {
	p.state, p.ticks, p.retried = placerWaiting, 0, false
}

// stop abandons placement (e.g. the user left fullscreen).
func (p *monitorPlacer) stop() { p.state = placerIdle }

// settled reports whether placement finished (or never started).
func (p *monitorPlacer) settled() bool { return p == nil || p.state == placerIdle }

func (p *monitorPlacer) enter(s placerState) { p.state, p.ticks = s, 0 }

func (p *monitorPlacer) trace(ws windowSystem, step string) {
	if p.debug {
		cur := ws.currentMonitor()
		p.logf("[boozle placer] %s (target %d %q, reported %d %q)", step, p.target, ws.monitorName(p.target), cur, ws.monitorName(cur))
	}
}

// tick advances placement by one Update. Safe on a nil placer.
func (p *monitorPlacer) tick(ws windowSystem) {
	if p == nil || p.state == placerIdle {
		return
	}
	p.ticks++
	switch p.state {
	case placerWaiting:
		if p.ticks < placerSettleTicks {
			return
		}
		if ws.currentMonitor() == p.target {
			p.trace(ws, "window is on target, going fullscreen")
			ws.setFullscreen(true)
			p.enter(placerVerifying)
			return
		}
		p.trace(ws, "moving window to target")
		ws.moveToMonitor(p.target)
		p.enter(placerMoving)

	case placerMoving:
		if ws.currentMonitor() == p.target {
			p.trace(ws, "window reached target, going fullscreen")
			ws.setFullscreen(true)
			p.enter(placerVerifying)
			return
		}
		if p.ticks >= placerMoveTimeout {
			p.logf("boozle: the window manager has not moved the window to monitor %d (%s); going fullscreen anyway",
				p.target, ws.monitorName(p.target))
			ws.setFullscreen(true)
			p.enter(placerVerifying)
		}

	case placerVerifying:
		if p.ticks < placerVerifyTicks {
			return
		}
		cur := ws.currentMonitor()
		if cur == p.target || cur < 0 {
			p.trace(ws, "fullscreen placement verified")
			p.enter(placerIdle)
			return
		}
		if !p.retried {
			p.trace(ws, "fullscreen on wrong monitor, retrying")
			p.retried = true
			ws.setFullscreen(false)
			p.enter(placerLeaving)
			return
		}
		p.logf("boozle: fullscreen ended up on monitor %d (%s) instead of %d (%s); see --list-monitors and the README's Linux notes",
			cur, ws.monitorName(cur), p.target, ws.monitorName(p.target))
		p.enter(placerIdle)

	case placerLeaving:
		if p.ticks >= placerLeavingTicks {
			ws.moveToMonitor(p.target)
			p.enter(placerMoving)
		}
	}
}

// toggleFullscreen leaves fullscreen, or re-enters it on the target monitor
// through the placer.
func toggleFullscreen(p *monitorPlacer, target int) *monitorPlacer {
	if ebiten.IsFullscreen() {
		if p != nil {
			p.stop()
		}
		ebiten.SetFullscreen(false)
		return p
	}
	if p == nil {
		p = newMonitorPlacer(target)
	}
	p.start()
	p.ticks = placerSettleTicks - 1 // the window is already shown
	return p
}
