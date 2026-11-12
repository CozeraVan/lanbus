"""统一入口: python -m lanbus {hub,worker,doctor,logs,shot,browser} ..."""

import getpass
import json
import os
import shutil
import socket
import subprocess
import sys
import time
import urllib.request
import webbrowser

import re

from urllib.parse import quote

from . import __version__, connections

USAGE = """lanbus —— LAN agent bus

用法:
  lanbus hub     [--port 8788] [--bind 0.0.0.0] [--token T] [--worker-token W] [--data-dir DIR]
  lanbus worker  --name NAME --env ENV --hub URL [--agent claude] [--cwd DIR]
  lanbus worker  --name NAME --env ENV --direct [--port 8790] [--agent claude]
  lanbus chat    --to PEER --message "..." [--timeout 300]
  lanbus run     --to PEER --cwd DIR --message "..."
  lanbus inbox|allow|deny|revoke [--name NAME]  # 目标机器本机审批
  lanbus peers   [--url URL]
  lanbus doctor  [--hub URL] [--token T]
  lanbus discover [--cidr 192.168.1.0/24] [--ports 11434,1234] [--json]
  lanbus discover --hub URL [--env windows] [--json]
  lanbus connect [HOST] [--token T] [--no-save | --status]
  lanbus disconnect [HOST]
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


def _health(hub):
    req = urllib.request.Request(hub.rstrip("/") + "/health")
    with urllib.request.urlopen(req, timeout=8) as response:
        return json.loads(response.read().decode() or "{}")


def _online_envs(hub, token):
    response = _mcp(hub, token, "tools/call",
                    {"name": "lan_envs", "arguments": {}}, timeout=60)
    return connections.select_online_envs(response)


def _connect(argv):
    try:
        requested = _arg(argv, "--hub")
        positional = next((item for item in argv if not item.startswith("--")
                           and item != _arg(argv, "--token")), "")
        hub, token = connections.resolve_connection(
            hub=requested or positional, token=_arg(argv, "--token"))
        if "--status" in argv:
            health = _health(hub)
            envs = _online_envs(hub, token) if token else []
            print(json.dumps({"hub": hub, "version": health.get("version", ""),
                              "reachable": True, "online_envs": envs,
                              "has_saved_token": bool(token)}, ensure_ascii=False, indent=2))
            return 0
        if not positional and not requested:
            from . import discovery
            candidates = []
            for agent in discovery.discover({}).get("agents") or []:
                endpoint = agent.get("endpoint")
                if agent.get("kind") == "lanbus" and endpoint:
                    normalized = connections.normalize_hub(endpoint)
                    if normalized not in candidates:
                        candidates.append(normalized)
            if len(candidates) != 1:
                print(json.dumps({"ok": False, "hubs": candidates,
                                  "error": "请指定 Hub：lanbus connect HOST"},
                                 ensure_ascii=False))
                return 2
            hub = candidates[0]
        health = _health(hub)
        if not token:
            if sys.stdin.isatty():
                token = getpass.getpass("agent token: ").strip()
            else:
                print("缺少 agent token；请使用 --token，或设置 LANBUS_TOKEN 后重试")
                return 1
        if token and "--no-save" not in argv:
            connections.save_connection(hub, token)
        print(json.dumps({"ok": True, "hub": hub, "version": health.get("version", ""),
                          "saved": "--no-save" not in argv}, ensure_ascii=False, indent=2))
        return 0
    except Exception as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False))
        return 1


def _disconnect(argv):
    try:
        hub = next((item for item in argv if not item.startswith("--")), "")
        removed = connections.remove_connection(_arg(argv, "--hub") or hub)
        print(json.dumps({"ok": removed, "hub": hub or "default"}, ensure_ascii=False))
        return 0 if removed else 1
    except Exception as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False))
        return 1


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


def _peer_data_dir(name=""):
    configured = os.environ.get("LANBUS_PEER_DIR")
    if configured:
        return os.path.abspath(os.path.expanduser(configured))
    return os.path.join(os.path.expanduser("~"), ".lanbus", "peers", name or "initiator")


def _resolve_peer(value):
    if value.startswith("http://") or value.startswith("https://"):
        return value.rstrip("/")
    if re.match(r"^\d{1,3}(?:\.\d{1,3}){3}:\d+$", value):
        return "http://" + value
    from . import discovery
    candidates = []
    for agent in discovery.discover({}).get("agents") or []:
        if agent.get("kind") == "lanbus-peer" and agent.get("name") == value:
            candidates.append((agent.get("endpoint"),
                               agent.get("peer_id") or agent.get("endpoint")))
    candidates = [item for item in dict.fromkeys(candidates) if item[0]]
    if not candidates:
        raise ValueError("目标名未发现，请使用 --to IP:PORT；候选：")
    if len({pid for _, pid in candidates}) != 1:
        raise ValueError("目标名不唯一（多个不同 peer 同名），请使用 --to IP:PORT；候选：" +
                         ", ".join(e for e, _ in candidates))
    endpoints = [e for e, _ in candidates]
    if len(endpoints) > 1:
        # 同一 peer 在多网卡上被报告：优先主网卡所在子网的 endpoint。
        prefix = ""
        try:
            probe = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            probe.connect(("8.8.8.8", 80))
            prefix = probe.getsockname()[0].rsplit(".", 1)[0] + "."
            probe.close()
        except OSError:
            pass
        preferred = [e for e in endpoints
                     if prefix and (urllib.parse.urlsplit(e).hostname or "").startswith(prefix)]
        endpoints = preferred or endpoints
    return endpoints[0]


def _direct_chat(argv, scope):
    from . import peer
    message = _arg(argv, "--message") or _arg(argv, "--prompt")
    if not message:
        print("用法: lanbus chat --to PEER --message TEXT", file=sys.stderr)
        return 2
    cwd = _arg(argv, "--cwd")
    try:
        url = _resolve_peer(_arg(argv, "--to"))
        client = peer.DirectClient(url, _peer_data_dir())
        health = peer.json_request(url + "/peer/health", timeout=10)

        def await_pairing():
            pending = peer.request_pairing(url, client.identity, message, scope, cwd)
            request_id = pending["request_id"]
            print(f"目标 {health.get('name')} 待确认 {request_id}；请在目标机器运行 lanbus inbox",
                  flush=True)
            if "--no-wait" in argv:
                print(json.dumps(pending, ensure_ascii=False, indent=2))
                return None, 2
            deadline = time.time() + float(_arg(argv, "--pair-timeout", "600") or 600)
            while time.time() < deadline:
                result = peer.pairing_status(url, request_id, client.identity.peer_id)
                if result.get("status") == "approved":
                    grant = result["grant"]
                    client.save_grant(grant)
                    return grant, 0
                if result.get("status") in ("denied", "expired"):
                    print(json.dumps({"ok": False, "error": "目标已拒绝或申请已过期",
                                      "status": result["status"]}, ensure_ascii=False))
                    return None, 1
                time.sleep(0.5)
            print(json.dumps({"ok": False, "error": "等待目标确认超时"}, ensure_ascii=False))
            return None, 1

        grant = client.find_grant(health["peer_id"], scope)
        if not grant:
            grant, rc = await_pairing()
            if not grant:
                return rc
        timeout = float(_arg(argv, "--timeout", "300") or 300)
        try:
            result = client.request(grant, message, scope, cwd, timeout=timeout)
        except RuntimeError as exc:
            # 授权被撤销/会话失效后自动重新配对，不再要求手动删凭证缓存。
            if "授权不存在" not in str(exc) and "已过期或已撤销" not in str(exc):
                raise
            client.drop_grant(health["peer_id"])
            print("目标授权已失效，自动重新发起配对…", file=sys.stderr, flush=True)
            grant, rc = await_pairing()
            if not grant:
                return rc
            result = client.request(grant, message, scope, cwd, timeout=timeout)
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0 if result.get("ok") else 1
    except Exception as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False))
        return 1


def _peer_local(argv, action):
    from . import peer
    try:
        url = _arg(argv, "--url", "http://127.0.0.1:8790").rstrip("/")
        data_dir = _arg(argv, "--data-dir") or _peer_data_dir(_arg(argv, "--name"))
        if action == "peer-ui":
            import webbrowser
            peer_file = os.path.join(data_dir, "peer.json")
            if not os.path.exists(peer_file):
                print(f"未找到运行信息 {peer_file}；请确认直连 worker 正在运行",
                      file=sys.stderr)
                return 2
            with open(peer_file, encoding="utf-8") as handle:
                info = json.load(handle)
            with open(os.path.join(data_dir, "approval.json"), encoding="utf-8") as handle:
                secret = json.load(handle)["secret"]
            print(f"审批页面：{info['url']}/peer/ui（密钥经 URL 片段自动带入，不会发往服务器）")
            webbrowser.open(f"{info['url']}/peer/ui#secret={secret}")
            return 0
        if action == "peers":
            out = peer.json_request(url + "/peer/health", timeout=10)
            print(json.dumps(out, ensure_ascii=False, indent=2))
            return 0
        with open(os.path.join(data_dir, "approval.json"), encoding="utf-8") as handle:
            secret = json.load(handle)["secret"]
        port = urllib.parse.urlsplit(url).port or 80
        if action == "inbox":
            out = peer.local_request(port, "/peer/inbox", None, secret)
        elif action in ("allow", "deny"):
            request_id = argv[0] if argv and not argv[0].startswith("--") else ""
            if action == "deny":
                out = peer.local_request(port, "/peer/deny", {"request_id": request_id}, secret)
            else:
                out = peer.local_request(port, "/peer/approve", {
                    "request_id": request_id,
                    "mode": "session" if "--session" in argv else "pair",
                    "scope": _arg(argv, "--scope", "chat"),
                    "cwd": _arg(argv, "--cwd")}, secret)
        else:
            peer_id = argv[0] if argv and not argv[0].startswith("--") else ""
            out = peer.local_request(port, "/peer/revoke", {"peer_id": peer_id}, secret)
        print(json.dumps(out, ensure_ascii=False, indent=2))
        return 0
    except Exception as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False))
        return 1


def _peer_daemon(argv):
    from . import peer
    from . import worker
    name = _arg(argv, "--name")
    if not name:
        print("用法: lanbus peer --name NAME [--agent claude] [--cwd DIR] [--port 8790]",
              file=sys.stderr)
        return 2
    agent = _arg(argv, "--agent", "claude")
    command = worker.apply_permission_mode(
        _arg(argv, "--cmd") or worker.DEFAULTS.get(agent, ""),
        _arg(argv, "--agent-permission-mode") or
        os.environ.get("LANBUS_AGENT_PERMISSION_MODE", ""))
    server = peer.PeerServer(name=name, agent=agent, cwd=_arg(argv, "--cwd") or os.getcwd(),
                             command=command, data_dir=_arg(argv, "--data-dir") or _peer_data_dir(name),
                             host=_arg(argv, "--bind", "0.0.0.0"),
                             port=int(_arg(argv, "--port", "8790") or 8790))
    server.start()
    print(f"[peer:{name}] direct={server.url} peer_id={server.identity.peer_id}", flush=True)
    print(f"[peer:{name}] 本机审批: lanbus inbox --name {name}", flush=True)
    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        server.stop()
    return 0


def _chat(argv):
    if "--to" in argv:
        return _direct_chat(argv, "chat")
    try:
        hub, token = connections.resolve_connection(
            hub=_arg(argv, "--hub"), token=_arg(argv, "--token"))
        if "--list" in argv:
            out = _mcp(hub, token, "tools/call",
                       {"name": "lan_conversations",
                        "arguments": {"limit": int(_arg(argv, "--limit", "20") or 20)}},
                       timeout=60)
            print(json.dumps(out, ensure_ascii=False, indent=2))
            return 0
        envs = [x.strip() for x in (_arg(argv, "--envs") or _arg(argv, "--env") or "").split(",")
                if x.strip()]
        message = _arg(argv, "--message") or _arg(argv, "--prompt") or ""
        cid = _arg(argv, "--conversation") or _arg(argv, "--id")
        if not message:
            print("用法: lanbus chat --message TEXT [--envs ENV_A,ENV_B] [--rounds 2] "
                  "[--conversation ID] [--hub URL] [--json]", file=sys.stderr)
            print("      lanbus chat --list [--hub URL]", file=sys.stderr)
            return 2
        if not cid and not envs:
            envs = _online_envs(hub, token)
        if not cid and len(envs) < 2:
            print(json.dumps({"ok": False, "online_envs": envs,
                              "error": "至少需要 2 个在线 Worker；请启动 Worker 或用 --envs 明确指定"},
                             ensure_ascii=False))
            return 1
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
        out = _mcp(hub, token, "tools/call",
                   {"name": "lan_chat", "arguments": args}, timeout=900)
    except Exception as e:
        print(json.dumps({"ok": False, "error": str(e)}, ensure_ascii=False))
        return 1
    if out.get("error") == "human_confirmation_required":
        out["hint"] = "请在 Hub 上运行 lanbus web 完成远程人工确认后重试"
        print(json.dumps(out, ensure_ascii=False, indent=2))
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
    if cmd == "connect":
        return _connect(rest)
    if cmd == "disconnect":
        return _disconnect(rest)
    if cmd == "ask":
        return _ask_cmd(rest)
    if cmd == "chat":
        return _chat(rest)
    if cmd == "run":
        return _direct_chat(rest, "project")
    if cmd == "peer":
        return _peer_daemon(rest)
    if cmd in ("peers", "inbox", "allow", "deny", "revoke", "peer-ui"):
        return _peer_local(rest, cmd)
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
