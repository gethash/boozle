package pdf_test

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/gethash/boozle/internal/pdf"
)

// TestOpenAndRender exercises the full open → page-info → render → close cycle
// against a real PDF. The PDF path is taken from BOOZLE_TEST_PDF; the test
// is skipped if that env var is unset, so CI without a fixture doesn't fail.
func TestOpenAndRender(t *testing.T) {
	path := os.Getenv("BOOZLE_TEST_PDF")
	if path == "" {
		t.Skip("BOOZLE_TEST_PDF not set")
	}

	doc, err := pdf.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer doc.Close()

	if got := doc.PageCount(); got < 1 {
		t.Fatalf("PageCount = %d, want >= 1", got)
	}

	page, err := doc.PageSize(0)
	if err != nil {
		t.Fatalf("PageSize(0): %v", err)
	}
	if page.WidthPoints <= 0 || page.HeightPoints <= 0 {
		t.Fatalf("PageSize(0) returned non-positive dims: %+v", page)
	}

	const targetW, targetH = 800, 600
	img, cleanup, err := doc.RenderPage(0, targetW, targetH)
	if err != nil {
		t.Fatalf("RenderPage(0): %v", err)
	}
	defer cleanup()

	// PDFium aspect-fits inside the requested box, so we don't get exactly
	// targetW × targetH unless the page is square; we just want a non-empty
	// image whose larger dimension was honoured.
	dx, dy := img.Bounds().Dx(), img.Bounds().Dy()
	if dx <= 0 || dy <= 0 {
		t.Fatalf("rendered image is empty: %dx%d", dx, dy)
	}
	if dx > targetW || dy > targetH {
		t.Errorf("rendered image %dx%d exceeds requested box %dx%d", dx, dy, targetW, targetH)
	}

	out := filepath.Join(t.TempDir(), "page0.png")
	f, err := os.Create(out)
	if err != nil {
		t.Fatalf("create %s: %v", out, err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatalf("png.Encode: %v", err)
	}
	f.Close()

	st, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat %s: %v", out, err)
	}
	if st.Size() == 0 {
		t.Fatalf("png is empty")
	}
	t.Logf("rendered %dx%d page 0 → %s (%d bytes)", targetW, targetH, out, st.Size())
}
