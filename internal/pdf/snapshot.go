package pdf

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gethash/boozle/internal/cachedir"
)

// ErrChanged reports that the PDF was modified while it was being copied,
// so the copy may be torn. Try again once the file has settled.
var ErrChanged = errors.New("pdf changed while it was being read")

// snapshotMaxAge is how old a leftover snapshot (from a crashed run) must be
// before a later run deletes it. Deleting the snapshot of a deck that is
// still open is harmless: Unix keeps the open file alive and Windows refuses
// to delete it.
const snapshotMaxAge = 24 * time.Hour

var pruneSnapshotsOnce sync.Once

// afterSnapshotCopy runs between copying the PDF and re-checking it (tests
// use it to modify the source mid-copy).
var afterSnapshotCopy func()

// OpenSnapshot opens a private copy of the PDF at path and returns the
// source's file info as of the copy.
//
// PDFium reads documents lazily, so a PDF overwritten in place (pdflatex
// does that) would feed the open document bytes from the new version. The
// copy keeps the open document consistent, and on Windows it stops boozle
// from holding the original open, so authoring tools can replace it. The
// copy lives in <user cache>/boozle/snapshots and is deleted by Close.
//
// If the file changes while it is copied, OpenSnapshot returns ErrChanged.
func OpenSnapshot(path string) (*Doc, os.FileInfo, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	root, err := cachedir.Dir("snapshots")
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot dir: %w", err)
	}
	pruneSnapshotsOnce.Do(func() { go pruneSnapshots(root, time.Now(), snapshotMaxAge) })

	// One directory per snapshot: Open mounts the PDF's directory into the
	// WASM sandbox, which should see nothing but this document.
	dir, err := os.MkdirTemp(root, "doc-")
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	copyPath := filepath.Join(dir, "deck.pdf")
	if err := copyFile(path, copyPath); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("snapshot %s: %w", path, err)
	}
	if afterSnapshotCopy != nil {
		afterSnapshotCopy()
	}
	after, err := os.Stat(path)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		cleanup()
		return nil, nil, ErrChanged
	}

	doc, err := Open(copyPath)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if doc.PageCount() == 0 {
		_ = doc.Close()
		cleanup()
		return nil, nil, fmt.Errorf("%s has no pages", path)
	}
	// Unix keeps an unlinked file readable through the descriptor PDFium
	// holds, so delete the copy now: nothing is left behind however the
	// process ends (the presenter process exits via os.Exit, skipping
	// Close). Windows refuses while the file is open; Close deletes it there.
	if os.RemoveAll(dir) != nil {
		doc.cleanup = cleanup
	}
	return doc, before, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// pruneSnapshots deletes snapshot directories older than maxAge.
func pruneSnapshots(root string, now time.Time, maxAge time.Duration) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}
