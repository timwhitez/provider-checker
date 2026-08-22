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
