"""测试辅助：起 hub / 起 worker / 说 MCP。"""

import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "src")
TOKEN = "test-token"
PY = sys.executable


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def child_env(data_dir):
    e = dict(os.environ)
    e["PYTHONPATH"] = SRC + (os.pathsep + e["PYTHONPATH"] if e.get("PYTHONPATH") else "")
    e["LANBUS_DATA_DIR"] = data_dir
    e["LANBUS_QUIET"] = "1"
    e["PYTHONUNBUFFERED"] = "1"
    e["PYTHONIOENCODING"] = "utf-8"
    return e


class Hub:
    def __init__(self, token=TOKEN, worker_token=None, extra_env=None,
                 worker_tokens=None):
        self.data_dir = tempfile.mkdtemp(prefix="lanbus-test-")
        self.port = free_port()
        self.url = f"http://127.0.0.1:{self.port}"
        self.token = token
        self.worker_token = worker_token
        self.log = open(os.path.join(self.data_dir, "hub.log"), "w+", encoding="utf-8")
        args = [PY, "-m", "lanbus", "hub", "--port", str(self.port),
                "--bind", "127.0.0.1", "--token", token,
                "--reap-s", "1"]
        if worker_token:
            args += ["--worker-token", worker_token]
        if worker_tokens:
            args += ["--worker-tokens", json.dumps(worker_tokens)]
        env = child_env(self.data_dir)
        env.update(extra_env or {})
        self.proc = subprocess.Popen(
            args, env=env, stdout=self.log, stderr=subprocess.STDOUT)
        if not self.wait():
            self.stop()
            raise RuntimeError("hub 未就绪: " + self.tail())

    def wait(self, t=15):
        end = time.time() + t
        while time.time() < end:
            try:
                urllib.request.urlopen(self.url + "/health", timeout=2)
                return True
            except Exception:
                time.sleep(0.2)
        return False

    def tail(self, n=4000):
        try:
            self.log.flush()
            self.log.seek(0)
            return self.log.read()[-n:]
        except Exception:
            return ""

    def stop(self):
        try:
            self.proc.terminate()
            self.proc.wait(timeout=8)
        except Exception:
            self.proc.kill()
        try:
            self.log.close()
        except Exception:
            pass


def start_worker(hub, name, env, cwd, cmd, token=None, extra_args=None):
    log = open(os.path.join(hub.data_dir, f"worker-{name}.log"), "w+", encoding="utf-8")
    args = [PY, "-m", "lanbus", "worker", "--name", name, "--env", env,
            "--hub", hub.url, "--token", token or TOKEN, "--cwd", cwd,
            "--agent", "shell", "--cmd", cmd]
    args += list(extra_args or [])
    p = subprocess.Popen(
        args, env=child_env(hub.data_dir), stdout=log, stderr=subprocess.STDOUT)
    p._log = log
    return p


def stop_worker(p):
    try:
        p.terminate()
        p.wait(timeout=8)
    except Exception:
        p.kill()
    try:
        p._log.close()
    except Exception:
        pass


class MCP:
    """最小 MCP 客户端：stateless streamable-HTTP。"""

    def __init__(self, url, token=TOKEN):
        self.url, self.token, self.id = url, token, 0

    def raw(self, method, params=None, notify=False, token=None, host=None):
        self.id += 1
        body = {"jsonrpc": "2.0", "method": method, "params": params or {}}
        if not notify:
            body["id"] = self.id
        headers = {"Content-Type": "application/json",
                   "Accept": "application/json, text/event-stream",
                   "Authorization": f"Bearer {token or self.token}"}
        req = urllib.request.Request(self.url, data=json.dumps(body).encode(),
                                     headers=headers, method="POST")
        with urllib.request.urlopen(req, timeout=300) as r:
            raw = r.read().decode()
            return r.status, (json.loads(raw) if raw else None)

    def call(self, method, params=None, notify=False):
        return self.raw(method, params, notify)[1]

    def tool(self, name, **kw):
        r = self.call("tools/call", {"name": name, "arguments": kw})
        if "error" in r:
            raise AssertionError(f"tool error: {r['error']}")
        return json.loads(r["result"]["content"][0]["text"])

    def init(self):
        r = self.call("initialize", {"protocolVersion": "2025-06-18",
                                     "capabilities": {}, "clientInfo": {"name": "pytest"}})
        self.call("notifications/initialized", notify=True)
        return r["result"]


def git_repo(path):
    for c in ("git init -q", "git config user.email t@t", "git config user.name t"):
        subprocess.run(c, shell=True, cwd=path, capture_output=True)
    with open(os.path.join(path, "app.txt"), "w", encoding="utf-8") as f:
        f.write("login button radius: 4px\n")
    subprocess.run("git add -A && git commit -qm init", shell=True, cwd=path, capture_output=True)


def wait_envs(m, n=2, t=20):
    end = time.time() + t
    last = {}
    while time.time() < end:
        last = m.tool("lan_envs")
        if len([w for w in last["workers"] if w["online"]]) >= n:
            return last
        time.sleep(0.2)
    return last