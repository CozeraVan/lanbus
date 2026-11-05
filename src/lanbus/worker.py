#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
lanbus worker —— 装在每个环境里的执行端（拉取模式）

零依赖。worker 主动连 hub，所以子环境不需要开任何入站端口/防火墙规则。
一台上可以跑多个 worker（例如 Windows 上同时挂 win-ui 与 win-build）。

新增能力：
  * 实时日志：agent 的 stdout/stderr 按行流式回传（lan_logs / SSE task.log）
  * 真取消：per-task progress 轮询，收到取消后终止整个进程树
  * 租约恢复：执行期间续租；worker 崩溃后 hub 自动重排队
  * 可靠提交：提交失败落本地 outbox，恢复网络后自动补交
  * 产物上传：截图/日志跨机以 base64 上传，不再依赖共享文件系统
  * 浏览器：Playwright 或本机 Chrome/Edge headless 截图/点击/填表
  * 上下文交接：context + .lanbus/context.md + git 状态自动拼进 prompt

用法（在目标环境里跑）:
  Windows:
    python worker.py --name win-ui --env windows --hub http://192.168.1.10:8788 ^
        --token devtoken --cwd D:\\proj\\webui --agent claude
  WSL:
    python worker.py --name wsl-core --env wsl --hub http://192.168.1.10:8788 \
        --token devtoken --cwd /mnt/d/proj --agent claude
  macOS:
    python worker.py --name mac-ui --env macos --hub http://192.168.1.10:8788 \
        --token devtoken --cwd /Users/me/proj --agent claude
"""

import argparse
import base64
import collections
import glob
import hashlib
import json
import mimetypes
import os
import queue
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

try:
    from . import __version__ as VERSION
    from . import browser as browser_mod
    from . import discovery as discovery_mod
except ImportError:                            # 允许直接跑 src/lanbus/worker.py
    VERSION = "0.0.0+local"
    try:
        import browser as browser_mod
    except Exception:
        browser_mod = None
    try:
        import discovery as discovery_mod
    except Exception:
        discovery_mod = None

DEFAULTS = {
    "claude": "claude -p --permission-mode acceptEdits",
    "codex": "codex exec --skip-git-repo-check -",
    # Kimi 的 -p 只接受“单个参数”，不能通过 stdin；用 helper 把 prompt 文件作为 argv 传入，
    # 避免 Windows cmd/PowerShell 对换行和引号的二次解释。
    "kimi": (f'"{sys.executable}" -m lanbus.prompt_cli --cmd kimi '
             f'--arg=-p --prompt-file "{{prompt_file}}"'),
    # DeepSeek Harness 支持 headless + stdin（dsh headless -）
    "deepseek": "dsh headless -",
    "shell": "",
    "echo": "",
}

DEFAULT_ARTIFACT_GLOBS = [
    ".lanbus-shot.*",
    "screenshot*.png",
    "build.log",
    "artifacts/**/*.png",
    "playwright-report/**/*.png",
]

MAX_OUTPUT_LINES = 8000
MAX_LOG_PENDING = 6000


def _ssl_context(args):
    import ssl
    if not args.hub.lower().startswith("https"):
        return None
    if getattr(args, "insecure", False):
        ctx = ssl.create_default_context()
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
        return ctx
    if getattr(args, "ca", ""):
        return ssl.create_default_context(cafile=args.ca)
    return ssl.create_default_context()


def post(hub, token, path, payload, timeout=120, ssl_ctx=None):
    data = json.dumps(payload, ensure_ascii=False, default=str).encode()
    req = urllib.request.Request(
        hub.rstrip("/") + path, data=data,
        headers={"Content-Type": "application/json",
                 "Authorization": f"Bearer {token}",
                 "User-Agent": f"lanbus-worker/{VERSION}"},
        method="POST")
    kw = {"timeout": timeout}
    if ssl_ctx is not None:
        kw["context"] = ssl_ctx
    with urllib.request.urlopen(req, **kw) as r:
        return json.loads(r.read().decode() or "{}")


def post_retry(hub, token, path, payload, attempts=6, base=1.0, timeout=120,
               ssl_ctx=None):
    """网络错误 / 5xx 重试；4xx（除 408/429）立即失败。"""
    last = None
    for i in range(max(1, attempts)):
        try:
            return post(hub, token, path, payload, timeout=timeout, ssl_ctx=ssl_ctx)
        except urllib.error.HTTPError as e:
            last = e
            if 400 <= e.code < 500 and e.code not in (408, 429):
                raise
        except Exception as e:
            last = e
        if i + 1 < attempts:
            time.sleep(min(30.0, base * (2 ** i)))
    raise last


def _shell_join(args):
    if os.name == "nt":
        return subprocess.list2cmdline([str(a) for a in args])
    return " ".join(shlex.quote(str(a)) for a in args)


def _kill_tree(p):
    try:
        if os.name == "nt":
            subprocess.run(["taskkill", "/F", "/T", "/PID", str(p.pid)],
                           capture_output=True, timeout=15)
        else:
            try:
                os.killpg(os.getpgid(p.pid), signal.SIGTERM)
            except Exception:
                p.terminate()
            try:
                p.wait(timeout=3)
            except Exception:
                try:
                    os.killpg(os.getpgid(p.pid), signal.SIGKILL)
                except Exception:
                    p.kill()
    except Exception:
        try:
            p.kill()
        except Exception:
            pass


def run_streaming(cmd, cwd, log_cb=None, cancel_event=None, timeout=900,
                  stdin_text=None, env_extra=None):
    """执行命令并逐行回调 stdout/stderr 合并流。

    返回 (exit_code, output_tail)。130 = 被取消，124 = 超时，125 = 启动失败。
    """
    if not cmd or not str(cmd).strip():
        return 0, "(skip: empty command)"
    env = dict(os.environ)
    env["LANBUS_WORKER"] = "1"
    env.setdefault("CI", "1")
    env["PYTHONUNBUFFERED"] = "1"
    env["PYTHONIOENCODING"] = "utf-8"
    env.update(env_extra or {})
    kwargs = dict(cwd=cwd or None, shell=True, stdin=subprocess.DEVNULL,
                  stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                  text=True, encoding="utf-8", errors="replace",
                  bufsize=1, env=env)
    if stdin_text is not None:
        kwargs["stdin"] = subprocess.PIPE
    if os.name == "nt":
        kwargs["creationflags"] = 0x00000200      # CREATE_NEW_PROCESS_GROUP
    else:
        kwargs["start_new_session"] = True
    try:
        p = subprocess.Popen(str(cmd), **kwargs)
    except Exception as e:
        return 125, f"(exec error: {e})"

    if stdin_text is not None:
        def feed():
            try:
                p.stdin.write(stdin_text)
                p.stdin.close()
            except Exception:
                pass
        threading.Thread(target=feed, daemon=True).start()

    lines = collections.deque(maxlen=MAX_OUTPUT_LINES)
    q = queue.Queue()
    eof = threading.Event()

    def reader():
        try:
            for line in p.stdout:
                q.put(line)
        except Exception:
            pass
        finally:
            eof.set()
            q.put(None)

    threading.Thread(target=reader, daemon=True).start()
    t0 = time.time()
    rc = None
    cancelled = False
    timed_out = False
    done = False
    kill_started = None
    while True:
        item = None
        try:
            item = q.get(timeout=0.2)
        except queue.Empty:
            pass
        if item is None:
            if eof.is_set():
                done = True
        else:
            text = item.rstrip("\r\n")
            lines.append(text)
            if log_cb:
                try:
                    log_cb(text, "stdout")
                except Exception:
                    pass
        if done:
            rc = p.poll()
            if rc is None:
                try:
                    rc = p.wait(timeout=5)
                except Exception:
                    _kill_tree(p)
                    rc = p.poll() if p.poll() is not None else 124
            break
        if cancel_event is not None and cancel_event.is_set() and not cancelled:
            cancelled = True
            kill_started = time.time()
            if log_cb:
                log_cb("[lanbus] 收到取消请求，终止进程树", "system")
            _kill_tree(p)
        if not timed_out and timeout and (time.time() - t0) > timeout:
            timed_out = True
            kill_started = time.time()
            if log_cb:
                log_cb(f"[lanbus] 超时 {timeout}s，终止进程树", "system")
            _kill_tree(p)
        if ((cancelled or timed_out) and kill_started
                and time.time() - kill_started > 8):
            rc = p.poll() if p.poll() is not None else (130 if cancelled else 124)
            break
    out = "\n".join(lines)
    if cancelled:
        return 130, out + "\n(cancelled)"
    if timed_out:
        return 124, out + f"\n(timeout after {timeout}s)"
    return (rc if rc is not None else 125), out


def run_simple(cmd, cwd, timeout=120):
    if not cmd or not str(cmd).strip():
        return 0, ""
    try:
        p = subprocess.run(cmd, cwd=cwd, shell=True, capture_output=True,
                           text=True, timeout=timeout, encoding="utf-8",
                           errors="replace", stdin=subprocess.DEVNULL)
        return p.returncode, (p.stdout or "") + (p.stderr or "")
    except subprocess.TimeoutExpired:
        return 124, f"(timeout after {timeout}s)"
    except Exception as e:
        return 125, f"(exec error: {e})"


def git_snapshot(cwd):
    if not cwd or not os.path.isdir(cwd):
        return "", "(cwd 不存在)"
    if not shutil.which("git"):
        return "", "(无 git)"
    rc, status = run_simple("git status --porcelain", cwd, timeout=60)
    if rc != 0:
        return "", status[:2000]
    rc, diff = run_simple("git diff HEAD -- . ':(exclude)*.lock'", cwd, timeout=120)
    if rc != 0:
        rc2, diff = run_simple("git diff -- .", cwd, timeout=120)
        diff = diff if rc2 == 0 else ""
    changed = "\n".join(l for l in status.splitlines()
                        if l[:2].strip() not in ("!!",))[:8000]
    return diff[:200000], changed


def guess_mime(path):
    m = mimetypes.guess_type(path)[0]
    if m:
        return m
    ext = os.path.splitext(path)[1].lower()
    return {".log": "text/plain", ".txt": "text/plain", ".md": "text/markdown",
            ".csv": "text/csv", ".json": "application/json",
            ".xml": "application/xml", ".diff": "text/plain",
            ".patch": "text/plain", ".html": "text/html"}.get(
                ext, "application/octet-stream")


def _load_json(v, default):
    if v is None or v == "":
        return default
    if isinstance(v, str):
        try:
            return json.loads(v)
        except Exception:
            return default
    return v


class LogSender:
    """把执行输出按批写到 hub；失败留在内存里重试，不阻塞 agent。"""

    def __init__(self, w, task):
        self.w = w
        self.tid = task["id"]
        self.claim = task.get("claim_token") or ""
        self.seq = int(task.get("log_seq") or 0)
        self.pending = []
        self.q = queue.Queue()
        self.stop = threading.Event()
        self.stale = False
        self.local = None
        try:
            os.makedirs(w.log_dir, exist_ok=True)
            self.local = open(os.path.join(w.log_dir, f"{self.tid}-{int(time.time())}.log"),
                              "a", encoding="utf-8", errors="replace")
        except Exception:
            self.local = None
        self.thread = threading.Thread(target=self._loop, daemon=True,
                                       name=f"log-{self.tid}")
        self.thread.start()

    def write(self, text, stream="stdout"):
        for line in str(text).splitlines() or [""]:
            if self.local:
                try:
                    self.local.write(f"[{stream}] {line}\n")
                    self.local.flush()
                except Exception:
                    pass
            self.q.put((stream, line))
        if self.q.qsize() > MAX_LOG_PENDING:
            # 极端输出下丢弃最老的行，保留尾部
            try:
                while self.q.qsize() > MAX_LOG_PENDING:
                    self.q.get_nowait()
            except Exception:
                pass

    def _loop(self):
        last = time.time()
        while not self.stop.is_set() or not self.q.empty() or self.pending:
            try:
                item = self.q.get(timeout=0.25)
                self.pending.append(item)
            except queue.Empty:
                pass
            if self.pending and (len(self.pending) >= 40 or time.time() - last >= 0.7):
                self.flush()
                last = time.time()
            if self.stop.is_set() and self.q.empty():
                self.flush()
                if not self.pending:
                    break
        self.flush()

    def flush(self):
        while self.pending and not self.stale:
            batch = self.pending[:40]
            lines = []
            for i, (stream, text) in enumerate(batch):
                lines.append({"seq": self.seq + i + 1, "stream": stream,
                              "text": text[:20000]})
            try:
                r = self.w.post("/worker/log", {
                    "name": self.w.name, "task_id": self.tid,
                    "claim_token": self.claim, "lines": lines}, timeout=15)
            except Exception:
                time.sleep(1.0)
                return
            if r.get("stale") or r.get("error"):
                self.stale = True
                self.pending = []
                return
            self.seq += len(batch)
            del self.pending[:len(batch)]

    def close(self, timeout=25):
        self.stop.set()
        try:
            self.thread.join(timeout)
        except Exception:
            pass
        try:
            self.flush()
        except Exception:
            pass
        if self.local:
            try:
                self.local.close()
            except Exception:
                pass


def compose_prompt(task, w):
    """把主 agent 的 context、worker 本地约定、git 状态、验收标准拼成完整 prompt。"""
    parts = [
        "# lanbus 远程任务上下文",
        f"- task_id: {task.get('id')}",
        f"- env: {w.env}    worker: {w.name}",
        f"- title: {task.get('title') or '(无标题)'}",
        f"- kind: {task.get('kind') or 'agent'}",
        f"- cwd: {task.get('cwd') or w.cwd or '(worker 默认目录)'}",
        f"- 本轮 retry: {task.get('retry', 0)}/{task.get('max_retry', 2)}",
        f"- 恢复次数: {task.get('recoveries', 0)}",
    ]
    if task.get("verify"):
        parts.append(f"- 验收命令: `{task['verify']}`（退出码必须为 0）")
    globs = _load_json(task.get("artifact_globs"), [])
    if globs:
        parts.append(f"- 需要回传的产物: {', '.join(globs)}")

    ctx = (task.get("context") or "").strip()
    if ctx:
        parts += ["", "## 主 agent 交接的背景与约束", ctx[:40000]]

    local = []
    cwd = task.get("cwd") or w.cwd
    seen_files = set()
    for rel in [".lanbus/context.md", ".lanbus/rules.md", w.context_file]:
        if not rel or rel in seen_files:
            continue
        seen_files.add(rel)
        p = os.path.join(cwd or "", rel)
        try:
            if os.path.isfile(p) and os.path.getsize(p) <= 32768:
                with open(p, encoding="utf-8", errors="replace") as f:
                    local.append(f"### {rel}\n{f.read()}")
        except Exception:
            pass
    if cwd and os.path.isdir(cwd):
        rc, branch = run_simple("git rev-parse --abbrev-ref HEAD", cwd, timeout=15)
        if rc == 0 and branch.strip():
            local.append(f"### git branch\n{branch.strip()}")
        rc, st = run_simple("git status --porcelain", cwd, timeout=30)
        if rc == 0 and st.strip():
            local.append("### git status（执行前）\n" + st[:4000])
    if local:
        parts += ["", "## worker 自动注入的本地上下文", "\n\n".join(local)]

    parts += ["", "## 本轮任务", (task.get("prompt") or "").strip()]

    fb = (task.get("feedback") or "").strip()
    if fb:
        parts += ["", "## 上一轮验收意见（必须逐条修掉）", fb[:20000]]

    parts += [
        "",
        "## 执行约束",
        "- 你运行在无人值守的远程环境，不要等待人工输入，不要向用户提问。",
        "- 只修改上面 cwd 内的文件；不要动无关文件、不要 git commit/push。",
        "- 需要截图时写到 cwd/.lanbus-shot.png（或任务里指定的路径），完成后保留文件。",
        "- 完成后输出一段简短摘要：改了什么文件、跑了什么验证、剩余风险。",
    ]
    return "\n".join(parts)[:120000]


def collect_artifacts(cwd, task, w, since, extra_paths=None):
    """读取产物内容并 base64 编码，支持跨机上传。"""
    if not cwd or not os.path.isdir(cwd):
        return []
    patterns = []
    patterns += _load_json(task.get("artifact_globs"), [])
    patterns += w.artifact_globs
    browser_spec = _load_json(task.get("browser"), {}) or {}
    for k in ("out", "pdf"):
        if browser_spec.get(k):
            patterns.append(browser_spec[k])
    seen = set()
    out = []
    total = 0
    for pat in patterns:
        if not pat:
            continue
        pat = str(pat)
        full = pat if os.path.isabs(pat) else os.path.join(cwd, pat)
        candidates = glob.glob(full, recursive=True)
        if os.path.isfile(full) and full not in candidates:
            candidates.append(full)
        for p in candidates:
            try:
                if not os.path.isfile(p):
                    continue
                rp = os.path.realpath(p)
                if rp in seen:
                    continue
                seen.add(rp)
                st = os.stat(rp)
                if st.st_mtime < since - 5:
                    continue
                if st.st_size > w.max_artifact_bytes:
                    continue
                if total + st.st_size > w.max_artifact_bytes * 2:
                    break
                with open(rp, "rb") as f:
                    data = f.read()
                name = os.path.relpath(os.path.abspath(p), os.path.abspath(cwd))
                if name.startswith(".."):
                    name = os.path.basename(rp)
                mime = guess_mime(rp)
                out.append({"name": name.replace("\\", "/"),
                            "mime": mime, "size": len(data),
                            "sha256": hashlib.sha256(data).hexdigest(),
                            "data": base64.b64encode(data).decode()})
                total += len(data)
                if len(out) >= 50:
                    break
            except Exception:
                continue
        if len(out) >= 50:
            break
    return out


class TaskCancel:
    def __init__(self):
        self.event = threading.Event()
        self.ids = set()

    def set(self, tid):
        self.ids.add(tid)
        self.event.set()

    def clear(self):
        self.ids.clear()
        self.event.clear()


class Worker:
    def __init__(self, a):
        self.a = a
        self.name = a.name
        self.env = a.env
        self.hub = a.hub.rstrip("/")
        self.token = a.token
        self.cwd = a.cwd
        self.agent = a.agent
        self.cmd = a.cmd or DEFAULTS.get(a.agent, "")
        self.ssl_ctx = _ssl_context(a)
        self.stopping = threading.Event()
        self.previous_task = None
        self.current = None
        self.current_lock = threading.Lock()
        self.cancelled = TaskCancel()
        self.progress_s = max(1.0, float(getattr(a, "progress_s", 3.0)))
        self.max_artifact_bytes = int(getattr(a, "max_artifact_mb", 32)) * 1024 * 1024
        self.context_file = getattr(a, "context_file", "") or ""
        self.log_dir = (getattr(a, "log_dir", "") or
                        os.path.join(os.path.expanduser("~"), ".lanbus", "logs"))
        self.outbox = (getattr(a, "outbox", "") or
                       os.path.join(os.path.expanduser("~"), ".lanbus", "outbox", self.name))
        caps = [c for c in (a.caps or "").replace(";", ",").split(",") if c]
        if not getattr(a, "no_browser", False) and browser_mod is not None:
            try:
                b = browser_mod.detect()
                caps += b.get("caps") or []
                self.browser_info = b
            except Exception:
                self.browser_info = {"ok": False, "caps": []}
        else:
            self.browser_info = {"ok": False, "caps": []}
        if self.agent:
            caps.append(self.agent)
        if self.cmd:
            caps.append("cmd")
        if discovery_mod is not None:
            caps.append("discover")
            caps.append("ask")
        self.caps = sorted(set(caps))
        self.artifact_globs = []
        for g in (getattr(a, "artifact_glob", None) or []):
            self.artifact_globs += [x.strip() for x in g.split(",") if x.strip()]
        if not self.artifact_globs and os.environ.get("LANBUS_ARTIFACT_GLOB"):
            self.artifact_globs = [x.strip() for x in
                                   os.environ["LANBUS_ARTIFACT_GLOB"].split(",") if x.strip()]
        if not self.artifact_globs:
            self.artifact_globs = list(DEFAULT_ARTIFACT_GLOBS)

    # ---- http helpers
    def post(self, path, payload, timeout=120, attempts=1):
        if attempts > 1:
            return post_retry(self.hub, self.token, path, payload, attempts=attempts,
                              timeout=timeout, ssl_ctx=self.ssl_ctx)
        return post(self.hub, self.token, path, payload, timeout=timeout,
                    ssl_ctx=self.ssl_ctx)

    # ---- lifecycle
    def register(self):
        payload = {"name": self.name, "env": self.env, "agent": self.agent,
                   "caps": self.caps, "version": VERSION, "pid": os.getpid(),
                   "hostname": socket.gethostname()}
        while not self.stopping.is_set():
            try:
                r = self.post("/worker/register", payload, timeout=30, attempts=3)
                if not r.get("ok"):
                    print(f"[worker:{self.name}] register rejected: {r}", flush=True)
                    time.sleep(5)
                    continue
                cfg = r.get("config") or {}
                try:
                    self.max_artifact_bytes = min(
                        self.max_artifact_bytes,
                        int(cfg.get("max_artifact_bytes") or self.max_artifact_bytes))
                except Exception:
                    pass
                self.previous_task = r.get("previous_task")
                print(f"[worker:{self.name}] registered env={self.env} agent={self.agent} "
                      f"caps={self.caps} cwd={self.cwd}", flush=True)
                return
            except Exception as e:
                print(f"[worker:{self.name}] hub unreachable ({e}); retry in 5s", flush=True)
                time.sleep(5)

    def recover(self):
        try:
            r = self.post("/worker/recover", {
                "name": self.name, "max_age_s": 5,
                "force": bool(self.previous_task)}, timeout=30, attempts=3)
            if r.get("recovered") or r.get("failed"):
                print(f"[worker:{self.name}] recovered={r.get('recovered')} "
                      f"failed={r.get('failed')}", flush=True)
        except Exception as e:
            print(f"[worker:{self.name}] recover failed: {e}", flush=True)

    def beat(self):
        while not self.stopping.is_set():
            try:
                r = self.post("/worker/heartbeat", {"name": self.name}, timeout=20)
                for tid in r.get("cancel") or []:
                    self.cancelled.set(tid)
                with self.current_lock:
                    cur = self.current
                if cur and cur["task_id"] in self.cancelled.ids:
                    cur["cancel"].set()
            except Exception:
                pass
            self.stopping.wait(5.0)

    def flush_outbox_once(self, limit=20):
        os.makedirs(self.outbox, exist_ok=True)
        files = sorted(glob.glob(os.path.join(self.outbox, "*.json")))
        for p in files[:limit]:
            try:
                with open(p, encoding="utf-8") as f:
                    payload = json.load(f)
                r = self.post("/worker/submit", payload, timeout=180, attempts=2)
                if r.get("ok") or r.get("ignored"):
                    os.unlink(p)
            except Exception:
                break

    def outbox_loop(self):
        while not self.stopping.is_set():
            try:
                self.flush_outbox_once()
            except Exception:
                pass
            self.stopping.wait(10.0)

    def loop(self):
        self.register()
        threading.Thread(target=self.beat, daemon=True,
                         name=f"beat-{self.name}").start()
        # 先尝试补交上次崩溃前已经写好的结果，再决定是否要求 hub 重排
        self.flush_outbox_once()
        self.recover()
        threading.Thread(target=self.outbox_loop, daemon=True,
                         name=f"outbox-{self.name}").start()
        while not self.stopping.is_set():
            try:
                r = self.post("/worker/claim", {"name": self.name, "env": self.env},
                              timeout=60)
            except Exception as e:
                print(f"[worker:{self.name}] claim failed: {e}", flush=True)
                self.stopping.wait(5)
                continue
            task = r.get("task")
            if not task:
                self.stopping.wait(2)
                continue
            self.exec(task)
        try:
            self.post("/worker/leave", {"name": self.name}, timeout=10)
        except Exception:
            pass

    # ---- execution
    def _progress_loop(self, tid, claim, cancel_evt, stop_evt):
        while not stop_evt.is_set():
            try:
                r = self.post("/worker/progress", {
                    "name": self.name, "task_id": tid, "claim_token": claim},
                    timeout=15, attempts=2)
                if (r.get("invalid") or r.get("stale")
                        or r.get("error") == "task not found"
                        or tid in (r.get("cancel") or [])):
                    cancel_evt.set()
            except Exception:
                pass
            stop_evt.wait(self.progress_s)

    def _submit(self, payload, log):
        os.makedirs(self.outbox, exist_ok=True)
        path = os.path.join(self.outbox,
                            f"{payload['task_id']}-{(payload.get('claim_token') or '')[:8]}.json")
        try:
            with open(path, "w", encoding="utf-8") as f:
                json.dump(payload, f, ensure_ascii=False)
        except Exception:
            path = ""
        try:
            r = self.post("/worker/submit", payload, timeout=180, attempts=6)
        except Exception as e:
            log.write(f"[lanbus] 提交失败，已落 outbox 等待补交: {e}", "system")
            print(f"[worker:{self.name}] submit deferred: {e}", flush=True)
            return None
        if r.get("ignored"):
            log.write(f"[lanbus] hub 忽略了本次提交: {r['ignored']}", "system")
        if path and (r.get("ok") or r.get("ignored")):
            try:
                os.unlink(path)
            except Exception:
                pass
        return r

    def exec(self, task):
        tid = task["id"]
        claim = task.get("claim_token") or ""
        cwd = task.get("cwd") or self.cwd
        kind = (task.get("kind") or "agent").lower()
        title = task.get("title") or ""
        timeout = int(task.get("timeout_s") or 900)
        print(f"\n[worker:{self.name}] ▶ {tid} {title}  "
              f"(retry={task.get('retry', 0)}, kind={kind})", flush=True)

        cancel_evt = threading.Event()
        with self.current_lock:
            self.current = {"task_id": tid, "claim_token": claim,
                            "cancel": cancel_evt}
        if tid in self.cancelled.ids or task.get("cancel_requested"):
            cancel_evt.set()

        log = LogSender(self, task)
        stop_progress = threading.Event()
        threading.Thread(target=self._progress_loop,
                         args=(tid, claim, cancel_evt, stop_progress),
                         daemon=True, name=f"progress-{tid}").start()
        composed = compose_prompt(task, self)
        log.write(f"[lanbus] task={tid} env={self.env} kind={kind} "
                  f"retry={task.get('retry', 0)} cwd={cwd}", "system")
        log.write(composed, "context")

        t0 = time.time()
        rc, out = 0, ""
        pre_artifacts = []
        try:
            if cancel_evt.is_set():
                rc, out = 130, "(cancelled before start)"
                log.write(out, "system")
            elif kind == "ask":
                if discovery_mod is None:
                    rc, out = 127, "discovery helper unavailable"
                else:
                    spec = _load_json(task.get("ask_spec"), {}) or {}
                    res = discovery_mod.ask(
                        spec.get("host"), int(spec.get("port") or 0),
                        spec.get("prompt") or "",
                        model=spec.get("model") or "",
                        service=spec.get("service") or "",
                        api_key=spec.get("api_key") or "",
                        timeout=float(spec.get("timeout_s") or 120),
                        max_tokens=int(spec.get("max_tokens") or 1024),
                        tls=bool(spec.get("tls")))
                    out = json.dumps(res, ensure_ascii=False, default=str)
                    blob = out.encode("utf-8")
                    pre_artifacts.append({
                        "name": "ask.json", "mime": "application/json",
                        "size": len(blob),
                        "sha256": hashlib.sha256(blob).hexdigest(),
                        "data": base64.b64encode(blob).decode(),
                    })
                    rc = 0 if res.get("ok") else 1
            elif kind == "discover":
                if discovery_mod is None:
                    rc, out = 127, "discovery helper unavailable"
                else:
                    spec = _load_json(task.get("discover_spec"), {}) or {}
                    report = discovery_mod.discover(
                        spec, log=lambda line: log.write(line, "discover"),
                        cancel_event=cancel_evt)
                    out = json.dumps(report, ensure_ascii=False, default=str)
                    blob = out.encode("utf-8")
                    pre_artifacts.append({
                        "name": "discovery.json", "mime": "application/json",
                        "size": len(blob),
                        "sha256": hashlib.sha256(blob).hexdigest(),
                        "data": base64.b64encode(blob).decode(),
                    })
                    cx = bool(report.get("cancelled")) or cancel_evt.is_set()
                    rc = 130 if cx else (0 if report.get("ok") else 1)
            elif kind == "browser":
                spec = _load_json(task.get("browser"), {}) or {}
                if browser_mod is None:
                    rc, out = 127, "browser helper unavailable"
                else:
                    # 独立子进程运行浏览器：取消/超时可以杀掉整个进程树
                    spec_tmp = os.path.join(tempfile.gettempdir(),
                                            f"lanbus-browser-{tid}.json")
                    with open(spec_tmp, "w", encoding="utf-8") as f:
                        json.dump(spec, f, ensure_ascii=False)
                    browser_py = os.path.join(
                        os.path.dirname(os.path.abspath(__file__)), "browser.py")
                    cmd = _shell_join([sys.executable, browser_py,
                                       "--spec-file", spec_tmp,
                                       "--cwd", cwd or os.getcwd()])
                    rc, out = run_streaming(
                        cmd, cwd,
                        lambda line, s="browser": log.write(line, s),
                        cancel_evt, timeout, stdin_text=None,
                        env_extra={"LANBUS_TASK_ID": tid,
                                   "LANBUS_TASK_ENV": self.env})
                    log.write(out, "system")
            elif kind == "shell":
                rc, out = run_streaming(task.get("prompt") or "", cwd,
                                        lambda line, s="stdout": log.write(line, s),
                                        cancel_evt, timeout,
                                        stdin_text=None)
            else:
                prompt = composed
                tmp = os.path.join(tempfile.gettempdir(), f"lanbus-{tid}.txt")
                with open(tmp, "w", encoding="utf-8") as f:
                    f.write(prompt)
                cmd = (self.cmd or "").replace("{prompt_file}", tmp)
                use_stdin = (self.cmd and "{prompt_file}" not in self.cmd) or not self.cmd
                if not (self.cmd or "").strip():
                    rc, out = 0, "(skip: agent=shell, no --cmd)"
                    log.write(out, "system")
                else:
                    rc, out = run_streaming(
                        cmd, cwd, lambda line, s="stdout": log.write(line, s),
                        cancel_evt, timeout,
                        stdin_text=prompt if use_stdin else None,
                        env_extra={"LANBUS_TASK_ID": tid,
                                   "LANBUS_TASK_ENV": self.env})
        except Exception as e:
            rc, out = 125, f"(worker exec error: {e})"
            log.write(out, "system")

        took = round(time.time() - t0, 1)
        print(f"[worker:{self.name}] agent rc={rc} {took}s", flush=True)

        cancelled = cancel_evt.is_set()
        verify_ok, verify_out = True, ""
        if task.get("verify") and not cancelled:
            log.write(f"$ {task['verify']}", "verify")
            vrc, vout = run_streaming(
                task["verify"], cwd,
                lambda line, s="stdout": log.write(line, "verify"),
                cancel_evt, 1800, stdin_text=None)
            verify_ok = (vrc == 0)
            verify_out = f"$ {task['verify']}\n(exit {vrc})\n{vout}"[-20000:]
            if vout:
                log.write(vout, "verify")
            log.write(f"[lanbus] verify rc={vrc}", "system")
            print(f"[worker:{self.name}] verify rc={vrc}", flush=True)

        if cancel_evt.is_set():
            cancelled = True
            rc = 130
            log.write("[lanbus] 任务已取消，跳过 verify/diff/产物收集", "system")

        diff, changed = "", ""
        artifacts = []
        if not cancelled:
            diff, changed = git_snapshot(cwd)
            artifacts = pre_artifacts + collect_artifacts(cwd, task, self, t0)
            for a in artifacts:
                log.write(f"[lanbus] artifact {a['name']} {a.get('size', '')}", "system")
            if cancel_evt.is_set():
                cancelled = True
                rc = 130
                diff, changed, artifacts = "", "", []
                log.write("[lanbus] 产物收集期间收到取消，丢弃本轮结果", "system")

        status = "cancelled" if cancelled else ("submitted" if rc == 0 else "failed")
        payload = {
            "name": self.name, "task_id": tid, "claim_token": claim,
            "status": status,
            "exit_code": rc,
            "verify_ok": verify_ok,
            "verify_out": verify_out,
            "result": (out or "")[-20000:],
            "diff": diff,
            "changed": changed,
            "composed_prompt": composed,
            "artifacts": artifacts,
        }
        stop_progress.set()
        log.write(f"[lanbus] 结束 status={status} verify_ok={verify_ok} "
                  f"changed={len(changed.splitlines())} 行", "system")
        log.close()
        r = self._submit(payload, log)
        with self.current_lock:
            self.current = None
        if r and r.get("ok"):
            print(f"[worker:{self.name}] ✓ submitted {tid} status={status} "
                  f"verify_ok={verify_ok} artifacts={len(artifacts)}", flush=True)
        elif r and r.get("ignored"):
            print(f"[worker:{self.name}] ignored {tid}: {r.get('ignored')}", flush=True)
        else:
            print(f"[worker:{self.name}] submit queued for retry {tid}", flush=True)

    def shutdown(self):
        self.stopping.set()
        with self.current_lock:
            cur = self.current
        if cur:
            cur["cancel"].set()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--name", required=True)
    ap.add_argument("--env", required=True, help="windows | wsl | macos | 自定义")
    ap.add_argument("--hub", required=True, help="http://<hub主机IP>:8788")
    ap.add_argument("--token",
                    default=os.environ.get("LANBUS_WORKER_TOKEN")
                    or os.environ.get("LANBUS_TOKEN", "devtoken"))
    ap.add_argument("--cwd", default="")
    ap.add_argument("--agent", default="claude", choices=list(DEFAULTS))
    ap.add_argument("--cmd", default="")
    ap.add_argument("--caps", default="")
    ap.add_argument("--artifact-glob", action="append",
                    help="额外产物 glob，可重复；逗号分隔")
    ap.add_argument("--context-file", default="",
                    help="额外注入的上下文文件（相对 cwd）")
    ap.add_argument("--outbox", default="",
                    help="提交失败落盘目录，默认 ~/.lanbus/outbox/<name>")
    ap.add_argument("--log-dir", default="",
                    help="本地执行日志目录，默认 ~/.lanbus/logs")
    ap.add_argument("--progress-s", type=float, default=3.0,
                    help="续租/取消轮询间隔，默认 3 秒")
    ap.add_argument("--max-artifact-mb", type=int, default=32)
    ap.add_argument("--no-browser", action="store_true",
                    help="不探测/上报浏览器能力")
    ap.add_argument("--ca", default=os.environ.get("LANBUS_CA", ""))
    ap.add_argument("--insecure", action="store_true",
                    help="https 时跳过证书校验（仅限可信 LAN/Tailscale）")
    a = ap.parse_args()
    w = Worker(a)
    if w.browser_info.get("ok"):
        print(f"[worker:{w.name}] browser caps: {w.browser_info.get('caps')}", flush=True)
    try:
        w.loop()
    except KeyboardInterrupt:
        w.shutdown()
        print(f"\n[worker:{w.name}] bye", flush=True)


if __name__ == "__main__":
    main()
