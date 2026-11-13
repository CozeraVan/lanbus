import pytest

from lanbus import cli


def agents():
    return [
        {"kind": "lanbus-peer", "name": "kimi", "display_name": "Kimi 工作窗",
         "instance_label": "frontend", "hostname": "mac-a", "agent": "kimi",
         "cwd_display": "/work/frontend", "endpoint": "http://192.168.1.10:8790",
         "peer_id": "peer-a"},
        {"kind": "lanbus-peer", "name": "kimi", "display_name": "Kimi 工作窗",
         "instance_label": "backend", "hostname": "mac-a", "agent": "kimi",
         "cwd_display": "/work/backend", "endpoint": "http://192.168.1.10:8791",
         "peer_id": "peer-b"},
    ]


def test_duplicate_peer_name_lists_distinguishing_instances(monkeypatch):
    monkeypatch.setattr(cli.discovery, "discover", lambda options: {"agents": agents()})
    with pytest.raises(ValueError) as error:
        cli._resolve_peer("kimi")
    text = str(error.value)
    assert "frontend" in text and "backend" in text
    assert "/work/frontend" in text and "/work/backend" in text
    assert "http://192.168.1.10:8790" in text and "http://192.168.1.10:8791" in text


def test_unique_instance_label_resolves_without_manual_endpoint(monkeypatch):
    monkeypatch.setattr(cli.discovery, "discover", lambda options: {"agents": agents()})
    assert cli._resolve_peer("backend") == "http://192.168.1.10:8791"
