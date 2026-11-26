package pdf

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gethash/boozle/internal/pdf/pdftest"
)

var snapshotPages = []pdftest.Page{
	{W: 800, H: 450, R: 1},
	{W: 800, H: 450, G: 1},
	{W: 800, H: 450, B: 1},
}

// hermeticSnapshots points the cache at a temp dir. It opens a document
// first so the compiled PDFium module still comes from the shared user
// cache (compiling under -race takes ~10 s).
func hermeticSnapshots(t *testing.T) (dir, pdfPath string) {
	t.Helper()
	dir = t.TempDir()
	pdfPath = filepath.Join(dir, "deck.pdf")
	if err := pdftest.Write(pdfPath, snapshotPages); err != nil {
		t.Fatal(err)
	}
	warm, err := Open(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = warm.Close()
	cache := t.TempDir()
	t.Setenv("BOOZLE_CACHE_DIR", cache)
	return cache, pdfPath
}

func snapshotDirs(t *testing.T, cache string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(cache, "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestOpenSnapshotIsolatesFromSourceAndCleansUp(t *testing.T) {
	cache, pdfPath := hermeticSnapshots(t)
	before, _ := os.Stat(pdfPath)

	doc, info, err := OpenSnapshot(pdfPath)
	if err != nil {
		t.Fatalf("OpenSnapshot: %v", err)
	}
	if doc.PageCount() != len(snapshotPages) {
		t.Fatalf("PageCount = %d, want %d", doc.PageCount(), len(snapshotPages))
	}
	if info.Size() != before.Size() || !info.ModTime().Equal(before.ModTime()) {
		t.Fatalf("info = %d/%v, want the source's %d/%v", info.Size(), info.ModTime(), before.Size(), before.ModTime())
	}

	// Overwrite the source in place with garbage: the open document keeps
	// rendering the version it was opened with.
	if err := os.WriteFile(pdfPath, []byte("not a pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range snapshotPages {
		img, release, err := doc.RenderPage(i, 160, 90)
		if err != nil {
			t.Fatalf("RenderPage(%d) after overwrite: %v", i, err)
		}
		c := img.RGBAAt(img.Bounds().Dx()/2, img.Bounds().Dy()/2)
		want := snapshotPages[i]
		if c.R != uint8(want.R*255) || c.G != uint8(want.G*255) || c.B != uint8(want.B*255) {
			t.Errorf("page %d centre = %v, want the original colour", i, c)
		}
		release()
	}

	// On Unix the copy is unlinked right after opening (the renders above
	// read it through the open descriptor); on Windows it stays until Close.
	want := 0
	if runtime.GOOS == "windows" {
		want = 1
	}
	if n := len(snapshotDirs(t, cache)); n != want {
		t.Fatalf("snapshot dirs while open = %d, want %d", n, want)
	}
	if err := doc.Close(); err != nil {
		t.Fatal(err)
	}
	if n := len(snapshotDirs(t, cache)); n != 0 {
		t.Fatalf("snapshot dirs after Close = %d, want 0", n)
	}
}

func TestOpenSnapshotDetectsChangeDuringCopy(t *testing.T) {
	cache, pdfPath := hermeticSnapshots(t)
	afterSnapshotCopy = func() {
		later := time.Now().Add(time.Minute)
		_ = os.Chtimes(pdfPath, later, later)
	}
	t.Cleanup(func() { afterSnapshotCopy = nil })

	if _, _, err := OpenSnapshot(pdfPath); !errors.Is(err, ErrChanged) {
		t.Fatalf("OpenSnapshot = %v, want ErrChanged", err)
	}
	if n := len(snapshotDirs(t, cache)); n != 0 {
		t.Fatalf("snapshot dirs after a torn copy = %d, want 0", n)
	}
}

func TestOpenSnapshotRejectsBrokenFile(t *testing.T) {
	cache, pdfPath := hermeticSnapshots(t)
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\ntruncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenSnapshot(pdfPath); err == nil {
		t.Fatal("OpenSnapshot of a truncated PDF should fail")
	}
	if n := len(snapshotDirs(t, cache)); n != 0 {
		t.Fatalf("snapshot dirs after a failed open = %d, want 0", n)
	}
}

func TestPruneSnapshots(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	for name, age := range map[string]time.Duration{"doc-old": 48 * time.Hour, "doc-new": time.Hour} {
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	pruneSnapshots(root, now, snapshotMaxAge)
	if _, err := os.Stat(filepath.Join(root, "doc-old")); !os.IsNotExist(err) {
		t.Fatal("old snapshot not pruned")
	}
	if _, err := os.Stat(filepath.Join(root, "doc-new")); err != nil {
		t.Fatal("recent snapshot pruned")
	}
}
