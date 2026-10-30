# Contributing

Thanks for helping improve LLM Provider Checker.

## Development setup

- Go 1.22+ (module currently targets the toolchain in `go.mod`)
- Windows GUI: `github.com/lxn/walk`; Linux/macOS can still build the Windows binary via `./build.sh`
- macOS GUI: AppKit through cgo; needs a Mac with the Xcode Command Line Tools

```bash
go test ./...
./build.sh         # produces provider-checker.exe
./build_macos.sh   # on macOS: produces dist/Provider Checker.app
```

## Project layout

| Path | Role |
|---|---|
| `checker/` | Provider clients and capability probes (no GUI) |
| `runner/` | Sequential feature runner + events |
| `history/` | Cross-platform JSON history store |
| `ui_common.go` | Front-end logic shared by both GUIs (state, labels, log lines, cell text, CSV) |
| `main_windows.go` / `rows_windows.go` / `crypto_windows.go` | Windows GUI |
| `macapp_darwin.go` | macOS controller, driven through the `macView` interface |
| `cocoa_darwin.go` / `cocoa_export_darwin.go` / `crypto_darwin.go` | macOS AppKit view, callbacks and Keychain |
| `main_other.go` | Stub entrypoint for other platforms (and macOS without cgo) |

## Coding guidelines

1. Keep `checker` free of GUI imports so it stays unit-testable on any OS.
2. Prefer small, provider-specific request shapes over shared "one size fits all" bodies.
3. Do not log or persist API keys in plaintext. History uses DPAPI on Windows and a Keychain-held AES-GCM key on macOS.
4. Keep user-visible behavior shared: put new labels/log/summary logic in `ui_common.go` and wire it into both GUIs. macOS controller changes should come with `macapp_darwin_test.go` coverage (fake view, no window needed).
5. Add or extend unit tests for pure logic (`normalizeV1`, `geminiURL`, runner cancel, history store).
6. Avoid committing binaries (`*.exe`), local history, or secrets.

## Pull requests

- Describe what changed and why.
- Include `go test ./...` output (or note if a package is Windows-only).
- For GUI changes, attach a short screenshot or describe the interaction when possible.

## Feature requests

New providers should implement `checker.Checker` (and optionally `ModelLister`), register via `init()`, and document support in the README capability matrix.

## Streaming probes

Streaming PASS means that at least one complete, valid provider SSE event was
received. It does not assert that a full answer finished. Clean EOF after valid
progress is accepted without `[DONE]`; a later transport interruption or
unfinished frame is accepted as `partial`, with the reason shown in the result.
Explicit error events, malformed JSON in complete frames, and budget overruns
always fail, even after valid progress. `[DONE]`, heartbeats, and arbitrary JSON
alone do not prove streaming support. Models come from validated events (or
explicit error responses); a terminal Responses event's model takes precedence.

The small probe uses a 64 KiB physical SSE frame budget, a 1 MiB total body
budget, and a 120 Unicode character snippet. These are probe limits, not general
provider limits. Keep tests offline with local `httptest` fixtures; coverage in
`checker/http_test.go` and `checker/upstream_model_test.go` includes framing,
read boundaries, partial results, errors, budgets, and all four provider paths.
