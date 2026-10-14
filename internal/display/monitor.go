// Package display resolves monitor selections (by index or name) against
// the monitors Ebitengine reports, and lists them for --list-monitors.
package display

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/hajimehoshi/ebiten/v2"
)

// Info describes one connected monitor. Index 0 is the primary monitor.
type Info struct {
	Index  int
	Name   string
	Width  int // device-independent pixels
	Height int
	Scale  float64
}

// List returns the connected monitors in Ebitengine's order (primary first).
func List() []Info {
	monitors := ebiten.AppendMonitors(nil)
	out := make([]Info, len(monitors))
	for i, m := range monitors {
		w, h := m.Size()
		out[i] = Info{Index: i, Name: m.Name(), Width: w, Height: h, Scale: m.DeviceScaleFactor()}
	}
	return out
}

// Resolve maps a monitor spec to an index into List(). A spec is either an
// index ("0", "1", …), a monitor name as printed by --list-monitors
// (case-insensitive, e.g. "HDMI-1"), or a unique part of one ("hdmi").
// An empty spec means the primary monitor.
func Resolve(spec string) (int, error) {
	return resolve(spec, List())
}

func resolve(spec string, monitors []Info) (int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		spec = "0"
	}
	if n, err := strconv.Atoi(spec); err == nil {
		if n < 0 || n >= len(monitors) {
			return 0, fmt.Errorf("monitor %d: only %d monitor(s) detected (see --list-monitors)", n, len(monitors))
		}
		return n, nil
	}
	want := strings.ToLower(spec)
	for _, m := range monitors {
		if strings.ToLower(m.Name) == want {
			return m.Index, nil
		}
	}
	var matches []Info
	for _, m := range monitors {
		if strings.Contains(strings.ToLower(m.Name), want) {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].Index, nil
	case 0:
		return 0, fmt.Errorf("monitor %q: no monitor with that name (see --list-monitors)", spec)
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = fmt.Sprintf("%d: %s", m.Index, m.Name)
		}
		return 0, fmt.Errorf("monitor %q is ambiguous (%s); use the index instead", spec, strings.Join(names, ", "))
	}
}

// Monitor returns the monitor at index, or nil if it no longer exists.
func Monitor(index int) *ebiten.MonitorType {
	monitors := ebiten.AppendMonitors(nil)
	if index < 0 || index >= len(monitors) {
		return nil
	}
	return monitors[index]
}

// Current returns the index of the monitor Ebitengine considers the window
// to be on, or -1. Monitors are compared by identity: Ebitengine keeps the
// same objects until monitors are plugged or unplugged, and names alone are
// not unique (on Windows every monitor is "Generic PnP Monitor").
func Current() int {
	cur := ebiten.Monitor()
	if cur == nil {
		return -1
	}
	for i, m := range ebiten.AppendMonitors(nil) {
		if m == cur {
			return i
		}
	}
	return -1
}

// Name returns the name of the monitor at index, or "?".
func Name(index int) string {
	if m := Monitor(index); m != nil {
		return m.Name()
	}
	return "?"
}

// PrepareWindow targets the window at monitor index before ebiten.RunGame
// and sizes it to fit comfortably inside that monitor. Ebitengine centres
// the initial window on the target monitor; a window larger than the
// monitor would instead be pinned to its edge, where window managers and
// Ebitengine's position-based monitor detection can mistake it for the
// neighbouring monitor.
func PrepareWindow(index, width, height int) error {
	m := Monitor(index)
	if m == nil {
		return fmt.Errorf("monitor %d is not connected", index)
	}
	ebiten.SetMonitor(m)
	if mw, mh := m.Size(); mw > 0 && mh > 0 {
		width = min(width, mw*4/5)
		height = min(height, mh*4/5)
	}
	ebiten.SetWindowSize(max(1, width), max(1, height))
	return nil
}

// Describe formats monitors as printed by --list-monitors.
func Describe(w io.Writer, monitors []Info) {
	nameW := 4
	for _, m := range monitors {
		nameW = max(nameW, len(m.Name))
	}
	for _, m := range monitors {
		primary := ""
		if m.Index == 0 {
			primary = "  (primary)"
		}
		fmt.Fprintf(w, "  %d: %-*s  %5dx%-5d  scale %.2g%s\n", m.Index, nameW, m.Name, m.Width, m.Height, m.Scale, primary)
	}
	fmt.Fprintln(w, "\nPass an index or a name to --monitor / --presenter-monitor, e.g. --monitor 1 or --monitor HDMI-1.")
}

var errListDone = errors.New("list done")

type listGame struct{ w io.Writer }

func (g *listGame) Update() error {
	Describe(g.w, List())
	return errListDone
}

func (g *listGame) Draw(*ebiten.Image) {}

func (g *listGame) Layout(int, int) (int, int) { return 1, 1 }

// PrintMonitors lists connected monitors. It runs a minimal one-frame
// Ebitengine window first, because some platforms only report complete
// monitor information (names, scale) once the windowing system is up; the
// window is 1×1 and undecorated so the flash is imperceptible.
func PrintMonitors(w io.Writer) error {
	ebiten.SetWindowDecorated(false)
	ebiten.SetWindowSize(1, 1)
	ebiten.SetWindowTitle("")
	if err := ebiten.RunGame(&listGame{w: w}); err != nil && !errors.Is(err, errListDone) {
		return err
	}
	return nil
}
