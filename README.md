# LLM Provider Checker

Desktop GUI tool (Windows and macOS) for verifying LLM provider availability and capabilities.

Fill in Base URL, API Key, and Model, pick a provider type and the capabilities to probe, then click **Run Check**. Results, logs, and reloadable history appear in tabs.

中文：一个 Windows / macOS 桌面工具，用于检测不同 LLM Provider 的可用性与能力（基础对话、流式、视觉、工具调用、JSON、Token 上限、系统提示等）。

## Features

- OpenAI Chat Completions, OpenAI Responses API, Anthropic Messages, Google Gemini
- Capability matrix with auto-disabled unsupported checks
- List models from provider `/models` endpoints
- Reasoning effort / mode for OpenAI-style APIs
- CSV export of results
- History (config reload; API keys encrypted with Windows DPAPI / a macOS Keychain key)
- Pure-Go Windows GUI via [`lxn/walk`](https://github.com/lxn/walk) (no CGO)
- Native macOS GUI via AppKit (cgo, no third-party dependencies), with light/dark mode and a universal app bundle

## Requirements

- Go 1.22+ to build
- Windows: runs the `.exe`; Linux/macOS can cross-compile it
- macOS 13+: runs the app; building it requires a Mac with the Xcode Command Line Tools (`xcode-select --install`) because the AppKit GUI uses cgo

## Build (Windows)

```bash
./build.sh
# or
make build
```

Manual:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -trimpath \
  -ldflags "-s -w -H windowsgui" \
  -o provider-checker.exe \
  .
```

Output: `provider-checker.exe` (embeds Common Controls 6 + DPI awareness via `resource_windows.syso`).

## Build (macOS)

```bash
./build_macos.sh
# or
make build-macos      # make run-macos builds and opens the app
```

Output: `dist/Provider Checker.app` (universal arm64 + x86_64, ad-hoc signed,
minimum macOS 13) and `dist/provider-checker-macos.zip`. Set `ARCHS=arm64` to
build one architecture, or `MACOSX_DEPLOYMENT_TARGET` / `VERSION` to override
the minimum OS and bundle version. For a quick local run, `go build -o
provider-checker . && ./provider-checker` also works.

The app is not notarized. If a downloaded copy is blocked by Gatekeeper,
right-click it and choose **Open**, or run
`xattr -dr com.apple.quarantine "Provider Checker.app"`.

The macOS window mirrors the Windows layout: the same form, capability
checkboxes, Results / Log / History tabs, CSV export (save panel) and
double-click-to-load history. **Return** runs a check and **⌘.** stops it;
the standard Edit menu provides copy/paste in all fields.

## Usage

1. **Type** — provider API style  
2. **Base URL** — leave empty for official defaults; proxy hosts usually do not need `/v1` (auto-appended for OpenAI/Anthropic)  
3. **API Key / Auth Token** — password field; Anthropic supports both `ANTHROPIC_API_KEY` (`x-api-key`) and `ANTHROPIC_AUTH_TOKEN` (`Authorization: Bearer`) semantics  
4. **Model** — required; or use **List models**  
5. **Prompt / Timeout / Reasoning** — optional  
6. Tick capabilities and click **Run Check**  
7. Use **Stop**, **Export CSV**, **Clear display**, and the **History** tab as needed

**Clear display** clears the visible results, log and summary strip. Run records
and persisted history counts retain every result, including cancellation skips,
and the configuration captured when the run started. An in-progress run still
shows its full summary on completion. CSV exports the currently visible table.
Editing the form or loading history during a run configures the next run.
Model listings are cancelled and discarded when connection settings change,
history is loaded, a run starts, or the window closes; edits to the model name
are preserved when a current listing arrives. Reasoning Mode is enabled only
for the Responses API.

For every completed probe, the result table, log, and CSV also show the raw
**Upstream Model / 上游响应模型** declared by the provider response. This is
useful for detecting a proxy that routes a requested alias to a different
concrete model. It is intentionally left blank when the provider does not
declare a model; the app never substitutes the requested model name.

History is stored under the user config dir: `provider-checker/history.json` (max 200 entries; `%AppData%` on Windows, `~/Library/Application Support` on macOS). API keys are never written in plaintext: they are DPAPI-protected on Windows, and on macOS sealed with AES-256-GCM using a random key kept in your login Keychain (item "Provider Checker history key").

## Capability matrix

| Key | Label | openai-chat | openai-response | anthropic | gemini |
|---|---|:---:|:---:|:---:|:---:|
| basic | Basic chat | ✓ | ✓ | ✓ | ✓ |
| stream | Streaming | ✓ | ✓ | ✓ | ✓ |
| vision | Vision | ✓ | ✓ | ✓ | ✓ |
| tools | Tool calling | ✓ | ✓ | ✓ | ✓ |
| json | JSON mode | ✓ | ✓ | ✗ | ✓ |
| max_tokens | Max tokens | ✓ | ✗ | ✓ | ✗ |
| system | System prompt | ✓ | ✗ | ✓ | ✓ |

Vision probes use an embedded 1×1 PNG (data URI / base64), not an external image host.

Reasoning effort is sent consistently for every supported OpenAI probe; mode is
only sent to Responses. Empty settings are omitted (the effective default is
decided by the model/provider, not fixed to medium), and Anthropic/Gemini do
not receive OpenAI reasoning fields. When an explicit effort other than `none`
is selected, Chat Completions probes omit their implicit `temperature=0` so the
probe itself cannot be rejected for combining reasoning controls with sampling
parameters; empty and `none` keep the legacy request shape. Support for
particular values depends on the upstream model; rejected parameters retain
the provider's HTTP error.

System probes require `PINEAPPLE` after trimming surrounding Unicode whitespace.
Max Tokens probes require nonempty output and reported output usage within the
requested budget of 5; normal early stopping is allowed. Missing usage fails
with an explicit "cannot verify" explanation, rather than being treated as 0.
Responses JSON probes validate the requested schema: an object containing only
the required boolean `ok`. These checks use returned evidence, not a local token
estimate or a guarantee about a provider's internal implementation.

## Tests

```bash
go test ./...
# or
make test
```

## Layout

```
provider-checker/
├── checker/                 # Provider probes (HTTP, no GUI)
│   ├── checker.go           # Interfaces, registry, reasoning helpers
│   ├── http.go              # Shared HTTP/result helpers + vision blob
│   ├── openai_chat.go
│   ├── openai_response.go
│   ├── anthropic.go
│   ├── gemini.go
│   └── models.go            # ListModels implementations
├── runner/                  # Sequential run + cancel + events
├── history/                 # JSON history store (cross-platform)
├── ui_common.go             # Shared front-end state, labels, cell text, CSV
├── run_state.go / store_init.go  # Run records, model listing, history store init
├── main_windows.go          # Windows GUI
├── rows_windows.go          # Table models / colors
├── crypto_windows.go        # DPAPI secret helpers
├── macapp_darwin.go         # macOS controller (pure Go, tested with a fake view)
├── cocoa_darwin.go          # macOS AppKit view (Objective-C via cgo)
├── cocoa_export_darwin.go   # AppKit -> Go callbacks
├── crypto_darwin.go         # Keychain-held key for history API keys
├── secret_seal.go           # AES-GCM sealing used by the macOS build
├── main_other.go            # Entry stub for other platforms / macOS without cgo
├── provider.manifest
├── resource_windows.syso
├── build.sh                 # Windows build
├── build_macos.sh           # macOS .app build
├── Makefile
├── LICENSE
└── README.md
```

## Security notes

- API keys are only sent to the Base URL you configure.
- Anthropic requests start with the official `x-api-key` scheme and retry once with Bearer auth only when the same endpoint returns `401`; this keeps official API keys and `ANTHROPIC_AUTH_TOKEN` gateways compatible.
- History encrypts keys with DPAPI (current Windows user) or with a key in the macOS login Keychain; ciphertext is not portable across machines/users.
- On macOS, rebuilding the (ad-hoc signed) app changes its code signature, so the first history save/load after a rebuild may ask for Keychain access; choose **Always Allow**. Denying it only skips storing/restoring the API key.
- Prefer HTTPS endpoints and treat export/logs as potentially sensitive (requested and upstream model names, response snippets).

## License

MIT — see [LICENSE](LICENSE).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
