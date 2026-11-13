import threading
import time

import pytest

from lanbus import peer
from test_peer import approved_grant, running_server


def test_request_v2_requires_approval_and_runs_once(tmp_path):
    calls = []
    notices = []
    server = running_server(tmp_path, display_name="窗口 2", instance_label="frontend",
                            request_policy="each", notifier=lambda notice: notices.append(notice),
                            executor=lambda **kw: calls.append(kw) or {"ok": True, "reply": kw["message"]})
    caller = peer.Identity.load(str(tmp_path / "caller"))
    try:
        grant = approved_grant(server, caller)
        client = peer.DirectClient(server.url, str(tmp_path / "caller"))
        health = peer.json_request(server.url + "/peer/health")
        assert health["display_name"] == "窗口 2"
        assert health["instance_label"] == "frontend"
        assert health["request_policy"] == "each"
        assert "request_v2" in health["approval_features"]

        approved = {}
        def approve_when_pending():
            deadline = time.time() + 5
            while time.time() < deadline:
                inbox = peer.local_request(server.port, "/peer/inbox", None, server.approval_secret)
                execution = [r for r in inbox["requests"] if r.get("kind") == "execution"]
                if execution:
                    approved.update(peer.local_request(server.port, "/peer/approve", {
                        "request_id": execution[0]["request_id"], "kind": "execution"}, server.approval_secret))
                    return
                time.sleep(0.05)
            raise AssertionError("execution approval did not arrive")
        thread = threading.Thread(target=approve_when_pending)
        thread.start()
        try:
            result = client.request(grant, "第二次也要批准", "chat", status_timeout=5)
        finally:
            thread.join(timeout=6)
        assert not thread.is_alive()
        assert result["reply"] == "第二次也要批准"
        assert approved["status"] == "approved"
        assert len(calls) == 1
        assert any(n.get("kind") == "execution" for n in notices)
    finally:
        server.stop()


def test_request_v2_denial_does_not_execute(tmp_path):
    calls = []
    server = running_server(tmp_path, request_policy="each",
                            executor=lambda **kw: calls.append(kw) or {"ok": True})
    caller = peer.Identity.load(str(tmp_path / "caller"))
    try:
        grant = approved_grant(server, caller)
        client = peer.DirectClient(server.url, str(tmp_path / "caller"))
        def deny_when_pending():
            deadline = time.time() + 5
            while time.time() < deadline:
                inbox = peer.local_request(server.port, "/peer/inbox", None, server.approval_secret)
                execution = [r for r in inbox["requests"] if r.get("kind") == "execution"]
                if execution:
                    peer.local_request(server.port, "/peer/deny", {
                        "request_id": execution[0]["request_id"]}, server.approval_secret)
                    return
                time.sleep(0.05)
        thread = threading.Thread(target=deny_when_pending)
        thread.start()
        with pytest.raises(RuntimeError, match="拒绝"):
            client.request(grant, "不允许执行", "chat", status_timeout=5)
        thread.join(timeout=6)
        assert not calls
    finally:
        server.stop()


def test_request_v2_cached_result_and_digest_binding(tmp_path):
    calls = []
    server = running_server(tmp_path, request_policy="each",
                            executor=lambda **kw: calls.append(kw) or {"ok": True, "reply": "once"})
    caller = peer.Identity.load(str(tmp_path / "caller"))
    try:
        grant = approved_grant(server, caller)
        client = peer.DirectClient(server.url, str(tmp_path / "caller"))
        request_id = "cached-request"
        aad = grant["grant_id"] + ":" + request_id
        key = client.identity.session_key(grant["target_x_public"],
                                          grant["initiator_nonce"], grant["target_nonce"])
        body = {"grant_id": grant["grant_id"], "request_id": request_id,
                "request_version": 2,
                "packet": peer.encrypt(key, {"message": "run once", "scope": "chat",
                                             "cwd": "", "timeout": 30}, aad)}
        pending = peer.json_request(server.url + "/peer/request", body)
        assert pending["status"] == "approval_required"
        assert not calls
        peer.local_request(server.port, "/peer/approve", {"request_id": request_id},
                           server.approval_secret)
        first = peer.json_request(server.url + "/peer/request", body)
        second = peer.json_request(server.url + "/peer/request", body)
        assert first == second
        assert peer.decrypt(key, first["packet"], aad)["reply"] == "once"
        assert len(calls) == 1
        changed = dict(body, packet=dict(body["packet"], nonce="AA=="))
        with pytest.raises(RuntimeError, match="内容已改变"):
            peer.json_request(server.url + "/peer/request", changed)
        assert len(calls) == 1
    finally:
        server.stop()
