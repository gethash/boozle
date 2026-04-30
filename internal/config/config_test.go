package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestParsePageRange(t *testing.T) {
	tests := []struct {
		in       string
		wantAll  bool
		wantList []int
		wantErr  bool
	}{
		{"", true, nil, false},
		{"3", false, []int{3}, false},
		{"1,3,5", false, []int{1, 3, 5}, false},
		{"3-7", false, []int{3, 4, 5, 6, 7}, false},
		{"1,3-5,8", false, []int{1, 3, 4, 5, 8}, false},
		{"5-3", false, nil, true},
		{"abc", false, nil, true},
		{"0", false, nil, true},
		{"1,1,2", false, []int{1, 2}, false}, // dedup
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParsePageRange(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.All != tt.wantAll {
				t.Errorf("All=%v want %v", got.All, tt.wantAll)
			}
			if !tt.wantAll && !reflect.DeepEqual(got.Pages, tt.wantList) {
				t.Errorf("Pages=%v want %v", got.Pages, tt.wantList)
			}
		})
	}
}

func TestPageRangeFilterAndContains(t *testing.T) {
	pr, _ := ParsePageRange("2,4-5")
	if !pr.Contains(4) || pr.Contains(3) {
		t.Errorf("Contains: got 4=%v 3=%v", pr.Contains(4), pr.Contains(3))
	}
	if got := pr.Filter(10); !reflect.DeepEqual(got, []int{2, 4, 5}) {
		t.Errorf("Filter(10) = %v", got)
	}
	if got := pr.Filter(3); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("Filter(3) = %v (out-of-range pages should be dropped)", got)
	}

	all := PageRange{All: true}
	if got := all.Filter(3); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Errorf("All.Filter(3) = %v", got)
	}
}

func TestLoadSidecarMergesAndFlagsWin(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "deck.pdf")
	if err := os.WriteFile(pdf, []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(dir, "deck.boozle.toml")
	if err := os.WriteFile(sidecar, []byte(`
auto    = "20s"
loop    = true
start   = 4
monitor = 1
pages   = "2-5"
bg      = "#112233"

[[page]]
n    = 3
auto = "1s"

[[page]]
n    = 5
auto = "2m"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Flags carry cobra defaults (StartPage=1, MonitorIdx=0); sidecar fills
	// the rest because no explicit flags override.
	c, err := Load(Flags{PDFPath: pdf, StartPage: 1, Background: "#000000"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Auto != 20*time.Second {
		t.Errorf("Auto = %v, want 20s", c.Auto)
	}
	if !c.Loop {
		t.Error("Loop should be true from sidecar")
	}
	if c.StartPage != 4 {
		t.Errorf("StartPage = %d, want 4", c.StartPage)
	}
	if c.MonitorIdx != 1 {
		t.Errorf("MonitorIdx = %d, want 1", c.MonitorIdx)
	}
	if !reflect.DeepEqual(c.PageRange.Pages, []int{2, 3, 4, 5}) {
		t.Errorf("PageRange = %+v, want 2-5", c.PageRange)
	}
	if c.Background != (Color{0x11, 0x22, 0x33, 0xff}) {
		t.Errorf("Background = %+v, want #112233", c.Background)
	}
	if c.PerPage[3] != 1*time.Second || c.PerPage[5] != 2*time.Minute {
		t.Errorf("PerPage = %+v, want {3:1s, 5:2m}", c.PerPage)
	}

	// Now: explicit flag for --auto wins over sidecar.
	c2, err := Load(Flags{PDFPath: pdf, StartPage: 1, Auto: 5 * time.Second})
	if err != nil {
		t.Fatalf("Load with --auto: %v", err)
	}
	if c2.Auto != 5*time.Second {
		t.Errorf("flag --auto should win: got %v want 5s", c2.Auto)
	}
}

func TestLoadNoSidecarStillWorks(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "no-sidecar.pdf")
	if err := os.WriteFile(pdf, []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(Flags{PDFPath: pdf, StartPage: 1})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.PageRange.All {
		t.Error("PageRange should default to All when no sidecar and no --pages")
	}
	if c.Background.A != 255 {
		t.Error("Background should default to opaque black")
	}
}

func TestLoadSidecarProgressAndAutoQuit(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "deck.pdf")
	if err := os.WriteFile(pdfPath, []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecarPath := filepath.Join(dir, "deck.boozle.toml")
	if err := os.WriteFile(sidecarPath, []byte("progress = true\nautoquit = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := Load(Flags{PDFPath: pdfPath, StartPage: 1})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Progress {
		t.Error("Progress should be true from sidecar")
	}
	if !c.AutoQuit {
		t.Error("AutoQuit should be true from sidecar")
	}

	// Explicit flag wins: setting Progress/AutoQuit true via flag keeps them true
	// even when the sidecar would also set them (the flag path skips the sidecar check).
	c2, err := Load(Flags{PDFPath: pdfPath, StartPage: 1, Progress: true, AutoQuit: true})
	if err != nil {
		t.Fatalf("Load with flags: %v", err)
	}
	if !c2.Progress || !c2.AutoQuit {
		t.Error("explicit flag should keep Progress/AutoQuit true")
	}
}

func TestParseColor(t *testing.T) {
	tests := []struct {
		in   string
		want Color
		err  bool
	}{
		{"#000000", Color{0, 0, 0, 255}, false},
		{"#FFFFFF", Color{255, 255, 255, 255}, false},
		{"#ff8800", Color{0xff, 0x88, 0x00, 0xff}, false},
		{"#abc", Color{0xaa, 0xbb, 0xcc, 0xff}, false},
		{"#11223344", Color{0x11, 0x22, 0x33, 0x44}, false},
		{"", Color{0, 0, 0, 255}, false},
		{"abc", Color{}, true},
		{"#xyz", Color{}, true},
		{"#1234", Color{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseColor(tt.in)
			if (err != nil) != tt.err {
				t.Fatalf("err=%v want=%v", err, tt.err)
			}
			if err == nil && got != tt.want {
				t.Errorf("got %+v want %+v", got, tt.want)
			}
		})
	}
}
