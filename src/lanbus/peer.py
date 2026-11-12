"""Zero-configuration, locally approved direct agent peering."""

import base64
import hashlib
import json
import os
import tempfile
import threading
import time

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ed25519, x25519
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.kdf.hkdf import HKDF

SESSION_IDLE_S = 1800


def _b64(data):
    return base64.b64encode(data).decode("ascii")


def _unb64(value):
    return base64.b64decode(value.encode("ascii"), validate=True)


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True,
                      separators=(",", ":")).encode("utf-8")


def _atomic_json(path, value, private=False):
    parent = os.path.dirname(path)
    os.makedirs(parent, mode=0o700, exist_ok=True)
    if os.name != "nt":
        os.chmod(parent, 0o700)
    fd, temporary = tempfile.mkstemp(prefix=".peer-", dir=parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(value, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
        if os.name != "nt" and private:
            os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def _public_raw(public):
    return public.public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)


def _private_pem(private):
    return private.private_bytes(serialization.Encoding.PEM,
                                 serialization.PrivateFormat.PKCS8,
                                 serialization.NoEncryption()).decode("ascii")


class Identity:
    def __init__(self, signing, exchange):
        self.signing = signing
        self.exchange = exchange
        self.sign_public_b64 = _b64(_public_raw(signing.public_key()))
        self.x_public_b64 = _b64(_public_raw(exchange.public_key()))
        self.peer_id = hashlib.sha256(_unb64(self.sign_public_b64)).hexdigest()[:32]

    @classmethod
    def load(cls, data_dir):
        path = os.path.join(data_dir, "identity.json")
        try:
            with open(path, encoding="utf-8") as handle:
                saved = json.load(handle)
            signing = serialization.load_pem_private_key(saved["signing"].encode(), password=None)
            exchange = serialization.load_pem_private_key(saved["exchange"].encode(), password=None)
            if not isinstance(signing, ed25519.Ed25519PrivateKey) or not isinstance(exchange, x25519.X25519PrivateKey):
                raise ValueError("wrong key type")
            return cls(signing, exchange)
        except FileNotFoundError:
            pass
        except Exception as exc:
            raise ValueError(f"直连身份文件损坏：{path}") from exc
        identity = cls(ed25519.Ed25519PrivateKey.generate(), x25519.X25519PrivateKey.generate())
        _atomic_json(path, {"version": 1,
                            "signing": _private_pem(identity.signing),
                            "exchange": _private_pem(identity.exchange)}, private=True)
        return identity

    def sign(self, payload):
        return _b64(self.signing.sign(canonical(payload)))

    @staticmethod
    def verify(public_b64, payload, signature):
        try:
            public = ed25519.Ed25519PublicKey.from_public_bytes(_unb64(public_b64))
            public.verify(_unb64(signature), canonical(payload))
            return True
        except Exception:
            return False

    def session_key(self, peer_x_public, initiator_nonce, target_nonce):
        shared = self.exchange.exchange(x25519.X25519PublicKey.from_public_bytes(
            _unb64(peer_x_public)))
        return HKDF(algorithm=hashes.SHA256(), length=32,
                    salt=hashlib.sha256(_unb64(initiator_nonce) + _unb64(target_nonce)).digest(),
                    info=b"lanbus-direct-v1").derive(shared)


class GrantStore:
    def __init__(self, data_dir):
        self.path = os.path.join(data_dir, "grants.json")
        self.lock = threading.RLock()
        self.memory = {}
        try:
            with open(self.path, encoding="utf-8") as handle:
                self.saved = json.load(handle)
            if not isinstance(self.saved, dict):
                raise ValueError("not an object")
        except FileNotFoundError:
            self.saved = {}
        except Exception as exc:
            raise ValueError(f"直连授权文件损坏：{self.path}") from exc

    def put_grant(self, value):
        with self.lock:
            item = dict(value)
            item["last_used"] = time.time()
            if item.get("mode") == "session":
                self.memory[item["grant_id"]] = item
            else:
                self.saved[item["grant_id"]] = item
                _atomic_json(self.path, self.saved, private=True)

    def grant(self, grant_id, peer_id=""):
        with self.lock:
            item = self.memory.get(grant_id) or self.saved.get(grant_id)
            if not item or (peer_id and item.get("peer_id") != peer_id):
                return None
            if item.get("mode") == "session" and time.time() - float(
                    item.get("last_used") or item.get("created_at") or 0) > SESSION_IDLE_S:
                self.memory.pop(grant_id, None)
                return None
            item["last_used"] = time.time()
            return dict(item)

    def revoke(self, peer_id):
        removed = False
        with self.lock:
            for grant_id in list(self.memory):
                if self.memory[grant_id].get("peer_id") == peer_id:
                    del self.memory[grant_id]
                    removed = True
            for grant_id in list(self.saved):
                if self.saved[grant_id].get("peer_id") == peer_id:
                    del self.saved[grant_id]
                    removed = True
            if removed:
                _atomic_json(self.path, self.saved, private=True)
            return removed

    def list_grants(self):
        with self.lock:
            return [dict(item) for item in list(self.saved.values()) + list(self.memory.values())]


def encrypt(key, payload, aad):
    nonce = os.urandom(12)
    return {"nonce": _b64(nonce),
            "ciphertext": _b64(AESGCM(key).encrypt(nonce, canonical(payload),
                                                   aad.encode("utf-8")))}


def decrypt(key, packet, aad):
    try:
        raw = AESGCM(key).decrypt(_unb64(packet["nonce"]),
                                  _unb64(packet["ciphertext"]), aad.encode("utf-8"))
        value = json.loads(raw.decode("utf-8"))
        if not isinstance(value, dict):
            raise ValueError("payload must be an object")
        return value
    except Exception as exc:
        raise ValueError("直连请求解密或认证失败") from exc


# HTTP pairing service
import ipaddress
import socket
import urllib.error
import urllib.request
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MAX_BODY = 64 * 1024
PENDING_TTL_S = 600


def is_loopback(address):
    try:
        return ipaddress.ip_address(address).is_loopback
    except ValueError:
        return False


def json_request(url, payload=None, headers=None, timeout=30):
    data = None if payload is None else canonical(payload)
    request = urllib.request.Request(url, data=data, headers={
        "Content-Type": "application/json", **(headers or {})},
        method="POST" if data is not None else "GET")
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return json.loads(response.read().decode("utf-8") or "{}")
    except urllib.error.HTTPError as exc:
        try:
            detail = json.loads(exc.read().decode("utf-8"))
        except Exception:
            detail = {"error": str(exc)}
        raise RuntimeError(detail.get("error") or str(exc)) from exc


def local_request(port, path, payload, secret):
    return json_request(f"http://127.0.0.1:{int(port)}{path}", payload,
                        {"X-Lanbus-Approval": secret}, timeout=30)


def request_pairing(url, identity, message, scope, cwd):
    payload = {
        "source": {"peer_id": identity.peer_id, "hostname": socket.gethostname(),
                   "agent": "lanbus-cli", "sign_public": identity.sign_public_b64,
                   "x_public": identity.x_public_b64},
        "message": message, "scope": scope, "cwd": cwd,
        "created_at": time.time(), "expires_at": time.time() + PENDING_TTL_S,
        "nonce": _b64(os.urandom(24)),
    }
    payload["signature"] = identity.sign(payload)
    return json_request(url.rstrip("/") + "/peer/pair", payload)


def pairing_status(url, request_id, peer_id):
    return json_request(url.rstrip("/") + "/peer/pair/status?id=" +
                        urllib.parse.quote(request_id) + "&peer_id=" +
                        urllib.parse.quote(peer_id))


def _primary_lan_ip():
    """尽力获取主网卡 LAN IPv4（UDP connect 不发包）；离线时返回空串。"""
    try:
        probe = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        try:
            probe.connect(("10.255.255.255", 1))
            return probe.getsockname()[0]
        finally:
            probe.close()
    except OSError:
        return ""


class PeerServer:
    def __init__(self, name, agent, cwd, command, data_dir, host="0.0.0.0", port=8790,
                 executor=None):
        self.name = name
        self.agent = agent
        self.cwd = os.path.realpath(cwd or os.getcwd())
        self.command = command
        self.executor = executor
        self.data_dir = os.path.abspath(data_dir)
        self.identity = Identity.load(self.data_dir)
        self.grants = GrantStore(self.data_dir)
        self.pending = {}
        self.completed = {}
        self.seen_requests = set()
        self.pair_nonces = set()
        self.rate = {}
        self.lock = threading.RLock()
        secret_path = os.path.join(self.data_dir, "approval.json")
        try:
            with open(secret_path, encoding="utf-8") as handle:
                self.approval_secret = json.load(handle)["secret"]
        except FileNotFoundError:
            self.approval_secret = uuid.uuid4().hex + uuid.uuid4().hex
            _atomic_json(secret_path, {"secret": self.approval_secret}, private=True)
        self.bind_host = host
        self.http = ThreadingHTTPServer((host, int(port)), self._handler())
        self.http.daemon_threads = True
        self.port = self.http.server_address[1]
        self.url = f"http://127.0.0.1:{self.port}"
        self.thread = threading.Thread(target=self.http.serve_forever,
                                       name=f"peer-{name}", daemon=True)

    def start(self):
        _atomic_json(os.path.join(self.data_dir, "peer.json"),
                     {"name": self.name, "port": self.port, "url": self.url,
                      "peer_id": self.identity.peer_id})
        self.thread.start()
        return self

    def stop(self):
        self.http.shutdown()
        self.http.server_close()
        self.thread.join(timeout=5)

    def _public_peer(self, host_header=None):
        return {"name": self.name, "agent": self.agent, "hostname": socket.gethostname(),
                "peer_id": self.identity.peer_id, "direct_pairing": True,
                "endpoint": self._advertised_endpoint(host_header),
                "scopes": ["chat", "project"]}

    def _advertised_endpoint(self, host_header=None):
        # 绑定通配地址时通告 LAN 可达端点：优先调用方实际使用的 Host，
        # 否则主网卡 IP，避免远端客户端拿到无用的 127.0.0.1。
        if self.bind_host not in ("0.0.0.0", "::", ""):
            return self.url
        candidate = (host_header or "").strip()
        host = candidate
        if host.startswith("["):
            host = host.split("]")[0].lstrip("[")
        elif host.count(":") == 1:
            host = host.rsplit(":", 1)[0]
        host = host.lower()
        if host and host != "localhost" and not host.startswith("127.") and host != "::1":
            return f"http://{candidate}"
        ip = _primary_lan_ip()
        if ip:
            return f"http://{ip}:{self.port}"
        return self.url

    def _expire(self):
        cutoff = time.time()
        for request_id in list(self.pending):
            if float(self.pending[request_id].get("expires_at") or 0) < cutoff:
                self.completed[request_id] = {"status": "expired",
                                               "peer_id": self.pending[request_id]["source"]["peer_id"]}
                del self.pending[request_id]

    def _pair(self, payload):
        source = payload.get("source") or {}
        signed = {key: value for key, value in payload.items() if key != "signature"}
        expected = hashlib.sha256(_unb64(source.get("sign_public") or "")).hexdigest()[:32]
        if expected != source.get("peer_id") or not Identity.verify(
                source.get("sign_public") or "", signed, payload.get("signature") or ""):
            return 400, {"error": "无效的来源身份或签名"}
        if payload.get("scope") not in ("chat", "project"):
            return 400, {"error": "scope 必须是 chat 或 project"}
        if not str(payload.get("message") or "").strip():
            return 400, {"error": "请求摘要不能为空"}
        if float(payload.get("expires_at") or 0) < time.time() or float(
                payload.get("created_at") or 0) > time.time() + 30:
            return 400, {"error": "配对申请已过期"}
        with self.lock:
            self._expire()
            nonce = payload.get("nonce")
            if nonce in self.pair_nonces:
                return 409, {"error": "重复配对申请"}
            now = time.time()
            recent = [stamp for stamp in self.rate.get(source["peer_id"], [])
                      if now - stamp < 10]
            if len(recent) >= 5:
                return 429, {"error": "配对申请频率过高，请稍后重试"}
            recent.append(now)
            self.rate[source["peer_id"]] = recent
            source_pending = [item for item in self.pending.values()
                              if item["source"]["peer_id"] == source["peer_id"]]
            if len(source_pending) >= 8 or len(self.pending) >= 64:
                return 429, {"error": "待确认请求过多，请稍后重试"}
            self.pair_nonces.add(nonce)
            if len(self.pair_nonces) > 1024:
                self.pair_nonces = set(list(self.pair_nonces)[-512:])
            request_id = uuid.uuid4().hex[:16]
            self.pending[request_id] = signed
        print(f"[peer:{self.name}] 待确认 {request_id}: {source.get('hostname')} "
              f"{source.get('agent')} scope={payload['scope']} message={payload['message'][:120]}",
              flush=True)
        return 202, {"status": "pending", "request_id": request_id,
                     "target": self._public_peer(), "expires_in": PENDING_TTL_S}

    def _status(self, query):
        request_id = query.get("id", [""])[0]
        peer_id = query.get("peer_id", [""])[0]
        with self.lock:
            self._expire()
            if request_id in self.pending:
                if self.pending[request_id]["source"]["peer_id"] != peer_id:
                    return 200, {"status": "pending"}
                return 200, {"status": "pending", "request_id": request_id}
            result = self.completed.get(request_id)
            if not result or result.get("peer_id") != peer_id:
                return 404, {"error": "配对申请不存在"}
            return 200, result

    def _local(self, handler, path, payload):
        if not is_loopback(handler.client_address[0]) or handler.headers.get(
                "X-Lanbus-Approval") != self.approval_secret:
            return 403, {"error": "只能在目标机器本机审批"}
        if path == "/peer/inbox":
            with self.lock:
                self._expire()
                requests = [dict(item, request_id=request_id)
                            for request_id, item in self.pending.items()]
            return 200, {"requests": requests, "grants": self.grants.list_grants()}
        request_id = (payload or {}).get("request_id", "")
        if path == "/peer/revoke":
            removed = self.grants.revoke((payload or {}).get("peer_id", ""))
            return 200, {"ok": removed}
        with self.lock:
            pending = self.pending.pop(request_id, None)
        if not pending:
            return 404, {"error": "配对申请不存在或已处理"}
        source = pending["source"]
        if path == "/peer/deny":
            self.completed[request_id] = {"status": "denied", "peer_id": source["peer_id"]}
            return 200, {"ok": True, "status": "denied"}
        if path != "/peer/approve":
            return 404, {"error": "未知审批操作"}
        mode = payload.get("mode")
        scope = payload.get("scope")
        if mode not in ("pair", "session") or scope not in ("chat", "project"):
            return 400, {"error": "请选择 pair/session 和 chat/project"}
        if scope == "project" and pending["scope"] != "project":
            return 400, {"error": "不能把仅交流申请提升为项目任务"}
        cwd = os.path.realpath(payload.get("cwd") or pending.get("cwd") or self.cwd)
        if scope == "project" and not os.path.isdir(cwd):
            return 400, {"error": "授权工作目录不存在"}
        grant = {
            "grant_id": uuid.uuid4().hex, "peer_id": source["peer_id"],
            "target_peer_id": self.identity.peer_id, "mode": mode, "scope": scope,
            "cwds": [cwd] if scope == "project" else [],
            "sign_public": source["sign_public"], "x_public": source["x_public"],
            "target_sign_public": self.identity.sign_public_b64,
            "target_x_public": self.identity.x_public_b64,
            "initiator_nonce": pending["nonce"], "target_nonce": _b64(os.urandom(24)),
            "created_at": time.time(),
        }
        grant["target_signature"] = self.identity.sign(
            {key: value for key, value in grant.items() if key != "target_signature"})
        self.grants.put_grant(grant)
        self.completed[request_id] = {"status": "approved", "peer_id": source["peer_id"],
                                      "grant": grant}
        return 200, {"ok": True, "status": "approved", "grant": grant}

    def _handler(self):
        server = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, fmt, *args):
                return

            def send(self, status, value):
                raw = json.dumps(value, ensure_ascii=False).encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", "application/json; charset=utf-8")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def body(self):
                length = int(self.headers.get("Content-Length") or 0)
                if length <= 0 or length > MAX_BODY:
                    raise ValueError("请求体为空或超过 64 KiB")
                value = json.loads(self.rfile.read(length).decode("utf-8"))
                if not isinstance(value, dict):
                    raise ValueError("JSON 必须是对象")
                return value

            def do_GET(self):
                parsed = urllib.parse.urlsplit(self.path)
                if parsed.path == "/peer/health":
                    status, value = 200, server._public_peer(self.headers.get("Host"))
                elif parsed.path == "/peer/ui":
                    if not is_loopback(self.client_address[0]):
                        status, value = 403, {"error": "审批页面只能在本机打开"}
                    else:
                        html_path = os.path.join(os.path.dirname(__file__),
                                                 "static", "peer_approval.html")
                        with open(html_path, encoding="utf-8") as handle:
                            raw = handle.read().encode("utf-8")
                        self.send_response(200)
                        self.send_header("Content-Type", "text/html; charset=utf-8")
                        self.send_header("Cache-Control", "no-store")
                        self.send_header("Content-Length", str(len(raw)))
                        self.end_headers()
                        self.wfile.write(raw)
                        return
                elif parsed.path == "/peer/pair/status":
                    status, value = server._status(urllib.parse.parse_qs(parsed.query))
                elif parsed.path == "/peer/inbox":
                    status, value = server._local(self, parsed.path, None)
                else:
                    status, value = 404, {"error": "not found"}
                self.send(status, value)

            def do_POST(self):
                try:
                    payload = self.body()
                    parsed = urllib.parse.urlsplit(self.path)
                    if parsed.path == "/peer/pair":
                        status, value = server._pair(payload)
                    elif parsed.path == "/peer/request":
                        status, value = server.direct_request(payload)
                    elif parsed.path in ("/peer/approve", "/peer/deny", "/peer/revoke"):
                        status, value = server._local(self, parsed.path, payload)
                    else:
                        status, value = 404, {"error": "not found"}
                except Exception as exc:
                    status, value = 400, {"error": str(exc)}
                self.send(status, value)

        return Handler


def _peer_execute(self, message, scope, cwd, timeout=300):
    if self.executor:
        return self.executor(message=message, scope=scope, cwd=cwd, timeout=timeout)
    from .worker import run_streaming
    cwd = os.path.realpath(cwd or self.cwd)
    if self.agent == "echo" and not self.command:
        return {"ok": True, "exit_code": 0, "reply": message, "peer": self.name}
    fd, prompt_file = tempfile.mkstemp(prefix="lanbus-peer-", suffix=".md", text=True)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(message)
        command = (self.command or "").replace("{prompt_file}", prompt_file)
        stdin_text = message if "{prompt_file}" not in (self.command or "") else None
        exit_code, output = run_streaming(command, cwd, timeout=timeout,
                                          stdin_text=stdin_text)
        return {"ok": exit_code == 0, "exit_code": exit_code,
                "reply": output[-20000:], "peer": self.name}
    finally:
        try:
            os.unlink(prompt_file)
        except OSError:
            pass


def _peer_direct_request(self, body):
    grant_id = body.get("grant_id") or ""
    request_id = body.get("request_id") or ""
    grant = self.grants.grant(grant_id)
    if not grant:
        return 401, {"error": "授权不存在、已过期或已撤销"}
    if not request_id or len(request_id) > 128:
        return 400, {"error": "无效 request_id"}
    with self.lock:
        if request_id in self.seen_requests:
            return 409, {"error": "重复请求已拒绝"}
    try:
        key = self.identity.session_key(grant["x_public"], grant["initiator_nonce"],
                                        grant["target_nonce"])
        aad = grant_id + ":" + request_id
        payload = decrypt(key, body.get("packet") or {}, aad)
        scope = payload.get("scope")
        if scope not in ("chat", "project"):
            return 400, {"error": "无效 scope"}
        if grant["scope"] != "project" and scope != grant["scope"]:
            return 403, {"error": "请求 scope 超出授权范围"}
        cwd = self.cwd
        if scope == "project":
            cwd = os.path.realpath(payload.get("cwd") or self.cwd)
            allowed = False
            for parent in grant.get("cwds") or []:
                parent = os.path.realpath(parent)
                if cwd == parent or cwd.startswith(parent + os.sep):
                    allowed = True
                    break
            if not allowed or not os.path.isdir(cwd):
                return 403, {"error": "请求工作目录超出授权范围"}
        message = str(payload.get("message") or "").strip()
        if not message or len(message) > 40000:
            return 400, {"error": "消息为空或超过 40000 字符"}
        with self.lock:
            if request_id in self.seen_requests:
                return 409, {"error": "重复请求已拒绝"}
            self.seen_requests.add(request_id)
        result = self.execute(message, scope, cwd, min(900, float(payload.get("timeout") or 300)))
        return 200, {"packet": encrypt(key, result, aad)}
    except ValueError as exc:
        return 400, {"error": str(exc)}
    except Exception as exc:
        return 500, {"error": "直连执行失败：" + str(exc)}


PeerServer.execute = _peer_execute
PeerServer.direct_request = _peer_direct_request


class DirectClient:
    def __init__(self, url, data_dir):
        self.url = url.rstrip("/")
        self.data_dir = data_dir
        self.identity = Identity.load(data_dir)
        self.credentials_path = os.path.join(data_dir, "credentials.json")

    def save_grant(self, grant):
        try:
            with open(self.credentials_path, encoding="utf-8") as handle:
                saved = json.load(handle)
            if not isinstance(saved, dict):
                raise ValueError("not an object")
        except FileNotFoundError:
            saved = {}
        except Exception as exc:
            raise ValueError("直连凭证文件损坏") from exc
        saved[grant["target_peer_id"]] = grant
        _atomic_json(self.credentials_path, saved, private=True)

    def find_grant(self, target_peer_id, scope=""):
        try:
            with open(self.credentials_path, encoding="utf-8") as handle:
                saved = json.load(handle)
        except FileNotFoundError:
            return None
        except Exception as exc:
            raise ValueError("直连凭证文件损坏") from exc
        grant = saved.get(target_peer_id)
        if not grant or (scope and grant.get("scope") != scope and grant.get("scope") != "project"):
            return None
        return grant

    def drop_grant(self, target_peer_id):
        try:
            with open(self.credentials_path, encoding="utf-8") as handle:
                saved = json.load(handle)
        except FileNotFoundError:
            return
        except Exception as exc:
            raise ValueError("直连凭证文件损坏") from exc
        if isinstance(saved, dict) and saved.pop(target_peer_id, None) is not None:
            _atomic_json(self.credentials_path, saved, private=True)

    def request(self, grant, message, scope, cwd="", timeout=300):
        signature = grant.get("target_signature") or ""
        signed = {key: value for key, value in grant.items() if key != "target_signature"}
        expected = hashlib.sha256(_unb64(grant.get("target_sign_public") or "")).hexdigest()[:32]
        if expected != grant.get("target_peer_id") or not Identity.verify(
                grant.get("target_sign_public") or "", signed, signature):
            raise ValueError("目标授权签名或身份无效")
        key = self.identity.session_key(grant["target_x_public"],
                                        grant["initiator_nonce"], grant["target_nonce"])
        request_id = uuid.uuid4().hex
        aad = grant["grant_id"] + ":" + request_id
        payload = {"message": message, "scope": scope, "cwd": cwd, "timeout": timeout}
        response = json_request(self.url + "/peer/request", {
            "grant_id": grant["grant_id"], "request_id": request_id,
            "packet": encrypt(key, payload, aad)}, timeout=min(930, timeout + 30))
        return decrypt(key, response.get("packet") or {}, aad)
