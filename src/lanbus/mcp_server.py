"""Local MCP stdio bridge to a symmetric Lanbus agent node."""

import json
import os
import sys

from . import discovery
from .company import CompanyStore
from .peer import DirectClient, json_request, pairing_status, request_pairing


TOOLS = [
    ("lanbus_company_list", "列出当前公司及已知成员", {"type": "object", "properties": {}}),
    ("lanbus_agent_discover", "发现局域网 agent 及可区分实例", {"type": "object", "properties": {}}),
    ("lanbus_agent_send", "向公司成员发送交流请求", {"type": "object", "required": ["to", "message"],
        "properties": {"to": {"type": "string"}, "message": {"type": "string"}, "timeout": {"type": "number"}}}),
    ("lanbus_agent_delegate", "向公司成员派发项目任务", {"type": "object", "required": ["to", "message"],
        "properties": {"to": {"type": "string"}, "message": {"type": "string"}, "cwd": {"type": "string"}, "timeout": {"type": "number"}}}),
    ("lanbus_agent_result", "按 request_id 查询申请或执行状态", {"type": "object", "required": ["to", "request_id"],
        "properties": {"to": {"type": "string"}, "request_id": {"type": "string"}}}),
    ("lanbus_company_join", "申请加入目标节点所在公司，仍需目标人工批准", {"type": "object", "required": ["to"],
        "properties": {"to": {"type": "string"}, "message": {"type": "string"}, "timeout": {"type": "number"}}}),
]


class MCPServer:
    def __init__(self, data_dir=""):
        self.data_dir = os.path.abspath(os.path.expanduser(
            data_dir or os.environ.get("LANBUS_PEER_DIR") or
            os.path.join(os.path.expanduser("~"), ".lanbus", "peers", "initiator")))

    def resolve(self, value):
        if value.startswith(("http://", "https://")):
            return value.rstrip("/")
        if ":" in value and value.replace(":", "").replace(".", "").isdigit():
            return "http://" + value
        from .cli import _resolve_peer
        return _resolve_peer(value)

    def send(self, args, scope):
        url = self.resolve(args["to"])
        client = DirectClient(url, self.data_dir)
        health = json_request(url + "/peer/health")
        message = args["message"]
        cwd = args.get("cwd") or ""
        grant = client.find_grant(health["peer_id"], scope) or client.find_company_grant(
            health, message, scope, cwd)
        if not grant:
            pending = request_pairing(url, client.identity, message, scope, cwd)
            return {"status": "pairing_required", "request_id": pending["request_id"],
                    "target": health, "note": "请在目标机器批准配对后重试"}
        result = client.request(grant, message, scope, cwd,
                                timeout=float(args.get("timeout") or 300),
                                status_timeout=float(args.get("timeout") or 300))
        return {"status": "completed", "request_id": client.last_request_id,
                "target": health, "result": result}

    def call(self, name, args):
        args = args or {}
        if name == "lanbus_company_list":
            company = CompanyStore(self.data_dir).load()
            public = dict(company or {})
            public.pop("secret", None)
            return {"joined": bool(company), "company": public}
        if name == "lanbus_agent_discover":
            return discovery.discover({})
        if name in ("lanbus_agent_send", "lanbus_agent_delegate"):
            return self.send(args, "chat" if name == "lanbus_agent_send" else "project")
        if name == "lanbus_agent_result":
            url = self.resolve(args["to"])
            client = DirectClient(url, self.data_dir)
            return pairing_status(url, args["request_id"], client.identity.peer_id)
        if name == "lanbus_company_join":
            url = self.resolve(args["to"])
            client = DirectClient(url, self.data_dir)
            company = client.join_company(args.get("message") or "申请加入公司",
                                          status_timeout=float(args.get("timeout") or 600))
            public = dict(company)
            public.pop("secret", None)
            return {"status": "joined", "company": public}
        raise ValueError("未知工具：" + name)

    def handle(self, request):
        method = request.get("method")
        request_id = request.get("id")
        if method == "notifications/initialized":
            return None
        try:
            if method == "initialize":
                result = {"protocolVersion": "2024-11-05", "capabilities": {"tools": {}},
                          "serverInfo": {"name": "lanbus", "version": "1.0"}}
            elif method == "ping":
                result = {}
            elif method == "tools/list":
                result = {"tools": [{"name": name, "description": desc, "inputSchema": schema}
                                    for name, desc, schema in TOOLS]}
            elif method == "tools/call":
                params = request.get("params") or {}
                value = self.call(params.get("name"), params.get("arguments") or {})
                result = {"content": [{"type": "text", "text": json.dumps(value, ensure_ascii=False)}],
                          "structuredContent": value, "isError": False}
            else:
                return {"jsonrpc": "2.0", "id": request_id,
                        "error": {"code": -32601, "message": "Method not found"}}
            return {"jsonrpc": "2.0", "id": request_id, "result": result}
        except Exception as exc:
            if method == "tools/call":
                value = {"error": str(exc)}
                result = {"content": [{"type": "text", "text": str(exc)}],
                          "structuredContent": value, "isError": True}
                return {"jsonrpc": "2.0", "id": request_id, "result": result}
            return {"jsonrpc": "2.0", "id": request_id,
                    "error": {"code": -32603, "message": str(exc)}}


def read_frame(stream):
    line = stream.readline(4 * 1024 * 1024 + 1)
    if not line:
        return None
    if len(line) > 4 * 1024 * 1024 or not line.endswith(b"\n"):
        raise ValueError("MCP 消息必须是单行 JSON 且不超过 4 MiB")
    return json.loads(line.decode("utf-8"))


def write_frame(stream, value):
    raw = json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    stream.write(raw + b"\n")
    stream.flush()


def main():
    server = MCPServer()
    stream = sys.stdin.buffer
    while True:
        request = read_frame(stream)
        if request is None:
            return
        response = server.handle(request)
        if response is not None:
            write_frame(sys.stdout.buffer, response)


if __name__ == "__main__":
    main()
