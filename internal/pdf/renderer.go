// Package pdf wraps go-pdfium's WebAssembly backend so the rest of boozle
// can stay agnostic of the underlying PDF library.
//
// The PDFium WASM blob is embedded by go-pdfium itself (no need for us to
// embed it again), and wazero is pure Go — so this package compiles with
// CGO_ENABLED=0 and contributes nothing to the binary's dynamic dependencies.
//
// Pixels returned by RenderPage are Go-owned copies: the PDFium bitmap is
// destroyed before RenderPage returns, so callers never hold views into the
// WASM linear memory (which is not goroutine-safe and is invalidated when
// the module's memory grows).
package pdf

import (
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
)

// docMount is where the PDF's directory is mounted inside the WASM sandbox.
// go-pdfium cannot open Windows volume paths (C:\...), so we always mount
// the containing directory read-only and open the file by a POSIX path.
const docMount = "/doc"

// Doc is an opened PDF, ready to render. A Doc owns a single PDFium worker
// instance; calls are serialized via mu because PDFium instances are not
// goroutine-safe.
type Doc struct {
	mu       sync.Mutex
	pool     pdfium.Pool
	instance pdfium.Pdfium
	docRef   references.FPDF_DOCUMENT
	pages    int
	closed   bool

	pageSizes []Page
	sizeKnown []bool
}

// Page is the natural size of a PDF page, in PDF points (72 dpi).
type Page struct {
	Index        int     // 0-indexed
	WidthPoints  float64 // page width in points
	HeightPoints float64 // page height in points
}

// AspectRatio returns width/height; safe even for zero-height pages.
func (p Page) AspectRatio() float64 {
	if p.HeightPoints <= 0 {
		return 1
	}
	return p.WidthPoints / p.HeightPoints
}

// Open initializes a PDFium WASM worker pool, loads the file at path,
// and prepares it for rendering. The caller must call Close.
func Open(path string) (*Doc, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}

	pool, err := webassembly.Init(webassembly.Config{
		MinIdle:  1,
		MaxIdle:  1,
		MaxTotal: 1,
		FSConfig: wazero.NewFSConfig().WithReadOnlyDirMount(filepath.Dir(absPath), docMount),
	})
	if err != nil {
		return nil, fmt.Errorf("pdfium init: %w", err)
	}

	instance, err := pool.GetInstance(30 * time.Second)
	if err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("pdfium instance: %w", err)
	}

	wasmPath := docMount + "/" + filepath.Base(absPath)
	open, err := instance.OpenDocument(&requests.OpenDocument{FilePath: &wasmPath})
	if err != nil {
		_ = instance.Close()
		_ = pool.Close()
		return nil, fmt.Errorf("open document: %w", err)
	}

	cnt, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: open.Document})
	if err != nil {
		_, _ = instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: open.Document})
		_ = instance.Close()
		_ = pool.Close()
		return nil, fmt.Errorf("get page count: %w", err)
	}

	return &Doc{
		pool:      pool,
		instance:  instance,
		docRef:    open.Document,
		pages:     cnt.PageCount,
		pageSizes: make([]Page, cnt.PageCount),
		sizeKnown: make([]bool, cnt.PageCount),
	}, nil
}

// PageCount returns the total number of pages.
func (d *Doc) PageCount() int { return d.pages }

// PageSize returns the natural width/height (in PDF points) of page idx (0-indexed).
func (d *Doc) PageSize(idx int) (Page, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return Page{}, errors.New("doc is closed")
	}
	if idx < 0 || idx >= d.pages {
		return Page{}, fmt.Errorf("page %d out of range", idx)
	}
	if idx < len(d.sizeKnown) && d.sizeKnown[idx] {
		return d.pageSizes[idx], nil
	}
	resp, err := d.instance.FPDF_GetPageSizeByIndex(&requests.FPDF_GetPageSizeByIndex{
		Document: d.docRef,
		Index:    idx,
	})
	if err != nil {
		return Page{}, fmt.Errorf("page %d size: %w", idx, err)
	}
	page := Page{
		Index:        idx,
		WidthPoints:  resp.Width,
		HeightPoints: resp.Height,
	}
	if idx < len(d.pageSizes) {
		d.pageSizes[idx] = page
		d.sizeKnown[idx] = true
	}
	return page, nil
}

// RenderPage rasterizes page idx (0-indexed) to fit inside width × height
// pixels (PDFium aspect-fits, so the result may be smaller on one axis).
//
// The returned image owns its pixels; the PDFium bitmap is already freed.
// The release function hands the pixel buffer back to a pool for reuse; it
// is optional, safe to call from any goroutine, and the image must not be
// used afterwards.
func (d *Doc) RenderPage(idx, width, height int) (*image.RGBA, func(), error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, nil, errors.New("doc is closed")
	}
	if width <= 0 || height <= 0 {
		return nil, nil, fmt.Errorf("invalid render size %dx%d", width, height)
	}
	resp, err := d.instance.RenderPageInPixels(&requests.RenderPageInPixels{
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{
				Document: d.docRef,
				Index:    idx,
			},
		},
		Width:  width,
		Height: height,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("render page %d: %w", idx, err)
	}
	// Copy out of WASM memory and free the bitmap while still holding the
	// lock: dlmalloc inside the module is not goroutine-safe, and the view
	// returned by go-pdfium detaches once the module's memory grows.
	view := resp.Result.Image
	buf := getPix(len(view.Pix))
	copy(buf, view.Pix)
	resp.Cleanup()

	img := &image.RGBA{Pix: buf, Stride: view.Stride, Rect: view.Rect}
	release := func() { putPix(buf) }
	return img, release, nil
}

// Close releases the document, the worker instance, and the pool.
// It is safe to call Close more than once.
func (d *Doc) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	var firstErr error
	if _, err := d.instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: d.docRef}); err != nil {
		firstErr = err
	}
	if err := d.instance.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := d.pool.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
