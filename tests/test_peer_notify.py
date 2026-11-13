import sys

from lanbus import peer_notify


def test_notification_commands_never_expose_secret(monkeypatch):
    calls = []
    opened = []
    monkeypatch.setattr(peer_notify.sys, "platform", "darwin")
    peer_notify.notify_pending(
        "http://127.0.0.1:8790/peer/ui", "very-secret", "请求指挥 窗口 2",
        "来自主机 agent", open_page=True,
        runner=lambda *args, **kwargs: calls.append((args, kwargs)),
        opener=lambda url: opened.append(url))
    assert calls
    command = calls[0][0][0]
    assert isinstance(command, list)
    assert "very-secret" not in " ".join(command)
    assert opened == ["http://127.0.0.1:8790/peer/ui#secret=very-secret"]


def test_notification_failure_does_not_raise(monkeypatch):
    monkeypatch.setattr(peer_notify.sys, "platform", "darwin")
    peer_notify.notify_pending(
        "http://127.0.0.1:8790/peer/ui", "secret", "title", "body",
        runner=lambda *args, **kwargs: (_ for _ in ()).throw(OSError("missing")),
        opener=lambda url: False)
