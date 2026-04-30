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
)

// Set by goreleaser via -ldflags.
var (
	version = "dev"
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
		auto         time.Duration
		loop         bool
		startPage    int
		monitorIdx   int
		pages        string
		bg           string
		noFullscreen bool
		configPath   string
		progress     bool
		autoQuit     bool
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
  q  Esc                     quit`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			cfg, err := config.Load(config.Flags{
				PDFPath:      pdfPath,
				Auto:         auto,
				Loop:         loop,
				StartPage:    startPage,
				MonitorIdx:   monitorIdx,
				Pages:        pages,
				Background:   bg,
				NoFullscreen: noFullscreen,
				ConfigPath:   configPath,
				Progress:     progress,
				AutoQuit:     autoQuit,
			})
			if err != nil {
				return err
			}

			return app.Run(cfg)
		},
	}

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
	fmt.Fprintln(w, "  -m, --monitor <N>       monitor index (0 = primary)")
	fmt.Fprintln(w, "      --pages <range>     restrict to pages, e.g. 3-7,10")
	fmt.Fprintln(w, "      --no-fullscreen     run windowed (debugging)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  boozle slides.pdf --auto 30s --progress")
	fmt.Fprintln(w, "  boozle slides.pdf --auto 1m --loop --progress")
	fmt.Fprintln(w, "  boozle slides.pdf --auto 20s --autoquit")
	fmt.Fprintln(w, "  boozle slides.pdf --pages 1-5 --monitor 1")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'boozle --help' for keybindings and the full flag reference.")
}
