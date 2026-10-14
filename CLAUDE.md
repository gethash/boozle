# boozle

Modern, cross-platform fullscreen PDF presenter with auto-advance, presenter view and speaker notes. Single binary; the PDF engine is embedded. Spiritual successor to Impressive.

- Module: `github.com/gethash/boozle`
- License: Apache-2.0 (third-party attributions in [NOTICE](NOTICE))
- Go: 1.26.2+

## Build & test

```bash
go build -o boozle ./cmd/boozle
go test ./...                     # render tests use generated PDFs (internal/pdf/pdftest)
xvfb-run -a go test -race ./...   # headless Linux, as in CI

# Optional extra render check against a real-world PDF:
BOOZLE_TEST_PDF=/path/to/any.pdf go test -count=1 ./internal/pdf/...

# Local dev run (windowed; examples/sample.boozle.toml auto-loads):
./boozle --no-fullscreen examples/sample.pdf
./boozle --no-fullscreen -m 0 -P 0 --debug-stats examples/sample.pdf   # + presenter view

# Regenerate examples/sample.pdf after changing pdftest's sample pages:
go test ./internal/pdf/pdftest -update
```

CI also runs `gofmt -l`, `go mod tidy -diff`, `go vet` and staticcheck.

## Architecture

- [cmd/boozle/main.go](cmd/boozle/main.go) — cobra entry point: flags, `notes import` subcommand, and the hidden `--_presenter-socket` flag that turns the same binary into the presenter process.
- [internal/app/](internal/app/) — the two `ebiten.Game`s and everything they draw:
  - `app.go` audience window (`Game`): input, navigation (`goTo`), transitions, presenter spawning, state broadcast.
  - `presenter.go` presenter process (`PresenterGame`): current/next panes, clock, notes; forwards keys to the audience process.
  - `slides.go` `pipeline`: per-window slide cache + thumbnail store + render scheduler; `resolve` returns an exact render or a same-page stand-in.
  - `overview.go` the `Tab` grid, shared by both windows (the presenter process draws it when presenter view is on).
  - `window.go` fullscreen placement state machine; `redraw.go` frame-skipping; `stats.go` `--debug-stats`; `thumb_cache.go` thumbnail GPU/disk cache.
- [internal/pdf/](internal/pdf/) — `renderer.go` PDFium-WASM wrapper, `scheduler.go` prioritised render worker, `cache.go` LRU of GPU images with pinning, `pdftest/` tiny PDF writer for tests and the sample deck.
- [internal/ipc/](internal/ipc/) — audience↔presenter unix socket (newline-delimited JSON).
- [internal/config/](internal/config/) — flag + sidecar merge (`<file>.boozle.toml`, or `<file>.pdfpc`).
- [internal/display/](internal/display/) — monitor listing and resolution by index or name.
- [internal/pptxnotes/](internal/pptxnotes/) — `.pptx` speaker-note extraction for `boozle notes import`.
- [internal/timer/](internal/timer/) — auto-advance scheduler (injectable clock).
- [internal/cachedir/](internal/cachedir/) — `<user cache>/boozle/<sub>` (0700), overridable with `BOOZLE_CACHE_DIR`.

Rendering: PDFium compiled to WebAssembly (embedded by go-pdfium), run inside `wazero` (pure Go). Ebitengine handles windows, vsync, input and HiDPI.

## Non-obvious gotchas

- **CGO is required** for Ebitengine on macOS/Linux/Windows — `CGO_ENABLED=0` will not build. Only the PDF renderer is CGO-free.
- **Never render on the Ebiten goroutine.** All `Doc.RenderPage` calls go through the per-window `pdf.Scheduler` (urgent → visible → prefetch → background). The main loop only drains results and uploads them (`pipeline.drain`); misses show a stand-in or the previous slide. Transitions wait for the incoming slide.
- **`RenderPage` returns Go-owned pixels.** It copies out of WASM memory and destroys the PDFium bitmap under `Doc.mu` before returning. PDFium's allocator is not goroutine-safe and go-pdfium's image is a view that detaches when WASM memory grows — never hold or free WASM-side buffers outside the lock.
- **WASM paths:** the PDF's directory is mounted read-only at `/doc` and opened as `/doc/<base>`; go-pdfium rejects Windows drive-letter paths.
- **The compiled PDFium module is cached** in `<user cache>/boozle/wazero` (`Open` ~1 s cold → ~40 ms warm). Compiling under `-race` takes ~10 s, so tests share the user cache; set `BOOZLE_CACHE_DIR` only *after* the first `pdf.Open` in a test if you need hermetic thumbnails (see `newTestPipeline`).
- **GPU memory is counted at texture size.** Ebitengine rounds textures up to powers of two (1280×720 → 2048×1024), so `pdf.TextureBytes` is what the cache charges and what budgets use. Slides are *unmanaged* images (freed on `Deallocate`); thumbnails stay on atlases. The cache budget (auto or `--cache-mb`) is the main memory knob; `--render-scale` shrinks `Layout()`'s pixel dims. Both are forwarded to the presenter process.
- **Pin what is on screen.** `pinnedSlide` pins the displayed image (and the outgoing transition image) so eviction can't free it; `Cache.Put` on an existing key keeps the existing image. A window resize purges every unpinned render.
- **Transient memory:** finished-but-undrained renders are capped at 32 MB (`maxPendingBytes`), and `pipeline.drain` nudges a GC every 32 MB uploaded so Ebitengine's staging buffers (returned to its pool only after GC) get reused.
- **`Layout()` returns pixel dims, not logical dims** — `pixelBuffer` multiplies by `Monitor().DeviceScaleFactor()` (nil-safe) so PDFium rasterizes at the display's true pixel count.
- **PDFium aspect-fits internally.** Requesting 800×600 for a portrait page returns e.g. 464×600 (±1 px rounding). Tests assert "non-empty + within requested box", not exact dims; draw code scales by the image's real bounds.
- **Screen is not cleared between frames** (`SetScreenClearedEveryFrame(false)`). `Draw` returns early when the frame signature (`audienceFrame` / `presenterFrame`) is unchanged; anything new that affects drawing must be added to the signature (a forced redraw happens once per second regardless).
- **Fullscreen on a monitor (Linux):** Ebitengine picks the fullscreen monitor from a position-based guess cached ~60 ticks, and X11 moves are async. Never call `SetMonitor` + `SetFullscreen` in one tick — use `monitorPlacer` (wait → move → poll until reported → fullscreen → verify → retry once). Monitors are compared by identity (`display.Current`), not name (Windows names are all "Generic PnP Monitor").
- **Presenter view is a re-exec** of the binary with `--_presenter-socket`; it is spawned after the audience window is placed and gets the *resolved* monitor index. IPC: one `Hello` (play list, notes, start time, loop) per connection, then `PresenterState` only on change; the presenter extrapolates clock and progress. In presenter mode the overview (and its thumbnails) live only in the presenter process.
- **Flag precedence:** explicit flags (cobra `Changed`, passed as `config.Flags.Changed`) beat sidecar values, which beat flag defaults. Tests must set `Changed` for flags they expect to win.
- **Debug font** (`ebitenutil.DebugPrintAt`) covers U+0000–U+00FF only; `debugFontText` maps typographic characters to ASCII. Glyphs are 6 px wide.
- **Version string:** `main.version` defaults to the newest CHANGELOG release (Homebrew builds from source without ldflags); `TestDefaultVersionMatchesChangelog` enforces it — bump both together.

## Releases

Tag `v*` triggers [.github/workflows/release.yml](.github/workflows/release.yml): a native-runner matrix (darwin-arm64 on macos-latest, darwin-amd64 on macos-15-intel, linux-amd64 on ubuntu-latest, linux-arm64 on ubuntu-24.04-arm, windows-amd64 on windows-latest) builds, smoke-tests `--version`, packages tar.gz/zip, writes `checksums.txt`, creates the GitHub Release and bumps the Homebrew tap. Don't fold this into a single cross-compiling runner (CGO). [install.sh](install.sh) and [install.ps1](install.ps1) ship as release assets and SHA-256-verify the archive against `checksums.txt`.

macOS binaries are unsigned — README documents the `xattr -d com.apple.quarantine` workaround. Code signing is planned for a future release.
