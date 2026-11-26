package filewatch

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeFS is a stat function over an in-memory set of file versions.
type fakeFS map[string]fakeInfo

type fakeInfo struct {
	size int64
	mod  time.Time
}

func (f fakeInfo) Name() string       { return "" }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() fs.FileMode  { return 0o644 }
func (f fakeInfo) ModTime() time.Time { return f.mod }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

func (f fakeFS) stat(p string) (os.FileInfo, error) {
	if info, ok := f[p]; ok {
		return info, nil
	}
	return nil, fs.ErrNotExist
}

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// polls runs n polls and returns how many reported a change.
func polls(w *Watcher, n int) int {
	events := 0
	for range n {
		if w.poll() {
			events++
		}
	}
	return events
}

func TestNoChangeNoEvent(t *testing.T) {
	files := fakeFS{"deck.pdf": {size: 10, mod: t0}}
	w := newWatcher([]string{"deck.pdf", "deck.boozle.toml"}, files.stat)
	if got := polls(w, 5); got != 0 {
		t.Fatalf("events = %d, want 0", got)
	}
}

func TestChangeReportedOnceAfterSettling(t *testing.T) {
	files := fakeFS{"deck.pdf": {size: 10, mod: t0}}
	w := newWatcher([]string{"deck.pdf"}, files.stat)

	files["deck.pdf"] = fakeInfo{size: 20, mod: t0.Add(time.Second)}
	if w.poll() {
		t.Fatal("change reported before it settled")
	}
	if !w.poll() {
		t.Fatal("settled change not reported")
	}
	if got := polls(w, 3); got != 0 {
		t.Fatalf("events after settling = %d, want 0", got)
	}
}

func TestChunkedWriteIsOneEvent(t *testing.T) {
	files := fakeFS{"deck.pdf": {size: 10, mod: t0}}
	w := newWatcher([]string{"deck.pdf"}, files.stat)

	// The file grows on every poll, like pdflatex writing in place.
	events := 0
	for i := range 5 {
		files["deck.pdf"] = fakeInfo{size: int64(100 * (i + 1)), mod: t0.Add(time.Duration(i+1) * time.Second)}
		if w.poll() {
			events++
		}
	}
	if events != 0 {
		t.Fatalf("events while still writing = %d, want 0", events)
	}
	if got := polls(w, 3); got != 1 {
		t.Fatalf("events after the write finished = %d, want 1", got)
	}
}

func TestDeleteAndRecreateIsOneEvent(t *testing.T) {
	files := fakeFS{"deck.pdf": {size: 10, mod: t0}}
	w := newWatcher([]string{"deck.pdf"}, files.stat)

	delete(files, "deck.pdf")
	if w.poll() {
		t.Fatal("deletion reported before it settled")
	}
	files["deck.pdf"] = fakeInfo{size: 12, mod: t0.Add(time.Second)}
	if got := polls(w, 3); got != 1 {
		t.Fatalf("events = %d, want 1", got)
	}
}

func TestChangedBackIsNoEvent(t *testing.T) {
	files := fakeFS{"deck.pdf": {size: 10, mod: t0}}
	w := newWatcher([]string{"deck.pdf"}, files.stat)

	files["deck.pdf"] = fakeInfo{size: 11, mod: t0.Add(time.Second)}
	w.poll()
	files["deck.pdf"] = fakeInfo{size: 10, mod: t0}
	if got := polls(w, 3); got != 0 {
		t.Fatalf("events = %d, want 0", got)
	}
}

func TestAppearingFileIsAChange(t *testing.T) {
	files := fakeFS{"deck.pdf": {size: 10, mod: t0}}
	w := newWatcher([]string{"deck.pdf", "deck.boozle.toml"}, files.stat)

	files["deck.boozle.toml"] = fakeInfo{size: 5, mod: t0}
	if got := polls(w, 2); got != 1 {
		t.Fatalf("events = %d, want 1", got)
	}
}

// TestRealFiles runs the polling goroutine against a temp dir: an atomic
// rename over the watched file must be noticed.
func TestRealFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deck.pdf")
	if err := os.WriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := New([]string{path}, 10*time.Millisecond)
	defer w.Close()

	tmp := filepath.Join(dir, "deck.pdf.tmp")
	if err := os.WriteFile(tmp, []byte("two-two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Changes():
	case <-time.After(5 * time.Second):
		t.Fatal("no change reported after rename")
	}
	select {
	case <-w.Changes():
		t.Fatal("one rename reported twice")
	case <-time.After(100 * time.Millisecond):
	}
	w.Close() // idempotent
}
