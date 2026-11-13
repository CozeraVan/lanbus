import io
import json
import os
import subprocess
import sys

from lanbus.company import CompanyStore
from lanbus.mcp_server import MCPServer, read_frame, write_frame
from test_peer import running_server


def call(server, request_id, name, arguments=None):
    return server.handle({"jsonrpc": "2.0", "id": request_id, "method": "tools/call",
                          "params": {"name": name, "arguments": arguments or {}}})


def test_initialize_and_list_all_agent_company_tools(tmp_path):
    server = MCPServer(str(tmp_path / "caller"))
    initialized = server.handle({"jsonrpc": "2.0", "id": 1, "method": "initialize"})
    assert initialized["result"]["serverInfo"]["name"] == "lanbus"
    tools = server.handle({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
    assert {item["name"] for item in tools["result"]["tools"]} == {
        "lanbus_company_list", "lanbus_agent_discover", "lanbus_agent_send",
        "lanbus_agent_delegate", "lanbus_agent_result", "lanbus_company_join"}


def test_mcp_member_send_completes_and_returns_request_id(tmp_path):
    caller_dir = str(tmp_path / "caller")
    target = running_server(tmp_path / "target", request_policy="company",
                            executor=lambda **kw: {"ok": True, "reply": "echo:" + kw["message"]})
    target.company = CompanyStore(target.data_dir).create("测试公司")
    CompanyStore(caller_dir).save(target.company)
    try:
        response = call(MCPServer(caller_dir), 1, "lanbus_agent_send", {
            "to": target.url, "message": "你好", "timeout": 5})
        assert response["result"]["isError"] is False
        value = response["result"]["structuredContent"]
        assert value["status"] == "completed"
        assert value["result"]["reply"] == "echo:你好"
        assert len(value["request_id"]) == 32
    finally:
        target.stop()


def test_mcp_nonmember_cannot_execute_without_approval(tmp_path):
    target = running_server(tmp_path / "target", request_policy="company")
    try:
        response = call(MCPServer(str(tmp_path / "caller")), 1, "lanbus_agent_send", {
            "to": target.url, "message": "不得执行"})
        value = response["result"]["structuredContent"]
        assert value["status"] == "pairing_required"
        assert value["request_id"]
    finally:
        target.stop()


def test_mcp_stdio_uses_standard_newline_delimited_json(tmp_path):
    env = os.environ.copy()
    env["PYTHONPATH"] = str(os.path.join(os.path.dirname(__file__), "..", "src"))
    env["LANBUS_PEER_DIR"] = str(tmp_path / "caller")
    process = subprocess.Popen([sys.executable, "-m", "lanbus.mcp_server"], env=env,
                               stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    try:
        def send(value):
            process.stdin.write(json.dumps(value).encode("utf-8") + b"\n")
            process.stdin.flush()

        send({"jsonrpc": "2.0", "id": 1, "method": "initialize"})
        initialized = json.loads(process.stdout.readline())
        assert initialized["id"] == 1
        send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        send({"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
        tools = json.loads(process.stdout.readline())
        assert len(tools["result"]["tools"]) == 6
        process.stdin.close()
        assert process.wait(timeout=10) == 0
        assert process.stderr.read() == b""
    finally:
        if process.poll() is None:
            process.kill()
            process.wait(timeout=5)


def test_frame_round_trip_preserves_unicode_on_one_json_line():
    stream = io.BytesIO()
    value = {"jsonrpc": "2.0", "id": 1, "result": {"message": "你好"}}
    write_frame(stream, value)
    assert stream.getvalue().endswith(b"\n")
    assert not stream.getvalue().startswith(b"Content-Length")
    stream.seek(0)
    assert read_frame(stream) == value
    assert read_frame(io.BytesIO(b"")) is None
