# Go migration assessment (magpie-inspired)

Status: Phase 0 is implemented (`go/cmd/lanbus-scan` + `go/internal/scanner`). Phase 1.5 has a Go worker (`go/cmd/lanbus-worker` + `go/internal/worker`) speaking the existing hub protocol: register/claim/progress/log/submit/heartbeat/recover/outbox, streamed logs, prompt composition, artifacts, Windows Job Objects, POSIX process groups, native `discover` and `ask`, and `--pty` (ConPTY on Windows, Unix PTY elsewhere). Phase 2a/2b/2c has a native hub subset (`go/cmd/lanbusd`) with SQLite, worker protocol, MCP dispatch/envs/list/result/await/logs/review/cancel/interrupt/artifact, SSE, and artifact storage; a Python/Go dual-run test compares state. The Go hub is now the default; the Python hub remains the full-featured fallback.

## What magpie actually is

Observed from the installed `magpie.exe` / `magpie.com` and its runtime:

| Signal | Evidence |
|---|---|
| Go | binary embeds `go1.26.8` |
| Desktop shell | Wails + WebView2 |
| Console entry point | `magpie.com` (1.9 MB) |
| GUI entry point | `magpie.exe` (80 MB) |
| TUI | Charmbracelet/Bubble Tea symbols |
| Database | `modernc.org/sqlite` |
| Assets | embedded in the binary |
| Surfaces | `magpie` (window+tray), `tray`, `panel`, `tui`, `web`, `serve`, `daemon`, `healthcheck` |
| Lifecycle | autostart, launchd/systemd daemon install, healthcheck, pprof (`heap`, `cpu`, `goroutines`) |
| Config | JSON under `~/.config/magpie`, encrypted backup/restore |

The important lesson is not "Go is faster". It is **one core, many surfaces, one binary per OS**:

```
                 ┌─────────────────────────────┐
                 │        magpie core          │
                 │ settings / providers / DB   │
                 └───────┬───────┬───────┬─────┘
                         │       │       │
                    CLI/TUI    Wails    web/browser
                         │       │       │
                    magpie.com  magpie.exe  magpie web
```

## Python suitability for lanbus

Python is a good fit for the agent-facing part:

- prompt/context composition;
- CLI adapters and output parsing;
- MCP/JSON-RPC prototyping;
- review/rework state machine;
- tests and rapid iteration;
- stdlib-only deployment on machines that already have Python.

It is weak for the product shell:

| Area | Python weakness | Effect on lanbus |
|---|---|---|
| Distribution | Python + venv/pip/PATH on every worker; PyInstaller is large and sometimes flagged | worker bootstrap is harder than copying one binary |
| Windows process control | Job Objects, ConPTY, console Ctrl+C, services need ctypes/PowerShell | current cancellation/UI logic is fragile |
| GUI/tray | Qt/Tk/PySide dependencies; no native menu bar/tray in stdlib | no magpie-like tray/panel UI |
| Service lifecycle | systemd/launchd/Windows service wrappers are external | autostart has platform-specific scripts |
| Discovery | threads/GIL, raw ARP/mDNS/SSDP/Windows IP Helper are awkward | fine for a /24, weaker at scale |
| Type safety | dict/SQL rows and dynamic JSON | protocol/schema drift as features grow |
| Single binary | CPython runtime/extension ABI | wheel + interpreter per machine |
| Concurrency | threads are fine for I/O, but not ideal for hundreds of hosts/scans | Go is cleaner for mass scans |
| Observability | no built-in pprof/goroutine dump | harder to debug stuck scans/workers |

Python is **not** the bottleneck at the current LAN scale (3–10 machines, one hub). The real remaining weaknesses are transport, packaging, and live-session control.

## Would Go be better?

**Yes, for a magpie-like product shell.** Go is the better choice if the target is:

- one signed binary per OS, no Python installed;
- Windows/macOS/Linux tray/panel;
- a browser UI for WSL/headless servers (`lanbus web`);
- systemd/launchd/Windows service install;
- fast LAN discovery with goroutines;
- reliable process-tree/PTY/Job Object control;
- pprof/healthcheck/daemon lifecycle;
- versioned protocol and typed task/evidence structs.

**No, not automatically for agent logic.** Go makes adapters, prompt composition and dynamic CLI integrations slower to iterate. A full rewrite would reimplement:

- SQLite task/lease/claim state machine;
- MCP/SSE/REST compatibility;
- worker adapters and prompt composition;
- discovery fingerprints;
- tests that currently drive real processes.

The current code is about 5.5k lines with 39 tests; a full rewrite is feasible for one experienced Go developer, but the risk is migration/regression, not raw code size.

## Recommended target: Go core + Python adapters (hybrid)

```
lanbusd (Go)        task DB, lease/reaper, MCP/SSE/REST, TLS, discovery scheduler
lanbus (Go)         CLI/TUI; same process as lanbusd or thin client
lanbus-gui (Go)     Wails tray/panel/web UI; embedded assets
lanbus-worker (Go)  process supervisor: Job Objects/ConPTY/pty, process groups,
                    cancellation, streams, artifact upload
lanbus-adapter      optional Python plugin over JSON-RPC stdio:
                    custom prompt/CLI parsing; kept only where Python wins
```

Concrete Go libraries worth using:

- `github.com/mark3labs/mcp-go` or the official MCP Go SDK for MCP;
- `modernc.org/sqlite` for a cgo-free SQLite build;
- `github.com/aymanbagabas/go-pty` for ConPTY on Windows and Unix PTY on macOS/Linux;
- raw `kernel32` via `syscall.NewLazyDLL` for Job Objects (no external Go module); `golang.org/x/sys/windows` optional later;
- `github.com/wailsapp/wails/v2` for the GUI/tray shell;
- `github.com/charmbracelet/bubbletea` for TUI;
- `net`/`net/netip` for discovery; `golang.org/x/net` for mDNS/SSDP helpers.

Keep the current wire protocol: MCP tools, `/worker/*`, SSE, claim tokens, artifact contract. Version it `v1` and run Python and Go against the same contract tests.

## Migration phases

### Phase 0 — read-only Go spike (implemented)
- `lanbus-scan` Go binary implements the existing discovery report: interfaces, ARP, process/listener, TCP/HTTP fingerprints.
- Python `lanbus discover` auto-detects it (`bin/`, `PATH`, `LANBUS_SCANNER_BIN`, or `--scanner-bin`); Python remains the fallback.
- `go/README.md` and `make go-scan` / `make go-scan-all` are in the tree.

### Phase 1.5 — Go worker supervisor (done)
- `lanbus-worker` implements register/claim/progress/log/submit/heartbeat/recover/outbox.
- Windows: Job Object with `KILL_ON_JOB_CLOSE`, `TerminateJobObject`; `CREATE_NO_WINDOW`.
- Unix: process group with SIGTERM/SIGKILL; `lan_interrupt` kills the whole tree.
- Native `discover` (Go scanner), `ask` (OpenAI/Ollama), and `--pty` (ConPTY/Unix PTY) support.
- Go hub is the default; Python worker stays for browser and legacy tasks.
- **Phase 2a/2b/2c implemented**: `go/cmd/lanbusd` with SQLite, worker protocol, MCP dispatch/envs/list/result/await/logs/review/cancel/interrupt/artifact, SSE, artifact storage, and a Python/Go dual-run state comparison.
- Remaining Phase 2: browser/legacy parity polish and regression hardening.
- Go hub is the default; the Python hub remains a fallback.

### Phase 2 — Go hub/daemon (2a/2b/2c done)
- **2a/2b/2c**: `lanbusd` SQLite task store/lease/reaper, worker REST, MCP core tools, SSE, artifact storage, and a dual-run Python/Go state comparison.
- `lan_chat`, `lan_discover`, `lan_ask`, `lan_shot`, `lan_conversations`, and `/ui` are now implemented in Go; default flipped to `lanbusd`.
- Remaining: full regression parity for browser/legacy paths; `lanbus daemon install` (systemd user unit, launchd, Windows service), TLS/tokens, healthcheck/pprof.

### Phase 3 — GUI/tray and packaging (started)
- Wails/WebView2 window + systray implemented in `go/cmd/lanbus-gui`; same dashboard and token proxy.
- `lanbus web` / `lanbus gui` commands, portable Windows package, Inno Setup script, signing helper, SHA-256 update helper.
- Remaining: installer compilation on the release machine and notarization/signing certificates.
- `lanbus.exe` GUI vs `lanbus.com` console (magpie pattern).
- Embed assets, SQLite, migrations; add backup/restore of tasks/config.
- Code signing/notarization, updater.

### Phase 4 — adapter plugin (optional)
- Keep a Python `lanbus-adapter` JSON-RPC stdio process for exotic agent CLIs.
- Go worker spawns it only when an adapter is selected; cancellation kills the adapter process group.

Total realistic effort for a robust multi-OS product: **6–10 weeks** for one experienced Go developer, plus packaging/signing. A pure rewrite without the hybrid phases is riskier and slower.

## Immediate Python improvements (no rewrite)

If Go migration is deferred, these are the highest-value changes:

1. **Never use UI keystroke injection for task execution.** Use worker/CLI process groups. GUI injection is notification only.
2. **Make `lan_interrupt` the default stop path**; test it against Windows/macOS/Linux workers.
3. **Bundle a small Go helper only for the hard parts**:
   - `lanbus-scan` (discovery);
   - `lanbus-pty` (Windows Job Objects/ConPTY + Unix PTY supervisor).
   Python hub can call these binaries over JSON lines while keeping the current Python worker.
4. **Ship a worker bootstrap per OS** (single binary or a self-contained zip); remove the "install Python first" step.
5. **Add a local web UI** (`lanbus web`) served by the Python hub before Wails; it gives magpie-like visibility without a rewrite.
6. **Adopt typed protocol models** (dataclasses or JSON Schema) and contract tests; this makes a later Go port mechanical.

## Verdict

- **Personal LAN, 3–10 machines, rapid agent work:** keep Python; fix transport/cancellation/mac worker and packaging. Go is not required.
- **Distributable product with tray, web UI, no Python runtime, Windows/macOS/Linux, many hosts:** Go is the better core. Use the hybrid migration, not a big-bang rewrite.
- **Best next step:** build the Go `lanbus-scan` + `lanbus-worker` sidecars first. Those address the concrete weaknesses (discovery, Ctrl+C/background, process trees, macOS workers) while leaving the Python orchestration and 39-test contract intact.
