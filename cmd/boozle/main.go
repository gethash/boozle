// Package main is the boozle entry point.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/gethash/boozle/internal/app"
	"github.com/gethash/boozle/internal/config"
	"github.com/gethash/boozle/internal/display"
	"github.com/gethash/boozle/internal/pptxnotes"
)

// Set by goreleaser via -ldflags.
var (
	version = "v1.1.1"
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
		monitorIdx       int
		pages            string
		bg               string
		noFullscreen     bool
		configPath       string
		progress         bool
		autoQuit         bool
		transition       string
		presenterMonitor int
		presenterSocket  string // hidden internal flag for the slave subprocess
		listMonitors     bool
	)

	cmd := &cobra.Command{
		Use:   "boozle <file.pdf>",
		Short: "Modern PDF auto-advance presenter",
		Long: `boozle plays PDF files full-screen with a configurable auto-advance timer.
A spiritual successor to Impressive, packaged as a single static binary.

Keybindings:
  →  PgDn  Space  Scroll↓   next page
  ←  PgUp         Scroll↑   previous page
  Backspace                  previous page (or delete a typed digit)
  Home / End                 first / last page
  0-9  then  Enter           jump to page number
  l                          return to previously viewed page
  p                          pause / resume auto-advance
  b                          black-out screen (toggle)
  w                          white-out screen (toggle)
  f                          toggle fullscreen
  Tab                        slide overview (navigate thumbnails, Enter or click to jump)
  q  Esc                     quit

Presenter view (--presenter-monitor):
  Same navigation keys work when either display has focus. Speaker notes from
  [[page]] notes entries are shown in the presenter window.

Monitor selection:
  Use --list-monitors (-M) to print connected displays and their indices.

Speaker notes:
  Use "boozle notes import deck.pptx" to extract PowerPoint speaker notes into
  a standalone deck.boozle.toml sidecar.`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		RunE: func(cmd *cobra.Command, args []string) error {
			if listMonitors {
				fmt.Fprintln(cmd.OutOrStdout(), "Available monitors:")
				return display.PrintMonitors()
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
				return app.RunPresenter(presenterSocket, pdfPath, monitorIdx)
			}

			cfg, err := config.Load(config.Flags{
				PDFPath:          pdfPath,
				Auto:             auto,
				Loop:             loop,
				StartPage:        startPage,
				MonitorIdx:       monitorIdx,
				Pages:            pages,
				Background:       bg,
				NoFullscreen:     noFullscreen,
				ConfigPath:       configPath,
				Progress:         progress,
				AutoQuit:         autoQuit,
				Transition:       transition,
				PresenterMonitor: presenterMonitor,
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
	cmd.Flags().IntVarP(&monitorIdx, "monitor", "m", 0, "monitor index (0 = primary)")
	cmd.Flags().StringVar(&pages, "pages", "", `restrict to a page range, e.g. "3-7,10"`)
	cmd.Flags().StringVar(&bg, "bg", "#000000", "background color hex")
	cmd.Flags().BoolVar(&noFullscreen, "no-fullscreen", false, "windowed mode (debugging)")
	cmd.Flags().StringVar(&configPath, "config", "", "explicit sidecar config path")
	cmd.Flags().BoolVar(&progress, "progress", false, "show page-position and auto-advance progress overlay")
	cmd.Flags().BoolVar(&autoQuit, "autoquit", false, "quit after the last page instead of stopping")
	cmd.Flags().StringVar(&transition, "transition", "", "slide transition style: slide, fade, none (default slide)")
	cmd.Flags().IntVarP(&presenterMonitor, "presenter-monitor", "P", -1, "monitor index for presenter view (-1 = disabled)")
	cmd.Flags().BoolVarP(&listMonitors, "list-monitors", "M", false, "list available monitors and exit")
	cmd.Flags().StringVar(&presenterSocket, "_presenter-socket", "", "")
	_ = cmd.Flags().MarkHidden("_presenter-socket")

	return cmd
}

func newNotesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notes",
		Short: "Work with speaker notes sidecars",
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
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if outPath != "" && configPath != "" {
				return fmt.Errorf("use either --out or --config, not both")
			}
			target := outPath
			if target == "" {
				target = configPath
			}
			if target == "" {
				target = pptxnotes.DefaultSidecarPath(args[0])
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
// no PDF — short enough to scan, with concrete examples and a pointer to
// --help for the full flag reference.
func printNoArgsHint(w io.Writer) {
	fmt.Fprintln(w, "boozle: no PDF given.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  boozle <file.pdf> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Common flags:")
	fmt.Fprintln(w, "  -a, --auto <duration>   advance every duration (e.g. 30s, 1m30s)")
	fmt.Fprintln(w, "  -l, --loop              loop back to first page after last")
	fmt.Fprintln(w, "      --progress          show page-position and countdown bars")
	fmt.Fprintln(w, "      --autoquit          quit after the last page")
	fmt.Fprintln(w, "  -s, --start <N>         start at page N (1-indexed)")
	fmt.Fprintln(w, "  -m, --monitor <N>       monitor index for slides (0 = primary)")
	fmt.Fprintln(w, "  -P, --presenter-monitor <N>  monitor index for presenter view")
	fmt.Fprintln(w, "  -M, --list-monitors     list connected displays and exit")
	fmt.Fprintln(w, "      --pages <range>     restrict to pages, e.g. 3-7,10")
	fmt.Fprintln(w, "      --no-fullscreen     run windowed (debugging)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Notes:")
	fmt.Fprintln(w, "  boozle notes import deck.pptx --out deck.boozle.toml")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  boozle slides.pdf --auto 30s --progress")
	fmt.Fprintln(w, "  boozle slides.pdf --auto 1m --loop --progress")
	fmt.Fprintln(w, "  boozle slides.pdf --auto 20s --autoquit")
	fmt.Fprintln(w, "  boozle slides.pdf --pages 1-5 --monitor 1")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'boozle --help' for keybindings and the full flag reference.")
}
