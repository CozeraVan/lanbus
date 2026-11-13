"""Local desktop notifications for direct-peer approvals."""

import json
import subprocess
import sys
import urllib.parse
import webbrowser


def notify_pending(url, secret, title, body, open_page=True, runner=subprocess.run,
                   opener=webbrowser.open):
    """Notify on the target desktop; approval secrets only enter a URL fragment."""
    page = url + "#secret=" + urllib.parse.quote(secret, safe="")
    try:
        if sys.platform == "darwin":
            script = "display notification %s with title %s sound name \"Glass\"" % (
                json.dumps(str(body)[:240], ensure_ascii=False),
                json.dumps(str(title)[:120], ensure_ascii=False))
            runner(["osascript", "-e", script], check=False, timeout=10,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        elif sys.platform == "win32":
            def ps_quote(value):
                return "'" + str(value).replace("'", "''") + "'"
            script = (
                "[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, "
                "ContentType=WindowsRuntime] | Out-Null; "
                "$x=[Windows.Data.Xml.Dom.XmlDocument]::new(); "
                "$x.LoadXml('<toast><visual><binding template=\"ToastGeneric\">"
                "<text>' + " + ps_quote(str(title)[:120]).replace("&", "&amp;").replace("<", "&lt;") +
                " + '</text><text>' + " +
                ps_quote(str(body)[:240]).replace("&", "&amp;").replace("<", "&lt;") +
                " + '</text></binding></visual></toast>'); "
                "$t=[Windows.UI.Notifications.ToastNotification]::new($x); "
                "[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Lanbus').Show($t)"
            )
            runner(["powershell", "-NoProfile", "-NonInteractive", "-Command", script],
                   check=False, timeout=10, stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL)
        if open_page:
            opener(page)
    except Exception:
        # A headless desktop or denied notification permission must not drop the
        # pending request. The CLI inbox remains available as the fallback.
        pass


def desktop_notifier(open_page=True):
    def notify(notice):
        target = notice.get("target") or {}
        label = target.get("instance_label") or target.get("display_name") or target.get("name")
        action = "请求指挥" if notice.get("kind") == "execution" else "请求配对"
        notify_pending(notice["url"], notice["secret"],
                       f"Lanbus：{action} {label}", notice.get("message") or "",
                       open_page=open_page)
    return notify
