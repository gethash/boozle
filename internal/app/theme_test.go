package app

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/hajimehoshi/ebiten/v2/text/v2"
)

// TestThemeTokensMatchDesignSystem pins the colours to the Boozle design
// system's tokens.json (dark theme), so drift is a deliberate change.
func TestThemeTokensMatchDesignSystem(t *testing.T) {
	hex := func(c color.RGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }
	for name, tt := range map[string]struct {
		got  color.RGBA
		want string
	}{
		"ground":          {tokGround, "#0b0b12"},
		"surface":         {tokSurface, "#12121b"},
		"surface-raised":  {tokSurfaceRaised, "#1a1a26"},
		"line":            {tokLine, "#262636"},
		"line-control":    {tokLineControl, "#66668a"},
		"ink":             {tokInk, "#f4f4fa"},
		"ink-muted":       {tokInkMuted, "#a9a9c0"},
		"ink-faint":       {tokInkFaint, "#85859f"},
		"track":           {tokTrack, "#22222f"},
		"focus":           {tokFocus, "#00d4ff"},
		"warning":         {tokWarning, "#ffc94d"},
		"spectrum-rose":   {spectrumStops[0], "#ff4d6d"},
		"spectrum-amber":  {spectrumStops[1], "#ff9f1c"},
		"spectrum-yellow": {spectrumStops[2], "#f9e900"},
		"spectrum-mint":   {spectrumStops[3], "#2ee59d"},
		"spectrum-cyan":   {spectrumStops[4], "#00d4ff"},
		"spectrum-blue":   {spectrumStops[5], "#5b7cfa"},
		"spectrum-violet": {spectrumStops[6], "#b967ff"},
		"spectrum-pink":   {spectrumStops[7], "#ff61d8"},
	} {
		if got := hex(tt.got); got != tt.want || tt.got.A != 0xff {
			t.Errorf("%s = %s (alpha %d), want %s", name, got, tt.got.A, tt.want)
		}
	}
}

func TestSpectrum(t *testing.T) {
	if spectrumAt(0) != spectrumStops[0] || spectrumAt(1) != spectrumStops[7] {
		t.Fatalf("spectrum ends = %v, %v", spectrumAt(0), spectrumAt(1))
	}
	if spectrumAt(3.0/7) != spectrumStops[3] {
		t.Fatalf("spectrumAt(3/7) = %v, want mint %v", spectrumAt(3.0/7), spectrumStops[3])
	}
	if spectrumConicAt(0) != spectrumStops[0] || spectrumConicAt(1) != spectrumStops[0] {
		t.Fatal("conic spectrum should start and end on rose")
	}
	if got := withAlpha(color.RGBA{255, 255, 255, 255}, 0.16); got != (color.RGBA{41, 41, 41, 41}) {
		t.Fatalf("withAlpha(white, 0.16) = %v, want premultiplied {41 41 41 41}", got)
	}
}

func TestCubicBezier(t *testing.T) {
	for _, c := range []cubicBezier{curveEaseOut, curveEaseInOut} {
		if c.at(0) != 0 || c.at(1) != 1 {
			t.Fatalf("%v: ends = %v, %v", c, c.at(0), c.at(1))
		}
		prev := 0.0
		for i := 1; i <= 100; i++ {
			v := c.at(float64(i) / 100)
			if v < prev-1e-9 {
				t.Fatalf("%v not monotonic at %d: %v < %v", c, i, v, prev)
			}
			prev = v
		}
	}
	// ease-in-out starts and ends slowly.
	if v := curveEaseInOut.at(0.1); v > 0.05 {
		t.Fatalf("ease-in-out(0.1) = %v, want a slow start", v)
	}
	if v := curveEaseInOut.at(0.9); v < 0.95 {
		t.Fatalf("ease-in-out(0.9) = %v, want a slow end", v)
	}
	// ease-out front-loads the motion.
	if v := curveEaseOut.at(0.25); v < 0.6 {
		t.Fatalf("ease-out(0.25) = %v, want a fast start", v)
	}
}

func TestFormatCounterAndCountdown(t *testing.T) {
	for _, tt := range []struct {
		idx, total int
		want       string
	}{{3, 12, "03 / 12"}, {1, 5, "01 / 05"}, {7, 120, "007 / 120"}} {
		if got := formatCounter(tt.idx, tt.total); got != tt.want {
			t.Errorf("formatCounter(%d, %d) = %q, want %q", tt.idx, tt.total, got, tt.want)
		}
	}
	for _, tt := range []struct {
		d    time.Duration
		want string
	}{
		{14 * time.Second, "00:14"},
		{13*time.Second + time.Millisecond, "00:14"}, // rounds up
		{0, "00:00"},
		{-time.Second, "00:00"},
		{2*time.Minute + 5*time.Second, "02:05"},
		{time.Hour + 2*time.Minute + 3*time.Second, "1:02:03"},
	} {
		if got := formatCountdown(tt.d); got != tt.want {
			t.Errorf("formatCountdown(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestSegmentGeometry(t *testing.T) {
	segW, gap := segmentGeometry(1000, 10, 4)
	if gap != 4 || math.Abs(float64(segW*10+gap*9-1000)) > 1e-3 {
		t.Fatalf("10 segments: segW %v gap %v don't span 1000 px", segW, gap)
	}
	// Too many slides for gaps: segments touch and still span the bar.
	segW, gap = segmentGeometry(300, 200, 4)
	if gap != 0 || math.Abs(float64(segW*200-300)) > 1e-3 {
		t.Fatalf("200 segments: segW %v gap %v, want gapless 1.5 px", segW, gap)
	}
}

func TestHUDLayoutStaysOnScreen(t *testing.T) {
	for _, sz := range [][2]int{{1280, 720}, {1920, 1080}, {3840, 2160}, {1024, 768}, {800, 1280}} {
		W, H := sz[0], sz[1]
		for _, countdown := range []string{"", "00:14", "1:02:03"} {
			lo := layoutHUD(W, H, "007 / 120", countdown)
			name := fmt.Sprintf("%dx%d %q", W, H, countdown)
			screen := presenterRect{w: W, h: H}
			if !rectInside(lo.bar, screen) || lo.bar.w < W/2 {
				t.Errorf("%s: bar %+v should sit on screen and take most of the row", name, lo.bar)
			}
			if lo.bar.h < 3 {
				t.Errorf("%s: bar height %d, want >= 3", name, lo.bar.h)
			}
			if countdown != "" {
				c := lo.chip
				if c.x < float64(lo.bar.x+lo.bar.w) || c.x+c.w > float64(W) || c.y+c.h > float64(H) {
					t.Errorf("%s: chip %+v overlaps the bar or leaves the screen", name, c)
				}
			}
			if lo.counterX <= float64(lo.bar.x+lo.bar.w) {
				t.Errorf("%s: counter (right edge %v) overlaps the bar %+v", name, lo.counterX, lo.bar)
			}
		}
	}
}

func TestEmbeddedFontsLoad(t *testing.T) {
	for _, fam := range []fontFamily{fontSans, fontMono} {
		f := uiFace(fam, 20, 500)
		if w := text.Advance("Boozle 03 / 12", f); w <= 0 {
			t.Fatalf("family %d: advance %v, want > 0", fam, w)
		}
	}
	// Geist Mono is monospaced, so timers and counters don't jitter.
	f := uiFace(fontMono, 20, 500)
	if a, b := text.Advance("11:11", f), text.Advance("00:00", f); a != b {
		t.Fatalf("mono advances differ: %v vs %v", a, b)
	}
	// Notes are no longer limited to Latin-1.
	if !strings.ContainsRune("Größe — “quoted” …", '…') || text.Advance("— “” …", uiFace(fontSans, 20, 400)) <= 0 {
		t.Fatal("typographic characters should shape")
	}
	if uiFace(fontSans, 20, 400) != uiFace(fontSans, 20.01, 400) {
		t.Fatal("faces should be cached by quarter-pixel size")
	}
}

func TestFitTextSize(t *testing.T) {
	size := fitTextSize("15:04:05", styleTimer, 300, 200, 1000)
	f := styleTimer.faceAt(size)
	if w := textWidth("15:04:05", f, styleTimer.trackingPx(f)); w > 300 || w < 250 {
		t.Fatalf("fit to 300 px wide: size %v gives %v px", size, w)
	}
	if got := fitTextSize("15:04:05", styleTimer, 3000, 2000, 40); got != 40 {
		t.Fatalf("maxPx cap: got %v, want 40", got)
	}
}
