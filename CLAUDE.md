# boozle

Modern, cross-platform fullscreen PDF presenter with auto-advance. Single-file binary, no runtime, no shared libraries. Spiritual successor to Impressive.

- Module: `github.com/gethash/boozle`
- License: Apache-2.0 (PDFium attribution in [NOTICE](NOTICE))
- Go: 1.26.2+

## Build & test

```bash
go build -o boozle ./cmd/boozle
go test ./...

# Render-path tests need a real PDF:
BOOZLE_TEST_PDF=/path/to/any.pdf go test -count=1 ./internal/pdf/...

# Local dev run (windowed):
./boozle --no-fullscreen examples/sample.pdf
```

## Architecture

- [cmd/boozle/main.go](cmd/boozle/main.go) — cobra entry point
- [internal/app/](internal/app/) — `ebiten.Game` loop, input, draw
- [internal/pdf/](internal/pdf/) — PDFium-WASM renderer, LRU cache, prefetch goroutine
- [internal/config/](internal/config/) — flag + TOML sidecar merge (`<file>.boozle.toml`)
- [internal/timer/](internal/timer/) — auto-advance scheduler (injectable clock)
- [internal/display/](internal/display/) — monitor selection via Ebiten

Rendering: PDFium compiled to WebAssembly, run inside `wazero` (pure-Go). No native PDFium dependency. Ebitengine handles the window, vsync, HiDPI.

## Non-obvious gotchas

- **CGO is required** for Ebiten on macOS/Linux/Windows — `CGO_ENABLED=0` will not build. Plan originally claimed CGO-free; that's only true for the PDF renderer (wazero), not the windowing layer.
- **Cross-compile from one runner does not work.** [.github/workflows/release.yml](.github/workflows/release.yml) uses a per-OS native-runner matrix (macos-latest for arm64, macos-13 for amd64, ubuntu-latest, windows-latest). Don't try to fold this back into a single `goreleaser` matrix.
- **Binary is ~25 MB** because PDFium WASM (~10–12 MB) is embedded via `//go:embed`.
- **`Layout()` returns pixel dims, not logical dims** — multiply by `Monitor().DeviceScaleFactor()` so PDFium rasterizes at the display's true pixel count. Re-rasterize when scale changes.
- **PDFium aspect-fits internally.** If you request 800×600 for a portrait page you'll get back e.g. 464×600. Tests assert "non-empty + within requested box," not exact dims.
- **Flag defaults vs sidecar overrides:** `config.Load` treats `StartPage == 1` as the default and lets the sidecar override; tests must pass `StartPage: 1`, not `0`.
- **Cache eviction must call `cleanup`** — PDFium-side memory leaks otherwise.
- **Memory tuning lives in two flags**: `--cache-mb` hard-caps the page cache (skips `autoBudget` in `onLayoutChanged`); `--render-scale F` (0.5..1.0) shrinks `Layout()`'s returned pixel dims so PDFium rasterises smaller bitmaps and the cache holds less. Both are forwarded to the presenter subprocess via spawn args. The cache budget is the only knob we directly own — PDFium's WASM heap and Metal atlas textures grow monotonically.

## Releases

Tag `v*` triggers [.github/workflows/release.yml](.github/workflows/release.yml). Produces tar.gz/zip per platform, `checksums.txt`, and a GitHub Release. [install.sh](install.sh) and [install.ps1](install.ps1) are shipped as release assets and SHA-256-verify the archive against `checksums.txt`.

macOS binaries are unsigned for v0/v1 — README documents the `xattr -d com.apple.quarantine` workaround. Code signing is on the v1.1 roadmap.
