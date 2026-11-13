import os
import threading
import time

import pytest

from lanbus import peer
from lanbus.company import CompanyStore, company_proof
from test_peer import approved_grant, running_server


def test_company_join_once_then_free_member_chat(tmp_path):
    notices = []
    b = running_server(tmp_path / "b", request_policy="company", notifier=notices.append,
                       executor=lambda **kw: {"ok": True, "reply": "B:" + kw["message"]})
    CompanyStore(str(tmp_path / "b" / "target")).create("测试公司")
    b.company = CompanyStore(b.data_dir).load()
    a_client = peer.DirectClient(b.url, str(tmp_path / "a"))
    try:
        result = {}
        errors = []
        def join():
            try:
                result["company"] = a_client.join_company("申请加入", status_timeout=5)
            except Exception as exc:
                errors.append(exc)
        thread = threading.Thread(target=join)
        thread.start()
        deadline = time.time() + 5
        while time.time() < deadline:
            inbox = peer.local_request(b.port, "/peer/inbox", None, b.approval_secret)
            if inbox["requests"]:
                break
            time.sleep(0.05)
        request = inbox["requests"][0]
        assert request["kind"] == "company"
        peer.local_request(b.port, "/peer/approve", {"request_id": request["request_id"]},
                           b.approval_secret)
        thread.join(timeout=6)
        assert not thread.is_alive()
        if errors:
            raise AssertionError(
                f"join 线程异常: {errors[0]!r}; pending={list(b.pending)} "
                f"completed={b.completed}") from errors[0]
        company = result["company"]
        assert company["company_name"] == "测试公司"
        assert company["secret"] not in str(peer.json_request(b.url + "/peer/health"))
        grant = a_client.find_grant(b.identity.peer_id, "chat")
        assert a_client.request(grant, "hello", "chat")["reply"] == "B:hello"
        assert not [n for n in notices if n.get("kind") == "execution"]
    finally:
        b.stop()


def test_company_member_auto_pairs_with_other_member(tmp_path):
    target = running_server(tmp_path / "c", request_policy="company",
                            executor=lambda **kw: {"ok": True, "reply": kw["message"]})
    company = CompanyStore(str(tmp_path / "c" / "target")).create("测试公司")
    target.company = company
    caller = peer.DirectClient(target.url, str(tmp_path / "a"))
    CompanyStore(str(tmp_path / "a")).save(company)
    try:
        health = peer.json_request(target.url + "/peer/health")
        grant = caller.find_company_grant(health, "你好", "chat")
        assert grant["mode"] == "company"
        result = caller.request(grant, "自由交流", "chat")
        assert result["reply"] == "自由交流"
        inbox = peer.local_request(target.port, "/peer/inbox", None, target.approval_secret)
        assert inbox["requests"] == []
    finally:
        target.stop()


def test_wrong_company_proof_cannot_auto_pair(tmp_path):
    target = running_server(tmp_path / "c", request_policy="company")
    target.company = CompanyStore(str(tmp_path / "c" / "target")).create("测试公司")
    caller = peer.DirectClient(target.url, str(tmp_path / "a"))
    bad = dict(target.company, secret="00" * 32)
    try:
        with pytest.raises(RuntimeError, match="公司成员证明无效"):
            peer.request_company(target.url, caller.identity, bad, "冒充", "chat", "")
    finally:
        target.stop()


def test_company_join_has_dedicated_approval_ui():
    root = os.path.dirname(os.path.dirname(__file__))
    with open(os.path.join(root, "src", "lanbus", "static", "peer_approval.html"), encoding="utf-8") as handle:
        python_page = handle.read()
    with open(os.path.join(root, "go", "internal", "peer", "approval.html"), encoding="utf-8") as handle:
        go_page = handle.read()
    assert python_page == go_page
    assert "请求加入公司" in python_page
    assert "批准加入" in python_page
    assert "approveCompany" in python_page
