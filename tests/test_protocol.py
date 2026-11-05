"""协议层：认证、Host 白名单、MCP 握手与工具契约。"""

import json
import urllib.error

import pytest

from bus import MCP, TOKEN


def test_health_needs_no_token(hub):
    body = json.loads(__import__("urllib.request", fromlist=["x"]).urlopen(
        hub.url + "/health", timeout=5).read())
    assert body["ok"] is True


def test_mcp_requires_token(hub):
    m = MCP(hub.url + "/mcp", token="wrong")
    with pytest.raises(urllib.error.HTTPError) as e:
        m.raw("initialize", {"protocolVersion": "2025-06-18"})
    assert e.value.code == 401


def test_mcp_rejects_foreign_host(hub):
    """DNS rebinding 防线：非 IP 字面量且不在白名单的 Host 一律 403。"""
    m = MCP(hub.url + "/mcp")
    req = __import__("urllib.request", fromlist=["x"]).Request(
        hub.url + "/mcp",
        data=json.dumps({"jsonrpc": "2.0", "id": 1, "method": "ping"}).encode(),
        headers={"Content-Type": "application/json", "Host": "evil.example.com",
                 "Authorization": f"Bearer {TOKEN}"}, method="POST")
    with pytest.raises(urllib.error.HTTPError) as e:
        __import__("urllib.request", fromlist=["x"]).urlopen(req, timeout=5)
    assert e.value.code == 403


def test_initialize_and_version(hub):
    m = MCP(hub.url + "/mcp")
    r = m.init()
    assert r["serverInfo"]["name"] == "lanbus"
    assert r["protocolVersion"]
    assert "tools" in r["capabilities"]


def test_tools_list_contract(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    tools = {t["name"]: t for t in m.call("tools/list")["result"]["tools"]}
    assert set(tools) == {"lan_dispatch", "lan_dispatch_many", "lan_await",
                          "lan_result", "lan_review", "lan_list", "lan_envs",
                          "lan_cancel", "lan_interrupt", "lan_logs",
                          "lan_artifact", "lan_shot", "lan_discover", "lan_ask",
                          "lan_chat", "lan_conversations"}
    for t in tools.values():
        assert t["inputSchema"]["type"] == "object"
        assert t["description"].strip()
    assert tools["lan_dispatch"]["inputSchema"]["required"] == ["prompt"]


def test_unknown_tool_is_protocol_error(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    r = m.call("tools/call", {"name": "nope", "arguments": {}})
    assert r["error"]["code"] == -32602


def test_missing_required_argument(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    r = m.call("tools/call", {"name": "lan_dispatch", "arguments": {"env": "windows"}})
    assert r["error"]["code"] == -32602
    assert "prompt" in r["error"]["message"]


def test_unknown_method(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    r = m.call("no/such/method")
    assert r["error"]["code"] == -32601


def test_envs_empty_shape(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    r = m.tool("lan_envs")
    assert "workers" in r and "envs" in r


def test_review_requires_valid_verdict(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    r = m.tool("lan_dispatch", prompt="x", wait_s=0)
    tid = r["task_id"]
    bad = m.tool("lan_review", task_id=tid, verdict="maybe")
    assert "error" in bad
    ok = m.tool("lan_review", task_id=tid, verdict="fail", feedback="abort")
    assert ok["status"] == "failed"

def test_interrupt_cancels_task(hub):
    m = MCP(hub.url + "/mcp")
    m.init()
    tid = m.tool("lan_dispatch", prompt="x", wait_s=0)["task_id"]
    out = m.tool("lan_interrupt", task_id=tid, reason="pytest ctrl-c")
    assert out["status"] == "cancelled"
    assert out.get("ctrl_c_equivalent") is True
