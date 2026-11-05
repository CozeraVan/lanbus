# Case study: cross-machine LAN test

## Topology

```text
Windows hub  192.0.2.10:8788   lanbusd 1.9.0-go
macOS worker 192.0.2.20        macos-a-cli / 1.9.0-go / Darwin arm64
```

Mac bootstrap (no MCP/plugin):

```bash
curl -fsSL http://192.0.2.10:8788/download/lanbus-mac-bootstrap.sh | \
  bash -s -- http://192.0.2.10:8788 <worker-token> ADAPTER mac-a-cli macos-a-cli
```

The script downloads `lanbus-worker-darwin-arm64` from the hub and runs it with
`--agent ADAPTER`. No Python, MCP, or plugin is required on the Mac.

## Evidence

### 1. Mac worker registered

```text
env=macos-a-cli  worker=mac-a-cli  hostname=192.0.2.20
version=1.9.0-go  agent=ADAPTER  online=true
```

### 2. Cross-machine connectivity

Task `4300daffb87a`:

```text
status=submitted  exit_code=0
result: AGENT_OK
```

### 3. Cross-machine conversation

Conversation `c594d652fab00`, 2 rounds, 4 turns:

```text
win-a-cli     -> Windows 11 / PowerShell + Git Bash
macos-a-cli   -> macOS Darwin 25.5.0 arm64 / zsh
win-a-cli     -> read the macOS reply, requested macOS baseline commands
macos-a-cli   -> confirmed received, proposed the next round
```

The Mac turn was delivered back to Windows and read by the Windows agent, proving
bidirectional cross-machine dialogue.

### 4. Remote shell execution

Task `e7f9aec8a784` on `macos-a-cli`:

```text
Darwin 192.0.2.20 25.5.0 ... arm64
ProductName: macOS
ProductVersion: 26.5
/Users/test
/bin/zsh
status=submitted  exit_code=0
```

### 5. Remote process-tree interrupt

Task `c5eb3a7a2dea` (`sleep 60`) was interrupted:

```text
lan_interrupt -> status=cancelled  ctrl_c_equivalent=true
```

Follow-up shell check on the same Mac:

```text
ps -ax -o pid,command | grep '[s]leep 60' -> NO_SLEEP
```

The macOS process was actually terminated, not merely marked cancelled in the hub.

### 6. Human confirmation gate

- A remote dispatch without approval returns `waiting_confirmation` and does not
  reach the Mac worker.
- `lan_chat` refuses with `human_confirmation_required`.
- The main-agent token receives `401` on `/confirm/approve`.
- The operator token approves `macos-a-cli`; the waiting task becomes `queued`
  and the real Mac worker executes it.
- Approval TTL is 30 minutes and is revocable.
- Actual Mac task `789ca1a1c0ed` stayed `waiting_confirmation` until CLI
  operator approval, then became `queued`, was claimed by `mac-a-cli`, and
  completed with `exit_code=0`.

## Result

```text
cross-machine registration:  PASS
cross-machine dispatch:      PASS
cross-machine dialogue:      PASS
remote shell execution:      PASS
remote process-tree kill:    PASS
```

No internet relay was used; all traffic was on the local LAN.
