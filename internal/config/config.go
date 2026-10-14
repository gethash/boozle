// Package config merges command-line flags with an optional sidecar
// (`<file>.boozle.toml` or `<file>.pdfpc`) into a single Config struct used by the app.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Flags is the raw input from cobra. Values hold cobra's defaults for
// flags the user didn't pass; Changed records which flags were passed
// explicitly (by long name), and only those override sidecar values.
type Flags struct {
	PDFPath          string
	Changed          map[string]bool
	Auto             time.Duration
	Loop             bool
	StartPage        int
	Monitor          string // index or name; "" = primary
	Pages            string
	Background       string
	NoFullscreen     bool
	ConfigPath       string
	Progress         bool
	AutoQuit         bool
	Transition       string
	PresenterMonitor string  // index or name; "" or "-1" = disabled
	CacheMB          int     // 0 = auto-size to display
	RenderScale      float64 // 0 = native; 0.5..1.0 reduces per-page pixel count
	DebugStats       bool    // print memory/render diagnostics to stderr
}

// Config is the resolved configuration the app actually runs with.
type Config struct {
	PDFPath      string
	Auto         time.Duration
	Loop         bool
	StartPage    int
	Monitor      string // monitor spec (index or name) for the audience window
	PageRange    PageRange
	Background   Color
	NoFullscreen bool

	// PlaybackPages is an optional 1-indexed playback order imported from a
	// sidecar format that can encode ordering/hidden pages directly.
	PlaybackPages    []int
	UsePlaybackPages bool

	// PerPage maps 1-indexed page numbers to per-page auto-advance overrides.
	PerPage          map[int]time.Duration
	Notes            map[int]string
	Progress         bool    // show page-position and auto-advance progress overlay
	AutoQuit         bool    // quit after the last page instead of stopping
	Transition       string  // slide transition style: "none", "slide", "fade"
	PresenterMonitor string  // monitor spec for the presenter view; "" = disabled
	CacheMB          int     // 0 = auto-size to display; otherwise hard cap in MB
	RenderScale      float64 // 0 = native; 0.5..1.0 fraction of native pixels for renders
	DebugStats       bool    // print memory/render diagnostics to stderr
}

// PresenterEnabled reports whether a presenter view was requested.
func (c Config) PresenterEnabled() bool { return c.PresenterMonitor != "" }

// Color is an RGBA color parsed from a hex string.
type Color struct{ R, G, B, A uint8 }

// MonitorSpec is a monitor selection in a sidecar: an index (monitor = 1)
// or a name (monitor = "HDMI-1").
type MonitorSpec string

// UnmarshalTOML accepts both integers and strings.
func (m *MonitorSpec) UnmarshalTOML(v any) error {
	switch v := v.(type) {
	case int64:
		*m = MonitorSpec(strconv.FormatInt(v, 10))
	case string:
		*m = MonitorSpec(v)
	default:
		return fmt.Errorf("monitor must be an index or a name, got %T", v)
	}
	return nil
}

// sidecar is the normalized sidecar schema. TOML sidecars populate all fields;
// pdfpc sidecars populate PlaybackPages and Notes.
type sidecar struct {
	Auto             string         `toml:"auto"`
	Loop             *bool          `toml:"loop"`
	StartPage        *int           `toml:"start"`
	Monitor          *MonitorSpec   `toml:"monitor"`
	Pages            *string        `toml:"pages"`
	Background       *string        `toml:"bg"`
	Progress         *bool          `toml:"progress"`
	AutoQuit         *bool          `toml:"autoquit"`
	Transition       *string        `toml:"transition"`
	PresenterMonitor *MonitorSpec   `toml:"presenter_monitor"`
	CacheMB          *int           `toml:"cache_mb"`
	RenderScale      *float64       `toml:"render_scale"`
	PerPage          []perPageEntry `toml:"page"`
	PlaybackPages    []int          `toml:"-"`
	HasPlaybackPages bool           `toml:"-"`
}

type perPageEntry struct {
	N     int    `toml:"n"`
	Auto  string `toml:"auto"`
	Notes string `toml:"notes"`
}

// Load resolves the final Config from flags + optional sidecar.
// Precedence: explicit flags > sidecar > flag defaults.
func Load(f Flags) (Config, error) {
	c := Config{
		PDFPath:          f.PDFPath,
		Auto:             f.Auto,
		Loop:             f.Loop,
		StartPage:        f.StartPage,
		Monitor:          f.Monitor,
		NoFullscreen:     f.NoFullscreen,
		Progress:         f.Progress,
		AutoQuit:         f.AutoQuit,
		Transition:       f.Transition,
		PresenterMonitor: f.PresenterMonitor,
		CacheMB:          f.CacheMB,
		RenderScale:      f.RenderScale,
		DebugStats:       f.DebugStats,
		PerPage:          map[int]time.Duration{},
		Notes:            map[int]string{},
	}
	pagesSpec := f.Pages
	bgSpec := f.Background

	side, sidePath, err := loadSidecar(f.PDFPath, f.ConfigPath)
	if err != nil {
		return Config{}, err
	}
	if side != nil {
		// A sidecar value applies unless the same setting was given
		// explicitly on the command line.
		unset := func(flag string) bool { return !f.Changed[flag] }
		if unset("auto") && side.Auto != "" {
			d, err := time.ParseDuration(side.Auto)
			if err != nil {
				return Config{}, fmt.Errorf("%s: invalid auto duration %q: %w", sidePath, side.Auto, err)
			}
			c.Auto = d
		}
		setIf(&c.Loop, side.Loop, unset("loop"))
		setIf(&c.StartPage, side.StartPage, unset("start"))
		setIf(&pagesSpec, side.Pages, unset("pages"))
		setIf(&bgSpec, side.Background, unset("bg"))
		setIf(&c.Progress, side.Progress, unset("progress"))
		setIf(&c.AutoQuit, side.AutoQuit, unset("autoquit"))
		setIf(&c.Transition, side.Transition, unset("transition"))
		setIf(&c.CacheMB, side.CacheMB, unset("cache-mb"))
		setIf(&c.RenderScale, side.RenderScale, unset("render-scale"))
		if side.Monitor != nil && unset("monitor") {
			c.Monitor = string(*side.Monitor)
		}
		if side.PresenterMonitor != nil && unset("presenter-monitor") {
			c.PresenterMonitor = string(*side.PresenterMonitor)
		}
		if side.HasPlaybackPages {
			c.UsePlaybackPages = true
			c.PlaybackPages = append(c.PlaybackPages, side.PlaybackPages...)
		}
		for _, e := range side.PerPage {
			if e.Auto != "" {
				d, err := time.ParseDuration(e.Auto)
				if err != nil {
					return Config{}, fmt.Errorf("%s: page %d: invalid auto %q: %w", sidePath, e.N, e.Auto, err)
				}
				c.PerPage[e.N] = d
			}
			if strings.TrimSpace(e.Notes) != "" {
				c.Notes[e.N] = e.Notes
			}
		}
	}

	c.Monitor = strings.TrimSpace(c.Monitor)
	if c.Monitor == "" {
		c.Monitor = "0"
	}
	if pm := strings.TrimSpace(c.PresenterMonitor); pm == "-1" {
		c.PresenterMonitor = ""
	} else {
		c.PresenterMonitor = pm
	}

	switch c.Transition {
	case "":
		c.Transition = "slide"
	case "slide", "fade", "none":
	default:
		return Config{}, fmt.Errorf("--transition must be slide, fade or none, got %q", c.Transition)
	}

	pr, err := ParsePageRange(pagesSpec)
	if err != nil {
		return Config{}, fmt.Errorf("--pages: %w", err)
	}
	c.PageRange = pr
	if c.UsePlaybackPages && !pr.All {
		c.PlaybackPages = filterPlaybackPages(c.PlaybackPages, pr)
	}

	col, err := ParseColor(bgSpec)
	if err != nil {
		return Config{}, fmt.Errorf("--bg: %w", err)
	}
	c.Background = col

	if c.StartPage < 1 {
		return Config{}, fmt.Errorf("--start must be >= 1, got %d", c.StartPage)
	}

	if c.CacheMB < 0 {
		return Config{}, fmt.Errorf("--cache-mb must be >= 0, got %d", c.CacheMB)
	}
	if c.RenderScale != 0 && (c.RenderScale < 0.5 || c.RenderScale > 1.0) {
		return Config{}, fmt.Errorf("--render-scale must be between 0.5 and 1.0 (or 0 for native), got %v", c.RenderScale)
	}

	return c, nil
}

// setIf copies *src into *dst when src is set and ok is true.
func setIf[T any](dst *T, src *T, ok bool) {
	if ok && src != nil {
		*dst = *src
	}
}

func loadSidecar(pdfPath, override string) (*sidecar, string, error) {
	path, err := resolveSidecarPath(pdfPath, override)
	if err != nil || path == "" {
		return nil, path, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, path, fmt.Errorf("read sidecar %s: %w", path, err)
	}
	s, err := parseSidecar(path, data)
	if err != nil {
		return nil, path, err
	}
	return s, path, nil
}

// SidecarPath returns the conventional Boozle sidecar path for a file:
// deck.pdf (or deck.pptx) → deck.boozle.toml.
func SidecarPath(file string) string {
	return strings.TrimSuffix(file, filepath.Ext(file)) + ".boozle.toml"
}

func resolveSidecarPath(pdfPath, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	pdfpc := strings.TrimSuffix(pdfPath, filepath.Ext(pdfPath)) + ".pdfpc"
	for _, path := range []string{SidecarPath(pdfPath), pdfpc} {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return path, fmt.Errorf("stat sidecar %s: %w", path, err)
		}
	}
	return "", nil
}

func parseSidecar(path string, data []byte) (*sidecar, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".pdfpc" || (ext != ".toml" && bytes.HasPrefix(bytes.TrimSpace(data), []byte("{"))) {
		return parsePDFPCSidecar(path, data)
	}
	var s sidecar
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse sidecar %s: %w", path, err)
	}
	return &s, nil
}

type pdfpcSidecar struct {
	Format int         `json:"pdfpcFormat"`
	Pages  []pdfpcPage `json:"pages"`
}

type pdfpcPage struct {
	Idx    int    `json:"idx"`
	Hidden bool   `json:"hidden"`
	Note   string `json:"note"`
}

func parsePDFPCSidecar(path string, data []byte) (*sidecar, error) {
	var p pdfpcSidecar
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse pdfpc sidecar %s: %w", path, err)
	}
	if p.Format != 2 {
		return nil, fmt.Errorf("parse pdfpc sidecar %s: unsupported pdfpcFormat %d", path, p.Format)
	}
	if len(p.Pages) == 0 {
		return nil, fmt.Errorf("parse pdfpc sidecar %s: missing pages", path)
	}

	s := sidecar{HasPlaybackPages: true}
	notes := map[int]string{}
	for i, page := range p.Pages {
		if page.Idx < 0 {
			return nil, fmt.Errorf("parse pdfpc sidecar %s: page %d has negative idx %d", path, i, page.Idx)
		}
		page1 := page.Idx + 1
		if strings.TrimSpace(page.Note) != "" {
			notes[page1] = page.Note
		}
		if !page.Hidden {
			s.PlaybackPages = append(s.PlaybackPages, page1)
		}
	}
	if len(notes) > 0 {
		s.PerPage = make([]perPageEntry, 0, len(notes))
		for n, note := range notes {
			s.PerPage = append(s.PerPage, perPageEntry{N: n, Notes: note})
		}
	}
	return &s, nil
}

func filterPlaybackPages(pages []int, pr PageRange) []int {
	out := make([]int, 0, len(pages))
	for _, n := range pages {
		if pr.Contains(n) {
			out = append(out, n)
		}
	}
	return out
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
	_, ok := slices.BinarySearch(p.Pages, n)
	return ok
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

// MaxPage bounds page numbers in --pages, so a typo like "1-999999999"
// is rejected instead of allocating a billion-entry set.
const MaxPage = 100000

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
			if b > MaxPage {
				return PageRange{}, fmt.Errorf("range %q exceeds page %d", part, MaxPage)
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
		if n > MaxPage {
			return PageRange{}, fmt.Errorf("page %d exceeds %d", n, MaxPage)
		}
		seen[n] = struct{}{}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	slices.Sort(out)
	return PageRange{Pages: out}, nil
}
