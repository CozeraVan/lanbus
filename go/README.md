# lanbus-scan (Go)

Native Phase-0 scanner for lanbus. It reads the same JSON spec as
`lanbus.discovery.discover()` on stdin and writes the same report shape to
stdout. No external Go modules.

Make sure the Go toolchain is on `PATH` (`go version`). Then:

```bash
# native
go build -trimpath -ldflags="-s -w" -o ../bin/lanbus-scan ./cmd/lanbus-scan

# all targets
make go-scan-all
```

Python picks it up automatically from:

1. `LANBUS_SCANNER_BIN`
2. `lanbus-scan` on `PATH`
3. `bin/lanbus-scan*` in the source tree or `~/.lanbus/bin`
4. `--scanner-bin` / `scanner_bin` in the MCP/CLI arguments

Fallback: the Python `discovery.py` implementation.

## Go hub (Phase 2a)

```bash
go build -trimpath -ldflags="-s -w" -o ../bin/lanbusd ./cmd/lanbusd
../bin/lanbusd --bind 127.0.0.1 --port 8789 --data-dir /tmp/lanbus-go   --token test-token --worker-token test-token
```

`lanbusd` is a native SQLite-backed hub subset: worker register/claim/progress/log/
submit/recover, MCP dispatch/envs/list/result/await/logs/review/cancel/interrupt/
artifact, SSE and artifact files. It is now the default hub; the Python hub remains a full fallback. Cross-hub
protocol/state-machine comparison is in `tests/test_go_hub.py`.

## Wails GUI (Phase 3, Windows)

```bash
make go-gui
bin/lanbus-gui.exe --hub http://127.0.0.1:8788
```

The GUI embeds the same dashboard HTML, proxies authenticated API calls to the hub,
and adds a tray menu (open panel / open browser / quit). Closing the window hides
it to the tray. Build requires WebView2 Runtime (installed with Edge on current
Windows).

## Go worker (Phase 1.5)

```bash
go build -trimpath -ldflags="-s -w" -o ../bin/lanbus-worker ./cmd/lanbus-worker
```

Run against the existing Python hub:

```bash
./bin/lanbus-worker --name go-win --env windows   --hub http://127.0.0.1:8788 --token TOKEN   --cwd D:/proj --agent ADAPTER
```

It implements register/claim/progress/log/submit/heartbeat/recover/outbox,
prompt composition, git diff/status, and inline base64 artifact upload.
Cancellation uses a Windows Job Object (`KILL_ON_JOB_CLOSE` + `TerminateJobObject`)
or a POSIX process group with SIGTERM/SIGKILL. Native `discover` and `ask`
tasks are implemented; `browser` tasks remain on the Python worker.

`--pty` runs the agent on a real PTY via `github.com/aymanbagabas/go-pty`
(ConPTY on Windows, Unix PTY elsewhere). Go 1.25+ is required; the first build
needs the module cache, so use `GOPROXY=https://goproxy.cn,direct` if the default
Go proxy is unreachable. The resulting binary has no runtime dependency on the
module cache.

Report fields intentionally match Python: `summary`, `local_agents`, `hosts`,
`agents`, `local_services`, `interfaces`, `scope`, `notes`; Go adds
`"engine":"go"`.
