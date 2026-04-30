// Package timer implements boozle's auto-advance scheduler with
// pause/resume and per-page duration overrides.
package timer

import "time"

// Auto schedules page advances at a configured interval. It is not
// goroutine-safe; the boozle Game owns the only instance and reads
// it on the Ebiten Update goroutine.
type Auto struct {
	Default time.Duration
	// PerPage holds optional duration overrides keyed by 1-indexed page.
	PerPage map[int]time.Duration

	paused          bool
	nextAt          time.Time
	currentDur      time.Duration // duration of the currently active page's interval
	fractionAtPause float64       // fraction captured at the moment of pausing
	now             func() time.Time
}

// New creates an Auto. If def == 0, IsActive returns false and ShouldAdvance
// always returns false (i.e. auto-advance is disabled).
func New(def time.Duration, perPage map[int]time.Duration) *Auto {
	return &Auto{
		Default: def,
		PerPage: perPage,
		now:     time.Now,
	}
}

// IsActive reports whether auto-advance is configured at all.
func (a *Auto) IsActive() bool { return a.Default > 0 }

// Paused reports whether the timer is currently paused.
func (a *Auto) Paused() bool { return a.paused }

// Reset (re)arms the timer for the given 1-indexed page. Has no effect if
// auto-advance is inactive or paused.
func (a *Auto) Reset(page1 int) {
	if !a.IsActive() || a.paused {
		return
	}
	a.currentDur = a.durationFor(page1)
	a.nextAt = a.now().Add(a.currentDur)
}

// Fraction returns how far through the current page's interval we are
// (0 = just reset, 1 = about to fire). Returns 0 when inactive or paused.
func (a *Auto) Fraction() float64 {
	if !a.IsActive() || a.paused || a.currentDur == 0 {
		return 0
	}
	remaining := a.nextAt.Sub(a.now())
	if remaining <= 0 {
		return 1
	}
	return 1 - float64(remaining)/float64(a.currentDur)
}

// ShouldAdvance returns true once per scheduled interval. The caller is
// responsible for calling Reset(newPage) after acting on a true result.
func (a *Auto) ShouldAdvance() bool {
	if !a.IsActive() || a.paused {
		return false
	}
	return !a.now().Before(a.nextAt)
}

// TogglePause flips the paused state. Resuming arms the timer fresh
// (no held-over remaining time, by design — keeps the model simple).
func (a *Auto) TogglePause(page1 int) {
	if !a.paused {
		a.fractionAtPause = a.Fraction() // capture before pausing
	}
	a.paused = !a.paused
	if !a.paused {
		a.Reset(page1)
	}
}

// FractionAtPause returns the Fraction value captured at the most recent pause.
// Returns 0 if the timer has never been paused.
func (a *Auto) FractionAtPause() float64 { return a.fractionAtPause }

func (a *Auto) durationFor(page1 int) time.Duration {
	if v, ok := a.PerPage[page1]; ok && v > 0 {
		return v
	}
	return a.Default
}
