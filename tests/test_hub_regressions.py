import json
import sqlite3
import urllib.request

from bus import MCP, TOKEN


def worker_post(hub, op, payload):
    req = urllib.request.Request(
        hub.url + "/worker/" + op,
        data=json.dumps(payload).encode(),
        headers={"Content-Type": "application/json",
                 "Authorization": f"Bearer {TOKEN}"},
        method="POST")
    with urllib.request.urlopen(req, timeout=10) as response:
        return json.loads(response.read().decode() or "{}")


def dispatch(m, **kwargs):
    args = {"prompt": "regression", "wait_s": 0}
    args.update(kwargs)
    return m.tool("lan_dispatch", **args)["task_id"]


def test_explicit_zero_limits_are_preserved(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    tid = dispatch(m, max_retry=0, max_recoveries=0)
    row = m.tool("lan_result", task_id=tid)
    assert row["max_retry"] == 0
    assert row["max_recoveries"] == 0


def test_rework_rejects_non_submitted_task(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    tid = dispatch(m)
    out = m.tool("lan_review", task_id=tid, verdict="rework", feedback="bad")
    assert "error" in out
    assert m.tool("lan_result", task_id=tid)["status"] == "queued"


def test_rework_accumulates_feedback_and_clears_evidence(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    tid = dispatch(m, max_retry=2)
    with sqlite3.connect(hub.data_dir + "/bus.db") as conn:
        conn.execute(
            "UPDATE tasks SET status='submitted', feedback='old', result='stale', "
            "diff='stale', verify_out='stale' WHERE id=?", (tid,))
        conn.execute(
            "INSERT INTO artifacts(task_id,name,relpath,path,mime,size,created_at) "
            "VALUES(?,?,?,?,?,?,?)", (tid, "old.txt", "old.txt", "/old.txt", "text/plain", 1, "now"))
    out = m.tool("lan_review", task_id=tid, verdict="rework", feedback="new")
    assert out["status"] == "queued"
    row = m.tool("lan_result", task_id=tid)
    assert "old" in row["feedback"] and "new" in row["feedback"]
    assert not row["result"] and not row["diff"] and not row["verify_out"]
    assert row["artifacts"] == []


def test_zero_recoveries_fails_first_expired_claim(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    with sqlite3.connect(hub.data_dir + "/bus.db") as conn:
        conn.execute("UPDATE tasks SET status='cancelled' WHERE status='queued'")
    tid = dispatch(m, env="zero-recovery", max_recoveries=0)
    worker_post(hub, "register", {"name": "zero-recovery", "env": "zero-recovery", "caps": []})
    claimed = worker_post(hub, "claim", {"name": "zero-recovery", "env": "zero-recovery"})["task"]
    assert claimed["id"] == tid
    out = worker_post(hub, "recover", {"name": "zero-recovery", "force": True})
    assert out["failed"] == [tid]
    assert out["recovered"] == []
    assert m.tool("lan_result", task_id=tid)["status"] == "failed"


def test_stale_recovery_snapshot_does_not_replace_new_claim(hub):
    import lanbus.hub as python_hub

    m = MCP(hub.url + "/mcp")
    m.init()
    with sqlite3.connect(hub.data_dir + "/bus.db") as conn:
        conn.execute("UPDATE tasks SET status='cancelled' WHERE status='queued'")
    tid = dispatch(m, env="stale-recovery", max_recoveries=2)
    worker_post(hub, "register", {"name": "stale-recovery", "env": "stale-recovery", "caps": []})
    first = worker_post(hub, "claim", {"name": "stale-recovery", "env": "stale-recovery"})["task"]
    assert first["id"] == tid

    python_hub.DB_PATH = hub.data_dir + "/bus.db"
    snapshot = python_hub.q("SELECT * FROM tasks WHERE id=?", (tid,), one=True)
    worker_post(hub, "recover", {"name": "stale-recovery", "force": True})
    second = worker_post(hub, "claim", {"name": "stale-recovery", "env": "stale-recovery"})["task"]
    assert second["claim_token"] != first["claim_token"]

    assert python_hub._recover_task(snapshot, "stale reaper snapshot") is None
    assert m.tool("lan_result", task_id=tid)["status"] == "running"
