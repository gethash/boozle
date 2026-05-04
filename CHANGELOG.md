# Changelog

All notable changes to boozle are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Fixed

- **Linux multi-monitor fullscreen placement**: monitor selection is now re-applied after the window exists and before every fullscreen entry, including the `f` key toggle. This keeps `--monitor` and `--presenter-monitor` tied to the selected displays instead of letting the window manager/fullscreen transition fall back to the primary screen.

## [1.1.1] — 2026-05-04

### Added

- **`--list-monitors` / `-M`**: prints the index, name, and DPI scale of every connected display, then exits. Use this to discover which number to pass to `--monitor` and `--presenter-monitor` when you're not sure how your OS has ordered the screens.

### Fixed

- **Multi-monitor placement**: `--monitor 0 --presenter-monitor 1` (and any other combination) now reliably opens each window on the intended display. Previously, monitor 0 was a no-op that delegated placement to the OS — in setups where the OS default was not the primary, both windows landed on the same screen.

## [1.1.0] — 2026-05-02

### Added

- **Presenter view** (`--presenter-monitor`, `-P`): optional second fullscreen window for speaker use, showing the current slide, next-slide preview, slide counter, wall clock, elapsed session timer, and auto-advance timer. It can also be enabled from the TOML sidecar with `presenter_monitor = 1`; the presenter and audience displays must use different monitors.
- **Slide transitions** (`--transition`): animated page changes with `slide` (lateral push, the default) or `fade` (cross-dissolve). Pass `--transition none` to restore the instant-cut behaviour. The style can also be set in the TOML sidecar with `transition = "fade"`. Non-directional jumps (Home, End, digit+Enter, `l`) always use a fade regardless of the configured style.

### Fixed

- Progress bar: removed a stray 1 px white line that appeared above the bar on certain slide backgrounds.
- Presenter view: keyboard and mouse-wheel navigation now forward to the main presentation window, so the usual key bindings work when either screen has focus.
- Slide overview: selection, "you are here", and hover borders now hug the actual slide edges instead of the full cell box, so frames are correctly sized for non-square slide aspect ratios.

### Changed

- Presenter view: replaced the raw split-pane layout with framed current/next slide panels plus a larger clock, slide counter, and timer area.
- Presenter view: progress now uses the same segmented brand-rainbow treatment as the slide progress overlay.
- Slide overview: preview and thumbnail grid now use the same framed dark panel style as presenter mode.
- Slide overview: dark scrim opacity increased from 78 % to 90 % for better thumbnail contrast.

## [1.0.0] — 2026-04-29

First stable release.

### Added

- Fullscreen PDF playback via PDFium compiled to WebAssembly (CGO-free renderer, pure-Go `wazero` runtime).
- Auto-advance with `--auto <duration>` and per-page duration overrides in TOML sidecar.
- Manual navigation: `→`/`←`, `PgDn`/`PgUp`, `Space`, `Backspace`, `Home`/`End`, numeric jump (_digits_ + `Enter`).
- **Slide overview** (`Tab`): split-screen thumbnail grid with animated enter/exit; navigate with arrow keys or click to jump to any slide.
- **Progress overlay** (`--progress`): a segmented rainbow bar at the bottom of the screen — one segment per slide, completed slides glow in gradient colour, the current segment fills as the auto-advance timer counts down.
- **Page counter**: `"3 / 24"` position display in the bottom-right corner when `--progress` is active.
- **`--autoquit`**: quit the window automatically after the last page advances instead of stopping there. Useful for unattended kiosks.
- `l` key: jump back to the previously viewed page (like Impressive's "return to last" shortcut).
- `f` key: toggle fullscreen on/off at runtime.
- `p` key: pause / resume auto-advance.
- `b` / `w` keys: black-out / white-out screen toggles.
- Mouse wheel: scroll down = next page, scroll up = previous page.
- Auto-hide cursor: mouse cursor hides after ~3 s of inactivity and reappears on any movement.
- Loop after last page (`--loop`).
- Start at a specific page (`--start N`).
- Restrict playback to a page range (`--pages 3-7,10`).
- Multi-monitor support (`--monitor N`).
- HiDPI-correct rendering — re-rasterises at native pixel resolution per monitor.
- LRU page cache with background prefetch goroutine.
- TOML sidecar (`<file>.boozle.toml`) for per-deck defaults and per-page overrides; `progress` and `autoquit` can also be set in the sidecar — CLI flags take precedence.
- Background color flag (`--bg <hex>`).
- Windowed mode for development (`--no-fullscreen`).
- Single-binary distribution: `install.sh` (macOS/Linux) and `install.ps1` (Windows) with SHA-256 verification.
- GitHub Actions CI (ubuntu, macos, windows) and release pipeline producing native binaries for macOS arm64/amd64, Linux amd64, Windows amd64.
