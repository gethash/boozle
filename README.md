# boozle

[![CI](https://github.com/gethash/boozle/actions/workflows/ci.yml/badge.svg)](https://github.com/gethash/boozle/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/gethash/boozle)](https://github.com/gethash/boozle/releases/latest)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

A modern, cross-platform PDF presenter with an auto-advance timer. Spiritual successor to the venerable but aging [Impressive](https://sourceforge.net/projects/impressive/), rebuilt as a single static binary you can `curl` from a GitHub release and just run.

## What it does

You have a PDF deck. You want to play it full-screen and have it auto-advance every N seconds — for a booth, a kiosk, a lobby screen, or just to step through slides hands-free. That's the whole feature set.

- **One file.** No installer, no runtime, no Python. Download a binary, `chmod +x`, run.
- **Auto-advance with per-slide overrides.** Default 30 s, page 7 needs a minute, page 3 should fly by — say so in a TOML sidecar.
- **Slide transitions.** Pages animate in with a lateral push or cross-fade — choose `slide`, `fade`, or `none` via `--transition`.
- **Progress overlay.** A segmented brand-rainbow page-position bar and auto-advance countdown keep you oriented at a glance.
- **Presenter view.** Use a second monitor for a speaker display with current slide, next slide, wall clock, elapsed time, slide counter, and matching progress.
- **Slide overview.** Press `Tab` for a framed thumbnail grid with a large selected-slide preview. Navigate with arrow keys or click to jump anywhere.
- **Resolution-aware.** Pages re-rasterise at native pixel resolution on every monitor — crisp on Retina, 4K, mixed-DPI multi-monitor.
- **Permissively licensed.** Apache-2.0. Uses Chromium's PDFium (Apache-2.0) via a WebAssembly runtime, so the rendering engine is portable and never needs a system library.
- **Built in CI.** GitHub Actions produces native binaries for macOS (Apple Silicon + Intel), Linux x86-64, and Windows x86-64.

## Install

### Homebrew (macOS and Linux)

```bash
brew tap gethash/tap
brew install boozle
```

Compiles from source locally — no Gatekeeper quarantine workaround needed.

### macOS / Linux

```bash
curl -fsSL https://github.com/gethash/boozle/releases/latest/download/install.sh | sh
```

The script detects your OS/arch, fetches the right archive, verifies its SHA-256 against the release's `checksums.txt`, and installs to `/usr/local/bin` (falling back to `~/.local/bin` if you can't write there).

Override defaults with env vars:

```bash
BOOZLE_VERSION=v1.1.0 BOOZLE_INSTALL_DIR=~/bin \
  curl -fsSL https://github.com/gethash/boozle/releases/latest/download/install.sh | sh
```

#### macOS first-run note

boozle is currently distributed unsigned. macOS Gatekeeper will block it on first launch. After install, run once:

```bash
xattr -d com.apple.quarantine /usr/local/bin/boozle
```

(Code signing & notarisation are planned for a future release.)

### Windows

```powershell
iwr -useb https://github.com/gethash/boozle/releases/latest/download/install.ps1 | iex
```

The script installs to `%LOCALAPPDATA%\boozle` and prepends that directory to your user `PATH`. Open a new terminal afterwards.

Windows SmartScreen will warn on first run because the binary is unsigned — click **More info → Run anyway**.

### Manual

Grab the right archive for your platform from [Releases](https://github.com/gethash/boozle/releases), extract, and drop `boozle` (or `boozle.exe`) somewhere on your `PATH`.

## Usage

```bash
boozle slides.pdf --auto 30s
```

By default boozle opens fullscreen on your primary monitor and waits for you to advance manually. Add `--auto <duration>` to walk forward on its own, `--loop` to start over after the last page.

### Flags

| Flag | Default | Description |
|---|---|---|
| `-a, --auto <duration>` | _disabled_ | Advance every duration. Accepts any Go duration: `30s`, `1m30s`, `2m`, `750ms`. |
| `-l, --loop` | `false` | Loop back to the first page after the last. |
| `-s, --start <N>` | `1` | Start at page N (1-indexed). |
| `-m, --monitor <N>` | `0` | Open on the N-th monitor (0 = primary). |
| `--pages <range>` | _all_ | Restrict to pages, e.g. `3-7,10`. |
| `--bg <hex>` | `#000000` | Background fill (`#RGB`, `#RRGGBB`, or `#RRGGBBAA`). |
| `--progress` | `false` | Show page-position bar and auto-advance countdown overlay. |
| `--autoquit` | `false` | Quit after the last page instead of stopping. |
| `--transition <style>` | `slide` | Page transition: `slide` (lateral push), `fade` (cross-dissolve), `none` (instant cut). |
| `-P, --presenter-monitor <N>` | `-1` | Open presenter view on monitor N. Use a different monitor than `--monitor`. |
| `--no-fullscreen` | `false` | Run windowed (debugging / dev). |
| `--config <path>` | _auto_ | Use this TOML sidecar instead of the auto-detected one. |
| `-h, --help` | | Show help. |
| `-v, --version` | | Show version. |

### Keybindings

| Key | Action |
|---|---|
| `→` `PgDn` `Space` `Scroll down` | next page |
| `←` `PgUp` `Scroll up` | previous page |
| `Backspace` | previous page (or delete a digit if you're typing a page number) |
| `Home` / `End` | first / last page |
| _digits_ + `Enter` | jump to page (e.g. `1`, `2`, Enter → page 12) |
| `l` | return to the previously viewed page |
| `p` | pause / resume auto-advance |
| `b` | black-out the screen (toggle) |
| `w` | white-out the screen (toggle) |
| `f` | toggle fullscreen |
| `Tab` | open slide overview (arrow keys or click to select, `Enter` to jump) |
| `q` `Esc` | quit |

When presenter mode is enabled, the same navigation keys also work when the presenter window has focus.

### Sidecar configuration (per-PDF)

Create `slides.boozle.toml` next to `slides.pdf`. Command-line flags always win over sidecar values, so the sidecar is a good place for per-deck defaults:

```toml
auto       = "30s"
loop       = true
pages      = "1-5,8,10-12"
bg         = "#0a0a0a"
progress   = true
autoquit   = false
transition = "fade"
# presenter_monitor = 0

[[page]]
n    = 3
auto = "1m"      # this slide needs longer

[[page]]
n    = 8
auto = "5s"      # this one just blips past
```

A complete annotated example lives at [examples/sample.boozle.toml](examples/sample.boozle.toml).

### Examples

```bash
# Lobby kiosk, looping every 20 seconds with a progress bar:
boozle deck.pdf --auto 20s --loop --progress

# Play once and close the window when done:
boozle deck.pdf --auto 30s --autoquit

# Open the last quarter of a long deck on a second monitor:
boozle deck.pdf --pages 80-100 --monitor 1

# Use the sidecar for everything, just press play:
boozle deck.pdf

# Smooth fade transition instead of the default push:
boozle deck.pdf --transition fade

# Audience display on monitor 1, presenter view on monitor 0:
boozle deck.pdf --monitor 1 --presenter-monitor 0
```

## Build from source

You shouldn't need to — every release ships pre-built binaries — but if you want to:

```bash
git clone https://github.com/gethash/boozle.git
cd boozle
go build -o boozle ./cmd/boozle
./boozle --no-fullscreen examples/sample.pdf
```

Requirements: Go 1.22+ and a working CGO toolchain (Ebiten links system OpenGL on Linux/Windows and Cocoa/Metal on macOS). On Linux, install:

```bash
sudo apt-get install libgl1-mesa-dev libxcursor-dev libxi-dev libxinerama-dev \
                     libxrandr-dev libxxf86vm-dev libxkbcommon-dev
```

Run the test suite:

```bash
go test ./...
# Render-path coverage requires a real PDF:
BOOZLE_TEST_PDF=/path/to/any.pdf go test -count=1 ./internal/pdf/...
```

## How it works

- **Rendering:** [PDFium](https://pdfium.googlesource.com/pdfium/) (Chromium's PDF engine) compiled to WebAssembly, run inside [`wazero`](https://github.com/tetratelabs/wazero) — a pure-Go WASM runtime. No native PDFium library, no `.dylib`/`.so`/`.dll` to ship alongside the binary.
- **Windowing & input:** [Ebitengine](https://github.com/hajimehoshi/ebiten) handles the fullscreen window, vsync, monitor selection, and HiDPI scale factors.
- **Presenter view:** the main window stays the source of truth and streams presenter state over a local Unix socket; presenter-window key presses are forwarded back so either display can drive the deck.
- **Caching:** an LRU keyed by `(page, pixel-width, pixel-height)` keeps the current and a few neighbour pages rasterised; a background goroutine pre-fetches the next page so auto-advance never stalls on PDFium.
- **Sidecar:** [BurntSushi/toml](https://github.com/BurntSushi/toml) parses the per-PDF config; flags override sidecar values via [cobra](https://github.com/spf13/cobra)/[pflag](https://github.com/spf13/pflag).

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE) for third-party attributions.
