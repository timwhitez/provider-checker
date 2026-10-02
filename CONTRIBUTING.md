# Contributing

Thanks for helping improve LLM Provider Checker.

## Development setup

- Go 1.22+ (module currently targets the toolchain in `go.mod`)
- Windows is required to run the GUI (`github.com/lxn/walk`)
- Linux/macOS can still build the Windows binary via `./build.sh`

```bash
go test ./...
./build.sh   # produces provider-checker.exe
```

## Project layout

| Path | Role |
|---|---|
| `checker/` | Provider clients and capability probes (no GUI) |
| `runner/` | Sequential feature runner + events |
| `history/` | Cross-platform JSON history store |
| `main_windows.go` / `rows_windows.go` / `crypto_windows.go` | Windows GUI |
| `main_other.go` | Non-Windows stub entrypoint |

## Coding guidelines

1. Keep `checker` free of GUI imports so it stays unit-testable on any OS.
2. Prefer small, provider-specific request shapes over shared "one size fits all" bodies.
3. Do not log or persist API keys in plaintext. History uses DPAPI on Windows.
4. Add or extend unit tests for pure logic (`normalizeV1`, `geminiURL`, runner cancel, history store).
5. Avoid committing binaries (`*.exe`), local history, or secrets.

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
