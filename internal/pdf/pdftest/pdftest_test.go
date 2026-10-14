package pdftest

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "regenerate examples/sample.pdf")

// samplePages is the deck shipped as examples/sample.pdf (used for local dev
// runs and referenced from the README).
var samplePages = []Page{
	{W: 960, H: 540, R: 0.11, G: 0.13, B: 0.20, Lines: []string{"boozle sample deck", "Right / Space: next   Left: previous"}},
	{W: 960, H: 540, R: 0.10, G: 0.35, B: 0.55, Lines: []string{"Tab: slide overview", "Digits + Enter: jump to slide"}},
	{W: 960, H: 540, R: 0.15, G: 0.45, B: 0.30, Lines: []string{"b / w: black / white screen", "p: pause auto-advance"}},
	{W: 960, H: 540, R: 0.55, G: 0.30, B: 0.10, Lines: []string{"f: toggle fullscreen", "q / Esc: quit"}},
	{W: 540, H: 960, R: 0.45, G: 0.15, B: 0.40, Lines: []string{"Portrait page", "(aspect-fit)"}},
	{W: 960, H: 540, R: 0.20, G: 0.20, B: 0.20, Lines: []string{"The end"}},
}

func TestSamplePDFUpToDate(t *testing.T) {
	path := filepath.Join("..", "..", "..", "examples", "sample.pdf")
	want := Bytes(samplePages)
	if *update {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./internal/pdf/pdftest -update)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; run: go test ./internal/pdf/pdftest -update", path)
	}
}
