// Package main is the boozle entry point.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/gethash/boozle/internal/app"
	"github.com/gethash/boozle/internal/config"
	"github.com/gethash/boozle/internal/display"
	"github.com/gethash/boozle/internal/pptxnotes"
)

// Set by the release workflow via -ldflags. The default tracks the latest
// release because Homebrew builds from the source tarball without ldflags
// (TestDefaultVersionMatchesChangelog keeps it in sync).
var (
	version = "v1.3.0"
	commit  = "none"
	date    = "unknown"
)

// errNoArgs is returned from RunE when invoked with no PDF; main() uses
// it to exit non-zero without prefixing the message with "error:".
var errNoArgs = errors.New("no PDF given")

func main() {
	err := newRootCmd().Execute()
	if err == nil {
		return
	}
	if errors.Is(err, errNoArgs) {
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func newRootCmd() *cobra.Command {
	var (
		auto             time.Duration
		loop             bool
		startPage        int
		monitor          string
		pages            string
		bg               string
		noFullscreen     bool
		configPath       string
		progress         bool
		autoQuit         bool
		transition       string
		presenterMonitor string
		presenterSocket  string // hidden internal flag for the slave subprocess
		debugStats       bool   // hidden: periodic memory/render diagnostics
		listMonitors     bool
		cacheMB          int
		renderScale      float64
	)

	cmd := &cobra.Command{
		Use:   "boozle <file.pdf>",
		Short: "Modern PDF auto-advance presenter",
		Long: `Boozle plays PDF decks on an audience display with manual controls,
auto-advance, presenter view, sidecar configuration, and speaker notes.
It is a spiritual successor to Impressive packaged as a single binary.

Common examples:
  boozle slides.pdf --auto 30s --progress
  boozle slides.pdf --auto 20s --loop --progress
  boozle slides.pdf --monitor 1 --presenter-monitor 0
  boozle notes import deck.pptx --out deck.boozle.toml

Audience display:
  By default Boozle opens the PDF full-screen on monitor 0 and waits for
  manual navigation. Use --auto to advance on a timer, --loop to restart
  after the last page, and --autoquit to close when advancing past the
  last page.

Keybindings:
  Right  PgDn  Space  Scroll down  next page
  Left   PgUp         Scroll up    previous page
  Backspace                  previous page (or delete a typed digit)
  Home / End                 first / last page
  0-9  then  Enter           jump to page number (top row or numpad)
  l                          return to previously viewed page
  p                          pause / resume auto-advance
  b                          black-out screen (toggle)
  w                          white-out screen (toggle)
  f                          toggle fullscreen (audience window)
  Tab                        slide overview (arrows select, Enter or click
                             jumps, Tab or Esc closes)
  q  Esc                     quit

Presenter view (--presenter-monitor):
  Opens a second window for the speaker with current slide, next slide,
  wall clock, elapsed time, slide counter, auto-advance progress, and notes.
  The same navigation keys work when either display has focus, and the Tab
  overview opens on the presenter screen. Use a monitor different from
  --monitor, or use --no-fullscreen for one-monitor testing.

Monitor selection:
  Use --list-monitors (-M) to print connected displays, then pass an index
  or a name (or a unique part of one, e.g. "hdmi") to --monitor and
  --presenter-monitor. On Linux, --debug-stats logs fullscreen placement.

Speaker notes:
  Presenter view shows notes from Boozle TOML sidecars, .pdfpc sidecars,
  or imported PowerPoint notes.

PowerPoint speaker-note import:
  Use "boozle notes import deck.pptx" to extract PowerPoint speaker notes into
  a standalone deck.boozle.toml sidecar. The .pptx is not needed during
  presentation after the sidecar has been written.

Sidecars:
  Boozle auto-loads <file>.boozle.toml, or <file>.pdfpc when no Boozle TOML
  sidecar exists. Use --config <path> to choose one explicitly. Flags given
  on the command line always override sidecar values.

  Supported TOML keys:
    auto, loop, start, monitor, pages, bg, progress, autoquit, transition,
    presenter_monitor, cache_mb, render_scale

  Per-page entries use [[page]] with n, auto, and notes fields.`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		RunE: func(cmd *cobra.Command, args []string) error {
			if listMonitors {
				fmt.Fprintln(cmd.OutOrStdout(), "Available monitors:")
				return display.PrintMonitors(cmd.OutOrStdout())
			}

			if len(args) == 0 {
				printNoArgsHint(cmd.ErrOrStderr())
				return errNoArgs
			}
			pdfPath := args[0]
			if _, err := os.Stat(pdfPath); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("file not found: %s", pdfPath)
				}
				return err
			}

			// Presenter slave mode: spawned by the master with a hidden flag.
			if presenterSocket != "" {
				return app.RunPresenter(app.PresenterOptions{
					SocketPath:  presenterSocket,
					PDFPath:     pdfPath,
					Monitor:     monitor,
					Windowed:    noFullscreen,
					CacheMB:     cacheMB,
					RenderScale: renderScale,
					DebugStats:  debugStats,
				})
			}

			cfg, err := config.Load(config.Flags{
				PDFPath:          pdfPath,
				Auto:             auto,
				Loop:             loop,
				StartPage:        startPage,
				Changed:          changedFlags(cmd),
				Monitor:          monitor,
				Pages:            pages,
				Background:       bg,
				NoFullscreen:     noFullscreen,
				ConfigPath:       configPath,
				Progress:         progress,
				AutoQuit:         autoQuit,
				Transition:       transition,
				PresenterMonitor: presenterMonitor,
				CacheMB:          cacheMB,
				RenderScale:      renderScale,
				DebugStats:       debugStats,
			})
			if err != nil {
				return err
			}

			return app.Run(cfg)
		},
	}

	cmd.AddCommand(newNotesCmd())

	cmd.Flags().DurationVarP(&auto, "auto", "a", 0, "advance every duration (e.g. 30s, 1m30s); 0 disables")
	cmd.Flags().BoolVarP(&loop, "loop", "l", false, "loop back to first page after last")
	cmd.Flags().IntVarP(&startPage, "start", "s", 1, "start at page N (1-indexed)")
	cmd.Flags().StringVarP(&monitor, "monitor", "m", "0", "monitor index or name (0 = primary; see --list-monitors)")
	cmd.Flags().StringVar(&pages, "pages", "", `restrict to a page range, e.g. "3-7,10"`)
	cmd.Flags().StringVar(&bg, "bg", "#000000", "background color hex")
	cmd.Flags().BoolVar(&noFullscreen, "no-fullscreen", false, "windowed mode (debugging)")
	cmd.Flags().StringVar(&configPath, "config", "", "explicit sidecar config path (.boozle.toml or .pdfpc)")
	cmd.Flags().BoolVar(&progress, "progress", false, "show page-position and auto-advance progress overlay")
	cmd.Flags().BoolVar(&autoQuit, "autoquit", false, "quit after the last page instead of stopping")
	cmd.Flags().StringVar(&transition, "transition", "", "slide transition style: slide, fade, none (default slide)")
	cmd.Flags().StringVarP(&presenterMonitor, "presenter-monitor", "P", "", "monitor index or name for the presenter view (empty or -1 = disabled)")
	cmd.Flags().BoolVarP(&listMonitors, "list-monitors", "M", false, "list available monitors and exit")
	cmd.Flags().IntVar(&cacheMB, "cache-mb", 0, "GPU page cache cap in MB (0 = auto-size to display)")
	cmd.Flags().Float64Var(&renderScale, "render-scale", 0, "render at fraction of native pixels (0.5..1.0; 0 = native)")
	cmd.Flags().StringVar(&presenterSocket, "_presenter-socket", "", "")
	_ = cmd.Flags().MarkHidden("_presenter-socket")
	cmd.Flags().BoolVar(&debugStats, "debug-stats", false, "print memory and render diagnostics to stderr every 2s")
	_ = cmd.Flags().MarkHidden("debug-stats")

	return cmd
}

// changedFlags returns the long names of flags set explicitly on the
// command line; only those override sidecar values.
func changedFlags(cmd *cobra.Command) map[string]bool {
	changed := map[string]bool{}
	cmd.Flags().Visit(func(f *pflag.Flag) { changed[f.Name] = true })
	return changed
}

func newNotesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notes",
		Short: "Work with speaker notes sidecars",
		Long: `Work with speaker notes sidecars.

Boozle shows notes in presenter view from .boozle.toml page entries or from
.pdfpc sidecars. Use "boozle notes import" to extract PowerPoint speaker notes
from a .pptx file into a standalone Boozle TOML sidecar.`,
	}
	cmd.AddCommand(newNotesImportCmd())
	return cmd
}

func newNotesImportCmd() *cobra.Command {
	var (
		outPath    string
		configPath string
		force      bool
	)
	cmd := &cobra.Command{
		Use:   "import <file.pptx>",
		Short: "Extract PowerPoint speaker notes into a Boozle TOML sidecar",
		Long: `Extract PowerPoint speaker notes into a Boozle TOML sidecar.

By default this writes <file>.boozle.toml next to the .pptx. Use --out to pick
another path. --config is accepted as an alias for --out so the output path
matches the sidecar terminology used by the presenter command.

The generated sidecar contains normal [[page]] notes entries. Present with the
PDF plus sidecar later; the .pptx file is not needed at presentation time.
Use --force to replace an existing sidecar.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outPath != "" && configPath != "" {
				return fmt.Errorf("use either --out or --config, not both")
			}
			target := outPath
			if target == "" {
				target = configPath
			}
			if target == "" {
				target = config.SidecarPath(args[0])
			}
			notes, err := pptxnotes.Extract(args[0])
			if err != nil {
				return err
			}
			if err := pptxnotes.WriteSidecar(target, notes, force); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s with notes for %d slide(s)\n", target, len(notes))
			return nil
		},
	}
	cmd.Flags().StringVar(&outPath, "out", "", "output TOML sidecar path (default: <file>.boozle.toml)")
	cmd.Flags().StringVar(&configPath, "config", "", "output TOML sidecar path; alias for --out")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing TOML sidecar")
	return cmd
}

// printNoArgsHint shows a friendly summary when boozle is invoked with
// no PDF - short enough to scan, with concrete examples and a pointer to
// --help for the full flag reference.
func printNoArgsHint(w io.Writer) {
	fmt.Fprintln(w, "boozle: no PDF given.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  boozle <file.pdf> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Fast starts:")
	fmt.Fprintln(w, "  boozle slides.pdf --auto 30s --progress")
	fmt.Fprintln(w, "  boozle slides.pdf --auto 20s --loop --progress")
	fmt.Fprintln(w, "  boozle slides.pdf --monitor 1 --presenter-monitor 0")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Common playback flags:")
	fmt.Fprintln(w, "  -a, --auto <duration>   advance every duration (e.g. 30s, 1m30s)")
	fmt.Fprintln(w, "  -l, --loop              loop back to first page after last")
	fmt.Fprintln(w, "      --progress          show page-position and countdown bars")
	fmt.Fprintln(w, "      --autoquit          quit after the last page")
	fmt.Fprintln(w, "  -s, --start <N>         start at page N (1-indexed)")
	fmt.Fprintln(w, "      --pages <range>     restrict to pages, e.g. 3-7,10")
	fmt.Fprintln(w, "      --transition <style>  slide, fade, or none")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "      --bg <#hex>         background color around the slide")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Displays:")
	fmt.Fprintln(w, "  -m, --monitor <N|name>  monitor for slides (0 = primary)")
	fmt.Fprintln(w, "  -P, --presenter-monitor <N|name>  monitor for presenter view")
	fmt.Fprintln(w, "  -M, --list-monitors     list connected displays and exit")
	fmt.Fprintln(w, "      --no-fullscreen     run windowed (debugging)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Sidecars and notes:")
	fmt.Fprintln(w, "  Auto-loads slides.boozle.toml or slides.pdfpc next to slides.pdf;")
	fmt.Fprintln(w, "  flags given on the command line win over sidecar values.")
	fmt.Fprintln(w, "  TOML keys include auto, loop, pages, bg, progress, autoquit, transition,")
	fmt.Fprintln(w, "  monitor, presenter_monitor, cache_mb, render_scale, and [[page]] notes.")
	fmt.Fprintln(w, "      --config <path>     explicit .boozle.toml or .pdfpc sidecar")
	fmt.Fprintln(w, "  boozle notes import deck.pptx --out deck.boozle.toml")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Performance:")
	fmt.Fprintln(w, "      --cache-mb <N>      GPU page cache cap in MB (0 = auto)")
	fmt.Fprintln(w, "      --render-scale <F>  render at 0.5..1.0 of native pixels")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'boozle --help' for keybindings and the full flag reference.")
}
