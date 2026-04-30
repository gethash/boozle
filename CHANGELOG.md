# Changelog

All notable changes to boozle are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

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
