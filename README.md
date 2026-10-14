# boozle

[![CI](https://github.com/gethash/boozle/actions/workflows/ci.yml/badge.svg)](https://github.com/gethash/boozle/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/gethash/boozle)](https://github.com/gethash/boozle/releases/latest)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

Boozle is a modern, cross-platform PDF presenter for timed decks, kiosk loops, presenter view, and speaker notes.

## Quick Start

Install with Homebrew:

```bash
brew tap gethash/tap
brew install boozle
```

Or install the latest release on macOS/Linux:

```bash
curl -fsSL https://github.com/gethash/boozle/releases/latest/download/install.sh | sh
```

Run a deck:

```bash
boozle slides.pdf
```

Run an unattended loop with a progress overlay:

```bash
boozle slides.pdf --auto 20s --loop --progress
```

Use a projector plus presenter view:

```bash
boozle --list-monitors
boozle slides.pdf --monitor 1 --presenter-monitor 0
```

## What Boozle Can Do

- **PDF playback:** open a deck full-screen on an audience display, or use `--no-fullscreen` for windowed testing.
- **Auto-advance:** advance every Go-style duration such as `30s`, `1m30s`, or `750ms`.
- **Per-slide timing:** override individual slide durations in a `.boozle.toml` sidecar.
- **Deck control:** start at a specific page, restrict playback to a page range, loop after the last page, or quit automatically after a one-way run.
- **Progress overlay:** show a segmented page-position bar, countdown fill, and page counter.
- **Transitions:** choose `slide`, `fade`, or `none`.
- **Presenter view:** open a second window with current slide, next slide, clock, elapsed time, slide counter, auto-advance progress, and notes.
- **Speaker notes:** read notes from Boozle TOML sidecars, `.pdfpc` sidecars, or imported PowerPoint notes.
- **PowerPoint speaker-note import:** extract `.pptx` notes once with `boozle notes import`, then present with only the PDF plus sidecar.
- **Slide overview:** press `Tab` for a thumbnail grid and jump to any slide with the keyboard or mouse.
- **Monitor selection:** list displays with `--list-monitors`, then choose audience and presenter monitors by index or by name (e.g. `--monitor HDMI-1`).
- **HiDPI rendering:** rasterise pages at native monitor resolution for Retina and 4K displays.
- **Fast and lean:** slides render in the background with prefetching, startup takes a fraction of a second, and memory stays bounded on large decks; `--cache-mb` and `--render-scale` tune it further.
- **Sidecars:** keep deck-specific defaults next to the PDF in `.boozle.toml`; Boozle also understands `.pdfpc` sidecars.
- **Single-binary distribution:** releases ship native binaries for macOS (Apple Silicon and Intel), Linux (x86-64 and ARM64), and Windows x86-64.

## Installation

### Homebrew

```bash
brew tap gethash/tap
brew install boozle
```

Homebrew compiles from source locally, so macOS does not need the Gatekeeper quarantine workaround used by downloaded unsigned binaries.

### macOS and Linux

```bash
curl -fsSL https://github.com/gethash/boozle/releases/latest/download/install.sh | sh
```

The script detects your OS and architecture, downloads the right archive, verifies its SHA-256 against the release `checksums.txt`, and installs to `/usr/local/bin`. If that path is not writable, it falls back to `~/.local/bin`.

Override the version or install directory with environment variables:

```bash
BOOZLE_VERSION=v1.2.0 BOOZLE_INSTALL_DIR=~/bin \
  curl -fsSL https://github.com/gethash/boozle/releases/latest/download/install.sh | sh
```

On Linux the binary needs the usual X11/OpenGL runtime libraries, which desktop installs already have. On minimal systems install them with `sudo apt-get install libgl1 libxrandr2 libxinerama1 libxcursor1 libxi6 libxxf86vm1` (Debian/Ubuntu).

#### macOS First Run

Boozle release binaries are currently unsigned. If macOS Gatekeeper blocks the first launch, remove the quarantine attribute once:

```bash
xattr -d com.apple.quarantine /usr/local/bin/boozle
```

Code signing and notarisation are planned for a future release.

### Windows

```powershell
iwr -useb https://github.com/gethash/boozle/releases/latest/download/install.ps1 | iex
```

The script installs to `%LOCALAPPDATA%\boozle` and prepends that directory to your user `PATH`. Open a new terminal afterwards.

Windows SmartScreen may warn on first run because the binary is unsigned. Choose **More info** and then **Run anyway**.

### Manual Install

Download the right archive from [Releases](https://github.com/gethash/boozle/releases), extract it, and put `boozle` or `boozle.exe` somewhere on your `PATH`.

## Usage Guide

### Basic Playback

```bash
boozle slides.pdf
boozle slides.pdf --start 4
boozle slides.pdf --pages 3-7,10
```

By default, Boozle opens full-screen on monitor `0` and waits for manual navigation.

### Unattended Playback

```bash
boozle slides.pdf --auto 30s --progress
boozle slides.pdf --auto 20s --loop --progress
boozle slides.pdf --auto 30s --autoquit
```

Use `--auto <duration>` for timed advance. Add `--loop` for kiosk or lobby screens, or `--autoquit` to close Boozle when it advances past the last slide — whether by timer or by pressing next. Without `--loop` or `--autoquit`, navigation simply stops at the first and last slide.

### Displays and Presenter View

```bash
boozle --list-monitors
boozle slides.pdf --monitor 1
boozle slides.pdf --monitor 1 --presenter-monitor 0
boozle slides.pdf --monitor HDMI-1 --presenter-monitor 0
```

`--list-monitors` prints every connected display with its index, name, size, and scale factor; index `0` is the primary display. `--monitor` and `--presenter-monitor` accept either the index or the name (case-insensitive, and any unique part of it works, e.g. `--monitor hdmi`). Names are stable across reboots on Linux and macOS, so they are the better choice in sidecars; on Windows every monitor is called "Generic PnP Monitor", so use indices there.

The audience display is the main presentation window. Presenter view is a second window for the speaker. It shows the current slide, next slide, notes, clock, elapsed time, slide counter, and auto-advance progress. Keys pressed in either window drive the presentation; `f` always toggles the audience window's fullscreen. While presenter view is active, the `Tab` overview opens on the presenter screen only, so the audience keeps seeing the current slide.

In full-screen mode, the audience and presenter windows must be on different displays. For local testing on one monitor, run both windows in windowed mode:

```bash
boozle slides.pdf --no-fullscreen --monitor 0 --presenter-monitor 0
```

#### Linux Notes

- Boozle uses X11. On Wayland desktops (GNOME, KDE) it runs through XWayland, which works for fullscreen presenting; window placement is ultimately up to the compositor.
- Multi-monitor fullscreen needs the XRandR and Xinerama runtime libraries (`libxrandr2` and `libxinerama1` on Debian/Ubuntu). Without them, only one monitor is listed or fullscreen may land on the wrong display.
- Boozle waits until the window manager has actually moved the window to the requested monitor before going fullscreen, verifies the result, and retries once. Run with `--debug-stats` to log each placement step if a display still ends up wrong — that log is the most useful thing to attach to a bug report.
- The windows carry the X11 class `boozle` with instance `audience` or `presenter`, so tiling window managers (i3, sway via XWayland, KWin rules) can match them.
- X11 reports a single scale factor for all monitors, so on mixed-DPI setups slides render at the global scale; use `--render-scale` or your desktop's scaling settings if that is too sharp or too soft.

### PowerPoint Speaker-Note Import

```bash
# Writes deck.boozle.toml by default:
boozle notes import deck.pptx

# Choose the sidecar path explicitly:
boozle notes import deck.pptx --out deck.boozle.toml

# --config is accepted as an alias for --out:
boozle notes import deck.pptx --config deck.boozle.toml

# Replace an existing generated sidecar:
boozle notes import deck.pptx --out deck.boozle.toml --force
```

The importer supports modern `.pptx` files. Convert old binary `.ppt` files to `.pptx` first. Slides without real speaker notes are omitted, and notes-page slide-number placeholders such as `notes = "17"` are ignored.

After import, Boozle only needs the PDF plus generated sidecar at presentation time.

### Performance and Memory

```bash
boozle slides.pdf --cache-mb 64
boozle slides.pdf --render-scale 0.75 --cache-mb 64
```

Boozle renders slides on a background worker and keeps the current slide plus its neighbours in a GPU cache, so turning a page is usually instant. Memory use is bounded by that cache, not by the length of the deck:

- The cache budget is sized automatically from the display — about four full-screen slides, between 32 MB and 192 MB (more only on 5K+ displays, where three slides need more). It counts the GPU memory slides really occupy.
- `--cache-mb <N>` replaces the automatic budget with a hard cap. Prefetching adapts so prefetched slides are not evicted before they are shown.
- `--render-scale <F>` rasterises pages at a fraction of native pixels (`0.5..1.0`). Memory per slide falls with the square of the factor (`0.75` uses about 56%), at the cost of some sharpness. It is the strongest lever on 4K and Retina displays.
- Presenter view runs as a second process with its own renderer and cache (sized for its smaller panes), so expect it to add to the total.
- `--debug-stats` prints GPU and heap usage, cache occupancy, and render timings to stderr every two seconds, for both windows.

Boozle keeps two caches in your user cache directory (`~/Library/Caches/boozle` on macOS, `~/.cache/boozle` on Linux, `%LocalAppData%\boozle` on Windows): the compiled PDF engine, which makes every launch after the first start in a fraction of a second, and overview thumbnails, which are pruned automatically after 30 days or beyond 200 MB. Set `BOOZLE_CACHE_DIR` to use a different location; deleting the directory is always safe.

## Command Reference

### `boozle <file.pdf>`

```text
boozle <file.pdf> [flags]
```

| Flag | Default | Description |
|---|---:|---|
| `-a, --auto <duration>` | `0` / disabled | Advance every duration, e.g. `30s`, `1m30s`; `0` disables. |
| `-l, --loop` | `false` | Loop back to first page after last. |
| `-s, --start <N>` | `1` | Start at page N, 1-indexed. |
| `-m, --monitor <N\|name>` | `0` | Audience monitor: index (`0` is primary) or name, see `--list-monitors`. |
| `--pages <range>` | all pages | Restrict to a page range, e.g. `3-7,10`. |
| `--bg <hex>` | `#000000` | Background color hex: `#RGB`, `#RRGGBB`, or `#RRGGBBAA`. |
| `--no-fullscreen` | `false` | Windowed mode for debugging or one-monitor presenter testing. |
| `--config <path>` | auto | Explicit sidecar config path, `.boozle.toml` or `.pdfpc`. |
| `--progress` | `false` | Show page-position and auto-advance progress overlay. |
| `--autoquit` | `false` | Quit after the last page instead of stopping. |
| `--transition <style>` | `slide` | Slide transition style: `slide`, `fade`, or `none`. |
| `-P, --presenter-monitor <N\|name>` | disabled | Presenter view monitor: index or name; `-1` or empty disables it. |
| `-M, --list-monitors` | `false` | List available monitors and exit. |
| `--cache-mb <N>` | `0` / auto | GPU page cache cap in MB; `0` auto-sizes to display. |
| `--render-scale <F>` | `0` / native | Render at fraction of native pixels (`0.5..1.0`); `0` keeps native. |
| `--debug-stats` | `false` | Print memory, render and monitor-placement diagnostics to stderr. |
| `-h, --help` | | Show help. |
| `-v, --version` | | Show version. |

### `boozle notes import <file.pptx>`

```text
boozle notes import <file.pptx> [flags]
```

| Flag | Default | Description |
|---|---:|---|
| `--out <path>` | unset | Output TOML sidecar path; if neither output flag is set, writes `<file>.boozle.toml`. |
| `--config <path>` | unset | Output TOML sidecar path; alias for `--out`. |
| `--force` | `false` | Overwrite an existing TOML sidecar. |
| `-h, --help` | | Show help. |

## Keyboard Controls

The same keys work from the audience display and from presenter view when that window has focus. The mouse cursor hides itself after three seconds without movement.

| Key | Action |
|---|---|
| `Right`, `PgDn`, `Space`, scroll down | Next page. |
| `Left`, `PgUp`, scroll up | Previous page. |
| `Backspace` | Previous page, or delete a digit while typing a page number. |
| `Home` / `End` | First / last page. |
| Digits then `Enter` | Jump to a page number, e.g. `1`, `2`, `Enter` jumps to page 12 (top-row or numpad digits). |
| `l` | Return to the previously viewed page (after any jump, including `Home`, `End`, and page numbers). |
| `p` | Pause / resume auto-advance. |
| `b` | Toggle black-out screen. |
| `w` | Toggle white-out screen. |
| `f` | Toggle fullscreen (of the audience window). |
| `Tab` | Open the slide overview (not while blacked or whited out). |
| `q`, `Esc` | Quit. |

In the overview, the arrow keys move the selection (`Up` / `Down` by a whole row), `Enter` or a click jumps to the selected slide, `Tab` or `Esc` closes it without jumping, and `q` quits.

## Sidecar Configuration

Create `slides.boozle.toml` next to `slides.pdf`, or pass a path with `--config`. Boozle also auto-loads `slides.pdfpc` when no Boozle TOML sidecar exists.

Flags given on the command line always override sidecar values — even when they repeat a default, so `--monitor 0` or `-P -1` overrides a sidecar's `monitor` or `presenter_monitor`. Sidecars are a good place for deck-specific defaults:

```toml
auto       = "30s"
loop       = true
start      = 1
monitor    = 0
pages      = "1-5,8,10-12"
bg         = "#0a0a0a"
progress   = true
autoquit   = false
transition = "fade"

# presenter_monitor = "HDMI-1"   # index or name
# cache_mb     = 64
# render_scale = 0.75

[[page]]
n    = 3
auto = "1m"
notes = """
Mention the rollout date.
Pause for questions before moving on.
"""

[[page]]
n    = 8
auto = "5s"
```

Supported top-level TOML keys are `auto`, `loop`, `start`, `monitor`, `pages`, `bg`, `progress`, `autoquit`, `transition`, `presenter_monitor`, `cache_mb`, and `render_scale`. `monitor` and `presenter_monitor` take an index or a monitor name.

Per-page entries use `[[page]]` with:

| Field | Description |
|---|---|
| `n` | 1-indexed page number. |
| `auto` | Optional per-page auto-advance duration. |
| `notes` | Optional speaker notes shown in presenter view. |

A complete annotated example lives at [examples/sample.boozle.toml](examples/sample.boozle.toml).

### pdfpc Sidecars

`.pdfpc` sidecars are JSON files created by PDF Presenter Console. Boozle imports their page order, skips entries marked `hidden`, preserves duplicate page entries used for overlays, and shows plain-text `note` values in presenter view.

pdfpc-specific features such as movies, drawing state, timers, transition metadata, and Markdown formatting of notes are ignored; notes are shown as plain text.

## Examples

```bash
# Basic manual playback:
boozle deck.pdf

# Timed playback with progress:
boozle deck.pdf --auto 30s --progress

# Lobby kiosk:
boozle deck.pdf --auto 20s --loop --progress

# Play once and close:
boozle deck.pdf --auto 30s --autoquit

# Present only selected pages:
boozle deck.pdf --pages 1-5,8,10-12

# Smooth fade transition:
boozle deck.pdf --transition fade

# Audience display on monitor 1, presenter view on monitor 0:
boozle deck.pdf --monitor 1 --presenter-monitor 0

# One-monitor presenter test:
boozle deck.pdf --no-fullscreen --monitor 0 --presenter-monitor 0

# Large deck on a high-DPI display:
boozle deck.pdf --render-scale 0.75 --cache-mb 64

# Extract PowerPoint speaker notes, then present with PDF plus sidecar:
boozle notes import deck.pptx --out deck.boozle.toml
boozle deck.pdf --presenter-monitor 0
```

## Build From Source

Every release ships pre-built binaries, but you can build locally:

```bash
git clone https://github.com/gethash/boozle.git
cd boozle
go build -o boozle ./cmd/boozle
./boozle --no-fullscreen examples/sample.pdf
```

`examples/sample.pdf` is a small generated deck with a matching `examples/sample.boozle.toml` sidecar.

Requirements: Go 1.26.2+ and a working CGO toolchain. Ebitengine links system OpenGL on Linux/Windows and Cocoa/Metal on macOS.

On Linux, install the native windowing dependencies:

```bash
sudo apt-get install libgl1-mesa-dev libxcursor-dev libxi-dev libxinerama-dev \
                     libxrandr-dev libxxf86vm-dev libxkbcommon-dev
```

Run tests:

```bash
go test ./...

# Optionally also render a real-world PDF of your choice:
BOOZLE_TEST_PDF=/path/to/any.pdf go test -count=1 ./internal/pdf/...
```

The render path is covered by small PDFs generated during the tests, so no fixture is needed.

On headless Linux CI, run tests that import Ebitengine under Xvfb:

```bash
xvfb-run -a go test -race -count=1 ./...
```

## How It Works

- **Rendering:** [PDFium](https://pdfium.googlesource.com/pdfium/) compiled to WebAssembly and run inside [`wazero`](https://github.com/tetratelabs/wazero), so Boozle does not ship a native PDFium `.dylib`, `.so`, or `.dll`. The compiled module is cached on disk, so only the first launch pays for compilation.
- **Render scheduling:** a single background worker per window renders by priority — the slide on screen first, then the presenter's next slide and the overview, then prefetch, then overview thumbnails. Navigation never waits for a render: until the new slide is ready, the previous one (or a scaled render of the new one) stays up.
- **Caching:** an LRU of GPU images keyed by page and pixel size keeps the current and nearby slides; its budget counts real GPU memory. Overview thumbnails have their own small cache and are also stored on disk.
- **Windowing and input:** [Ebitengine](https://github.com/hajimehoshi/ebiten) handles windows, fullscreen, vsync, monitor selection, input, and HiDPI scale factors. Frames that would look identical are skipped, so a static slide costs almost no GPU time.
- **Presenter view:** a second Boozle process. The audience display is the source of truth: it sends the play list and notes once over a private local socket and afterwards only state changes; presenter-window key presses are forwarded back so either display can drive the deck.
- **Speaker notes:** Boozle reads `[[page]] notes` entries from TOML sidecars or `note` fields from `.pdfpc` sidecars. The `.pptx` importer writes normal TOML notes entries.
- **Sidecars:** [BurntSushi/toml](https://github.com/BurntSushi/toml) parses Boozle TOML config, the standard library parses pdfpc JSON, and [cobra](https://github.com/spf13/cobra)/[pflag](https://github.com/spf13/pflag) provide the CLI flag layer.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE) for third-party attributions.
