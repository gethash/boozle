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
- **Monitor selection:** list displays with `--list-monitors`, then choose audience and presenter monitors by index.
- **HiDPI rendering:** rasterise pages at native monitor resolution for Retina, 4K, and mixed-DPI setups.
- **Memory tuning:** cap the GPU page cache with `--cache-mb` or render below native resolution with `--render-scale`.
- **Sidecars:** keep deck-specific defaults next to the PDF in `.boozle.toml`; Boozle also understands `.pdfpc` sidecars.
- **Single-binary distribution:** releases ship native binaries for macOS Apple Silicon, macOS Intel, Linux x86-64, and Windows x86-64.

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

Use `--auto <duration>` for timed advance. Add `--loop` for kiosk or lobby screens, or `--autoquit` when the deck should close after the final auto-advance.

### Presenter View

```bash
boozle --list-monitors
boozle slides.pdf --monitor 1 --presenter-monitor 0
```

The audience display is the main presentation window. Presenter view is a second window for the speaker. It shows current slide, next slide, notes, clock, elapsed time, slide counter, and auto-advance progress.

In full-screen mode, `--monitor` and `--presenter-monitor` must point at different displays. For local testing on one monitor, run both windows in windowed mode:

```bash
boozle slides.pdf --no-fullscreen --monitor 0 --presenter-monitor 0
```

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

### Display Setup

```bash
boozle --list-monitors
boozle slides.pdf --monitor 1
boozle slides.pdf --monitor 1 --presenter-monitor 0
```

`--list-monitors` prints the index, name, and DPI scale of every connected display. Monitor index `0` is the primary display.

### Performance Tuning

```bash
boozle slides.pdf --cache-mb 64
boozle slides.pdf --render-scale 0.75 --cache-mb 64
```

`--cache-mb` hard-caps the GPU page cache. `--render-scale` rasterises pages at a fraction of native pixels (`0.5..1.0`), trading some sharpness for lower memory use on large decks or high-DPI displays.

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
| `-m, --monitor <N>` | `0` | Monitor index for the audience display; `0` is primary. |
| `--pages <range>` | all pages | Restrict to a page range, e.g. `3-7,10`. |
| `--bg <hex>` | `#000000` | Background color hex: `#RGB`, `#RRGGBB`, or `#RRGGBBAA`. |
| `--no-fullscreen` | `false` | Windowed mode for debugging or one-monitor presenter testing. |
| `--config <path>` | auto | Explicit sidecar config path, `.boozle.toml` or `.pdfpc`. |
| `--progress` | `false` | Show page-position and auto-advance progress overlay. |
| `--autoquit` | `false` | Quit after the last page instead of stopping. |
| `--transition <style>` | unset / `slide` | Slide transition style: `slide`, `fade`, or `none`; unset uses `slide`. |
| `-P, --presenter-monitor <N>` | `-1` / disabled | Monitor index for presenter view. |
| `-M, --list-monitors` | `false` | List available monitors and exit. |
| `--cache-mb <N>` | `0` / auto | GPU page cache cap in MB; `0` auto-sizes to display. |
| `--render-scale <F>` | `0` / native | Render at fraction of native pixels (`0.5..1.0`); `0` keeps native. |
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

The same navigation keys work from the audience display and from presenter view when that window has focus.

| Key | Action |
|---|---|
| `Right`, `PgDn`, `Space`, scroll down | Next page. |
| `Left`, `PgUp`, scroll up | Previous page. |
| `Backspace` | Previous page, or delete a digit while typing a page number. |
| `Home` / `End` | First / last page. |
| Digits then `Enter` | Jump to a page number, e.g. `1`, `2`, `Enter` jumps to page 12. |
| `l` | Return to the previously viewed page. |
| `p` | Pause / resume auto-advance. |
| `b` | Toggle black-out screen. |
| `w` | Toggle white-out screen. |
| `f` | Toggle fullscreen. |
| `Tab` | Open slide overview; use arrow keys or click to select, `Enter` to jump. |
| `q`, `Esc` | Quit. |

## Sidecar Configuration

Create `slides.boozle.toml` next to `slides.pdf`, or pass a path with `--config`. Boozle also auto-loads `slides.pdfpc` when no Boozle TOML sidecar exists.

CLI flags override sidecar values. Sidecars are a good place for deck-specific defaults:

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

# presenter_monitor = 1
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

Supported top-level TOML keys are `auto`, `loop`, `start`, `monitor`, `pages`, `bg`, `progress`, `autoquit`, `transition`, `presenter_monitor`, `cache_mb`, and `render_scale`.

Per-page entries use `[[page]]` with:

| Field | Description |
|---|---|
| `n` | 1-indexed page number. |
| `auto` | Optional per-page auto-advance duration. |
| `notes` | Optional speaker notes shown in presenter view. |

A complete annotated example lives at [examples/sample.boozle.toml](examples/sample.boozle.toml).

### pdfpc Sidecars

`.pdfpc` sidecars are JSON files created by PDF Presenter Console. Boozle imports their page order, skips entries marked `hidden`, preserves duplicate page entries used for overlays, and shows plain-text `note` values in presenter view.

pdfpc-specific features such as movies, drawing state, timers, and transition metadata are ignored.

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

Requirements: Go 1.26.2+ and a working CGO toolchain. Ebiten links system OpenGL on Linux/Windows and Cocoa/Metal on macOS.

On Linux, install the native windowing dependencies:

```bash
sudo apt-get install libgl1-mesa-dev libxcursor-dev libxi-dev libxinerama-dev \
                     libxrandr-dev libxxf86vm-dev libxkbcommon-dev
```

Run tests:

```bash
go test ./...

# Render-path coverage requires a real PDF:
BOOZLE_TEST_PDF=/path/to/any.pdf go test -count=1 ./internal/pdf/...
```

On headless Linux CI, run tests that import Ebitengine under Xvfb:

```bash
xvfb-run -a go test -race -count=1 ./...
```

## How It Works

- **Rendering:** [PDFium](https://pdfium.googlesource.com/pdfium/) compiled to WebAssembly and run inside [`wazero`](https://github.com/tetratelabs/wazero), so Boozle does not ship a native PDFium `.dylib`, `.so`, or `.dll`.
- **Windowing and input:** [Ebitengine](https://github.com/hajimehoshi/ebiten) handles windows, fullscreen, vsync, monitor selection, input, and HiDPI scale factors.
- **Presenter view:** the audience display is the source of truth and streams presenter state over a local socket; presenter-window key presses are forwarded back so either display can drive the deck.
- **Speaker notes:** Boozle reads `[[page]] notes` entries from TOML sidecars or `note` fields from `.pdfpc` sidecars. The `.pptx` importer writes normal TOML notes entries.
- **Caching:** an LRU keyed by page and pixel dimensions keeps the current and nearby pages rasterised; background prefetch prepares likely next pages.
- **Sidecars:** [BurntSushi/toml](https://github.com/BurntSushi/toml) parses Boozle TOML config, the standard library parses pdfpc JSON, and [cobra](https://github.com/spf13/cobra)/[pflag](https://github.com/spf13/pflag) provide the CLI flag layer.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE) for third-party attributions.
