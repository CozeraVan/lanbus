"""Phase 2a: Go lanbusd speaks the worker protocol and MCP subset.

The test is skipped when the Go daemon has not been built. Build it with
`make go-hub` before running the Python suite.
"""
import json
import os
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.request

import pytest

from bus import MCP, PY, ROOT, child_env, start_worker, stop_worker, wait_envs

GO_BIN = os.path.join(ROOT, "bin", "lanbusd.exe" if os.name == "nt" else "lanbusd")
FAKE = os.path.join(ROOT, "tests", "fake_agent.py")
TOKEN = "test-token"


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


class GoHub:
    def __init__(self):
        self.data_dir = tempfile.mkdtemp(prefix="lanbus-go-hub-")
        self.port = free_port()
        self.url = f"http://127.0.0.1:{self.port}"
        self.token = TOKEN
        self.log = open(os.path.join(self.data_dir, "hub.log"), "w+", encoding="utf-8")
        self.proc = subprocess.Popen(
            [GO_BIN, "--bind", "127.0.0.1", "--port", str(self.port),
             "--data-dir", self.data_dir, "--token", TOKEN,
             "--worker-token", TOKEN, "--reap-s", "1"],
            stdout=self.log, stderr=subprocess.STDOUT)
        end = time.time() + 10
        while time.time() < end:
            try:
                urllib.request.urlopen(self.url + "/health", timeout=1)
                return
            except Exception:
                time.sleep(0.1)
        raise RuntimeError("go hub failed: " + self.tail())

    def tail(self):
        self.log.flush()
        self.log.seek(0)
        return self.log.read()[-4000:]

    def stop(self):
        try:
            self.proc.terminate()
            self.proc.wait(timeout=5)
        except Exception:
            self.proc.kill()
        try:
            self.log.close()
        except Exception:
            pass
        shutil.rmtree(self.data_dir, ignore_errors=True)


def test_go_hub_worker_mcp_and_artifacts(hub):
    if not os.path.exists(GO_BIN):
        pytest.skip("build the Go hub first: make go-hub")
    go = GoHub()
    work = tempfile.mkdtemp(prefix="lanbus-go-work-")
    cmd = f'{PY} "{FAKE}" "{{prompt_file}}"'
    worker = start_worker(go, "go-win", "go-win", work, cmd)
    m = MCP(go.url + "/mcp", token=TOKEN)
    m.init()
    try:
        envs = wait_envs(m, 1)
        assert any(w["online"] for w in envs["workers"])
        r = m.tool("lan_dispatch", env="go-win", prompt="hello go hub",
                   cwd=work, wait_s=30)
        assert r["results"][0]["status"] == "submitted"
        tid = r["task_id"]
        res = m.tool("lan_result", task_id=tid)
        assert res["status"] == "submitted"
        assert "hello go hub" in res["result"]
        assert m.tool("lan_list", status="submitted")["tasks"]
        assert m.tool("lan_review", task_id=tid, verdict="approve")["status"] == "approved"

        r2 = m.tool("lan_dispatch", env="go-win", prompt="REWORK artifact",
                    cwd=work, artifact_globs=["done.txt"], wait_s=30)
        tid2 = r2["task_id"]
        res2 = m.tool("lan_result", task_id=tid2)
        assert res2["status"] == "submitted"
        art = json.loads(m.call("tools/call", {
            "name": "lan_artifact",
            "arguments": {"task_id": tid2, "name": "done.txt"}})["result"]["content"][0]["text"])
        assert "fixed per review" in (art.get("text") or "")
    finally:
        stop_worker(worker)
        go.stop()
        shutil.rmtree(work, ignore_errors=True)


def test_python_and_go_hub_same_task_state(hub):
    if not os.path.exists(GO_BIN):
        pytest.skip("build the Go hub first: make go-hub")
    go = GoHub()
    work = tempfile.mkdtemp(prefix="lanbus-dual-")
    fake = f'{PY} "{FAKE}" "{{prompt_file}}"'
    py_worker = start_worker(hub, "dual-py", "dual-py", work, fake)
    go_worker = start_worker(go, "dual-go", "dual-go", work, fake)
    mp = MCP(hub.url + "/mcp")
    mg = MCP(go.url + "/mcp", token=TOKEN)
    mp.init()
    mg.init()
    try:
        wait_envs(mp, 1)
        wait_envs(mg, 1)
        rp = mp.tool("lan_dispatch", env="dual-py", prompt="COMPARE",
                     cwd=work, wait_s=30)
        rg = mg.tool("lan_dispatch", env="dual-go", prompt="COMPARE",
                     cwd=work, wait_s=30)
        sp = rp["results"][0]
        sg = rg["results"][0]
        assert sp["status"] == sg["status"] == "submitted"
        assert sp["exit_code"] == sg["exit_code"] == 0
        assert sp["verify_ok"] == sg["verify_ok"] == 1
        rp2 = mp.tool("lan_result", task_id=rp["task_id"])
        rg2 = mg.tool("lan_result", task_id=rg["task_id"])
        assert "COMPARE" in rp2["result"] and "COMPARE" in rg2["result"]
    finally:
        stop_worker(py_worker)
        stop_worker(go_worker)
        go.stop()
        shutil.rmtree(work, ignore_errors=True)


def test_go_hub_chat_conversation_discover_ui(hub):
    if not os.path.exists(GO_BIN):
        pytest.skip("build the Go hub first: make go-hub")
    go = GoHub()
    work = tempfile.mkdtemp(prefix="lanbus-go-tools-")
    fake = f'{PY} "{FAKE}" "{{prompt_file}}"'
    w1 = start_worker(go, "tool-a", "tool-a", work, fake)
    w2 = start_worker(go, "tool-b", "tool-b", work, fake)
    m = MCP(go.url + "/mcp", token=TOKEN)
    m.init()
    try:
        wait_envs(m, 2)
        out = m.tool("lan_chat", envs=["tool-a", "tool-b"],
                     message="用一句话互相问好", rounds=1,
                     timeout_s=30, total_timeout_s=90)
        assert out["ok"], out
        cid = out["conversation_id"]
        assert len(out["turns"]) == 2
        conv = m.tool("lan_conversations", conversation_id=cid)
        assert conv["conversation_id"] == cid
        assert conv["turn_count"] == 2
        assert any(x["conversation_id"] == cid
                   for x in m.tool("lan_conversations")["conversations"])

        rep = m.tool("lan_discover", cidr="192.0.2.0/30", include_local=False,
                     include_processes=False, timeout=0.05, http_timeout=0.2)
        assert rep.get("engine") == "go"
        assert "hosts" in rep

        # UI and authenticated API are served by the Go hub as well.
        with urllib.request.urlopen(go.url + "/ui", timeout=5) as r:
            body = r.read()
            assert r.status == 200 and b"lanbus" in body
        req = urllib.request.Request(go.url + "/api/envs",
                                     headers={"Authorization": "Bearer " + TOKEN})
        with urllib.request.urlopen(req, timeout=5) as r:
            assert r.status == 200

        # Real browser artifact -> image content through the Go hub MCP layer.
        shot = m.tool("lan_shot", env="tool-a", url=go.url + "/ui",
                      out="go-ui.png", wait_s=90)
        sid = shot["task_id"]
        raw = m.call("tools/call", {"name": "lan_result",
                                    "arguments": {"task_id": sid}})
        kinds = [c.get("type") for c in raw["result"]["content"]]
        assert "image" in kinds, kinds
    finally:
        stop_worker(w1)
        stop_worker(w2)
        go.stop()
        shutil.rmtree(work, ignore_errors=True)


def test_remote_target_requires_human_confirmation(hub):
    if not os.path.exists(GO_BIN):
        pytest.skip("build the Go hub first: make go-hub")
    go = GoHub()
    m = MCP(go.url + "/mcp", token=TOKEN)
    m.init()
    try:
        reg = urllib.request.Request(
            go.url + "/worker/register",
            data=json.dumps({
                "name": "remote-fake", "env": "remote-fake", "agent": "claude",
                "hostname": "mac-test-host", "cwd": "/Users/test/test-project",
                "caps": ["claude"], "version": "test", "pid": 1,
            }).encode(),
            headers={"Content-Type": "application/json",
                     "Authorization": "Bearer " + TOKEN}, method="POST")
        with urllib.request.urlopen(reg, timeout=10) as r:
            assert r.status == 200

        envs = m.tool("lan_envs")
        target = next(w for w in envs["workers"] if w["env"] == "remote-fake")
        assert target["remote"] is True
        assert target["cwd"] == "/Users/test/test-project"
        assert target["approved"] is False

        r = m.tool("lan_dispatch", env="remote-fake", prompt="should not dispatch",
                   wait_s=0)
        assert r["status"] == "waiting_confirmation"
        tid = r["task_id"]
        assert r["target"]["workers"][0]["cwd"] == "/Users/test/test-project"

        blocked = m.tool("lan_chat", envs=["remote-fake", "missing-env"],
                         message="blocked", rounds=1)
        assert blocked.get("error") == "human_confirmation_required"

        # The agent token cannot approve; the human/operator token can.
        req = urllib.request.Request(
            go.url + "/confirm/approve",
            data=json.dumps({"env": "remote-fake"}).encode(),
            headers={"Content-Type": "application/json",
                     "Authorization": "Bearer " + TOKEN}, method="POST")
        try:
            urllib.request.urlopen(req, timeout=5)
            raise AssertionError("agent token must not approve remote targets")
        except urllib.error.HTTPError as e:
            assert e.code == 401

        op = open(os.path.join(go.data_dir, "operator-token.txt"),
                  encoding="utf-8").read().strip()
        assert op and op != TOKEN
        req = urllib.request.Request(
            go.url + "/confirm/approve",
            data=json.dumps({"env": "remote-fake", "ttl_s": 1800}).encode(),
            headers={"Content-Type": "application/json",
                     "Authorization": "Bearer " + op}, method="POST")
        with urllib.request.urlopen(req, timeout=10) as r:
            out = json.loads(r.read().decode())
        assert out["ok"] and out["queued"] == 1
        after = m.tool("lan_result", task_id=tid)
        assert after["status"] == "queued"

        req = urllib.request.Request(
            go.url + "/api/confirmations",
            headers={"Authorization": "Bearer " + op})
        with urllib.request.urlopen(req, timeout=10) as r:
            out = json.loads(r.read().decode())
        assert any(t["env"] == "remote-fake" and t["approved"]
                   for t in out["targets"])
        m.tool("lan_cancel", task_id=tid, reason="confirmation test cleanup")
    finally:
        go.stop()
