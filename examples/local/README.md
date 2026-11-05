# Local worker supervisor

`lanbus local` keeps the agents on **this machine** online across reboots.
It is deliberately separate from LAN discovery: no remote machine is touched.

## Quick start

```bash
lanbus local init
# edit ~/.lanbus/local.json: paths, agents, optional WSL entry
lanbus local up
lanbus local status
```

`up` skips an env that the hub already reports online. It is safe to call from
Windows Startup / a login script.

## Config

`~/.lanbus/local.json`:

```json
{
  "hub": "http://127.0.0.1:8788",
  "token_file": "C:/Users/me/.lanbus/token.txt",
  "workers": [
    {
      "name": "win-a-cli",
      "env": "win-a-cli",
      "agent": "ADAPTER_A",
      "cwd": "D:/proj",
      "binary": "D:/lanbus/bin/lanbus-worker.exe"
    },
    {
      "name": "wsl-b-cli",
      "env": "wsl-b-cli",
      "agent": "ADAPTER_B",
      "cwd": "/home/me",
      "wsl": "Ubuntu-24.04",
      "binary": "/home/me/bin/lanbus-worker-go",
      "hub": "http://HUB_LAN_IP:8788"
    }
  ]
}
```

- `kind`: `go` (default) or `python`.
- `binary`: Go worker path; for WSL this is the path **inside** the distro.
- `wsl`: distro name; the process is launched with `wsl.exe -d <distro>`.
- `hub`: optional per-worker override (useful for WSL NAT).
- `pty`: `true` to pass `--pty` (ConPTY / Unix PTY).
- `agent`: a built-in adapter value (see [adapters](../..//docs/architecture.md#adapters)).
- `cmd`: any custom CLI, e.g. `"my-agent --print {prompt_file}"`.
- `token_file`: defaults to `~/.lanbus/token.txt`.

## Windows startup

Create `%USERPROFILE%\.lanbus\start-local-workers.cmd`:

```bat
@echo off
timeout /t 20 /nobreak >nul
python -m lanbus local up
```

Point a hidden `.vbs` in `shell:startup` at it:

```vbs
Set sh = CreateObject("WScript.Shell")
sh.Run """C:\Users\me\.lanbus\start-local-workers.cmd""", 0, False
```

The 20-second delay lets `lanbus hub` bind first.

## WSL binary

```bash
cp /mnt/d/nixiang/lanbus/bin/lanbus-worker-linux-amd64 ~/bin/lanbus-worker-go
chmod +x ~/bin/lanbus-worker-go
```

Use the Windows host IP (not `127.0.0.1`) for the WSL worker when the hub is on
Windows; WSL2 localhost forwarding is not guaranteed for long-lived processes.

## Commands

```text
lanbus local init [--force] [--config FILE]
lanbus local up   [--only NAME] [--force] [--dry-run]
lanbus local down [--only NAME] [--dry-run]
lanbus local status
```
