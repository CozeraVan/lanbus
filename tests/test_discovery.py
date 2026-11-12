# -*- coding: utf-8 -*-
"""无插件侦查：本机进程/端口 + LAN HTTP 指纹 + 直接向发现的 AI 端点提问。"""

import http.server
import json
import os
import socketserver
import sys
import tempfile
import threading
import urllib.parse
import urllib.request

from bus import MCP, PY, ROOT, TOKEN, start_worker, stop_worker, wait_envs

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "src"))
from lanbus import discovery  # noqa: E402


class FakeAI(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, obj, code=200):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/api/tags":
            return self._send({"models": [{"name": "fake-ollama"}]})
        if self.path == "/api/version":
            return self._send({"version": "0.0.1"})
        if self.path == "/v1/models":
            return self._send({"data": [{"id": "fake-openai"}]})
        if self.path == "/health":
            return self._send({"ok": True, "version": "1.2.0", "counts": {}})
        if self.path == "/openapi.json":
            return self._send({"openapi": "3.1.0",
                               "info": {"title": "Fake AI Agent API", "version": "0.1.0"},
                               "paths": {"/api/v1/chat": {"post": {}}}})
        if self.path == "/":
            return self._send({"hello": "lanbus-test"})
        return self._send({"error": "not found"}, 404)

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        data = json.loads(self.rfile.read(n) or b"{}")
        if self.path == "/v1/chat/completions":
            return self._send({"choices": [{"message": {"content": "fake reply"}}]})
        if self.path == "/api/chat":
            return self._send({"message": {"content": "fake ollama reply"}})
        if self.path == "/mcp":
            return self._send({"jsonrpc": "2.0", "id": data.get("id", 1),
                               "result": {"serverInfo": {"name": "fake-mcp",
                                                          "version": "1"}}})
        return self._send({}, 404)


class FakeServer:
    def __init__(self):
        self.httpd = socketserver.ThreadingTCPServer(("127.0.0.1", 0), FakeAI)
        self.httpd.daemon_threads = True
        self.port = self.httpd.server_address[1]
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.httpd.shutdown()
        self.httpd.server_close()


def test_probe_fingerprints_fake_ai():
    srv = FakeServer()
    try:
        svcs = discovery.probe_http("127.0.0.1", srv.port)
        kinds = {s["kind"] for s in svcs}
        assert {"ollama", "openai", "lanbus", "mcp", "ai-api"} <= kinds, svcs
        assert any("fake-openai" in (s.get("models") or []) for s in svcs)
    finally:
        srv.close()


def test_discover_and_ask_without_plugin():
    srv = FakeServer()
    try:
        rep = discovery.discover({
            "cidr": "127.0.0.1/32", "ports": str(srv.port),
            "include_local": False, "include_processes": False,
            "timeout": 0.5,
        })
        assert rep["ok"] and rep["summary"]["hosts_in_scope"] == 1
        assert rep["hosts"][0]["ip"] == "127.0.0.1"
        kinds = {a["kind"] for a in rep["agents"]}
        assert {"ollama", "openai", "lanbus", "mcp", "ai-api"} <= kinds
        out = discovery.ask("127.0.0.1", srv.port, "hello")
        assert out["ok"] and out["reply"] == "fake reply"
        assert out["model"] == "fake-openai"
    finally:
        srv.close()


def test_parse_ports():
    ports = discovery.parse_ports("11434,1234,8000-8002")
    assert 11434 in ports and 1234 in ports
    assert 8000 in ports and 8002 in ports
    assert len(ports) == len(set(ports))


def test_mcp_discover_and_ask(hub):
    srv = FakeServer()
    try:
        m = MCP(hub.url + "/mcp")
        m.init()
        rep = m.tool("lan_discover", cidr="127.0.0.1/32", ports=str(srv.port),
                     include_local=False, include_processes=False, timeout=0.5)
        assert rep["ok"], rep
        assert rep["summary"]["hosts_in_scope"] == 1
        kinds = {a["kind"] for a in rep["agents"]}
        assert "openai" in kinds and "ollama" in kinds

        out = m.tool("lan_ask", host="127.0.0.1", port=srv.port, prompt="hi")
        assert out["ok"] and out["reply"] == "fake reply"

        # REST convenience endpoints for non-MCP scripts/GUI clients
        qs = urllib.parse.urlencode({"cidr": "127.0.0.1/32", "ports": str(srv.port),
                                     "include_local": "false",
                                     "include_processes": "false"})
        req = urllib.request.Request(
            hub.url + "/discover?" + qs,
            headers={"Authorization": f"Bearer {TOKEN}"})
        with urllib.request.urlopen(req, timeout=30) as r:
            rep2 = json.loads(r.read().decode())
        assert rep2["hosts"][0]["ip"] == "127.0.0.1"
        req = urllib.request.Request(
            hub.url + "/ask",
            data=json.dumps({"host": "127.0.0.1", "port": srv.port,
                             "prompt": "hi"}).encode(),
            headers={"Content-Type": "application/json",
                     "Authorization": f"Bearer {TOKEN}"}, method="POST")
        with urllib.request.urlopen(req, timeout=30) as r:
            ask2 = json.loads(r.read().decode())
        assert ask2["ok"] and ask2["reply"] == "fake reply"
    finally:
        srv.close()


def test_remote_worker_discovery(hub):
    """lan_discover(env=...) 让另一个环境扫描它自己的局域网。"""
    srv = FakeServer()
    work = tempfile.mkdtemp(prefix="lanbus-discover-")
    cmd = f'{PY} "{os.path.join(ROOT, "tests", "flex_agent.py")}" "{{prompt_file}}"'
    p = start_worker(hub, "discover-win", "windows", work, cmd,
                     extra_args=["--no-browser"])
    try:
        m = MCP(hub.url + "/mcp")
        m.init()
        wait_envs(m, 1)
        out = m.tool("lan_discover", env="windows", cidr="127.0.0.1/32",
                     ports=str(srv.port), include_local=False,
                     include_processes=False, timeout=0.5, wait_s=60)
        assert out["status"] == "submitted", out
        rep = out.get("report")
        assert rep and rep["hosts"][0]["ip"] == "127.0.0.1", out
        kinds = {a["kind"] for a in rep["agents"]}
        assert {"openai", "ollama"} <= kinds, rep.get("agents")

        ask = m.tool("lan_ask", env="windows", host="127.0.0.1",
                     port=srv.port, prompt="hi", wait_s=60)
        assert ask["status"] == "submitted", ask
        assert ask["reply"] == "fake reply", ask
    finally:
        stop_worker(p)
        srv.close()


def test_process_classification_ignores_shell_false_positive():
    procs = [
        {"pid": 1, "name": "claude.exe", "command": "claude --resume x"},
        {"pid": 2, "name": "bash.exe", "command": "bash -c 'grep claude file'"},
        {"pid": 3, "name": "node.exe",
         "command": "node /usr/lib/@anthropic-ai/claude-code/cli.js"},
        {"pid": 4, "name": "Kimi.exe", "command": "Kimi.exe --hidden"},
        {"pid": 5, "name": "node.exe", "command": "node webpack build"},
    ]
    agents = discovery.detect_local_agents(procs)
    kinds = {a["kind"] for a in agents}
    assert kinds == {"claude", "kimi"}, agents
    claude = next(a for a in agents if a["kind"] == "claude")
    assert 1 in claude["pids"] and 3 in claude["pids"]
    assert 2 not in claude["pids"]


def test_os_guess():
    assert discovery.guess_os([22, 5000, 7000]) == "macos"
    assert discovery.guess_os([135, 445, 3389]) == "windows"
    assert discovery.guess_os([22]) == "unix"


def test_default_ports_include_lanbus_hub():
    assert 8788 in discovery.BASE_PORTS
    assert 8789 in discovery.BASE_PORTS
