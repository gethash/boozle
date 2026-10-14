package app

import "testing"

// fakeWindow simulates Linux behaviour: the reported monitor only follows a
// move after lag ticks (Ebitengine's position cache + async X11 moves), and
// a window manager may ignore moves entirely.
type fakeWindow struct {
	reported   int  // what currentMonitor returns
	actual     int  // where the window really is
	ignoreMove bool // window manager refuses to move the window
	lag, since int
	fullscreen bool
	fsMonitor  int // monitor fullscreen landed on
	moves      int
}

func (w *fakeWindow) currentMonitor() int {
	w.since++
	if w.since >= w.lag {
		w.reported = w.actual
	}
	return w.reported
}

func (w *fakeWindow) moveToMonitor(i int) {
	w.moves++
	if !w.ignoreMove {
		w.actual = i
		w.since = 0
	}
}

func (w *fakeWindow) setFullscreen(on bool) {
	w.fullscreen = on
	if on {
		// Ebitengine goes fullscreen on the monitor it *believes* the
		// window is on — the stale cached value.
		w.fsMonitor = w.reported
		w.actual = w.reported
	}
}

func (w *fakeWindow) monitorName(int) string { return "fake" }

func runPlacer(p *monitorPlacer, w *fakeWindow, ticks int) {
	for range ticks {
		p.tick(w)
	}
}

func quietPlacer(target int) (*monitorPlacer, *[]string) {
	var logs []string
	p := newMonitorPlacer(target)
	p.logf = func(format string, args ...any) { logs = append(logs, format) }
	p.start()
	return p, &logs
}

func TestPlacerFastPathWhenAlreadyOnTarget(t *testing.T) {
	p, logs := quietPlacer(1)
	w := &fakeWindow{reported: 1, actual: 1}
	runPlacer(p, w, 200)
	if !w.fullscreen || w.fsMonitor != 1 || w.moves != 0 {
		t.Fatalf("fullscreen=%v on %d after %d moves; want fullscreen on 1 without moving", w.fullscreen, w.fsMonitor, w.moves)
	}
	if !p.settled() || len(*logs) != 0 {
		t.Fatalf("settled=%v logs=%v", p.settled(), *logs)
	}
}

func TestPlacerWaitsForStaleMonitorCache(t *testing.T) {
	// The window manager put the window on the primary (0); after the move
	// the monitor cache keeps reporting 0 for ~60 ticks. The old code went
	// fullscreen immediately and landed on 0.
	p, logs := quietPlacer(1)
	w := &fakeWindow{reported: 0, actual: 0, lag: 60}
	runPlacer(p, w, 300)
	if !w.fullscreen || w.fsMonitor != 1 {
		t.Fatalf("fullscreen=%v on monitor %d, want fullscreen on 1", w.fullscreen, w.fsMonitor)
	}
	if !p.settled() || len(*logs) != 0 {
		t.Fatalf("settled=%v logs=%v", p.settled(), *logs)
	}
}

func TestPlacerGivesUpAndLogsWhenWindowManagerRefuses(t *testing.T) {
	p, logs := quietPlacer(1)
	w := &fakeWindow{reported: 0, actual: 0, ignoreMove: true}
	runPlacer(p, w, 1000)
	if !w.fullscreen {
		t.Fatal("placer should still go fullscreen (best effort)")
	}
	if !p.settled() {
		t.Fatal("placer should finish")
	}
	if w.moves != 2 {
		t.Fatalf("moves = %d, want initial move + one retry", w.moves)
	}
	if len(*logs) == 0 {
		t.Fatal("a failed placement should be logged")
	}
}

func TestPlacerNilIsSafe(t *testing.T) {
	var p *monitorPlacer
	p.tick(&fakeWindow{})
	if !p.settled() {
		t.Fatal("nil placer counts as settled")
	}
}
