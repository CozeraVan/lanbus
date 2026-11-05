"""多 agent 对话：真实 hub + 两个 fake worker，验证轮转、上下文和 task 链。"""
import os
import tempfile

from bus import MCP, PY, ROOT, start_worker, stop_worker, wait_envs

FAKE = os.path.join(ROOT, "tests", "fake_agent.py")


def test_local_agent_chat(hub):
    work = tempfile.mkdtemp(prefix="lanbus-chat-")
    cmd = f'{PY} "{FAKE}" "{{prompt_file}}"'
    procs = [start_worker(hub, "chat-a", "chat-a", work, cmd),
             start_worker(hub, "chat-b", "chat-b", work, cmd)]
    m = MCP(hub.url + "/mcp")
    m.init()
    try:
        assert len([w for w in wait_envs(m, 2)["workers"] if w["online"]]) >= 2
        out = m.tool("lan_chat", envs=["chat-a", "chat-b"],
                     message="请用一句话介绍你自己", rounds=1,
                     timeout_s=30, total_timeout_s=90)
        assert out["ok"], out
        assert [t["env"] for t in out["turns"]] == ["chat-a", "chat-b"]
        assert all(t["task_id"] and t["status"] == "submitted"
                   for t in out["turns"])
        # 第二个 turn 必须能看到第一个 turn 的回复（fake agent 会回显 prompt）
        assert "chat-a" in out["turns"][1]["reply"]
        cid = out["conversation_id"]
        saved = m.tool("lan_conversations", conversation_id=cid)
        assert saved["conversation_id"] == cid
        assert saved["status"] == "completed"
        assert saved["turn_count"] == 2
        assert len(saved["turns"]) == 2
        listed = m.tool("lan_conversations", limit=10)["conversations"]
        assert any(x["conversation_id"] == cid for x in listed)
        # 续聊：同一 conversation_id 只追加新 turn，保留历史
        more = m.tool("lan_chat", conversation_id=cid,
                      message="继续，用一句话给出结论", rounds=1,
                      timeout_s=30, total_timeout_s=90)
        assert more["ok"], more
        assert more["conversation_id"] == cid
        assert more["start_round"] == 2
        saved2 = m.tool("lan_conversations", conversation_id=cid)
        assert saved2["turn_count"] == 4
        assert [t["round"] for t in saved2["turns"]] == [1, 1, 2, 2]
    finally:
        for p in procs:
            stop_worker(p)
