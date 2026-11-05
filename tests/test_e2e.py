"""端到端：派单 → 执行 → 交回 → 打回 → 重做 → 验收 → 扇出。"""

import os
import tempfile

import pytest

from bus import MCP, ROOT, PY, git_repo, start_worker, stop_worker, wait_envs

FAKE = os.path.join(ROOT, "tests", "fake_agent.py")


def verify_cmd(name="done.txt"):
    return (f'{PY} -c "import os,sys; '
            f"sys.exit(0 if os.path.exists(r'{name}') else 1)\"")


@pytest.fixture(scope="module")
def live(hub):
    work = tempfile.mkdtemp(prefix="lanbus-work-")
    git_repo(work)
    cmd = f'{PY} "{FAKE}" "{{prompt_file}}"'
    procs = [start_worker(hub, "win-ui", "windows", work, cmd),
             start_worker(hub, "mac-ui", "macos", work, cmd)]
    m = MCP(hub.url + "/mcp")
    m.init()
    envs = wait_envs(m, 2)
    try:
        assert len([w for w in envs["workers"] if w["online"]]) >= 2, envs
        yield m, work
    finally:
        for p in procs:
            stop_worker(p)


def test_review_rework_approve_loop(live):
    m, work = live
    # ① 派单：agent 还不写 done.txt，首轮 verify 必须失败
    r = m.tool("lan_dispatch", env="windows", title="改按钮圆角",
               prompt="把 app.txt 的 radius 改成 12px",
               cwd=work, verify=verify_cmd(), wait_s=90)
    tid = r["task_id"]
    assert r["results"][0]["status"] == "submitted"

    res = m.tool("lan_result", task_id=tid)
    assert res["status"] == "submitted"
    assert res["verify_ok"] == 0
    assert res["verify_out"]

    # ② 打回
    rv = m.tool("lan_review", task_id=tid, verdict="rework",
                feedback="REWORK: done.txt 没生成，build 没过")
    assert rv["status"] == "queued" and rv["retry"] == 1

    # ③ 重做回来，这次 verify 通过
    aw = m.tool("lan_await", task_ids=[tid], wait_s=90)
    assert aw["results"][0]["retry"] == 1
    assert aw["results"][0]["status"] == "submitted"
    assert aw["results"][0]["verify_ok"] == 1

    res2 = m.tool("lan_result", task_id=tid)
    assert "done.txt" in res2["changed"]

    # ④ 通过
    assert m.tool("lan_review", task_id=tid, verdict="approve")["status"] == "approved"


def test_rework_cap_leads_to_failed(live):
    """超过 max_retry 后不允许无限重做。"""
    m, work = live
    r = m.tool("lan_dispatch", env="windows", prompt="永不达标的活",
               cwd=work, verify=verify_cmd("never.txt"), max_retry=0, wait_s=90)
    tid = r["task_id"]
    out = m.tool("lan_review", task_id=tid, verdict="rework", feedback="再改")
    assert out["status"] == "failed"
    assert "exhausted" in out.get("reason", "")


def test_fanout_to_two_envs(live):
    m, work = live
    f = m.tool("lan_dispatch_many", wait_s=90, tasks=[
        {"env": "windows", "title": "改样式", "prompt": "REWORK 已修",
         "cwd": work, "verify": verify_cmd()},
        {"env": "macos", "title": "核对截图", "prompt": "REWORK 已修",
         "cwd": work, "verify": verify_cmd()}])
    assert len(f["task_ids"]) == 2
    by_env = {x["env"]: x for x in f["results"]}
    assert set(by_env) == {"windows", "macos"}
    for x in f["results"]:
        assert x["status"] == "submitted"
        assert x["verify_ok"] == 1


def test_task_for_absent_env_stays_queued(live):
    m, work = live
    r = m.tool("lan_dispatch", env="no-such-env", prompt="没人接",
               cwd=work, wait_s=1.5)
    assert r["results"][0]["status"] == "queued"


def test_list_filters(live):
    m, work = live
    approved = m.tool("lan_list", status="approved")["tasks"]
    assert approved and all(t["status"] == "approved" for t in approved)
    win = m.tool("lan_list", env="windows")["tasks"]
    assert win and all(t["env"] == "windows" for t in win)