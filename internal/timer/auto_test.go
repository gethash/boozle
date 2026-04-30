package timer

import (
	"testing"
	"time"
)

// fakeClock makes Auto deterministic in tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time         { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newWithClock(def time.Duration, perPage map[int]time.Duration, c *fakeClock) *Auto {
	a := New(def, perPage)
	a.now = c.now
	return a
}

func TestAutoInactiveWhenZero(t *testing.T) {
	a := New(0, nil)
	if a.IsActive() {
		t.Fatal("zero duration should be inactive")
	}
	if a.ShouldAdvance() {
		t.Fatal("inactive timer should not advance")
	}
}

func TestAutoFiresAfterInterval(t *testing.T) {
	c := &fakeClock{t: time.Unix(0, 0)}
	a := newWithClock(10*time.Second, nil, c)
	a.Reset(1)

	if a.ShouldAdvance() {
		t.Error("should not advance immediately after Reset")
	}
	c.advance(5 * time.Second)
	if a.ShouldAdvance() {
		t.Error("should not advance halfway through interval")
	}
	c.advance(5 * time.Second)
	if !a.ShouldAdvance() {
		t.Error("should advance once interval has fully elapsed")
	}
}

func TestAutoPausePreventsFire(t *testing.T) {
	c := &fakeClock{t: time.Unix(0, 0)}
	a := newWithClock(10*time.Second, nil, c)
	a.Reset(1)
	a.TogglePause(1)
	c.advance(time.Hour)
	if a.ShouldAdvance() {
		t.Error("paused timer must not advance regardless of elapsed time")
	}
	a.TogglePause(1) // resume
	if a.ShouldAdvance() {
		t.Error("freshly resumed timer should re-arm full interval")
	}
	c.advance(10 * time.Second)
	if !a.ShouldAdvance() {
		t.Error("resumed timer should advance after the new interval elapses")
	}
}

func TestAutoFraction(t *testing.T) {
	c := &fakeClock{t: time.Unix(0, 0)}
	a := newWithClock(10*time.Second, nil, c)

	if f := a.Fraction(); f != 0 {
		t.Fatalf("fraction before Reset should be 0, got %v", f)
	}
	a.Reset(1)
	if f := a.Fraction(); f != 0 {
		t.Fatalf("fraction immediately after Reset should be 0, got %v", f)
	}
	c.advance(5 * time.Second)
	if f := a.Fraction(); f < 0.49 || f > 0.51 {
		t.Fatalf("fraction at midpoint should be ~0.5, got %v", f)
	}
	c.advance(5 * time.Second)
	if f := a.Fraction(); f != 1 {
		t.Fatalf("fraction after interval elapsed should be 1, got %v", f)
	}

	// Paused timer always returns 0.
	a.Reset(1)
	c.advance(3 * time.Second)
	a.TogglePause(1)
	if f := a.Fraction(); f != 0 {
		t.Fatalf("fraction while paused should be 0, got %v", f)
	}
}

func TestFractionAtPause(t *testing.T) {
	c := &fakeClock{t: time.Unix(0, 0)}
	a := newWithClock(10*time.Second, nil, c)

	if got := a.FractionAtPause(); got != 0 {
		t.Fatalf("FractionAtPause before any event should be 0, got %v", got)
	}

	a.Reset(1)
	c.advance(3 * time.Second) // fraction is now 0.3

	a.TogglePause(1)
	if !a.Paused() {
		t.Fatal("expected paused after TogglePause")
	}
	got := a.FractionAtPause()
	if got < 0.29 || got > 0.31 {
		t.Fatalf("FractionAtPause should be ~0.3 after pausing at 3s/10s, got %v", got)
	}

	// Time passing while paused must not change the captured fraction.
	c.advance(5 * time.Second)
	if got2 := a.FractionAtPause(); got2 != got {
		t.Fatalf("FractionAtPause changed while paused: %v → %v", got, got2)
	}

	// Resuming must not reset fractionAtPause.
	a.TogglePause(1)
	if a.Paused() {
		t.Fatal("expected running after second TogglePause")
	}
	if got3 := a.FractionAtPause(); got3 != got {
		t.Fatalf("FractionAtPause changed on resume: %v → %v", got, got3)
	}

	// Second pause captures the new fraction.
	c.advance(5 * time.Second) // timer re-armed at resume; 5s/10s = 0.5
	a.TogglePause(1)
	got4 := a.FractionAtPause()
	if got4 < 0.49 || got4 > 0.51 {
		t.Fatalf("second pause FractionAtPause should be ~0.5, got %v", got4)
	}
}

func TestAutoPerPageOverride(t *testing.T) {
	c := &fakeClock{t: time.Unix(0, 0)}
	a := newWithClock(10*time.Second, map[int]time.Duration{
		3: 1 * time.Second,
	}, c)
	a.Reset(3)
	c.advance(2 * time.Second)
	if !a.ShouldAdvance() {
		t.Error("page 3's 1s override should have fired by t=2s")
	}
}
