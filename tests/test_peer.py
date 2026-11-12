import json
import os
import stat

import pytest

from lanbus import peer


def grant(peer_id="peer-a", mode="pair", **changes):
    value = {
        "grant_id": "grant-a", "peer_id": peer_id, "mode": mode,
        "scope": "chat", "cwds": [], "sign_public": "sign-a",
        "x_public": "x-a", "initiator_nonce": "nonce-a",
        "target_nonce": "nonce-b", "created_at": 1,
    }
    value.update(changes)
    return value


def test_identity_is_stable_and_private(tmp_path):
    data = str(tmp_path / "private" / "peer")
    first = peer.Identity.load(data)
    second = peer.Identity.load(data)
    assert first.peer_id == second.peer_id
    assert first.sign_public_b64 == second.sign_public_b64
    assert first.x_public_b64 == second.x_public_b64
    assert len(first.peer_id) == 32
    if os.name != "nt":
        assert stat.S_IMODE(os.stat(data).st_mode) == 0o700
        assert stat.S_IMODE(os.stat(os.path.join(data, "identity.json")).st_mode) == 0o600


def test_grant_store_persists_pair_but_not_session(tmp_path):
    data = str(tmp_path / "peer")
    store = peer.GrantStore(data)
    store.put_grant(grant())
    store.put_grant(grant(grant_id="temporary", mode="session"))
    assert store.grant("grant-a")["peer_id"] == "peer-a"
    assert store.grant("temporary")["mode"] == "session"
    reloaded = peer.GrantStore(data)
    assert reloaded.grant("grant-a")["scope"] == "chat"
    assert reloaded.grant("temporary") is None
    assert reloaded.revoke("peer-a") is True
    assert reloaded.grant("grant-a") is None


def test_grant_store_rejects_wrong_peer_and_expires_session(tmp_path, monkeypatch):
    store = peer.GrantStore(str(tmp_path / "peer"))
    store.put_grant(grant())
    assert store.grant("grant-a", peer_id="peer-b") is None
    monkeypatch.setattr(peer.time, "time", lambda: 100)
    store.put_grant(grant(grant_id="temporary", mode="session"))
    monkeypatch.setattr(peer.time, "time", lambda: 100 + 1801)
    assert store.grant("temporary") is None


def test_aes_gcm_round_trip_and_tamper_rejection():
    key = b"k" * 32
    packet = peer.encrypt(key, {"message": "hello"}, "aad")
    assert packet["ciphertext"] != "hello"
    assert peer.decrypt(key, packet, "aad") == {"message": "hello"}
    packet["ciphertext"] = packet["ciphertext"][:-2] + "AA"
    with pytest.raises(ValueError):
        peer.decrypt(key, packet, "aad")
    with pytest.raises(ValueError):
        peer.decrypt(key, peer.encrypt(key, {"x": 1}, "aad"), "other")


def running_server(tmp_path, **kwargs):
    options = {"command": "unused"}
    options.update(kwargs)
    server = peer.PeerServer(name="target-agent", agent="fake", cwd=str(tmp_path),
                             data_dir=str(tmp_path / "target"),
                             host="127.0.0.1", port=0, **options)
    server.start()
    return server


def test_pairing_requires_local_approval(tmp_path):
    server = running_server(tmp_path)
    caller = peer.Identity.load(str(tmp_path / "caller"))
    try:
        pending = peer.request_pairing(server.url, caller, "hello", "chat", "")
        assert pending["status"] == "pending"
        request_id = pending["request_id"]
        assert peer.pairing_status(server.url, request_id, caller.peer_id)["status"] == "pending"
        with pytest.raises(Exception):
            peer.local_request(server.port, "/peer/approve", {
                "request_id": request_id, "mode": "pair", "scope": "chat"}, "wrong")
        inbox = peer.local_request(server.port, "/peer/inbox", None, server.approval_secret)
        assert inbox["requests"][0]["message"] == "hello"
        approved = peer.local_request(server.port, "/peer/approve", {
            "request_id": request_id, "mode": "pair", "scope": "chat"}, server.approval_secret)
        assert approved["ok"] is True
        result = peer.pairing_status(server.url, request_id, caller.peer_id)
        assert result["status"] == "approved"
        assert result["grant"]["peer_id"] == caller.peer_id
        assert server.grants.grant(result["grant"]["grant_id"])["mode"] == "pair"
    finally:
        server.stop()


def test_pairing_session_denial_and_source_binding(tmp_path):
    server = running_server(tmp_path)
    caller = peer.Identity.load(str(tmp_path / "caller"))
    other = peer.Identity.load(str(tmp_path / "other"))
    try:
        first = peer.request_pairing(server.url, caller, "hello", "project", str(tmp_path))
        peer.local_request(server.port, "/peer/approve", {
            "request_id": first["request_id"], "mode": "session", "scope": "project",
            "cwd": str(tmp_path)}, server.approval_secret)
        result = peer.pairing_status(server.url, first["request_id"], caller.peer_id)
        assert result["grant"]["mode"] == "session"
        with pytest.raises(RuntimeError, match="不存在"):
            peer.pairing_status(server.url, first["request_id"], other.peer_id)

        second = peer.request_pairing(server.url, caller, "do not run", "chat", "")
        denied = peer.local_request(server.port, "/peer/deny", {
            "request_id": second["request_id"]}, server.approval_secret)
        assert denied["ok"] is True
        assert peer.pairing_status(server.url, second["request_id"], caller.peer_id)["status"] == "denied"
        assert peer.is_loopback("192.168.1.20") is False
        assert peer.is_loopback("127.0.0.1") is True
        assert peer.is_loopback("::1") is True
    finally:
        server.stop()


def approved_grant(server, caller, scope="chat", cwd="", mode="pair"):
    pending = peer.request_pairing(server.url, caller, "test request", scope, cwd)
    peer.local_request(server.port, "/peer/approve", {
        "request_id": pending["request_id"], "mode": mode, "scope": scope,
        "cwd": cwd}, server.approval_secret)
    return peer.pairing_status(server.url, pending["request_id"], caller.peer_id)["grant"]


def test_direct_request_is_encrypted_and_replay_protected(tmp_path, monkeypatch):
    calls = []
    server = running_server(tmp_path, executor=lambda **kw: calls.append(kw) or {
        "ok": True, "reply": "encrypted reply"})
    caller = peer.Identity.load(str(tmp_path / "caller"))
    try:
        grant = approved_grant(server, caller)
        client = peer.DirectClient(server.url, str(tmp_path / "caller"))
        client.save_grant(grant)
        assert client.find_grant(server.identity.peer_id)["grant_id"] == grant["grant_id"]
        result = client.request(grant, "hello", "chat")
        assert result["reply"] == "encrypted reply"
        assert calls[0]["message"] == "hello"
        monkeypatch.setattr(peer.uuid, "uuid4", lambda: type("ID", (), {"hex": "fixed-request"})())
        client.request(grant, "first", "chat")
        with pytest.raises(RuntimeError, match="重复"):
            client.request(grant, "replay", "chat")
        assert calls[-1]["message"] == "first"
    finally:
        server.stop()


def test_direct_scope_cwd_and_revocation(tmp_path):
    calls = []
    server = running_server(tmp_path, executor=lambda **kw: calls.append(kw) or {"ok": True})
    caller = peer.Identity.load(str(tmp_path / "caller"))
    try:
        chat = approved_grant(server, caller)
        client = peer.DirectClient(server.url, str(tmp_path / "caller"))
        with pytest.raises(RuntimeError, match="scope"):
            client.request(chat, "escape", "project", str(tmp_path))
        assert not calls

        project_dir = tmp_path / "project"
        project_dir.mkdir()
        project = approved_grant(server, caller, "project", str(project_dir))
        outside = str(tmp_path / "outside")
        os.mkdir(outside)
        with pytest.raises(RuntimeError, match="工作目录"):
            client.request(project, "escape", "project", outside)
        assert not calls
        assert client.request(project, "test", "project", str(project_dir))["ok"] is True
        assert calls[-1]["cwd"] == os.path.realpath(project_dir)
        peer.local_request(server.port, "/peer/revoke", {"peer_id": caller.peer_id},
                           server.approval_secret)
        with pytest.raises(RuntimeError, match="授权"):
            client.request(project, "after revoke", "project", str(project_dir))
    finally:
        server.stop()


def test_default_executor_runs_fake_agent(tmp_path):
    fake = os.path.join(os.path.dirname(__file__), "fake_agent.py")
    command = f'"{os.sys.executable}" "{fake}" "{{prompt_file}}"'
    server = running_server(tmp_path, command=command)
    caller = peer.Identity.load(str(tmp_path / "caller"))
    try:
        grant = approved_grant(server, caller, "project", str(tmp_path))
        result = peer.DirectClient(server.url, str(tmp_path / "caller")).request(
            grant, "hello fake", "project", str(tmp_path), timeout=30)
        assert result["ok"] is True
        assert "hello fake" in result["reply"]
        assert result["exit_code"] == 0
    finally:
        server.stop()


def test_cli_zero_token_chat_waits_for_target_approval(tmp_path, monkeypatch, capsys):
    import threading
    from lanbus import cli
    server = running_server(tmp_path, executor=lambda **kw: {"ok": True, "reply": "pong"})
    monkeypatch.setenv("LANBUS_PEER_DIR", str(tmp_path / "cli-caller"))
    monkeypatch.delenv("LANBUS_TOKEN", raising=False)
    monkeypatch.delenv("LANBUS_HUB", raising=False)
    result = []
    thread = threading.Thread(target=lambda: result.append(cli.main([
        "chat", "--to", server.url, "--message", "hello", "--timeout", "30"])))
    thread.start()
    try:
        deadline = peer.time.time() + 5
        while peer.time.time() < deadline:
            inbox = peer.local_request(server.port, "/peer/inbox", None, server.approval_secret)
            if inbox["requests"]:
                break
            peer.time.sleep(0.05)
        request_id = inbox["requests"][0]["request_id"]
        assert cli.main(["allow", request_id, "--url", server.url, "--data-dir",
                         str(tmp_path / "target"), "--pair", "--scope", "chat"]) == 0
        thread.join(timeout=10)
        assert not thread.is_alive()
        assert result == [0]
        output = capsys.readouterr().out
        assert "pong" in output
        assert "待确认" in output
        assert cli.main(["chat", "--to", server.url, "--message", "again",
                         "--timeout", "30"]) == 0
    finally:
        server.stop()


def test_cli_inbox_peers_deny_and_revoke(tmp_path, monkeypatch, capsys):
    from lanbus import cli
    server = running_server(tmp_path)
    caller = peer.Identity.load(str(tmp_path / "caller"))
    monkeypatch.setenv("LANBUS_PEER_DIR", str(tmp_path / "caller"))
    try:
        assert cli.main(["peers", "--url", server.url]) == 0
        assert "target-agent" in capsys.readouterr().out
        pending = peer.request_pairing(server.url, caller, "hello", "chat", "")
        base = ["--url", server.url, "--data-dir", str(tmp_path / "target")]
        assert cli.main(["inbox"] + base) == 0
        assert pending["request_id"] in capsys.readouterr().out
        assert cli.main(["deny", pending["request_id"]] + base) == 0
        approved = approved_grant(server, caller)
        assert cli.main(["revoke", caller.peer_id] + base) == 0
        assert server.grants.grant(approved["grant_id"]) is None
    finally:
        server.stop()


def test_discovery_finds_direct_peer(tmp_path):
    from lanbus import discovery
    server = running_server(tmp_path)
    try:
        services = discovery.probe_http("127.0.0.1", server.port, timeout=2)
        direct = next(item for item in services if item["kind"] == "lanbus-peer")
        assert direct["name"] == "target-agent"
        assert direct["peer_id"] == server.identity.peer_id
        assert direct["direct_pairing"] is True
        assert 8790 in discovery.BASE_PORTS
    finally:
        server.stop()


def test_worker_direct_mode_needs_no_hub_or_token(tmp_path):
    import subprocess
    import sys
    from bus import child_env, free_port
    port = free_port()
    env = child_env(str(tmp_path / "worker-runtime"))
    env.pop("LANBUS_TOKEN", None)
    env.pop("LANBUS_HUB", None)
    process = subprocess.Popen([
        sys.executable, "-m", "lanbus", "worker", "--name", "direct-worker",
        "--env", "direct", "--direct", "--direct-port", str(port),
        "--direct-data-dir", str(tmp_path / "direct-worker"),
        "--agent", "shell", "--cmd", "cat", "--cwd", str(tmp_path)],
        env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    try:
        deadline = peer.time.time() + 10
        health = None
        while peer.time.time() < deadline:
            try:
                health = peer.json_request(f"http://127.0.0.1:{port}/peer/health", timeout=1)
                break
            except Exception:
                peer.time.sleep(0.1)
        assert health and health["name"] == "direct-worker"
        assert health["direct_pairing"] is True
    finally:
        process.terminate()
        process.wait(timeout=8)


def test_zero_config_cross_process_chat_with_local_consent(tmp_path):
    import subprocess
    import sys
    from bus import ROOT, child_env, free_port
    port = free_port()
    target_data = tmp_path / "os-target"
    target_env = child_env(str(tmp_path / "runtime-target"))
    target_env.pop("LANBUS_TOKEN", None)
    target_env.pop("LANBUS_HUB", None)
    fake = os.path.join(ROOT, "tests", "fake_agent.py")
    target_log = open(tmp_path / "target.log", "w+", encoding="utf-8")
    target = subprocess.Popen([
        sys.executable, "-m", "lanbus", "worker", "--name", "os-target",
        "--env", "os-target", "--direct", "--direct-port", str(port),
        "--direct-data-dir", str(target_data), "--agent", "shell",
        "--cmd", f'{sys.executable} "{fake}" "{{prompt_file}}"',
        "--cwd", str(tmp_path)], env=target_env, stdout=target_log,
        stderr=subprocess.STDOUT, text=True)
    caller = None
    try:
        deadline = peer.time.time() + 10
        while peer.time.time() < deadline:
            try:
                peer.json_request(f"http://127.0.0.1:{port}/peer/health", timeout=1)
                break
            except Exception:
                peer.time.sleep(0.1)
        caller_env = child_env(str(tmp_path / "runtime-caller"))
        caller_env.pop("LANBUS_TOKEN", None)
        caller_env.pop("LANBUS_HUB", None)
        caller_env["LANBUS_PEER_DIR"] = str(tmp_path / "os-caller")
        caller = subprocess.Popen([
            sys.executable, "-m", "lanbus", "chat", "--to", f"http://127.0.0.1:{port}",
            "--message", "cross process hello", "--timeout", "30"],
            env=caller_env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        secret = json.loads((target_data / "approval.json").read_text(encoding="utf-8"))["secret"]
        while peer.time.time() < deadline:
            inbox = peer.local_request(port, "/peer/inbox", None, secret)
            if inbox["requests"]:
                break
            peer.time.sleep(0.1)
        request_id = inbox["requests"][0]["request_id"]
        approved = subprocess.run([
            sys.executable, "-m", "lanbus", "allow", request_id,
            "--url", f"http://127.0.0.1:{port}", "--data-dir", str(target_data),
            "--pair", "--scope", "chat"], env=caller_env,
            capture_output=True, text=True, timeout=10)
        assert approved.returncode == 0, approved.stdout + approved.stderr
        output, _ = caller.communicate(timeout=20)
        assert caller.returncode == 0, output
        assert "cross process hello" in output
        second = subprocess.run([
            sys.executable, "-m", "lanbus", "chat", "--to", f"http://127.0.0.1:{port}",
            "--message", "second without approval", "--timeout", "30"],
            env=caller_env, capture_output=True, text=True, timeout=20)
        assert second.returncode == 0, second.stdout + second.stderr
        assert "second without approval" in second.stdout
    finally:
        if caller and caller.poll() is None:
            caller.terminate()
            caller.wait(timeout=5)
        target.terminate()
        target.wait(timeout=8)
        target_log.close()


def test_pairing_replay_and_rate_limit(tmp_path):
    server = running_server(tmp_path)
    caller = peer.Identity.load(str(tmp_path / "caller"))
    try:
        payload = {
            "source": {"peer_id": caller.peer_id, "hostname": "caller", "agent": "test",
                       "sign_public": caller.sign_public_b64, "x_public": caller.x_public_b64},
            "message": "signed once", "scope": "chat", "cwd": "",
            "created_at": peer.time.time(), "expires_at": peer.time.time() + 600,
            "nonce": peer._b64(os.urandom(24))}
        payload["signature"] = caller.sign(payload)
        first = peer.json_request(server.url + "/peer/pair", payload)
        peer.local_request(server.port, "/peer/approve", {
            "request_id": first["request_id"], "mode": "pair", "scope": "chat"},
            server.approval_secret)
        with pytest.raises(RuntimeError, match="重复"):
            peer.json_request(server.url + "/peer/pair", payload)
        for index in range(4):
            peer.request_pairing(server.url, caller, f"request {index}", "chat", "")
        with pytest.raises(RuntimeError, match="频率"):
            peer.request_pairing(server.url, caller, "too fast", "chat", "")
    finally:
        server.stop()
