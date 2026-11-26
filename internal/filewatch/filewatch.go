// Package filewatch notices when files change by polling their size and
// modification time.
//
// Polling is deliberate. Editors and PDF exporters often save by writing a
// temp file and renaming it over the original, which drops inotify/kqueue
// watches on the old inode, and file-change events are unreliable on network
// shares and cloud-synced folders. Two stats every half second cost nothing.
package filewatch

import (
	"os"
	"sync"
	"time"
)

// DefaultInterval is how often New polls when given a zero interval.
const DefaultInterval = 500 * time.Millisecond

// signature identifies a version of a file; the zero value means "absent".
type signature struct {
	exists bool
	size   int64
	mod    time.Time
}

func (s signature) equal(o signature) bool {
	return s.exists == o.exists && s.size == o.size && s.mod.Equal(o.mod)
}

func sameAll(a, b []signature) bool {
	for i := range a {
		if !a[i].equal(b[i]) {
			return false
		}
	}
	return true
}

// Watcher reports changes to a fixed set of paths. A change is reported once
// the files have stopped changing for one poll interval, so a file written
// in several chunks (pdflatex writes in place) or replaced by delete and
// recreate yields a single event. Missing files are fine: appearing and
// disappearing count as changes.
type Watcher struct {
	paths   []string
	stat    func(string) (os.FileInfo, error)
	changes chan struct{}

	last    []signature // the version last reported (or seen at start)
	pending []signature // a changed version waiting to settle; nil = none

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// New starts watching paths, polling every interval (DefaultInterval when
// zero). The current state of the files is the baseline: only later
// changes are reported. Call Close to stop.
func New(paths []string, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = DefaultInterval
	}
	w := newWatcher(paths, os.Stat)
	go w.run(interval)
	return w
}

// newWatcher returns a watcher that is not polling yet (tests drive poll).
func newWatcher(paths []string, stat func(string) (os.FileInfo, error)) *Watcher {
	w := &Watcher{
		paths:   append([]string(nil), paths...),
		stat:    stat,
		changes: make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	w.last = w.snapshot()
	return w
}

// Changes delivers one value per settled change. Changes that happen before
// the previous one was received are merged into it.
func (w *Watcher) Changes() <-chan struct{} { return w.changes }

// Close stops polling. It is safe to call more than once.
func (w *Watcher) Close() {
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
}

func (w *Watcher) run(interval time.Duration) {
	defer close(w.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			if w.poll() {
				select {
				case w.changes <- struct{}{}:
				default: // an unread event already covers this one
				}
			}
		}
	}
}

// poll compares the files with the last reported version and reports
// whether a change has now settled.
func (w *Watcher) poll() bool {
	cur := w.snapshot()
	switch {
	case w.pending == nil:
		if !sameAll(cur, w.last) {
			w.pending = cur // changed: wait one interval for it to settle
		}
		return false
	case sameAll(cur, w.last):
		w.pending = nil // changed and changed back
		return false
	case sameAll(cur, w.pending):
		w.last, w.pending = cur, nil
		return true
	default:
		w.pending = cur // still being written
		return false
	}
}

func (w *Watcher) snapshot() []signature {
	out := make([]signature, len(w.paths))
	for i, p := range w.paths {
		if info, err := w.stat(p); err == nil {
			out[i] = signature{exists: true, size: info.Size(), mod: info.ModTime()}
		}
	}
	return out
}
