package app

import "time"

// redrawGate lets Draw skip frames whose inputs haven't changed. Both games
// run with ebiten.SetScreenClearedEveryFrame(false), so a skipped Draw keeps
// the previous frame on screen; a static slide then costs next to no GPU
// time. A full redraw still happens at least once per second as a safety
// net against state that isn't captured in the signature.
type redrawGate[T comparable] struct {
	last  T
	drawn bool
	at    time.Time
}

// needed reports whether a frame with signature sig must be drawn, and
// records it as drawn if so.
func (r *redrawGate[T]) needed(sig T, now time.Time) bool {
	if r.drawn && sig == r.last && now.Sub(r.at) < time.Second {
		return false
	}
	r.last, r.drawn, r.at = sig, true, now
	return true
}
