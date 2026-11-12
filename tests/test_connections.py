import os
import stat

import pytest

from lanbus import connections


def test_normalize_hub():
    assert connections.normalize_hub("192.168.1.236") == "http://192.168.1.236:8788"
    assert connections.normalize_hub("192.168.1.236:8789/") == "http://192.168.1.236:8789"
    assert connections.normalize_hub("https://[::1]:8788/") == "https://[::1]:8788"
    assert connections.normalize_hub("::1") == "http://[::1]:8788"
    with pytest.raises(ValueError):
        connections.normalize_hub("http://host:8788/mcp")


def test_save_resolve_and_remove_per_hub(tmp_path, monkeypatch):
    # 隔离本机真实 ~/.lanbus/token.txt（legacy_token 回退），避免凭据泄漏进断言。
    monkeypatch.setenv("LANBUS_DATA_DIR", str(tmp_path / "empty-data"))
    monkeypatch.delenv("LANBUS_TOKEN", raising=False)
    path = str(tmp_path / "private" / "connections.json")
    connections.save_connection("192.168.1.236", "secret-a", path)
    connections.save_connection("192.168.1.240", "secret-b", path)
    assert connections.resolve_connection(path=path)[0] == "http://192.168.1.240:8788"
    assert connections.resolve_connection("192.168.1.236", path=path) == (
        "http://192.168.1.236:8788", "secret-a")
    assert connections.resolve_connection("192.168.1.241", path=path)[1] == ""
    if os.name != "nt":
        assert stat.S_IMODE(os.stat(path).st_mode) == 0o600
        assert stat.S_IMODE(os.stat(os.path.dirname(path)).st_mode) == 0o700
    assert connections.remove_connection(path=path)
    assert os.path.exists(path)
    assert connections.remove_connection("192.168.1.236", path=path)
    assert not os.path.exists(path)


def test_environment_and_explicit_token_priority(tmp_path, monkeypatch):
    path = str(tmp_path / "connections.json")
    connections.save_connection("host", "stored", path)
    monkeypatch.setenv("LANBUS_TOKEN", "from-env")
    assert connections.resolve_connection("host", path=path)[1] == "from-env"
    assert connections.resolve_connection("host", "explicit", path)[1] == "explicit"


def test_corrupt_store_is_not_overwritten(tmp_path):
    path = tmp_path / "connections.json"
    path.write_text("not-json", encoding="utf-8")
    with pytest.raises(ValueError, match="配置损坏"):
        connections.load_store(str(path))
    assert path.read_text(encoding="utf-8") == "not-json"


def test_select_online_envs():
    response = {"workers": [
        {"env": "a", "online": True}, {"env": "b", "online": False},
        {"env": "a", "online": True}, {"env": "c", "online": True},
        {"env": "d", "online": True}, {"env": "e", "online": True},
        {"env": "f", "online": True}]}
    assert connections.select_online_envs(response) == ["a", "c", "d", "e"]


def test_chat_uses_saved_hub_token_and_online_envs(monkeypatch):
    from lanbus import cli
    seen = {}

    def fake_mcp(hub, token, method, params, **kwargs):
        seen.update(hub=hub, token=token, params=params)
        return {"ok": True, "conversation_id": "c1", "turns": []}

    monkeypatch.setattr(cli, "_mcp", fake_mcp)
    monkeypatch.setattr(connections, "resolve_connection",
                        lambda **kw: ("http://remote:8788", "secret"))
    monkeypatch.setattr(cli, "_online_envs", lambda hub, token: ["mac-a", "win-b"])
    assert cli.main(["chat", "--message", "hello"]) == 0
    assert seen["hub"] == "http://remote:8788"
    assert seen["token"] == "secret"
    assert seen["params"]["arguments"]["envs"] == ["mac-a", "win-b"]


def test_connect_noninteractive_missing_token(monkeypatch, capsys, tmp_path):
    from lanbus import cli
    monkeypatch.setenv("LANBUS_CONNECTIONS_FILE", str(tmp_path / "connections.json"))
    monkeypatch.setenv("LANBUS_DATA_DIR", str(tmp_path / "empty-data"))
    monkeypatch.setattr(cli.sys.stdin, "isatty", lambda: False)
    monkeypatch.setattr(cli, "_health", lambda hub: {"ok": True, "version": "1.9.0-go"})
    monkeypatch.delenv("LANBUS_TOKEN", raising=False)
    assert cli.main(["connect", "192.168.1.236", "--no-save"]) == 1
    assert "agent token" in capsys.readouterr().out


def test_chat_requires_two_online_envs(monkeypatch, capsys):
    from lanbus import cli
    monkeypatch.setattr(connections, "resolve_connection",
                        lambda **kw: ("http://remote:8788", "secret"))
    monkeypatch.setattr(cli, "_online_envs", lambda hub, token: ["only-one"])
    assert cli.main(["chat", "--message", "hello"]) == 1
    assert "在线 Worker" in capsys.readouterr().out


def test_explicit_envs_skip_auto_selection(monkeypatch):
    from lanbus import cli
    seen = {}
    monkeypatch.setattr(connections, "resolve_connection",
                        lambda **kw: ("http://remote:8788", "secret"))
    monkeypatch.setattr(cli, "_online_envs", lambda *a: (_ for _ in ()).throw(AssertionError("auto select")))
    monkeypatch.setattr(cli, "_mcp", lambda hub, token, method, params, **kw: seen.update(params=params) or {
        "ok": True, "conversation_id": "c1", "turns": []})
    assert cli.main(["chat", "--envs", "b,a", "--message", "hello"]) == 0
    assert seen["params"]["arguments"]["envs"] == ["b", "a"]


def test_confirmation_error_has_actionable_hint(monkeypatch, capsys):
    from lanbus import cli
    monkeypatch.setattr(connections, "resolve_connection",
                        lambda **kw: ("http://remote:8788", "secret"))
    monkeypatch.setattr(cli, "_online_envs", lambda *a: ["a", "b"])
    monkeypatch.setattr(cli, "_mcp", lambda *a, **kw: {
        "error": "human_confirmation_required", "target": {"env": "a"}})
    assert cli.main(["chat", "--message", "hello"]) == 1
    assert "lanbus web" in capsys.readouterr().out


def test_connect_status_disconnect_and_discovery(monkeypatch, capsys, tmp_path):
    from lanbus import cli, discovery
    monkeypatch.setenv("LANBUS_CONNECTIONS_FILE", str(tmp_path / "connections.json"))
    monkeypatch.delenv("LANBUS_TOKEN", raising=False)
    monkeypatch.delenv("LANBUS_HUB", raising=False)
    monkeypatch.setattr(cli, "_health", lambda hub: {"ok": True, "version": "1.9.0-go"})
    monkeypatch.setattr(cli, "_online_envs", lambda hub, token: ["a", "b"])
    assert cli.main(["connect", "192.168.1.236", "--token", "top-secret"]) == 0
    assert cli.main(["connect", "--status"]) == 0
    status = capsys.readouterr().out
    assert "top-secret" not in status
    assert '"has_saved_token": true' in status
    assert cli.main(["disconnect"]) == 0
    assert not (tmp_path / "connections.json").exists()

    monkeypatch.setattr(discovery, "discover", lambda args: {"agents": [
        {"kind": "lanbus", "endpoint": "http://192.168.1.236:8788"},
        {"kind": "ollama", "endpoint": "http://192.168.1.236:11434"}]})
    assert cli.main(["connect", "--token", "top-secret", "--no-save"]) == 0
    assert not (tmp_path / "connections.json").exists()
    monkeypatch.setattr(discovery, "discover", lambda args: {"agents": [
        {"kind": "lanbus", "endpoint": "http://192.168.1.236:8788"},
        {"kind": "lanbus", "endpoint": "http://192.168.1.240:8788"}]})
    assert cli.main(["connect", "--token", "top-secret"]) == 2


def test_python_hub_connect_then_zero_config_chat(hub, monkeypatch, capsys, tmp_path):
    import tempfile
    from lanbus import cli
    from bus import MCP, PY, ROOT, start_worker, stop_worker, wait_envs

    monkeypatch.setenv("LANBUS_CONNECTIONS_FILE", str(tmp_path / "connections.json"))
    monkeypatch.delenv("LANBUS_TOKEN", raising=False)
    monkeypatch.delenv("LANBUS_HUB", raising=False)
    fake = os.path.join(ROOT, "tests", "fake_agent.py")
    work = tempfile.mkdtemp(prefix="lanbus-connect-chat-")
    cmd = f'{PY} "{fake}" "{{prompt_file}}"'
    workers = [start_worker(hub, "saved-a", "saved-a", work, cmd),
               start_worker(hub, "saved-b", "saved-b", work, cmd)]
    try:
        wait_envs(MCP(hub.url + "/mcp"), 2)
        assert cli.main(["connect", hub.url, "--token", "test-token"]) == 0
        assert cli.main(["chat", "--message", "互相问好", "--rounds", "1",
                         "--timeout", "30", "--total-timeout", "90"]) == 0
        assert "conversation" in capsys.readouterr().out
    finally:
        for worker in workers:
            stop_worker(worker)


def test_go_hub_connect_then_zero_config_chat(monkeypatch, capsys, tmp_path):
    import shutil
    import tempfile
    from lanbus import cli
    from bus import MCP, PY, ROOT, start_worker, stop_worker, wait_envs
    from test_go_hub import GO_BIN, GoHub

    if not os.path.exists(GO_BIN):
        pytest.skip("build the Go hub first: make go-hub")
    monkeypatch.setenv("LANBUS_CONNECTIONS_FILE", str(tmp_path / "connections.json"))
    monkeypatch.delenv("LANBUS_TOKEN", raising=False)
    monkeypatch.delenv("LANBUS_HUB", raising=False)
    go = GoHub()
    fake = os.path.join(ROOT, "tests", "fake_agent.py")
    work = tempfile.mkdtemp(prefix="lanbus-go-connect-chat-")
    cmd = f'{PY} "{fake}" "{{prompt_file}}"'
    workers = [start_worker(go, "saved-go-a", "saved-go-a", work, cmd),
               start_worker(go, "saved-go-b", "saved-go-b", work, cmd)]
    try:
        wait_envs(MCP(go.url + "/mcp"), 2)
        assert cli.main(["connect", go.url, "--token", "test-token"]) == 0
        assert cli.main(["chat", "--message", "互相问好", "--rounds", "1",
                         "--timeout", "30", "--total-timeout", "90"]) == 0
        assert "conversation" in capsys.readouterr().out
    finally:
        for worker in workers:
            stop_worker(worker)
        go.stop()
        shutil.rmtree(work, ignore_errors=True)
