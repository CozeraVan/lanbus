# Troubleshooting

Order of checks: **hub alive → worker registered → task claimed → logs flowing → submitted → reviewed.** Then check lease/recovery if the task jumped back to `queued`.

| Symptom | Cause | Fix |
|---|---|---|
| `lan_envs` shows no workers | worker crashed or wrong env name | check worker terminal; env string must match exactly (`windows` ≠ `Windows`) |
| Task stays `queued` forever | no worker with that `env`/`required_caps`, or worker cannot reach hub | `curl http://HUB:8788/health` from the worker host; worker prints `claim failed` on error; check `lan_envs` `caps` for `browser` tasks |
| `401 unauthorized` | wrong role token | `/mcp` needs the agent token; `/worker/*` accepts the worker token (and the agent token as operator). Compare `~/.lanbus/token.txt` / `worker-token.txt` |
| `403 bad host` | client reached the hub via a hostname | use an IP literal, or add the name to `LANBUS_HOSTS` |
| `lan_logs` is empty but the task is running | agent buffers stdout, or the worker cannot reach `/worker/log` | run the agent with `PYTHONUNBUFFERED=1` / `stdio` flush; check the worker console for `hub unreachable`; local copy is in `~/.lanbus/logs/` |
| `lan_cancel` returns cancelled but the process keeps running | worker is stuck on a network call, or it is an old worker without progress polling | upgrade the worker; check `progress` responses; the hub reaper will eventually requeue if the lease expires, but the process tree is only killed by a worker that polls |
| Task bounces `running → queued → running` | lease is too short for the machine/network, or the worker's progress thread cannot reach the hub | raise `lease_s` on dispatch (e.g. 300); check LAN/firewall; reaper interval is `--reap-s` |
| Task ends `failed` with "执行租约丢失 … 超过恢复上限" | worker crashed more than `max_recoveries` times | inspect `lan_logs`/`~/.lanbus/logs`; fix the worker; dispatch a new task or raise `max_recoveries` |
| Task sticks in `running` | agent CLI is blocked (interactive prompt) or hung; worker alive and renewing lease | check `--cmd`; `claude -p` needs `--permission-mode acceptEdits`; `lan_cancel` to kill it |
| `verify_ok=0` but the agent printed success | acceptance command is wrong, or runs in the wrong `cwd` | check `cwd` is the repo root and the command works when run by hand there |
| `diff` is empty after edits | `cwd` is not a git repo, or edits landed outside it | `git -C cwd status`; set `cwd` to the repo root |
| `artifacts` is empty | glob did not match, file older than the task, or larger than the cap | pass task `artifact_globs`; write files under `cwd`; default patterns include `.lanbus-shot.*`, `screenshot*.png`, `build.log`; check `--max-artifact-mb` |
| `GET /artifacts/...` returns 404 | wrong artifact name, or the task id is wrong | call `lan_artifact` / `lan_result` first and use the exact `name` |
| `lan_result` returns no image block | artifact is not an image, >8 MB, or `include_images=false` | use `lan_artifact` with the exact name, or download via `artifact.url` |
| `lan_shot` stays `queued` | no online worker has `browser` capability | run `lanbus doctor` on the worker; install Playwright + Chromium, or Chrome/Edge; check `lan_envs` `has_browser` |
| Headless screenshot works but click/fill fails | only Playwright supports actions; headless fallback is screenshot-only | `pip install playwright && playwright install chromium` |
| Browser screenshot is blank / wrong size | page needs a wait, login, or a larger viewport | use `lan_shot` `actions` (`fill`, `click`, `wait`), `wait_ms`, `viewport`, or Playwright |
| `lan_discover` finds no agents | scope/ports too narrow, host firewall, or agent is GUI-only | run `lanbus discover` on the target host; add `--cidr`/`--ports`; inspect `--json` for open ports; GUI-only apps have no listener |
| Discovery is slow | wide CIDR, `deep`, or many open ports | default `/24`; `--timeout 0.15`; narrow `--ports`; `deep` scans 1-1024 and is intentionally slower |
| `lan_ask` says no model / 401 | endpoint has no model loaded or needs auth | run `lan_discover` and inspect `models`; pass `--model`; use `api_key` |
| `lan_discover env=...` stays queued | no worker reports `discover` capability | upgrade worker; check `lan_envs` `caps` contains `discover` |
| macOS sees no LAN hosts | macOS 14+ Local Network privacy permission denied | System Settings → Privacy & Security → Local Network, allow Terminal/your agent |
| macOS Kimi CLI produces no response | no lanbus worker on the Mac, or launchd PATH lacks `kimi` | run `lanbus doctor` and `lanbus worker --agent kimi` on the Mac; use `examples/macos/com.lanbus.kimi-worker.plist`; ensure `~/.kimi-code/bin` is in PATH |
| Need to stop a task like Ctrl+C | UI keystroke injection is focus-dependent | use `lan_interrupt` / `lan_cancel` on the task; worker kills the process tree in foreground or background |
| Chinese text arrives as mojibake | client decoded the body as latin-1 | hub sends `charset=utf-8`; if you proxy it, preserve that header; worker sets `PYTHONIOENCODING=utf-8` |
| Two agents clobber each other's edits | same files via `/mnt/d` and `D:\` | one tree — partition tasks by file, don't run two edits on one file |
| Wheel installs but writes to site-packages fail | stale build writing next to the source | data lives in `~/.lanbus`; override with `LANBUS_DATA_DIR` |
| `worker: submit deferred` repeatedly | hub down, token wrong, or outbox blocked | fix connectivity/token; outbox is retried every 10 s; files live in `~/.lanbus/outbox/<name>/` |

## Collecting a report

```bash
lanbus --version
lanbus doctor --hub http://HUB_LAN_IP:8788
curl -s http://HUB_LAN_IP:8788/health
curl -sN http://HUB_LAN_IP:8788/events -H "Authorization: Bearer $TOKEN"   # 10 seconds is enough
lanbus logs --hub http://HUB_LAN_IP:8788 --task TASK_ID --follow
```

Worker side: the launch command, the agent used (`--agent` / `--cmd`), browser caps from `lanbus doctor`, and the last 50 lines of its output. Hub side: `<data-dir>/bus.db`, `task_logs`, and `artifacts/<task_id>/`.

## Reset

```bash
# stop hub + workers, then
rm -rf ~/.lanbus          # tasks, artifacts, token (a new token is generated next start)
```

Deleting the data dir is safe: nothing else holds state. On workers, `~/.lanbus/outbox` may still contain submissions; delete it only if you do not want them retried.
