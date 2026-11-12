"""Per-hub connection profiles for low-configuration LAN conversations."""

import datetime
import json
import os
import tempfile
from urllib.parse import urlsplit, urlunsplit


def config_path(path=None):
    if path:
        return os.path.abspath(os.path.expanduser(path))
    configured = os.environ.get("LANBUS_CONNECTIONS_FILE", "")
    if configured:
        return os.path.abspath(os.path.expanduser(configured))
    data = os.environ.get("LANBUS_DATA_DIR") or os.path.join(os.path.expanduser("~"), ".lanbus")
    return os.path.join(data, "connections.json")


def normalize_hub(value):
    raw = (value or "").strip()
    if not raw:
        raise ValueError("Hub 地址不能为空")
    if "://" not in raw and raw.count(":") >= 2 and not raw.startswith("["):
        raw = "[" + raw + "]"
    candidate = raw if "://" in raw else "http://" + raw
    parsed = urlsplit(candidate)
    if parsed.scheme not in ("http", "https") or not parsed.hostname:
        raise ValueError("无效 Hub 地址")
    if parsed.path not in ("", "/") or parsed.query or parsed.fragment or parsed.username:
        raise ValueError("Hub 地址只能是协议、主机和可选端口，不能包含路径或用户信息")
    try:
        port = parsed.port or 8788
    except ValueError as exc:
        raise ValueError("无效 Hub 端口") from exc
    host = parsed.hostname
    if ":" in host:
        host = "[" + host + "]"
    return urlunsplit((parsed.scheme, f"{host}:{port}", "", "", ""))


def empty_store():
    return {"version": 1, "default": "", "connections": {}}


def load_store(path=None):
    target = config_path(path)
    if not os.path.exists(target):
        return empty_store()
    try:
        with open(target, encoding="utf-8") as handle:
            store = json.load(handle)
        if not isinstance(store, dict) or not isinstance(store.get("connections"), dict):
            raise ValueError("结构无效")
        if not isinstance(store.get("default", ""), str):
            raise ValueError("默认连接无效")
        store.setdefault("default", "")
        store["version"] = 1
        return store
    except (OSError, ValueError, TypeError, json.JSONDecodeError) as exc:
        raise ValueError(f"配置损坏：{target}；请先移走或修复该文件") from exc


def save_store(store, path=None):
    target = config_path(path)
    parent = os.path.dirname(target)
    os.makedirs(parent, mode=0o700, exist_ok=True)
    if os.name != "nt":
        os.chmod(parent, 0o700)
    fd, temporary = tempfile.mkstemp(prefix=".connections-", dir=parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(store, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
        if os.name != "nt":
            os.chmod(temporary, 0o600)
        os.replace(temporary, target)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    return target


def save_connection(hub, token, path=None):
    normalized = normalize_hub(hub)
    store = load_store(path)
    now = datetime.datetime.now(datetime.timezone.utc).isoformat()
    previous = store["connections"].get(normalized) or {}
    store["connections"][normalized] = {
        "token": token or "",
        "created_at": previous.get("created_at") or now,
        "last_used_at": now,
    }
    store["default"] = normalized
    save_store(store, path)
    return normalized, bool(token)


def remove_connection(hub="", path=None):
    target = config_path(path)
    store = load_store(path)
    normalized = normalize_hub(hub) if hub else store.get("default", "")
    if not normalized or normalized not in store["connections"]:
        return False
    del store["connections"][normalized]
    if store.get("default") == normalized:
        store["default"] = ""
    if store["connections"]:
        save_store(store, path)
    else:
        os.unlink(target)
    return True


def legacy_token():
    data = os.environ.get("LANBUS_DATA_DIR") or os.path.join(os.path.expanduser("~"), ".lanbus")
    try:
        with open(os.path.join(data, "token.txt"), encoding="utf-8") as handle:
            return handle.read().strip()
    except OSError:
        return ""


def resolve_connection(hub="", token="", path=None):
    selected_token = token or os.environ.get("LANBUS_TOKEN", "")
    store = load_store(path)
    selected_hub = hub or os.environ.get("LANBUS_HUB", "") or store.get("default", "")
    if selected_hub:
        normalized = normalize_hub(selected_hub)
    else:
        normalized = "http://127.0.0.1:8788"
    profile = store["connections"].get(normalized) or {}
    return normalized, selected_token or profile.get("token", "") or legacy_token()


def select_online_envs(response, limit=4):
    selected = []
    for worker in (response or {}).get("workers") or []:
        env = (worker.get("env") or "").strip()
        if worker.get("online") and env and env not in selected:
            selected.append(env)
        if len(selected) >= limit:
            break
    return selected
