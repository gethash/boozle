package pdf_test

import (
	"hash/crc32"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gethash/boozle/internal/pdf"
	"github.com/gethash/boozle/internal/pdf/pdftest"
)

// fixturePages alternates landscape and portrait pages in distinct colours
// so tests can tell pages apart by their centre pixel.
var fixturePages = []pdftest.Page{
	{W: 800, H: 450, R: 1, G: 0, B: 0},
	{W: 450, H: 800, R: 0, G: 1, B: 0},
	{W: 800, H: 450, R: 0, G: 0, B: 1},
	{W: 600, H: 600, R: 1, G: 1, B: 0},
}

func openFixture(t *testing.T, name string) *pdf.Doc {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := pdftest.Write(path, fixturePages); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	doc, err := pdf.Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = doc.Close() })
	return doc
}

func TestRenderFixtureColourAndSize(t *testing.T) {
	doc := openFixture(t, "deck.pdf")
	if got := doc.PageCount(); got != len(fixturePages) {
		t.Fatalf("PageCount = %d, want %d", got, len(fixturePages))
	}
	for i, fp := range fixturePages {
		page, err := doc.PageSize(i)
		if err != nil {
			t.Fatalf("PageSize(%d): %v", i, err)
		}
		if page.WidthPoints != fp.W || page.HeightPoints != fp.H {
			t.Errorf("PageSize(%d) = %vx%v, want %vx%v", i, page.WidthPoints, page.HeightPoints, fp.W, fp.H)
		}

		const boxW, boxH = 320, 320
		img, release, err := doc.RenderPage(i, boxW, boxH)
		if err != nil {
			t.Fatalf("RenderPage(%d): %v", i, err)
		}
		b := img.Bounds()
		if b.Dx() <= 0 || b.Dy() <= 0 || b.Dx() > boxW || b.Dy() > boxH {
			t.Errorf("page %d rendered %dx%d, want non-empty within %dx%d", i, b.Dx(), b.Dy(), boxW, boxH)
		}
		c := img.RGBAAt(b.Dx()/2, b.Dy()/2)
		want := [3]uint8{uint8(fp.R * 255), uint8(fp.G * 255), uint8(fp.B * 255)}
		if c.R != want[0] || c.G != want[1] || c.B != want[2] {
			t.Errorf("page %d centre pixel = %v, want rgb%v", i, c, want)
		}
		release()
	}
}

func TestOpenPathWithSpacesAndUnicode(t *testing.T) {
	doc := openFixture(t, "Präsentation mit Leerzeichen ✓.pdf")
	if doc.PageCount() != len(fixturePages) {
		t.Fatalf("PageCount = %d, want %d", doc.PageCount(), len(fixturePages))
	}
}

// TestConcurrentRenderMatchesSerial hammers one Doc from several goroutines
// (as the prefetcher, thumbnail loader and main thread do) and checks every
// result is byte-identical to a serial render. Run with -race.
func TestConcurrentRenderMatchesSerial(t *testing.T) {
	doc := openFixture(t, "deck.pdf")
	sizes := [][2]int{{64, 64}, {200, 120}, {333, 500}}

	want := map[[3]int]uint32{}
	for p := range fixturePages {
		for _, sz := range sizes {
			img, release, err := doc.RenderPage(p, sz[0], sz[1])
			if err != nil {
				t.Fatalf("serial RenderPage: %v", err)
			}
			want[[3]int{p, sz[0], sz[1]}] = crc32.ChecksumIEEE(img.Pix)
			release()
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(seed, seed))
			for n := 0; n < 40; n++ {
				p := r.IntN(len(fixturePages))
				sz := sizes[r.IntN(len(sizes))]
				img, release, err := doc.RenderPage(p, sz[0], sz[1])
				if err != nil {
					errs <- err
					return
				}
				if got := crc32.ChecksumIEEE(img.Pix); got != want[[3]int{p, sz[0], sz[1]}] {
					t.Errorf("page %d @%v: concurrent render differs from serial", p, sz)
				}
				release()
			}
		}(uint64(g + 1))
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 200; n++ {
			if _, err := doc.PageSize(n % len(fixturePages)); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

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
	img, release, err := doc.RenderPage(0, targetW, targetH)
	if err != nil {
		t.Fatalf("RenderPage(0): %v", err)
	}
	defer release()

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
