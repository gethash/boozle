// Package config merges command-line flags with an optional TOML sidecar
// (`<file>.boozle.toml`) into a single Config struct used by the app.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Flags is the raw input from cobra; zero-values mean "unset".
type Flags struct {
	PDFPath      string
	Auto         time.Duration
	Loop         bool
	StartPage    int
	MonitorIdx   int
	Pages        string
	Background   string
	NoFullscreen bool
	ConfigPath   string
	Progress     bool
	AutoQuit     bool
}

// Config is the resolved configuration the app actually runs with.
type Config struct {
	PDFPath      string
	Auto         time.Duration
	Loop         bool
	StartPage    int
	MonitorIdx   int
	PageRange    PageRange
	Background   Color
	NoFullscreen bool

	// PerPage maps 1-indexed page numbers to per-page auto-advance overrides.
	PerPage  map[int]time.Duration
	Progress bool // show page-position and auto-advance progress overlay
	AutoQuit bool // quit after the last page instead of stopping
}

// Color is an RGBA color parsed from a hex string.
type Color struct{ R, G, B, A uint8 }

// sidecar mirrors the TOML schema. All fields are optional; flags win on conflict.
type sidecar struct {
	Auto       string         `toml:"auto"`
	Loop       *bool          `toml:"loop"`
	StartPage  *int           `toml:"start"`
	MonitorIdx *int           `toml:"monitor"`
	Pages      string         `toml:"pages"`
	Background string         `toml:"bg"`
	Progress   *bool          `toml:"progress"`
	AutoQuit   *bool          `toml:"autoquit"`
	PerPage    []perPageEntry `toml:"page"`
}

type perPageEntry struct {
	N    int    `toml:"n"`
	Auto string `toml:"auto"`
}

// Load resolves the final Config from flags + optional sidecar TOML.
// Precedence: explicit flags > sidecar > defaults.
func Load(f Flags) (Config, error) {
	c := Config{
		PDFPath:      f.PDFPath,
		Auto:         f.Auto,
		Loop:         f.Loop,
		StartPage:    f.StartPage,
		MonitorIdx:   f.MonitorIdx,
		NoFullscreen: f.NoFullscreen,
		Progress:     f.Progress,
		AutoQuit:     f.AutoQuit,
		PerPage:      map[int]time.Duration{},
	}

	side, sidePath, err := loadSidecar(f.PDFPath, f.ConfigPath)
	if err != nil {
		return Config{}, err
	}

	pagesSpec := f.Pages
	bgSpec := f.Background

	if side != nil {
		if c.Auto == 0 && side.Auto != "" {
			d, err := time.ParseDuration(side.Auto)
			if err != nil {
				return Config{}, fmt.Errorf("%s: invalid auto duration %q: %w", sidePath, side.Auto, err)
			}
			c.Auto = d
		}
		if !f.Loop && side.Loop != nil {
			c.Loop = *side.Loop
		}
		if f.StartPage == 1 && side.StartPage != nil {
			c.StartPage = *side.StartPage
		}
		if f.MonitorIdx == 0 && side.MonitorIdx != nil {
			c.MonitorIdx = *side.MonitorIdx
		}
		if pagesSpec == "" {
			pagesSpec = side.Pages
		}
		if bgSpec == "" || bgSpec == "#000000" {
			if side.Background != "" {
				bgSpec = side.Background
			}
		}
		if !f.Progress && side.Progress != nil {
			c.Progress = *side.Progress
		}
		if !f.AutoQuit && side.AutoQuit != nil {
			c.AutoQuit = *side.AutoQuit
		}
		for _, e := range side.PerPage {
			if e.Auto == "" {
				continue
			}
			d, err := time.ParseDuration(e.Auto)
			if err != nil {
				return Config{}, fmt.Errorf("%s: page %d: invalid auto %q: %w", sidePath, e.N, e.Auto, err)
			}
			c.PerPage[e.N] = d
		}
	}

	pr, err := ParsePageRange(pagesSpec)
	if err != nil {
		return Config{}, fmt.Errorf("--pages: %w", err)
	}
	c.PageRange = pr

	col, err := ParseColor(bgSpec)
	if err != nil {
		return Config{}, fmt.Errorf("--bg: %w", err)
	}
	c.Background = col

	if c.StartPage < 1 {
		return Config{}, fmt.Errorf("--start must be >= 1, got %d", c.StartPage)
	}

	return c, nil
}

func loadSidecar(pdfPath, override string) (*sidecar, string, error) {
	path := override
	if path == "" {
		ext := filepath.Ext(pdfPath)
		base := strings.TrimSuffix(pdfPath, ext)
		path = base + ".boozle.toml"
		if _, err := os.Stat(path); err != nil {
			return nil, "", nil
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("read sidecar %s: %w", path, err)
	}
	var s sidecar
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, path, fmt.Errorf("parse sidecar %s: %w", path, err)
	}
	return &s, path, nil
}

// ParseColor parses #RGB, #RRGGBB, or #RRGGBBAA hex strings.
func ParseColor(s string) (Color, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Color{0, 0, 0, 255}, nil
	}
	if !strings.HasPrefix(s, "#") {
		return Color{}, fmt.Errorf("color must start with #: %q", s)
	}
	hex := s[1:]
	expand := func(b byte) (uint8, error) {
		v, err := strconv.ParseUint(string([]byte{b, b}), 16, 8)
		return uint8(v), err
	}
	parse2 := func(s string) (uint8, error) {
		v, err := strconv.ParseUint(s, 16, 8)
		return uint8(v), err
	}
	switch len(hex) {
	case 3:
		r, err := expand(hex[0])
		if err != nil {
			return Color{}, err
		}
		g, err := expand(hex[1])
		if err != nil {
			return Color{}, err
		}
		b, err := expand(hex[2])
		if err != nil {
			return Color{}, err
		}
		return Color{r, g, b, 255}, nil
	case 6, 8:
		r, err := parse2(hex[0:2])
		if err != nil {
			return Color{}, err
		}
		g, err := parse2(hex[2:4])
		if err != nil {
			return Color{}, err
		}
		b, err := parse2(hex[4:6])
		if err != nil {
			return Color{}, err
		}
		a := uint8(255)
		if len(hex) == 8 {
			a, err = parse2(hex[6:8])
			if err != nil {
				return Color{}, err
			}
		}
		return Color{r, g, b, a}, nil
	default:
		return Color{}, fmt.Errorf("color must be #RGB, #RRGGBB, or #RRGGBBAA: %q", s)
	}
}

// PageRange is a sorted, deduplicated list of 1-indexed page numbers.
// A nil/empty range means "all pages".
type PageRange struct {
	All   bool
	Pages []int // sorted ascending, deduplicated
}

// Contains reports whether page n (1-indexed) is in the range.
func (p PageRange) Contains(n int) bool {
	if p.All {
		return true
	}
	for _, x := range p.Pages {
		if x == n {
			return true
		}
	}
	return false
}

// Filter returns the subset of [1..total] that lies within the range.
func (p PageRange) Filter(total int) []int {
	if p.All {
		out := make([]int, total)
		for i := range out {
			out[i] = i + 1
		}
		return out
	}
	out := make([]int, 0, len(p.Pages))
	for _, n := range p.Pages {
		if n >= 1 && n <= total {
			out = append(out, n)
		}
	}
	return out
}

// ParsePageRange parses "1,3-5,8" syntax. Empty input means "all".
func ParsePageRange(spec string) (PageRange, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return PageRange{All: true}, nil
	}
	seen := map[int]struct{}{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			ab := strings.SplitN(part, "-", 2)
			a, err := strconv.Atoi(strings.TrimSpace(ab[0]))
			if err != nil || a < 1 {
				return PageRange{}, fmt.Errorf("invalid range %q", part)
			}
			b, err := strconv.Atoi(strings.TrimSpace(ab[1]))
			if err != nil || b < a {
				return PageRange{}, fmt.Errorf("invalid range %q", part)
			}
			for i := a; i <= b; i++ {
				seen[i] = struct{}{}
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 {
			return PageRange{}, fmt.Errorf("invalid page %q", part)
		}
		seen[n] = struct{}{}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	// insertion sort — sets are tiny
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return PageRange{Pages: out}, nil
}
