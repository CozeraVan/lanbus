"""统一入口: python -m lanbus {hub,worker,doctor,logs,shot,browser} ..."""

import json
import os
import shutil
import subprocess
import sys
import time
import urllib.request
import webbrowser

import re

from urllib.parse import quote

from . import __version__

USAGE = """lanbus —— LAN agent bus

用法:
  lanbus hub     [--port 8788] [--bind 0.0.0.0] [--token T] [--worker-token W] [--data-dir DIR]
  lanbus worker  --name NAME --env ENV --hub URL [--agent claude] [--cwd DIR]
  lanbus doctor  [--hub URL] [--token T]
  lanbus discover [--cidr 192.168.1.0/24] [--ports 11434,1234] [--json]
  lanbus discover --hub URL [--env windows] [--json]
  lanbus interrupt --hub URL --task ID [--reason TEXT] [--token T]
  lanbus logs    --hub URL --task ID [--follow] [--since N] [--token T]
  lanbus shot    --hub URL --env windows --url http://localhost:3000 [--out FILE] [--wait 180]
  lanbus ask     --endpoint 192.168.1.5:11434 --prompt "你好" [--model qwen]
  lanbus chat    --envs win-claude,win-deepseek --message "..." [--rounds 2] [--conversation ID]
  lanbus chat    --list [--hub URL]
  lanbus browser [--detect | --url URL --out FILE]
  lanbus web     [--hub URL] [--no-open] [--no-token]
  lanbus gui     [--hub URL] [--token T]
  lanbus confirm {list|approve|revoke} [--env ENV] [--ttl 1800] [--hub URL]
  lanbus local   {init|up|down|status} [--config FILE] [--only NAME] [--force]
  lanbus --version | --help

也可以直接用 console script: lanbus-hub / lanbus-worker
"""


def _token(explicit=""):
    if explicit:
        return explicit
    if os.environ.get("LANBUS_TOKEN"):
        return os.environ["LANBUS_TOKEN"]
    d = os.environ.get("LANBUS_DATA_DIR") or os.path.join(os.path.expanduser("~"), ".lanbus")
    p = os.path.join(d, "token.txt")
    if os.path.isfile(p):
        return open(p, encoding="utf-8").read().strip()
    return ""


def _operator_token(explicit=""):
    if explicit:
        return explicit
    if os.environ.get("LANBUS_OPERATOR_TOKEN"):
        return os.environ["LANBUS_OPERATOR_TOKEN"]
    d = os.environ.get("LANBUS_DATA_DIR") or os.path.join(os.path.expanduser("~"), ".lanbus")
    p = os.path.join(d, "operator-token.txt")
    if os.path.isfile(p):
        return open(p, encoding="utf-8").read().strip()
    return ""


def _get(url, token, timeout=30):
    req = urllib.request.Request(url, headers={"Authorization": f"Bearer {token}"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read().decode() or "{}")


def _mcp(hub, token, method, params=None, timeout=300):
    body = {"jsonrpc": "2.0", "id": 1, "method": method, "params": params or {}}
    req = urllib.request.Request(
        hub.rstrip("/") + "/mcp", data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json",
                 "Authorization": f"Bearer {token}"}, method="POST")
    with urllib.request.urlopen(req, timeout=timeout) as r:
        out = json.loads(r.read().decode() or "{}")
    if "error" in out:
        raise RuntimeError(out["error"])
    content = out.get("result", {}).get("content") or []
    for c in content:
        if c.get("type") == "text":
            try:
                return json.loads(c["text"])
            except Exception:
                return {"text": c["text"]}
    return out.get("result", {})


def _arg(argv, name, default=""):
    try:
        i = argv.index(name)
        return argv[i + 1]
    except Exception:
        return default


def _doctor(argv):
    print(f"lanbus {__version__}")
    print(f"python  {sys.version.split()[0]}  ({sys.executable})")
    for name in ("git", "claude", "codex", "kimi", "dsh", "node", "npx", "npm"):
        p = shutil.which(name)
        print(f"{name:7} {'OK  ' + p if p else '-- not found'}")
    try:
        from . import browser
        d = browser.detect()
        print(f"browser {json.dumps(d, ensure_ascii=False)}")
        if not d["ok"]:
            print("  hint: pip install playwright && playwright install chromium")
    except Exception as e:
        print(f"browser ERROR {e}")
    hub = _arg(argv, "--hub")
    if hub:
        try:
            req = urllib.request.Request(hub.rstrip("/") + "/health")
            with urllib.request.urlopen(req, timeout=8) as r:
                print("hub     " + r.read().decode())
        except Exception as e:
            print(f"hub     ERROR {e}")
    return 0


def _logs(argv):
    hub = _arg(argv, "--hub") or os.environ.get("LANBUS_HUB", "")
    tid = _arg(argv, "--task") or _arg(argv, "--id", "")
    if not hub or not tid:
        print("用法: lanbus logs --hub URL --task ID [--follow] [--since N] [--token T]",
              file=sys.stderr)
        return 2
    token = _token(_arg(argv, "--token"))
    since = int(_arg(argv, "--since", "0") or 0)
    follow = "--follow" in argv or "-f" in argv
    tail = int(_arg(argv, "--tail", "200") or 200)
    while True:
        out = _get(f"{hub.rstrip('/')}/tasks/{tid}/logs?since={since}&tail={tail}",
                   token)
        for lg in out.get("logs") or []:
            since = max(since, int(lg.get("seq") or 0))
            prefix = "" if lg.get("stream") in (None, "", "stdout") else f"[{lg['stream']}] "
            print(prefix + lg.get("text", ""), flush=True)
        if not follow or out.get("status") not in ("claimed", "running"):
            print(f"# status={out.get('status')} logs={out.get('log_count')} "
                  f"next_since={out.get('next_since')}", file=sys.stderr)
            return 0
        time.sleep(1)


def _shot(argv):
    hub = _arg(argv, "--hub") or os.environ.get("LANBUS_HUB", "")
    url = _arg(argv, "--url")
    if not hub or not url:
        print("用法: lanbus shot --hub URL --env windows --url URL [--out FILE] [--wait 180]",
              file=sys.stderr)
        return 2
    token = _token(_arg(argv, "--token"))
    args = {"url": url}
    for k in ("env", "cwd", "out", "context"):
        v = _arg(argv, "--" + k)
        if v:
            args[k] = v
    for k, typ in (("wait", "wait_s"), ("wait-ms", "wait_ms"),
                   ("timeout", "timeout_ms")):
        v = _arg(argv, "--" + k)
        if v:
            args[typ] = float(v) if typ == "wait_s" else int(v)
    if "--no-full-page" in argv:
        args["full_page"] = False
    print(json.dumps(_mcp(hub, token, "tools/call",
                          {"name": "lan_shot", "arguments": args}),
                     ensure_ascii=False, indent=2))
    return 0


def _discover(argv):
    from . import discovery
    hub = _arg(argv, "--hub") or os.environ.get("LANBUS_HUB", "")
    token = _token(_arg(argv, "--token"))
    args = {}
    for k in ("cidr", "ports", "env", "scanner-bin"):
        v = _arg(argv, "--" + k)
        if v:
            args[k.replace("-", "_")] = v
    for k, key, conv in (("max-hosts", "max_hosts", int),
                         ("timeout", "timeout", float),
                         ("http-timeout", "http_timeout", float)):
        v = _arg(argv, "--" + k)
        if v:
            try:
                args[key] = conv(v)
            except Exception:
                pass
    if "--deep" in argv:
        args["deep"] = True
    if "--no-processes" in argv:
        args["include_processes"] = False
    if "--no-wsl" in argv:
        args["include_wsl"] = False
    if "--no-local" in argv:
        args["include_local"] = False
    as_json = "--json" in argv
    interval = 0.0
    if "--watch" in argv:
        try:
            interval = max(3.0, float(_arg(argv, "--watch") or 10))
        except Exception:
            interval = 10.0

    def once():
        if hub:
            return _mcp(hub, token, "tools/call",
                        {"name": "lan_discover", "arguments": dict(args)})
        return discovery.discover(args)

    while True:
        try:
            out = once()
        except Exception as e:
            print(f"discover error: {e}", file=sys.stderr)
            return 1
        report = out.get("report") if isinstance(out, dict) else None
        if report is None and isinstance(out, dict) and out.get("hosts") is not None:
            report = out
        if as_json:
            print(json.dumps(out, ensure_ascii=False, indent=2))
        elif report:
            print(discovery.summary_text(report))
        else:
            print(json.dumps(out, ensure_ascii=False, indent=2))
        if not interval:
            return 0
        time.sleep(interval)

def _confirm(argv):
    hub = (_arg(argv, "--hub") or os.environ.get("LANBUS_HUB") or
           "http://127.0.0.1:8788").rstrip("/")
    token = _operator_token(_arg(argv, "--token"))
    action = argv[0] if argv else "list"
    if not token:
        print(json.dumps({"ok": False, "error": "operator token missing: ~/.lanbus/operator-token.txt"},
                         ensure_ascii=False))
        return 1
    try:
        if action == "list":
            out = _get(hub + "/api/confirmations", token)
        elif action in ("approve", "allow"):
            env = _arg(argv, "--env")
            if not env:
                print("用法: lanbus confirm approve --env ENV [--ttl 1800]", file=sys.stderr)
                return 2
            body = {"env": env, "ttl_s": int(_arg(argv, "--ttl", "1800") or 1800),
                    "approved_by": "cli-operator", "note": _arg(argv, "--note")}
            req = urllib.request.Request(hub + "/confirm/approve", data=json.dumps(body).encode(),
                                         headers={"Content-Type": "application/json",
                                                  "Authorization": "Bearer " + token}, method="POST")
            with urllib.request.urlopen(req, timeout=30) as r:
                out = json.loads(r.read().decode())
        elif action in ("revoke", "deny"):
            env = _arg(argv, "--env")
            if not env:
                print("用法: lanbus confirm revoke --env ENV", file=sys.stderr)
                return 2
            req = urllib.request.Request(hub + "/confirm/revoke", data=json.dumps({"env": env}).encode(),
                                         headers={"Content-Type": "application/json",
                                                  "Authorization": "Bearer " + token}, method="POST")
            with urllib.request.urlopen(req, timeout=30) as r:
                out = json.loads(r.read().decode())
        else:
            print("用法: lanbus confirm {list|approve|revoke} [--env ENV] [--ttl 1800]", file=sys.stderr)
            return 2
    except Exception as e:
        print(json.dumps({"ok": False, "error": str(e)}, ensure_ascii=False))
        return 1
    print(json.dumps(out, ensure_ascii=False, indent=2))
    return 0


def _web(argv):
    hub = (_arg(argv, "--hub") or os.environ.get("LANBUS_HUB") or
           "http://127.0.0.1:8788").rstrip("/")
    url = hub + "/ui"
    if "--no-token" not in argv:
        token = _operator_token(_arg(argv, "--token")) or _token(_arg(argv, "--token"))
        if token:
            url += "#token=" + quote(token, safe="")
    print(url)
    if "--no-open" not in argv:
        try:
            webbrowser.open(url)
        except Exception as e:
            print(json.dumps({"ok": False, "error": str(e)}, ensure_ascii=False))
            return 1
    return 0


def _gui(argv):
    hub = (_arg(argv, "--hub") or os.environ.get("LANBUS_HUB") or
           "http://127.0.0.1:8788").rstrip("/")
    token = _operator_token(_arg(argv, "--token")) or _token(_arg(argv, "--token"))
    root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    candidates = [
        os.path.join(root, "bin", "lanbus-gui.exe"),
        os.path.join(os.getcwd(), "bin", "lanbus-gui.exe"),
        shutil.which("lanbus-gui"),
    ]
    exe = next((p for p in candidates if p and os.path.isfile(p)), "")
    if not exe:
        print(json.dumps({"ok": False, "error": "lanbus-gui.exe not found; run make go-gui"},
                         ensure_ascii=False))
        return 1
    args = [exe, "--hub", hub]
    if token:
        args += ["--token", token]
    try:
        kwargs = {"close_fds": True}
        if os.name == "nt":
            kwargs["creationflags"] = 0x00000008 | 0x00000200
        subprocess.Popen(args, **kwargs)
    except Exception as e:
        print(json.dumps({"ok": False, "error": str(e)}, ensure_ascii=False))
        return 1
    print(json.dumps({"ok": True, "gui": exe, "hub": hub}, ensure_ascii=False))
    return 0


def _chat(argv):
    hub = (_arg(argv, "--hub") or os.environ.get("LANBUS_HUB") or
           "http://127.0.0.1:8788")
    token = _token(_arg(argv, "--token"))
    if "--list" in argv:
        try:
            out = _mcp(hub, token, "tools/call",
                       {"name": "lan_conversations",
                        "arguments": {"limit": int(_arg(argv, "--limit", "20") or 20)}},
                       timeout=60)
        except Exception as e:
            print(json.dumps({"ok": False, "error": str(e)}, ensure_ascii=False))
            return 1
        print(json.dumps(out, ensure_ascii=False, indent=2))
        return 0
    envs = [x.strip() for x in (_arg(argv, "--envs") or _arg(argv, "--env") or "").split(",")
            if x.strip()]
    message = _arg(argv, "--message") or _arg(argv, "--prompt") or ""
    cid = _arg(argv, "--conversation") or _arg(argv, "--id")
    if not message or (not cid and len(envs) < 2):
        print("用法: lanbus chat --envs ENV_A,ENV_B --message TEXT [--rounds 2] "
              "[--conversation ID] [--hub URL] [--json]", file=sys.stderr)
        print("      lanbus chat --list [--hub URL]", file=sys.stderr)
        return 2
    args = {
        "envs": envs, "message": message,
        "rounds": int(_arg(argv, "--rounds", "2") or 2),
        "timeout_s": int(_arg(argv, "--timeout", "240") or 240),
        "total_timeout_s": int(_arg(argv, "--total-timeout", "540") or 540),
    }
    if cid:
        args["conversation_id"] = cid
    ctx = _arg(argv, "--context")
    if ctx:
        args["context"] = ctx
    title = _arg(argv, "--title")
    if title:
        args["title"] = title
    try:
        out = _mcp(hub, token, "tools/call",
                   {"name": "lan_chat", "arguments": args}, timeout=900)
    except Exception as e:
        print(json.dumps({"ok": False, "error": str(e)}, ensure_ascii=False))
        return 1
    if "--json" in argv:
        print(json.dumps(out, ensure_ascii=False, indent=2))
        return 0 if out.get("ok") else 1
    print(f"conversation {out.get('conversation_id')}  "
          f"{out.get('elapsed_s')}s  ok={out.get('ok')}")
    for t in out.get("turns") or []:
        print(f"\n[{t.get('env')}] R{t.get('round')} ({t.get('status')})")
        print(t.get("reply") or "")
    if out.get("stopped"):
        print(f"\nstopped: {out['stopped']}")
    return 0 if out.get("ok") else 1


def _interrupt(argv):
    hub = _arg(argv, "--hub") or os.environ.get("LANBUS_HUB", "")
    tid = _arg(argv, "--task") or _arg(argv, "--id", "")
    if not hub or not tid:
        print("用法: lanbus interrupt --hub URL --task ID [--reason TEXT]",
              file=sys.stderr)
        return 2
    token = _token(_arg(argv, "--token"))
    args = {"task_id": tid, "reason": _arg(argv, "--reason") or "interrupted by user"}
    try:
        out = _mcp(hub, token, "tools/call",
                   {"name": "lan_interrupt", "arguments": args})
    except Exception as e:
        print(json.dumps({"ok": False, "error": str(e)}, ensure_ascii=False))
        return 1
    print(json.dumps(out, ensure_ascii=False, indent=2))
    return 0


def _ask_cmd(argv):
    from . import discovery
    prompt = _arg(argv, "--prompt") or ""
    if not prompt:
        print("用法: lanbus ask --endpoint IP:PORT --prompt TEXT [--model M]",
              file=sys.stderr)
        return 2
    endpoint = _arg(argv, "--endpoint")
    host = _arg(argv, "--host")
    port = _arg(argv, "--port")
    if endpoint and not host:
        m = re.match(r"^(?:https?://)?([^/:]+)(?::(\d+))?/?$",
                                   endpoint)
        if m:
            host = m.group(1)
            port = port or m.group(2)
    hub = _arg(argv, "--hub") or os.environ.get("LANBUS_HUB", "")
    token = _token(_arg(argv, "--token"))
    payload = {"prompt": prompt}
    for k in ("host", "port", "model", "service", "api_key", "env"):
        v = _arg(argv, "--" + k)
        if v:
            payload[k] = int(v) if k == "port" else v
    if endpoint:
        payload["endpoint"] = endpoint
        if endpoint.lower().startswith("https://"):
            payload["tls"] = True
    if "--tls" in argv:
        payload["tls"] = True
    timeout = _arg(argv, "--timeout")
    if timeout:
        payload["timeout_s"] = float(timeout)
    wait = _arg(argv, "--wait")
    if wait:
        payload["wait_s"] = float(wait)
    try:
        if hub:
            out = _mcp(hub, token, "tools/call",
                       {"name": "lan_ask", "arguments": payload})
        else:
            out = discovery.ask(host, int(port or 0), prompt,
                                model=payload.get("model", ""),
                                service=payload.get("service", ""),
                                api_key=payload.get("api_key", ""),
                                timeout=float(timeout or 120),
                                tls=bool(payload.get("tls")))
    except Exception as e:
        print(json.dumps({"ok": False, "error": str(e)}, ensure_ascii=False))
        return 1
    print(json.dumps(out, ensure_ascii=False, indent=2))
    return 0 if out.get("ok") else 1


def main(argv=None):
    # 重定向到文件/管道时也保持 UTF-8，避免 Windows 默认 GBK 把 JSON 写坏
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(encoding="utf-8")
        except Exception:
            pass
    argv = list(sys.argv[1:] if argv is None else argv)
    if argv and argv[0] in ("-v", "--version", "version"):
        print(f"lanbus {__version__}")
        return 0
    if not argv or argv[0] in ("-h", "--help", "help"):
        print(USAGE)
        return 0
    cmd, rest = argv[0], argv[1:]
    if cmd in ("hub", "server"):
        from . import hub
        sys.argv = ["lanbus-hub"] + rest
        hub.main()
        return 0
    if cmd in ("worker", "agent"):
        from . import worker
        sys.argv = ["lanbus-worker"] + rest
        worker.main()
        return 0
    if cmd == "doctor":
        return _doctor(rest)
    if cmd == "discover":
        return _discover(rest)
    if cmd == "ask":
        return _ask_cmd(rest)
    if cmd == "chat":
        return _chat(rest)
    if cmd == "interrupt":
        return _interrupt(rest)
    if cmd == "logs":
        return _logs(rest)
    if cmd == "shot":
        return _shot(rest)
    if cmd == "browser":
        from . import browser
        return browser.main(rest)
    if cmd == "web":
        return _web(rest)
    if cmd == "gui":
        return _gui(rest)
    if cmd == "confirm":
        return _confirm(rest)
    if cmd == "local":
        from . import local
        return local.command(rest)
    print(f"unknown command: {cmd}\n\n{USAGE}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
