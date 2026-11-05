# -*- coding: utf-8 -*-
"""新能力：实时日志、真取消、租约恢复、产物上传、上下文交接、角色 token。"""

import base64
import json
import os
import tempfile
import time
import urllib.error
import urllib.request

import pytest

from bus import (MCP, PY, ROOT, TOKEN, Hub, git_repo, start_worker,
                 stop_worker, wait_envs)

FLEX = os.path.join(ROOT, "tests", "flex_agent.py")


def worker_post(hub, op, payload, token=TOKEN):
    req = urllib.request.Request(
        hub.url + "/worker/" + op,
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json",
                 "Authorization": f"Bearer {token}"},
        method="POST")
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read().decode() or "{}")


def wait_for(fn, timeout=30.0, interval=0.3, what="condition"):
    end = time.time() + timeout
    last = None
    while time.time() < end:
        last = fn()
        if last:
            return last
        time.sleep(interval)
    raise AssertionError(f"timeout waiting for {what}; last={last!r}")


@pytest.fixture(scope="module")
def live(hub):
    work = tempfile.mkdtemp(prefix="lanbus-features-")
    git_repo(work)
    cmd = f'{PY} "{FLEX}" "{{prompt_file}}"'
    proc = start_worker(hub, "win-ui", "windows", work, cmd)
    m = MCP(hub.url + "/mcp")
    m.init()
    envs = wait_envs(m, 1)
    try:
        assert len([w for w in envs["workers"] if w["online"]]) >= 1, envs
        yield m, work
    finally:
        stop_worker(proc)


def test_realtime_logs_while_running(live):
    m, work = live
    r = m.tool("lan_dispatch", env="windows", prompt="SLEEP_SHORT",
               cwd=work, wait_s=0)
    tid = r["task_id"]
    since = 0
    saw_early = False
    saw_tick_running = False
    status = "queued"

    def poll():
        nonlocal since, saw_early, saw_tick_running, status
        out = m.tool("lan_logs", task_id=tid, since=since, wait_s=0)
        status = out["status"]
        for lg in out["logs"]:
            since = max(since, int(lg["seq"]))
            text = lg["text"]
            if "FLEX-EARLY" in text:
                saw_early = True
            if "FLEX-TICK" in text and status == "running":
                saw_tick_running = True
        return saw_early and saw_tick_running

    wait_for(poll, timeout=20, what="streamed early logs")
    aw = m.tool("lan_await", task_ids=[tid], wait_s=60)
    assert aw["results"][0]["status"] == "submitted"
    assert saw_tick_running, "tick 必须在任务结束前就能看到"
    res = m.tool("lan_result", task_id=tid)
    assert "FLEX-EARLY" in res["log_tail"]
    assert res["log_count"] > 0


def test_cancel_really_stops_process_and_prevents_late_submit(live):
    m, work = live
    r = m.tool("lan_dispatch", env="windows", prompt="SLEEP_UNTIL_CANCEL_SHORT",
               cwd=work, wait_s=0)
    tid = r["task_id"]
    wait_for(lambda: "FLEX-READY" in "".join(
        lg["text"] for lg in m.tool("lan_logs", task_id=tid, since=0)["logs"]),
        timeout=20, what="agent start")
    out = m.tool("lan_cancel", task_id=tid, reason="pytest cancel")
    assert out["status"] == "cancelled"

    # 等过 agent 原本会写 late.txt 的时间点，确认进程树真的被杀
    time.sleep(8)
    res = m.tool("lan_result", task_id=tid)
    assert res["status"] == "cancelled"
    assert not os.path.exists(os.path.join(work, "late.txt")),         "取消后 agent 仍写入了 late.txt，说明进程树没被杀干净"

    # worker 进程必须还活着，能继续接下一单
    r2 = m.tool("lan_dispatch", env="windows", prompt="SLEEP_SHORT",
                cwd=work, wait_s=60)
    assert r2["results"][0]["status"] == "submitted"
    m.tool("lan_review", task_id=r2["task_id"], verdict="approve")


def test_context_and_local_context_are_composed(live):
    m, work = live
    ctxdir = tempfile.mkdtemp(prefix="lanbus-ctx-")
    os.makedirs(os.path.join(ctxdir, ".lanbus"), exist_ok=True)
    with open(os.path.join(ctxdir, ".lanbus", "context.md"), "w", encoding="utf-8") as f:
        f.write("LOCAL-CTX-MARKER: 本仓库使用 npm，不要改 lockfile\n")
    marker = "MAIN-CTX-MARKER-" + str(int(time.time()))
    r = m.tool("lan_dispatch", env="windows", prompt="CAPTURE_CONTEXT",
               context="项目背景：" + marker + "；验收：不要提问",
               cwd=ctxdir, wait_s=90)
    tid = r["task_id"]
    assert r["results"][0]["status"] == "submitted"
    res = m.tool("lan_result", task_id=tid)
    assert marker in res["composed_prompt"]
    assert "LOCAL-CTX-MARKER" in res["composed_prompt"]
    seen = open(os.path.join(ctxdir, "seen-prompt.txt"), encoding="utf-8").read()
    assert marker in seen
    assert "LOCAL-CTX-MARKER" in seen
    assert "执行约束" in seen
    m.tool("lan_review", task_id=tid, verdict="approve")


def test_artifact_upload_and_image_content(live):
    m, work = live
    artdir = tempfile.mkdtemp(prefix="lanbus-art-")
    r = m.tool("lan_dispatch", env="windows", prompt="WRITE_ARTIFACTS",
               cwd=artdir,
               artifact_globs=["build.log", "shot.png", "dist/**/*.png"],
               wait_s=90)
    tid = r["task_id"]
    assert r["results"][0]["status"] == "submitted"
    res = m.tool("lan_result", task_id=tid, include_images=False)
    names = {a["name"] for a in res["artifacts"]}
    assert {"build.log", "shot.png", "dist/icon.png"} <= names

    raw = m.call("tools/call", {"name": "lan_result",
                                "arguments": {"task_id": tid}})
    content = raw["result"]["content"]
    imgs = [c for c in content if c.get("type") == "image"]
    assert imgs, content
    assert base64.b64decode(imgs[0]["data"]).startswith(b"\x89PNG")

    # 通用下载接口
    base = m.url[:-len("/mcp")]
    req = urllib.request.Request(base + res["artifact_urls"]["shot.png"],
                                 headers={"Authorization": f"Bearer {TOKEN}"})
    with urllib.request.urlopen(req, timeout=15) as rr:
        assert rr.read().startswith(b"\x89PNG")
        assert rr.headers.get("Content-Type") == "image/png"

    one = m.call("tools/call", {"name": "lan_artifact",
                                "arguments": {"task_id": tid, "name": "build.log"}})
    assert "build ok" in one["result"]["content"][0]["text"]
    img = m.call("tools/call", {"name": "lan_artifact",
                                "arguments": {"task_id": tid, "name": "shot.png"}})
    assert any(c.get("type") == "image" for c in img["result"]["content"])
    m.tool("lan_review", task_id=tid, verdict="approve")


def test_lan_shot_builds_browser_task(live):
    m, work = live
    r = m.tool("lan_shot", env="no-such-browser-env",
               url="http://127.0.0.1:9/", wait_s=0)
    tid = r["task_id"]
    res = m.tool("lan_result", task_id=tid, include_images=False)
    assert res["kind"] == "browser"
    spec = res["browser"]
    assert spec["url"] == "http://127.0.0.1:9/"
    assert ".lanbus-shot.png" in res["artifact_globs"]
    assert res["required_caps"] == ["browser"]
    m.tool("lan_cancel", task_id=tid)


def test_lease_recovery_requeues_lost_task(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    r = m.tool("lan_dispatch", env="recover-env", prompt="x",
               lease_s=5, max_recoveries=3, wait_s=0)
    tid = r["task_id"]
    claimed = worker_post(hub, "claim", {"name": "ghost", "env": "recover-env"})
    assert claimed["task"] and claimed["task"]["id"] == tid
    assert claimed["task"]["claim_token"]

    def requeued():
        res = m.tool("lan_result", task_id=tid, include_images=False)
        return res if res["status"] == "queued" else None

    res = wait_for(requeued, timeout=18, what="lease reaper requeue")
    assert res["recoveries"] >= 1
    assert "自动重排" in (res["review_note"] or "")

    # 网络恢复后，旧 worker 的 outbox 还能把已经跑完的结果补交回来
    old_claim = claimed["task"]["claim_token"]
    late = worker_post(hub, "submit", {
        "name": "ghost", "task_id": tid, "claim_token": old_claim,
        "status": "submitted", "verify_ok": True, "result": "late but valid"})
    assert late.get("ok") and not late.get("ignored"), late
    assert m.tool("lan_result", task_id=tid, include_images=False)["status"] == "submitted"
    m.tool("lan_review", task_id=tid, verdict="fail")


def test_stale_submit_cannot_resurrect_task(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    r = m.tool("lan_dispatch", env="stale-env", prompt="x", wait_s=0)
    tid = r["task_id"]
    claimed = worker_post(hub, "claim", {"name": "worker-a", "env": "stale-env"})
    task = claimed["task"]
    assert task and task["id"] == tid
    claim = task["claim_token"]

    wrong = worker_post(hub, "submit", {
        "name": "worker-b", "task_id": tid, "claim_token": claim,
        "status": "submitted", "verify_ok": True})
    assert wrong.get("ignored") == "worker mismatch"

    stale = worker_post(hub, "submit", {
        "name": "worker-a", "task_id": tid, "claim_token": "bad",
        "status": "submitted", "verify_ok": True})
    assert stale.get("ignored") == "stale claim token"

    ok = worker_post(hub, "submit", {
        "name": "worker-a", "task_id": tid, "claim_token": claim,
        "status": "submitted", "verify_ok": True, "result": "done"})
    assert ok.get("ok") and not ok.get("ignored")
    assert m.tool("lan_result", task_id=tid, include_images=False)["status"] == "submitted"

    dup = worker_post(hub, "submit", {
        "name": "worker-a", "task_id": tid, "claim_token": claim,
        "status": "failed", "verify_ok": False})
    assert dup.get("ignored") == "already submitted"
    assert m.tool("lan_result", task_id=tid, include_images=False)["status"] == "submitted"
    m.tool("lan_review", task_id=tid, verdict="fail")


def test_role_separated_tokens():
    hub = Hub(token="agent-secret", worker_token="worker-secret",
              worker_tokens={"win-ui": "token-win", "wsl-core": "token-wsl"})
    try:
        m = MCP(hub.url + "/mcp", token="agent-secret")
        m.init()

        wm = MCP(hub.url + "/mcp", token="worker-secret")
        with pytest.raises(urllib.error.HTTPError) as e:
            wm.raw("initialize", {"protocolVersion": "2025-06-18"})
        assert e.value.code == 401

        assert worker_post(hub, "heartbeat", {"name": "win-ui"},
                           token="token-win")["ok"]
        assert worker_post(hub, "heartbeat", {"name": "unmapped"},
                           token="worker-secret")["ok"]
        assert worker_post(hub, "heartbeat", {"name": "win-ui"},
                           token="agent-secret")["ok"]
        with pytest.raises(urllib.error.HTTPError) as e:
            worker_post(hub, "heartbeat", {"name": "win-ui"},
                        token="token-wsl")
        assert e.value.code == 401
        with pytest.raises(urllib.error.HTTPError) as e:
            worker_post(hub, "heartbeat", {"name": "win-ui"},
                        token="worker-secret")
        assert e.value.code == 401
        with pytest.raises(urllib.error.HTTPError) as e:
            worker_post(hub, "heartbeat", {"name": "win-ui"}, token="nope")
        assert e.value.code == 401
    finally:
        hub.stop()


def test_worker_restart_forces_immediate_recovery():
    """worker 崩溃重启后，不等租约过期也能通过 previous_task + force 立即重排。"""
    hub = Hub()
    work = tempfile.mkdtemp(prefix="lanbus-restart-")
    git_repo(work)
    cmd = f'{PY} "{FLEX}" "{{prompt_file}}"'
    p1 = p2 = None
    try:
        p1 = start_worker(hub, "win-ui", "windows", work, cmd)
        m = MCP(hub.url + "/mcp")
        m.init()
        wait_envs(m, 1)
        r = m.tool("lan_dispatch", env="windows", prompt="SLEEP_SHORT",
                   cwd=work, lease_s=30, wait_s=0)
        tid = r["task_id"]

        def started():
            out = m.tool("lan_logs", task_id=tid, since=0)
            return out if any("FLEX-EARLY" in x["text"] for x in out["logs"]) else None

        wait_for(started, timeout=20, what="short agent start")
        stop_worker(p1)
        p1 = None
        p2 = start_worker(hub, "win-ui", "windows", work, cmd)
        wait_envs(m, 1)
        aw = m.tool("lan_await", task_ids=[tid], wait_s=60)
        assert aw["results"][0]["status"] == "submitted", aw
        res = m.tool("lan_result", task_id=tid, include_images=False)
        assert res["recoveries"] >= 1
        m.tool("lan_review", task_id=tid, verdict="approve")
    finally:
        if p1:
            stop_worker(p1)
        if p2:
            stop_worker(p2)
        hub.stop()
