#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
lanbus hub —— 局域网跨环境 agent 编排中枢

零依赖：只用 Python 标准库。
对 agent 侧暴露 MCP (stateless streamable-HTTP)，对 worker 侧暴露 REST。
数据落 SQLite (WAL)，产物落 artifacts/<task_id>/。

用法:
    python hub.py --port 8788 --token devtoken --worker-token workertoken
"""

import argparse
import base64
import calendar
import hashlib
import hmac
import ipaddress
import json
import mimetypes
import os
import re
import sqlite3
import ssl
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, quote, unquote, urlparse

try:
    from . import __version__ as VERSION      # 正常包内导入
    from . import discovery, webui
except ImportError:                            # 允许 `python src/lanbus/hub.py` 直接跑
    VERSION = "0.0.0+local"
    try:
        import discovery
    except Exception:
        discovery = None
    try:
        import webui
    except Exception:
        webui = None

# 运行数据不写进源码/安装目录：打包成 wheel 后那里通常是只读的
DATA_DIR = os.path.abspath(
    os.environ.get("LANBUS_DATA_DIR") or os.path.join(os.path.expanduser("~"), ".lanbus"))
DB_PATH = os.path.join(DATA_DIR, "bus.db")
ART_DIR = os.path.join(DATA_DIR, "artifacts")
TOKEN_FILE = os.path.join(DATA_DIR, "token.txt")
WORKER_TOKEN_FILE = os.path.join(DATA_DIR, "worker-token.txt")

TERMINAL = ("approved", "failed", "cancelled")
AWAKE = ("submitted",) + TERMINAL          # 主 agent 该醒来处理的点
LIVE = ("queued", "claimed", "running", "submitted", "rework")
ACTIVE = ("claimed", "running")
STATUSES = ("queued", "claimed", "running", "submitted", "approved", "failed",
            "cancelled", "rework")

TOKEN = "devtoken"          # 向后兼容：agent token
AGENT_TOKEN = "devtoken"
WORKER_TOKEN = ""           # 为空时沿用 agent token（单口令部署）
WORKER_TOKENS = {}          # 可选：{worker_name: token} 每 worker 独立身份
HOSTS = {"localhost", "127.0.0.1", "::1"}
ALLOWED_ENVS = set()
MAX_ARTIFACT_BYTES = 32 * 1024 * 1024
MAX_ARTIFACT_COUNT = 50
LEASE_S = 120
REAP_S = 10.0
MAX_RECOVERIES = 3

# --------------------------------------------------------------------------- db

_lock = threading.RLock()
_reaper_stop = threading.Event()


def db():
    c = sqlite3.connect(DB_PATH, timeout=15)
    c.row_factory = sqlite3.Row
    c.execute("PRAGMA journal_mode=WAL")
    c.execute("PRAGMA busy_timeout=15000")
    return c


SCHEMA = """
CREATE TABLE IF NOT EXISTS tasks(
  id TEXT PRIMARY KEY, env TEXT, title TEXT, prompt TEXT, cwd TEXT,
  verify TEXT, status TEXT, retry INTEGER DEFAULT 0, max_retry INTEGER DEFAULT 2,
  feedback TEXT, worker TEXT, exit_code INTEGER, result TEXT,
  verify_ok INTEGER, verify_out TEXT, diff TEXT, changed TEXT,
  timeout_s INTEGER DEFAULT 900, created_at TEXT, updated_at TEXT, review_note TEXT,
  context TEXT, kind TEXT DEFAULT 'agent', browser TEXT, discover_spec TEXT,
  ask_spec TEXT, artifact_globs TEXT, required_caps TEXT, priority INTEGER DEFAULT 0,
  parent_id TEXT,
  lease_s INTEGER DEFAULT 120, lease_expires_at TEXT, heartbeat_at TEXT,
  cancel_requested INTEGER DEFAULT 0, cancel_reason TEXT,
  recoveries INTEGER DEFAULT 0, max_recoveries INTEGER DEFAULT 3,
  claim_token TEXT, last_claim_token TEXT, started_at TEXT, finished_at TEXT,
  composed_prompt TEXT, log_seq INTEGER DEFAULT 0, log_lines INTEGER DEFAULT 0,
  last_log_at TEXT);
CREATE TABLE IF NOT EXISTS events(
  id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, payload TEXT, created_at TEXT);
CREATE TABLE IF NOT EXISTS conversations(
  id TEXT PRIMARY KEY, title TEXT, envs TEXT, seed TEXT, context TEXT,
  rounds INTEGER DEFAULT 0, turns TEXT, status TEXT, created_at TEXT, updated_at TEXT);
CREATE TABLE IF NOT EXISTS workers(
  name TEXT PRIMARY KEY, env TEXT, agent TEXT, caps TEXT,
  started_at TEXT, last_seen TEXT, current_task TEXT,
  version TEXT, pid INTEGER, hostname TEXT);
CREATE TABLE IF NOT EXISTS artifacts(
  id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT, name TEXT, relpath TEXT,
  path TEXT, mime TEXT, sha256 TEXT, size INTEGER, created_at TEXT);
CREATE TABLE IF NOT EXISTS task_logs(
  id INTEGER PRIMARY KEY AUTOINCREMENT, task_id TEXT, seq INTEGER,
  stream TEXT, text TEXT, created_at TEXT);
CREATE INDEX IF NOT EXISTS ix_tasks_status ON tasks(status, env, created_at);
CREATE INDEX IF NOT EXISTS ix_tasks_worker ON tasks(worker, status);
CREATE INDEX IF NOT EXISTS ix_events_id ON events(id);
CREATE INDEX IF NOT EXISTS ix_artifacts_task ON artifacts(task_id, name);
CREATE INDEX IF NOT EXISTS ix_logs_task ON task_logs(task_id, seq);
CREATE INDEX IF NOT EXISTS ix_conversations_updated ON conversations(updated_at);
CREATE UNIQUE INDEX IF NOT EXISTS ux_logs_task_seq ON task_logs(task_id, seq);
"""

TASK_COLUMNS = {
    "context": "TEXT", "kind": "TEXT DEFAULT 'agent'", "browser": "TEXT",
    "discover_spec": "TEXT", "ask_spec": "TEXT", "artifact_globs": "TEXT",
    "required_caps": "TEXT",
    "priority": "INTEGER DEFAULT 0", "parent_id": "TEXT",
    "lease_s": "INTEGER DEFAULT 120", "lease_expires_at": "TEXT",
    "heartbeat_at": "TEXT", "cancel_requested": "INTEGER DEFAULT 0",
    "cancel_reason": "TEXT", "recoveries": "INTEGER DEFAULT 0",
    "max_recoveries": "INTEGER DEFAULT 3", "claim_token": "TEXT",
    "last_claim_token": "TEXT", "started_at": "TEXT", "finished_at": "TEXT",
    "composed_prompt": "TEXT",
    "log_seq": "INTEGER DEFAULT 0", "log_lines": "INTEGER DEFAULT 0",
    "last_log_at": "TEXT",
}
ARTIFACT_COLUMNS = {"relpath": "TEXT", "mime": "TEXT"}
WORKER_COLUMNS = {"version": "TEXT", "pid": "INTEGER", "hostname": "TEXT"}


def _columns(c, table):
    return {r[1] for r in c.execute(f"PRAGMA table_info({table})")}


def migrate(c):
    for table, extra in (("tasks", TASK_COLUMNS),
                         ("artifacts", ARTIFACT_COLUMNS),
                         ("workers", WORKER_COLUMNS)):
        have = _columns(c, table)
        for name, ddl in extra.items():
            if name not in have:
                c.execute(f"ALTER TABLE {table} ADD COLUMN {name} {ddl}")


def init_db():
    os.makedirs(ART_DIR, exist_ok=True)
    with db() as c:
        c.executescript(SCHEMA)
        migrate(c)
        c.commit()


def now(offset=0):
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + offset))


def parse_ts(s):
    try:
        return calendar.timegm(time.strptime(s, "%Y-%m-%dT%H:%M:%SZ"))
    except Exception:
        return 0.0


def q(sql, args=(), one=False):
    c = db()
    try:
        rows = [dict(r) for r in c.execute(sql, args)]
        return (rows[0] if rows else None) if one else rows
    finally:
        c.close()


def x(sql, args=()):
    with _lock:
        c = db()
        try:
            cur = c.execute(sql, args)
            c.commit()
            return cur
        finally:
            c.close()


def emit(type_, payload):
    x("INSERT INTO events(type,payload,created_at) VALUES(?,?,?)",
      (type_, json.dumps(payload, ensure_ascii=False, default=str), now()))


def set_status(tid, status, **fields):
    cols = ["status=?", "updated_at=?"]
    vals = [status, now()]
    for k, v in fields.items():
        if not re.match(r"^[a-z_][a-z0-9_]*$", k):
            continue
        cols.append(f"{k}=?")
        vals.append(v)
    vals.append(tid)
    x(f"UPDATE tasks SET {','.join(cols)} WHERE id=?", tuple(vals))
    keep = ("env", "retry", "worker", "exit_code", "verify_ok", "recoveries",
            "kind", "cancel_requested")
    emit("task." + status, {"id": tid, **{k: v for k, v in fields.items() if k in keep}})


def _guess_mime(path):
    m = mimetypes.guess_type(path)[0]
    if m:
        return m
    ext = os.path.splitext(path)[1].lower()
    return {".log": "text/plain", ".txt": "text/plain", ".md": "text/markdown",
            ".csv": "text/csv", ".json": "application/json",
            ".xml": "application/xml", ".diff": "text/plain",
            ".patch": "text/plain", ".html": "text/html"}.get(
                ext, "application/octet-stream")


def _json_list(v):
    if v is None or v == "":
        return []
    if isinstance(v, str):
        try:
            v = json.loads(v)
        except Exception:
            v = [p.strip() for p in re.split(r"[,;\n]", v) if p.strip()]
    return [str(i) for i in (v or [])]


def _caps_match(have, required):
    have = [str(h) for h in (have or [])]
    for req in (required or []):
        if req in have:
            continue
        if any(h.startswith(str(req) + ":") for h in have):
            continue
        return False
    return True


def _append_hub_log(tid, text, stream="hub"):
    r = q("SELECT log_seq,log_lines FROM tasks WHERE id=?", (tid,), one=True)
    if not r:
        return
    seq = int(r.get("log_seq") or 0) + 1
    try:
        x("INSERT OR IGNORE INTO task_logs(task_id,seq,stream,text,created_at) "
          "VALUES(?,?,?,?,?)", (tid, seq, stream, text, now()))
        x("UPDATE tasks SET log_seq=?,log_lines=COALESCE(log_lines,0)+1,"
          "last_log_at=?,updated_at=? WHERE id=?", (seq, now(), now(), tid))
    except Exception:
        pass


# ------------------------------------------------------------------------ tools

def t_dispatch(args):
    """派一张单。env 为空则广播到任一可用 worker。"""
    kind = (args.get("kind") or "agent").lower().strip()
    if kind not in ("agent", "shell", "browser", "discover", "ask"):
        return {"error": "kind must be agent|shell|browser|discover|ask"}
    prompt = (args.get("prompt") or "").strip()
    browser = args.get("browser") or None
    discover_spec = args.get("discover") or None
    ask_spec = args.get("ask") or None
    if kind == "discover":
        if not isinstance(discover_spec, dict):
            discover_spec = {}
        if not prompt:
            prompt = "侦查当前局域网内的 AI agent 环境，并交回 discovery.json"
    if kind == "ask":
        if not isinstance(ask_spec, dict):
            ask_spec = {}
        if not prompt:
            prompt = ask_spec.get("prompt") or "向发现的 AI 端点提问"
    if discover_spec is not None and not isinstance(discover_spec, dict):
        discover_spec = {}
    if ask_spec is not None and not isinstance(ask_spec, dict):
        ask_spec = {}
    if kind == "browser":
        if not isinstance(browser, dict):
            browser = {}
        if not browser.get("url"):
            browser["url"] = args.get("url") or ""
        browser.setdefault("out", args.get("out") or ".lanbus-shot.png")
        if not prompt:
            prompt = f"浏览器任务：打开 {browser.get('url', '')} 并执行指定动作/截图。"
    if not prompt:
        return {"error": "prompt required"}

    tid = uuid.uuid4().hex[:12]
    env = (args.get("env") or "").strip()
    retry = max(0, int(args.get("max_retry", 2) or 0))
    max_rec = max(0, int(args.get("max_recoveries", MAX_RECOVERIES) or 0))
    lease_s = max(5, int(args.get("lease_s", LEASE_S) or LEASE_S))
    timeout_s = max(1, int(args.get("timeout_s", 900) or 900))
    priority = int(args.get("priority", 0) or 0)
    ctx_val = args.get("context") or ""
    if isinstance(ctx_val, (dict, list)):
        ctx_val = json.dumps(ctx_val, ensure_ascii=False)
    context = str(ctx_val).strip()[:40000]
    globs = _json_list(args.get("artifact_globs"))
    required = _json_list(args.get("required_caps"))
    if kind == "browser":
        out = str(browser.get("out") or ".lanbus-shot.png")
        if out not in globs:
            globs.append(out)
        if not required:
            required = ["browser"]
    if kind == "discover" and not required:
        required = ["discover"]
    if kind == "ask" and not required:
        required = ["ask"]

    x("""INSERT INTO tasks(id,env,title,prompt,context,cwd,verify,status,retry,max_retry,
                           timeout_s,kind,browser,discover_spec,ask_spec,
                           artifact_globs,required_caps,priority,parent_id,lease_s,
                           max_recoveries,created_at,updated_at)
         VALUES(?,?,?,?,?,?,?,'queued',0,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
      (tid, env, str(args.get("title") or ""), prompt, context,
       str(args.get("cwd") or ""), str(args.get("verify") or ""),
       retry, timeout_s, kind,
       json.dumps(browser, ensure_ascii=False) if browser else None,
       json.dumps(discover_spec, ensure_ascii=False) if discover_spec else None,
       json.dumps(ask_spec, ensure_ascii=False) if ask_spec else None,
       json.dumps(globs, ensure_ascii=False), json.dumps(required, ensure_ascii=False),
       priority, args.get("parent_id") or None, lease_s, max_rec, now(), now()))
    emit("task.created", {"id": tid, "env": env, "title": args.get("title", ""),
                          "kind": kind})
    wait_s = float(args.get("wait_s", 0) or 0)
    if wait_s > 0:
        return {"task_id": tid, **await_ids([tid], wait_s)}
    return {"task_id": tid, "status": "queued"}


def t_shot(args):
    """便捷浏览器任务：打开 URL、执行动作、截图，并把截图作为产物交回。"""
    url = (args.get("url") or args.get("target") or "").strip()
    if not url:
        return {"error": "url required"}
    out = args.get("out") or ".lanbus-shot.png"
    viewport = args.get("viewport") or {"width": 1440, "height": 900}
    spec = {
        "url": url,
        "out": out,
        "viewport": viewport,
        "full_page": bool(args.get("full_page", True)),
        "timeout_ms": int(args.get("timeout_ms", 30000) or 30000),
        "wait_ms": int(args.get("wait_ms", 0) or 0),
        "actions": args.get("actions") or [],
        "pdf": args.get("pdf") or "",
    }
    prompt = (args.get("prompt") or
              f"在浏览器中打开 {url}，执行 {len(spec['actions'])} 个动作，"
              f"并把截图写到 {out}（full_page={spec['full_page']}）。")
    merged = dict(args)
    merged.update({
        "kind": "browser",
        "browser": spec,
        "prompt": prompt,
        "artifact_globs": list(_json_list(args.get("artifact_globs"))) + [out],
        "title": args.get("title") or "浏览器截图",
    })
    if not args.get("verify"):
        merged["verify"] = ""
    if args.get("wait_s") is None:
        merged["wait_s"] = 180
    return t_dispatch(merged)


def t_dispatch_many(args):
    """一次扇出到多个环境（每个 item 一张单）。"""
    items = args.get("tasks") or []
    ids = []
    for it in items:
        r = t_dispatch({**it, "wait_s": 0})
        if r.get("task_id"):
            ids.append(r["task_id"])
    emit("task.fanout", {"ids": ids})
    out = {"task_ids": ids}
    wait_s = float(args.get("wait_s", 0) or 0)
    if wait_s > 0:
        out.update(await_ids(ids, wait_s))
    return out


def await_ids(ids, wait_s=60.0, poll=0.4):
    ids = [i for i in ids if i]
    if not ids:
        return {"done": [], "pending": [], "results": []}
    end = time.time() + max(0.0, wait_s)
    marks = ",".join("?" * len(ids))
    rows = []
    while True:
        rows = q(f"SELECT * FROM tasks WHERE id IN ({marks})", tuple(ids))
        awake = [r for r in rows if r["status"] in AWAKE]
        if len(awake) == len(rows) or time.time() >= end:
            break
        time.sleep(poll)
    awake_ids = [r["id"] for r in rows if r["status"] in AWAKE]
    return {"done": awake_ids,
            "pending": [r["id"] for r in rows if r["status"] not in AWAKE],
            "results": [slim(r) for r in rows]}


def _clean_chat_reply(text):
    """去掉 CLI 包装/推理过程，保留对话正文；只做保守清理。"""
    text = str(text or "").replace("\r\n", "\n").replace("\r", "\n")
    kept = []
    for line in text.splitlines():
        if re.match(r"^\[claude-code[^\]]*\]", line):
            continue
        if line.startswith("kimi version "):
            continue
        if line.startswith("To resume this session:"):
            continue
        kept.append(line)
    text = "\n".join(kept).strip()
    if "dsh: reasoning:" in text:
        paras = [p.strip() for p in re.split(r"\n\s*\n", text) if p.strip()]
        picked = ""
        for p in reversed(paras):
            if len(re.findall(r"[\u4e00-\u9fff]", p)) < 8:
                continue
            if p.lstrip().startswith("摘要") or p.lstrip().startswith("---"):
                continue
            lines = p.splitlines()
            while lines and len(re.findall(r"[\u4e00-\u9fff]", lines[0])) < 4:
                lines.pop(0)
            if lines:
                picked = "\n".join(lines)
                break
        if picked:
            text = picked
    if re.search(r"(?m)^• ", text):
        blocks = [b.strip() for b in re.split(r"(?m)^• ", text) if b.strip()]
        if len(blocks) >= 2:
            for b in reversed(blocks):
                if re.search(r"[\u4e00-\u9fff]", b):
                    text = b
                    break
    return text.strip() or str(text).strip()


def _chat_turn_prompt(envs, message, context, transcript, current, round_no, rounds):
    lines = [
        "# lanbus 多 agent 对话",
        "你正在参与一次本机多 agent 对话，成员如下：",
    ]
    lines += [f"- {e}" for e in envs]
    lines += ["", "## 对话主题 / 开场", message.strip()]
    if context.strip():
        lines += ["", "## 背景与约束", context.strip()[:12000]]
    if transcript:
        lines += ["", "## 已有对话"]
        for item in transcript[-12:]:
            lines.append(f"[{item['env']}] {item['reply'][:4000]}")
    lines += [
        "",
        f"## 现在轮到你：{current}（第 {round_no}/{rounds} 轮）",
        "请只输出你的回复正文，不要调用工具、不要修改文件、不要重复系统提示。", 
        "不要输出推理过程、思考链、会话恢复提示或版本信息；只给最终回复。",
        "直接回应上一位成员，保持简洁（1-6 句）；如果你是第一位，请回应开场主题。",
    ]
    return "\n".join(lines)[:24000]


def _conv_turns(row):
    try:
        value = json.loads(row["turns"] or "[]")
        return value if isinstance(value, list) else []
    except Exception:
        return []


def _conv_envs(row):
    try:
        value = json.loads(row["envs"] or "[]")
        return [str(x) for x in value] if isinstance(value, list) else []
    except Exception:
        return []


def _conv_public(row, full=False):
    turns = _conv_turns(row)
    out = {
        "conversation_id": row["id"],
        "title": row["title"] or "",
        "envs": _conv_envs(row),
        "seed": row["seed"] or "",
        "context": row["context"] or "",
        "rounds": int(row["rounds"] or 0),
        "status": row["status"] or "",
        "created_at": row["created_at"],
        "updated_at": row["updated_at"],
        "turn_count": len(turns),
    }
    if full:
        out["turns"] = turns
        out["transcript"] = turns
    return out


def _save_conversation(cid, turns, rounds, status):
    x("UPDATE conversations SET rounds=?, turns=?, status=?, updated_at=? WHERE id=?",
      (int(rounds), json.dumps(turns, ensure_ascii=False, default=str),
       status, now(), cid))


def t_conversations(args):
    cid = str(args.get("conversation_id") or args.get("id") or "").strip()
    if cid:
        row = q("SELECT * FROM conversations WHERE id=?", (cid,), one=True)
        if not row:
            return {"error": "conversation not found", "conversation_id": cid}
        return _conv_public(row, full=True)
    limit = max(1, min(200, int(args.get("limit", 20) or 20)))
    rows = q("SELECT * FROM conversations ORDER BY updated_at DESC LIMIT ?", (limit,))
    return {"conversations": [_conv_public(r) for r in rows]}


def t_chat(args):
    # 让 2-4 个本机环境中的 agent 进行多轮对话；conversation_id 可续聊。
    envs = []
    for e in _json_list(args.get("envs") or args.get("agents")):
        e = e.strip()
        if e and e not in envs:
            envs.append(e)
    cid = str(args.get("conversation_id") or args.get("id") or "").strip()
    existing = q("SELECT * FROM conversations WHERE id=?", (cid,), one=True) if cid else None
    if cid and not existing:
        return {"error": "conversation not found", "conversation_id": cid}
    if existing and not envs:
        envs = _conv_envs(existing)
    if len(envs) < 2:
        return {"error": "envs must contain at least two environment names"}
    if len(envs) > 4:
        return {"error": "at most 4 environments per conversation"}
    message = (args.get("message") or args.get("prompt") or "").strip()
    if not message:
        return {"error": "message required"}
    rounds = max(1, min(6, int(args.get("rounds", 2) or 2)))
    per_turn = max(30, min(900, int(args.get("timeout_s", 240) or 240)))
    total_budget = max(60, min(1800, int(args.get("total_timeout_s", 540) or 540)))
    context = str(args.get("context") or "").strip()[:20000]
    user_title = (args.get("title") or "").strip()

    if existing:
        conv_id = cid
        title = user_title or (existing["title"] or "")
        if not context:
            context = existing["context"] or ""
        start_round = max([int(x.get("round") or 0) for x in _conv_turns(existing)],
                          default=0) + 1
        turns = _conv_turns(existing)
        seed = existing["seed"] or message
        total_rounds = start_round + rounds - 1
    else:
        conv_id = "c" + uuid.uuid4().hex[:10]
        title = user_title or "多 agent 对话"
        start_round = 1
        turns = []
        seed = message
        total_rounds = rounds
        x("""INSERT INTO conversations(id,title,envs,seed,context,rounds,turns,status,created_at,updated_at)
             VALUES(?,?,?,?,?,?,?,?,?,?)""",
          (conv_id, title, json.dumps(envs, ensure_ascii=False), seed,
           context, total_rounds, "[]", "running", now(), now()))
    if title and existing:
        x("UPDATE conversations SET title=?, envs=?, context=?, rounds=? WHERE id=?",
          (title, json.dumps(envs, ensure_ascii=False), context, total_rounds, conv_id))

    started = time.time()
    stopped = ""
    pending = 0
    for r in range(rounds):
        round_no = start_round + r
        for env in envs:
            if time.time() - started > total_budget:
                stopped = "total_timeout_s"
                break
            prompt = _chat_turn_prompt(envs, message, context, turns,
                                       env, round_no, total_rounds)
            dispatch_title = title + f" R{round_no}"
            try:
                out = t_dispatch({
                    "env": env, "kind": "agent", "title": dispatch_title,
                    "prompt": prompt, "timeout_s": per_turn,
                    "max_retry": 0, "wait_s": per_turn + 20,
                    "parent_id": conv_id,
                })
            except Exception as e:
                turns.append({"round": round_no, "env": env, "status": "error",
                              "task_id": "", "reply": f"(dispatch error: {e})"})
                _save_conversation(conv_id, turns, total_rounds, "failed")
                stopped = "dispatch_error"
                break
            tid = out.get("task_id") or ""
            status = out.get("status") or "queued"
            reply = ""
            if tid:
                try:
                    rep = t_result({"task_id": tid, "include_images": False})
                    status = rep.get("status") or status
                    reply = _clean_chat_reply(rep.get("result") or "")
                    if not reply:
                        tail = (rep.get("log_tail") or "").strip()
                        reply = tail[-4000:] if tail else ""
                except Exception as e:
                    reply = f"(result error: {e})"
            if not reply:
                reply = "(" + str(status) + ")"
            item = {"round": round_no, "env": env, "task_id": tid,
                    "status": status, "reply": reply[:12000]}
            turns.append(item)
            _save_conversation(conv_id, turns, total_rounds, "running")
            if status not in ("submitted", "approved"):
                pending += 1
                stopped = "turn_" + str(status)
                break
        if stopped:
            break
    ok = bool(turns) and all(x["status"] in ("submitted", "approved") for x in turns)
    status = "completed" if ok else ("partial" if turns else "empty")
    _save_conversation(conv_id, turns, total_rounds, status)
    return {
        "ok": ok,
        "conversation_id": conv_id,
        "title": title,
        "envs": envs,
        "rounds": total_rounds,
        "new_rounds": rounds,
        "start_round": start_round,
        "turns": turns,
        "transcript": turns,
        "stopped": stopped,
        "pending": pending,
        "elapsed_s": round(time.time() - started, 2),
        "status": status,
        "note": "每个 turn 是一张独立 lanbus 任务；transcript 已持久化，可用 conversation_id 续聊。",
    }


def t_await(args):
    return await_ids(args.get("task_ids") or [], float(args.get("wait_s", 60) or 60))


def slim(r):
    return {k: r.get(k) for k in
            ("id", "env", "title", "status", "retry", "max_retry", "kind",
             "verify_ok", "exit_code", "worker", "recoveries", "cancel_requested",
             "created_at", "updated_at")}


def _artifact_meta(row):
    name = row.get("name") or ""
    return {"name": name, "relpath": row.get("relpath") or name,
            "mime": row.get("mime") or "application/octet-stream",
            "size": row.get("size"), "sha256": row.get("sha256"),
            "created_at": row.get("created_at"),
            "url": f"/artifacts/{row.get('task_id')}/{quote(name, safe='/')}"}


def t_result(args):
    tid = args["task_id"]
    r = q("SELECT * FROM tasks WHERE id=?", (tid,), one=True)
    if not r:
        return {"error": "not found", "task_id": tid}
    arts = q("SELECT * FROM artifacts WHERE task_id=? ORDER BY name", (tid,))
    r["artifacts"] = [_artifact_meta(a) for a in arts]
    r["artifact_urls"] = {a["name"]: a["url"] for a in r["artifacts"]}
    r["prompt"] = (r["prompt"] or "")[:20000]
    r["context"] = (r["context"] or "")[:20000]
    r["composed_prompt"] = (r["composed_prompt"] or "")[:40000]
    r["result"] = (r["result"] or "")[:20000]
    r["diff"] = (r["diff"] or "")[:200000]
    r["verify_out"] = (r["verify_out"] or "")[:20000]
    r["feedback"] = (r["feedback"] or "")[:20000]
    r["review_note"] = (r["review_note"] or "")[:20000]
    for k in ("browser", "discover_spec", "ask_spec", "artifact_globs",
              "required_caps"):
        if isinstance(r.get(k), str) and r[k]:
            try:
                r[k] = json.loads(r[k])
            except Exception:
                pass
    for k in ("discover_spec", "ask_spec"):
        if isinstance(r.get(k), dict):
            r[k].pop("api_key", None)
    r["logs"] = q("SELECT seq,stream,text,created_at FROM task_logs "
                  "WHERE task_id=? ORDER BY seq DESC LIMIT 60", (tid,))[::-1]
    r["log_tail"] = "\n".join(
        (f"[{lg['stream']}] " if lg.get("stream") not in (None, "", "stdout") else "") + lg["text"]
        for lg in r["logs"])
    r["log_count"] = int(r.get("log_lines") or 0)
    r.pop("claim_token", None)
    r.pop("last_claim_token", None)
    # 图片不进 JSON 正文，交给 MCP 层作为 image content 返回
    if args.get("include_images", True):
        imgs = []
        for a in arts:
            if not str(a.get("mime") or "").startswith("image/"):
                continue
            if int(a.get("size") or 0) > 8 * 1024 * 1024:
                continue
            try:
                with open(a["path"], "rb") as f:
                    imgs.append({"name": a["name"], "mimeType": a["mime"],
                                 "data": base64.b64encode(f.read()).decode()})
            except Exception:
                continue
            if len(imgs) >= 4:
                break
        if imgs:
            r["_images"] = imgs
    return r


def t_logs(args):
    tid = args["task_id"]
    since = max(0, int(args.get("since", 0) or 0))
    limit = min(2000, max(1, int(args.get("limit", 200) or 200)))
    tail = min(2000, max(1, int(args.get("tail", 200) or 200)))
    wait_s = max(0.0, float(args.get("wait_s", 0) or 0))
    end = time.time() + wait_s
    rows = []
    r = None
    while True:
        r = q("SELECT id,status,log_seq,log_lines,cancel_requested,worker,kind "
              "FROM tasks WHERE id=?", (tid,), one=True)
        if not r:
            return {"error": "not found", "task_id": tid}
        if since > 0:
            rows = q("SELECT seq,stream,text,created_at FROM task_logs "
                     "WHERE task_id=? AND seq>? ORDER BY seq LIMIT ?",
                     (tid, since, limit))
        else:
            rows = q("SELECT * FROM (SELECT seq,stream,text,created_at FROM task_logs "
                     "WHERE task_id=? ORDER BY seq DESC LIMIT ?) ORDER BY seq",
                     (tid, tail))
        if rows or wait_s <= 0 or r["status"] not in ACTIVE or time.time() >= end:
            break
        time.sleep(0.4)
    next_since = max([int(x["seq"]) for x in rows], default=since)
    return {"task_id": tid, "status": r["status"], "logs": rows,
            "next_since": next_since, "log_count": int(r.get("log_lines") or 0),
            "running": r["status"] in ACTIVE and not r.get("cancel_requested")}


def t_log_tail(args):
    """同 lan_logs，但默认只取末尾，用于命令行/脚本。"""
    a = dict(args)
    a.setdefault("tail", 200)
    return t_logs(a)


def t_artifact(args):
    tid = args["task_id"]
    name = args.get("name")
    arts = q("SELECT * FROM artifacts WHERE task_id=? ORDER BY name", (tid,))
    if not name:
        return {"task_id": tid, "artifacts": [_artifact_meta(a) for a in arts]}
    row = next((a for a in arts if a["name"] == name or a.get("relpath") == name), None)
    if not row:
        return {"error": "artifact not found", "task_id": tid, "name": name}
    out = {"task_id": tid, "artifact": _artifact_meta(row)}
    mime = str(row.get("mime") or "")
    path = row.get("path") or ""
    try:
        if mime.startswith("image/") and os.path.getsize(path) <= 8 * 1024 * 1024:
            with open(path, "rb") as f:
                out["_images"] = [{"name": row["name"], "mimeType": mime,
                                   "data": base64.b64encode(f.read()).decode()}]
        elif mime.startswith("text/") or mime in ("application/json", "application/xml"):
            with open(path, "rb") as f:
                out["text"] = f.read(200000).decode("utf-8", "replace")
        else:
            out["note"] = "二进制产物请用 artifact.url 下载"
    except Exception as e:
        out["error"] = str(e)
    return out


def t_review(args):
    """主 agent 的验收闸门：approve 放行 / rework 打回并带上修改意见。"""
    tid = args["task_id"]
    verdict = (args.get("verdict") or "").lower()
    r = q("SELECT * FROM tasks WHERE id=?", (tid,), one=True)
    if not r:
        return {"error": "not found", "task_id": tid}
    if verdict not in ("approve", "rework", "fail"):
        return {"error": "verdict must be approve|rework|fail"}
    if r["status"] in TERMINAL:
        return {"error": f"task already {r['status']}", "task_id": tid,
                "status": r["status"]}
    feedback = args.get("feedback", "") or ""
    if verdict == "approve":
        if r["status"] != "submitted":
            return {"error": f"only submitted can be approved (now {r['status']})",
                    "task_id": tid, "status": r["status"]}
        set_status(tid, "approved", review_note=feedback, finished_at=now())
        return {"task_id": tid, "status": "approved"}
    if verdict == "fail":
        set_status(tid, "failed", review_note=feedback, cancel_requested=1,
                   cancel_reason=feedback or "failed by reviewer", finished_at=now())
        return {"task_id": tid, "status": "failed"}
    if r["status"] != "submitted":
        return {"error": f"only submitted can be reworked (now {r['status']})",
                "task_id": tid, "status": r["status"]}
    if r["retry"] >= r["max_retry"]:
        set_status(tid, "failed", review_note=f"[超出重试上限] {feedback}",
                   finished_at=now())
        return {"task_id": tid, "status": "failed",
                "reason": f"retry {r['retry']}/{r['max_retry']} exhausted"}
    old = (r["feedback"] or "")
    merged = (old + "\n---\n" + feedback).strip() if old else feedback
    x("""UPDATE tasks SET status='queued', retry=retry+1, feedback=?,
                          worker=NULL, claim_token=NULL, last_claim_token=NULL,
                          lease_expires_at=NULL, heartbeat_at=NULL,
                          cancel_requested=0, cancel_reason=NULL,
                          composed_prompt=NULL, finished_at=NULL,
                          result=NULL, diff=NULL, changed=NULL, verify_ok=NULL,
                          verify_out=NULL, exit_code=NULL, review_note=?,
                          updated_at=? WHERE id=?""",
      (merged, feedback, now(), tid))
    x("DELETE FROM artifacts WHERE task_id=?", (tid,))
    emit("task.rework", {"id": tid, "retry": r["retry"] + 1, "feedback": feedback})
    return {"task_id": tid, "status": "queued", "retry": r["retry"] + 1,
            "note": "已打回，worker 会带着 feedback 重做"}


def t_list(args):
    st = args.get("status") or ""
    env = args.get("env") or ""
    kind = args.get("kind") or ""
    lim = min(500, max(1, int(args.get("limit", 30) or 30)))
    rows = q("""SELECT * FROM tasks
                WHERE (?='' OR status=?) AND (?='' OR env=?)
                  AND (?='' OR kind=?)
                ORDER BY created_at DESC LIMIT ?""",
             (st, st, env, env, kind, kind, lim))
    return {"tasks": [slim(r) for r in rows]}


def t_envs(args):
    rows = q("SELECT * FROM workers ORDER BY env, name")
    out = []
    for w in rows:
        age = 9999
        try:
            age = int(time.time() - parse_ts(w["last_seen"]))
        except Exception:
            pass
        caps = _json_list(w.get("caps"))
        out.append({**w, "caps": caps, "online": age < 45, "idle_s": age,
                    "has_browser": _caps_match(caps, ["browser"])})
    return {"workers": out,
            "envs": sorted({w["env"] for w in rows if w.get("env")})}


def t_cancel(args):
    tid = args["task_id"]
    reason = args.get("reason", "") or ""
    with _lock:
        r = q("SELECT * FROM tasks WHERE id=?", (tid,), one=True)
        if not r:
            return {"error": "not found", "task_id": tid}
        if r["status"] in TERMINAL:
            return {"task_id": tid, "status": r["status"], "already": True}
        # 先把取消意图落库；正在跑的 worker 会在 progress/heartbeat 里看到
        x("""UPDATE tasks SET status='cancelled', cancel_requested=1, cancel_reason=?,
                              review_note=?, claim_token=COALESCE(claim_token,''),
                              lease_expires_at=NULL, finished_at=?, updated_at=?
              WHERE id=?""", (reason, reason, now(), now(), tid))
        emit("task.cancelled", {"id": tid, "worker": r.get("worker"),
                                "reason": reason, "was": r["status"]})
    if r.get("worker"):
        _append_hub_log(tid, f"[hub] 已请求 worker {r.get('worker')} 终止进程")
    return {"task_id": tid, "status": "cancelled", "was": r["status"],
            "worker": r.get("worker"),
            "note": "运行中的进程会被 worker 在 progress 周期内终止；之后的提交会被 hub 拒绝"}


def t_interrupt(args):
    """强制中断一个 lanbus 任务：等价于取消，但语义更明确地表示 Ctrl+C/停任务。

    对 worker 管理的任务会杀整个进程树；对纯 UI 注入的对话没有任务行，不适用。
    """
    tid = args["task_id"]
    r = q("SELECT id,status,worker FROM tasks WHERE id=?", (tid,), one=True)
    if not r:
        return {"error": "not found", "task_id": tid}
    if r["status"] in TERMINAL:
        return {"task_id": tid, "status": r["status"], "already": True,
                "note": "任务已是终态，无需中断"}
    out = t_cancel({"task_id": tid,
                    "reason": args.get("reason") or "interrupted by user"})
    out["interrupt"] = "worker process tree kill / cancellation flag"
    out["ctrl_c_equivalent"] = True
    return out


def _read_json_artifact(tid, name):
    row = q("SELECT path FROM artifacts WHERE task_id=? AND name=?", (tid, name),
            one=True)
    if row and row.get("path") and os.path.isfile(row["path"]):
        try:
            with open(row["path"], encoding="utf-8") as f:
                return json.load(f)
        except Exception:
            pass
    r = q("SELECT result FROM tasks WHERE id=?", (tid,), one=True)
    if r and r.get("result"):
        try:
            obj = json.loads(r["result"])
            if isinstance(obj, dict):
                return obj
        except Exception:
            pass
    return None


def _discovery_report(tid):
    return _read_json_artifact(tid, "discovery.json")


def t_discover(args):
    """侦查局域网 AI agent：本机进程 + 监听服务 + LAN 端口/HTTP 指纹。

    env 为空：在 hub 所在机器执行；env 指定：派给该环境 worker 执行。
    """
    if discovery is None:
        return {"error": "discovery module unavailable"}
    spec = {}
    for k in ("cidr", "ports", "deep", "timeout", "http_timeout", "max_hosts",
              "include_local", "include_processes", "include_wsl", "scanner_bin"):
        if k in args and args[k] is not None:
            spec[k] = args[k]
    env = (args.get("env") or "").strip()
    wait_s = float(args.get("wait_s", 120) or 0)
    if env:
        r = t_dispatch({
            "env": env,
            "title": args.get("title") or "LAN AI agent 侦查",
            "prompt": args.get("prompt") or "侦查当前局域网内的 AI agent 环境",
            "context": args.get("context") or "",
            "kind": "discover",
            "discover": spec,
            "timeout_s": int(args.get("timeout_s") or 300),
            "wait_s": wait_s,
            "artifact_globs": ["discovery.json"],
        })
        tid = r.get("task_id")
        status = r.get("status")
        if not status and r.get("results"):
            status = r["results"][0].get("status")
        report = _discovery_report(tid) if tid and status in ("submitted", "failed") else None
        out = {"task_id": tid, "env": env, "status": status}
        if report is not None:
            out["report"] = report
            for k in ("summary", "agents", "hosts", "local_agents",
                      "local_services", "interfaces", "notes", "cancelled"):
                if k in report:
                    out[k] = report[k]
        if r.get("pending"):
            out["pending"] = r["pending"]
        return out
    try:
        return discovery.discover(spec)
    except Exception as e:
        return {"error": str(e)}


def t_ask(args):
    """直接向发现的 OpenAI-compatible / Ollama 端点发一条消息，不需要对方装插件。"""
    if discovery is None:
        return {"error": "discovery module unavailable"}
    host = (args.get("host") or args.get("ip") or "").strip()
    port = args.get("port")
    endpoint = (args.get("endpoint") or "").strip()
    tls = bool(args.get("tls")) or endpoint.lower().startswith("https://")
    if endpoint:
        m = re.match(r"^(?:https?://)?([^/:]+)(?::(\d+))?/?$", endpoint)
        if m:
            host = m.group(1)
            port = port or m.group(2)
    if not host or not port:
        return {"error": "host and port are required (or endpoint=ip:port)"}
    spec = {
        "host": host, "port": int(port),
        "prompt": args.get("prompt") or "",
        "model": args.get("model") or "",
        "service": args.get("service") or "",
        "api_key": args.get("api_key") or "",
        "timeout_s": float(args.get("timeout_s") or 120),
        "max_tokens": int(args.get("max_tokens") or 1024),
        "tls": tls,
    }
    env = (args.get("env") or "").strip()
    if env:
        r = t_dispatch({
            "env": env, "kind": "ask", "ask": spec,
            "title": args.get("title") or "向局域网 AI 端点提问",
            "prompt": spec["prompt"],
            "timeout_s": int(args.get("timeout_s") or 120) + 30,
            "wait_s": float(args.get("wait_s", 120) or 0),
        })
        tid = r.get("task_id")
        status = r.get("status")
        if not status and r.get("results"):
            status = r["results"][0].get("status")
        out = {"task_id": tid, "env": env, "status": status}
        if status in ("submitted", "failed"):
            res = _read_json_artifact(tid, "ask.json")
            if isinstance(res, dict):
                out.update(res)
        return out
    try:
        return discovery.ask(
            host, spec["port"], spec["prompt"],
            model=spec["model"], service=spec["service"],
            api_key=spec["api_key"], timeout=spec["timeout_s"],
            max_tokens=spec["max_tokens"], tls=bool(spec.get("tls")))
    except Exception as e:
        return {"error": str(e)}


SCHEMA_T = {"type": "object"}

TOOLS = {
    "lan_dispatch": (
        t_dispatch,
        "把一张任务派给指定环境的子 agent。wait_s>0 时阻塞等到子 agent 交回结果（或超时），"
        "返回值里 status=submitted 表示等你验收。env 取 lan_envs 返回的 env 值，"
        "留空则任意空闲 worker 接单。context 是自动拼进 prompt 的背景/路径/约束/验收标准。",
        {
            "type": "object",
            "properties": {
                "env": {"type": "string", "description": "目标环境，如 windows / wsl / macos"},
                "title": {"type": "string", "description": "一句话标题"},
                "prompt": {"type": "string", "description": "对该环境 agent 的完整指令，要能独立执行"},
                "context": {"type": "string",
                            "description": "主 agent 的上下文交接：项目背景、相关路径、约束、验收标准。会自动拼在 prompt 前面"},
                "cwd": {"type": "string", "description": "该环境里的工作目录绝对路径，留空用 worker 默认目录"},
                "verify": {"type": "string", "description": "验收命令，如 npm run build；非 0 退出即判失败"},
                "max_retry": {"type": "integer", "description": "打回重做上限，默认 2"},
                "timeout_s": {"type": "integer", "description": "单次执行超时秒数，默认 900"},
                "lease_s": {"type": "integer", "description": "执行租约秒数，默认 120；worker 每 3 秒续租"},
                "max_recoveries": {"type": "integer", "description": "worker 崩溃后的自动重排上限，默认 3"},
                "kind": {"type": "string", "enum": ["agent", "shell", "browser"],
                         "description": "agent=本地 agent CLI；shell=直接把 prompt 当命令跑；browser=内置浏览器动作"},
                "browser": {"type": "object", "description": "kind=browser 时的动作描述"},
                "artifact_globs": {"type": "array", "items": {"type": "string"},
                                   "description": "要回传的产物 glob（相对 cwd），如 ['dist/**/*.png']"},
                "required_caps": {"type": "array", "items": {"type": "string"},
                                  "description": "要求 worker 具备的能力，如 ['browser']"},
                "priority": {"type": "integer", "description": "数值越大越先接单"},
                "parent_id": {"type": "string", "description": "父任务 id，便于串联"},
                "wait_s": {"type": "number", "description": "等待秒数，0 表示立即返回（纯异步）"},
            },
            "required": ["prompt"],
        }),
    "lan_dispatch_many": (
        t_dispatch_many,
        "扇出：一次把多张任务分别派给不同环境，并在同一调用里等待结果。",
        {
            "type": "object",
            "properties": {
                "tasks": {"type": "array", "items": SCHEMA_T},
                "wait_s": {"type": "number"},
            },
            "required": ["tasks"],
        }),
    "lan_shot": (
        t_shot,
        "便捷浏览器任务：在目标环境打开 URL、执行点击/填表等动作并截图，截图自动作为图片产物返回。"
        "需要目标 worker 具备 browser 能力（Playwright 或本机 Chrome/Edge）。",
        {"type": "object",
         "properties": {
             "env": {"type": "string"},
             "url": {"type": "string"},
             "cwd": {"type": "string"},
             "out": {"type": "string", "description": "截图路径（相对 cwd），默认 .lanbus-shot.png"},
             "pdf": {"type": "string", "description": "可选 PDF 输出路径（Playwright）"},
             "full_page": {"type": "boolean"},
             "viewport": {"type": "object"},
             "actions": {"type": "array", "items": {"type": "object"},
                         "description": "[{goto},{click},{fill:[sel,val]},{wait:ms},{screenshot:path},{evaluate:js}]"},
             "wait_ms": {"type": "integer"},
             "timeout_ms": {"type": "integer"},
             "context": {"type": "string"},
             "wait_s": {"type": "number"},
         },
         "required": ["url"]}),
    "lan_chat": (
        t_chat,
        "让本机 2-4 个不同环境的 agent 进行多轮对话。hub 按 envs 顺序接力派单，"
        "把已有 transcript 作为上下文传给下一位；返回 conversation_id、每轮 task_id 和回复。"
        "适合先验证本机 Claude / Kimi / DeepSeek / WSL 等 agent 之间的通信。",
        {"type": "object",
         "properties": {
             "envs": {"type": "array", "items": {"type": "string"},
                      "description": "参与对话的环境名（lan_envs 的 env），2-4 个，例如 ['win-claude','win-deepseek']"},
             "message": {"type": "string", "description": "对话主题/开场消息"},
             "rounds": {"type": "integer", "description": "轮数，默认 2；每轮每个 env 各回复一次"},
             "context": {"type": "string", "description": "共同背景/约束（可选）"},
             "title": {"type": "string", "description": "任务标题前缀（可选）"},
             "timeout_s": {"type": "integer", "description": "单轮超时秒数，默认 240"},
             "total_timeout_s": {"type": "integer", "description": "整场对话预算秒数，默认 540"},
             "conversation_id": {"type": "string", "description": "可选：继续已有对话"},
         },
         "required": ["message"]}),
    "lan_conversations": (
        t_conversations,
        "列出本机多 agent 对话记录，或传 conversation_id 读取完整 turns/transcript。"
        "配合 lan_chat 的 conversation_id 可续聊。",
        {"type": "object",
         "properties": {
             "conversation_id": {"type": "string", "description": "可选：读取某一场对话"},
             "limit": {"type": "integer", "description": "列表条数，默认 20"},
         }}),
    "lan_await": (
        t_await,
        "等待一批任务到达可验收状态（submitted/approved/failed/cancelled）。",
        {"type": "object",
         "properties": {"task_ids": {"type": "array", "items": {"type": "string"}},
                        "wait_s": {"type": "number"}},
         "required": ["task_ids"]}),
    "lan_result": (
        t_result,
        "取一张任务的完整结果：agent 输出、实时日志尾部、git diff、改动文件、verify 输出、产物清单。"
        "图片产物会作为 image content 一并返回。验收就看这个。",
        {"type": "object",
         "properties": {"task_id": {"type": "string"},
                        "include_images": {"type": "boolean",
                                           "description": "是否把图片产物作为 image content 返回，默认 true"}},
         "required": ["task_id"]}),
    "lan_logs": (
        t_logs,
        "读取任务的实时执行日志。since 传上一次返回的 next_since 可持续增量拉取；"
        "wait_s>0 时在任务运行中长轮询等待新日志。",
        {"type": "object",
         "properties": {"task_id": {"type": "string"},
                        "since": {"type": "integer"},
                        "tail": {"type": "integer", "description": "since=0 时取末尾 N 行，默认 200"},
                        "limit": {"type": "integer"},
                        "wait_s": {"type": "number"}},
         "required": ["task_id"]}),
    "lan_discover": (
        t_discover,
        "侦查局域网内有哪些环境打开了 AI agent，不需要对方预装 lanbus/MCP。"
        "默认扫描本机所在 /24 的 AI/开发端口，识别 Ollama、OpenAI-compatible、"
        "LM Studio、Open WebUI、ComfyUI、llama.cpp、vLLM、Gradio、Jupyter、"
        "Chrome DevTools、MCP server、lanbus hub；同时列出本机 agent 进程。"
        "env 为空在 hub 主机执行，指定 env 则派给该环境 worker 从那边扫描。",
        {"type": "object",
         "properties": {
             "env": {"type": "string", "description": "可选：从哪个 worker 环境扫描；留空在 hub 主机扫描"},
             "cidr": {"type": "string", "description": "扫描范围，如 192.168.1.0/24；留空用本机网卡"},
             "ports": {"type": "string", "description": "端口/范围，如 11434,1234,8000-8100"},
             "deep": {"type": "boolean", "description": "加上 1-1024 端口，慢"},
             "timeout": {"type": "number", "description": "TCP connect 超时，默认 0.22s"},
             "http_timeout": {"type": "number", "description": "HTTP 指纹超时，默认 1.5s"},
             "max_hosts": {"type": "integer", "description": "最多扫描主机数，默认 1024"},
             "include_local": {"type": "boolean", "description": "是否探测本机监听服务"},
             "include_processes": {"type": "boolean", "description": "是否扫描本机 agent 进程"},
             "include_wsl": {"type": "boolean", "description": "Windows 上是否同时读取正在运行的 WSL 进程（默认 true）"},
             "scanner_bin": {"type": "string", "description": "可选 Go lanbus-scan 二进制路径；留空自动查找"},
             "timeout_s": {"type": "integer", "description": "env 模式下任务超时，默认 300"},
             "wait_s": {"type": "number", "description": "env 模式下等待秒数，默认 120；0=异步"},
         },
         "required": []}),
    "lan_ask": (
        t_ask,
        "直接向发现的 OpenAI-compatible / Ollama 端点发一条 prompt 并取回反馈；"
        "对方不需要安装 lanbus。适合 Ollama、LM Studio、vLLM、LocalAI 等有 HTTP API 的 agent。",
        {"type": "object",
         "properties": {
             "host": {"type": "string"},
             "port": {"type": "integer"},
             "endpoint": {"type": "string", "description": "也可直接传 ip:port"},
             "prompt": {"type": "string"},
             "model": {"type": "string"},
             "service": {"type": "string", "description": "ollama / openai / 留空自动"},
             "api_key": {"type": "string"},
             "tls": {"type": "boolean", "description": "https 端点时置 true（endpoint 带 https:// 会自动识别）"},
             "env": {"type": "string", "description": "可选：从该环境 worker 发起请求"},
             "timeout_s": {"type": "number"},
             "max_tokens": {"type": "integer"},
             "wait_s": {"type": "number"},
         },
         "required": ["prompt"]}),
    "lan_artifact": (
        t_artifact,
        "列出/读取任务产物。name 为空时返回清单；图片会作为 image content 返回；"
        "文本产物返回文本；其它类型给出下载 URL。",
        {"type": "object",
         "properties": {"task_id": {"type": "string"},
                        "name": {"type": "string"}},
         "required": ["task_id"]}),
    "lan_review": (
        t_review,
        "验收闸门。verdict=approve 通过并结束；verdict=rework 打回，任务回到队列并带上 feedback 让子 agent 重做；"
        "verdict=fail 直接判死（运行中的任务会被通知终止）。",
        {"type": "object",
         "properties": {"task_id": {"type": "string"},
                        "verdict": {"type": "string", "enum": ["approve", "rework", "fail"]},
                        "feedback": {"type": "string", "description": "打回时必须写清哪里不对、怎么改"}},
         "required": ["task_id", "verdict"]}),
    "lan_list": (
        t_list,
        "列出任务（可按 status / env / kind 过滤）。",
        {"type": "object",
         "properties": {"status": {"type": "string"}, "env": {"type": "string"},
                        "kind": {"type": "string"}, "limit": {"type": "integer"}},
         "required": []}),
    "lan_envs": (
        t_envs,
        "看当前在线 worker、能力（caps/browser）与可用环境清单。派单前先调它确认目标环境有人接单。",
        {"type": "object", "properties": {}, "required": []}),
    "lan_cancel": (
        t_cancel,
        "取消一张任务：数据库立刻置 cancelled，正在运行的子进程会被 worker 终止，"
        "之后该任务的迟到提交会被 hub 拒绝。",
        {"type": "object",
         "properties": {"task_id": {"type": "string"}, "reason": {"type": "string"},
                        "wait_s": {"type": "number"}},
         "required": ["task_id"]}),
    "lan_interrupt": (
        t_interrupt,
        "强制中断一张 lanbus 任务（Ctrl+C 等价）：worker 会杀整个进程树，"
        "不会像 UI 键盘注入那样受窗口焦点/后台状态影响。"
        "只对通过 lanbus dispatch/worker 运行的任务有效。",
        {"type": "object",
         "properties": {"task_id": {"type": "string"}, "reason": {"type": "string"}},
         "required": ["task_id"]}),
}

# ------------------------------------------------------------------------ http

def is_ip(h):
    try:
        ipaddress.ip_address(h)
        return True
    except Exception:
        return False


def _worker_token():
    return WORKER_TOKEN or AGENT_TOKEN


class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    server_version = "lanbus/1.1"

    def log_message(self, fmt, *a):
        if os.environ.get("LANBUS_QUIET"):
            return
        print(f"[hub] {self.address_string()} {fmt % a}", flush=True)

    # ---- helpers
    def _json(self, code, obj, extra=None):
        body = json.dumps(obj, ensure_ascii=False, default=str).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        for k, v in (extra or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def _html(self, text):
        body = text.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def _body(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n > 128 * 1024 * 1024:
            return b""
        return self.rfile.read(n) if n else b""

    def _token(self):
        auth = self.headers.get("Authorization") or self.headers.get("x-api-key") or ""
        if auth.lower().startswith("bearer "):
            return auth[7:].strip()
        return auth.strip()

    def _guard(self, role="agent", worker_name=None):
        raw_host = (self.headers.get("Host") or "").strip()
        if raw_host.startswith("["):
            host = raw_host[1:].split("]", 1)[0]
        elif raw_host.count(":") == 1:
            host = raw_host.rsplit(":", 1)[0]
        else:
            host = raw_host
        if host not in HOSTS and not is_ip(host):
            self._json(403, {"error": "bad host", "host": host})
            return False
        tok = self._token()
        agent_ok = bool(tok) and hmac.compare_digest(tok, AGENT_TOKEN)
        ok = agent_ok
        if role == "worker" and not ok:
            if worker_name and WORKER_TOKENS.get(worker_name):
                ok = bool(tok) and hmac.compare_digest(tok, WORKER_TOKENS[worker_name])
            else:
                ok = bool(tok) and hmac.compare_digest(tok, _worker_token())
        if not ok:
            self._json(401, {"error": "unauthorized", "role": role,
                             "worker": worker_name})
            return False
        return True

    # ---- GET
    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path
        if path.startswith("/health"):
            return self._json(200, {"ok": True, "ts": now(),
                                    "version": VERSION,
                                    "counts": {s: len(q("SELECT id FROM tasks WHERE status=?", (s,)))
                                               for s in STATUSES}})
        if path in ("/", "/ui", "/ui/"):
            if webui is None:
                return self._json(500, {"error": "web UI unavailable"})
            return self._html(webui.HTML)
        if path in ("/api/envs", "/envs"):
            if not self._guard("agent"):
                return
            return self._json(200, t_envs({}))
        if path in ("/api/tasks", "/tasks"):
            if not self._guard("agent"):
                return
            qs = parse_qs(parsed.query)
            try:
                limit = int((qs.get("limit") or ["50"])[0] or 50)
            except Exception:
                limit = 50
            return self._json(200, t_list({"limit": limit}))
        if path.startswith("/events"):
            if not self._guard("agent"):
                return
            return self._sse(parsed)
        if path.startswith("/mcp"):
            self.send_response(405)
            self.send_header("Allow", "POST")
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if path.startswith("/artifacts/"):
            if not self._guard("agent"):
                return
            return self._artifact(path)
        if path in ("/conversations", "/api/conversations", "/chat/history") or path.startswith("/conversations/") or path.startswith("/api/conversations/"):
            if not self._guard("agent"):
                return
            if path.startswith("/conversations/"):
                cid = unquote(path[len("/conversations/"):]).strip("/")
                return self._json(200, t_conversations({"conversation_id": cid}))
            if path.startswith("/api/conversations/"):
                cid = unquote(path[len("/api/conversations/"):]).strip("/")
                return self._json(200, t_conversations({"conversation_id": cid}))
            qs = parse_qs(parsed.query)
            args = {"limit": int((qs.get("limit") or ["20"])[0] or 20)}
            return self._json(200, t_conversations(args))
        if path in ("/discover", "/agents"):
            if not self._guard("agent"):
                return
            qs = parse_qs(parsed.query)
            args = {}
            for k in ("env", "cidr", "ports"):
                if qs.get(k):
                    args[k] = qs[k][0]
            for k in ("deep", "include_local", "include_processes"):
                if qs.get(k):
                    args[k] = qs[k][0].lower() in ("1", "true", "yes", "on")
            for k in ("timeout", "http_timeout"):
                if qs.get(k):
                    try:
                        args[k] = float(qs[k][0])
                    except Exception:
                        pass
            if qs.get("max_hosts"):
                try:
                    args["max_hosts"] = int(qs["max_hosts"][0])
                except Exception:
                    pass
            if qs.get("wait_s"):
                try:
                    args["wait_s"] = float(qs["wait_s"][0])
                except Exception:
                    pass
            try:
                return self._json(200, t_discover(args))
            except Exception as e:
                return self._json(400, {"error": str(e)})
        if path.startswith("/tasks/") and path.endswith("/logs"):
            if not self._guard("agent"):
                return
            tid = path[len("/tasks/"):-len("/logs")].strip("/")
            qs = parse_qs(parsed.query)
            try:
                out = t_logs({"task_id": tid,
                              "since": qs.get("since", ["0"])[0],
                              "tail": qs.get("tail", ["200"])[0],
                              "limit": qs.get("limit", ["200"])[0],
                              "wait_s": qs.get("wait_s", ["0"])[0]})
            except Exception as e:
                return self._json(400, {"error": str(e)})
            return self._json(200, out)
        self._json(404, {"error": "not found"})

    def _artifact(self, path):
        rest = path[len("/artifacts/"):]
        if "/" in rest:
            tid, name = rest.split("/", 1)
            name = unquote(name)
        else:
            tid, name = rest.strip("/"), ""
        if not name:
            rows = q("SELECT * FROM artifacts WHERE task_id=? ORDER BY name", (tid,))
            return self._json(200, {"task_id": tid,
                                    "artifacts": [_artifact_meta(a) for a in rows]})
        row = q("SELECT * FROM artifacts WHERE task_id=? AND (name=? OR relpath=?)",
                (tid, name, name), one=True)
        if not row:
            return self._json(404, {"error": "artifact not found"})
        real = os.path.realpath(row.get("path") or "")
        root = os.path.realpath(ART_DIR)
        if not (real == root or real.startswith(root + os.sep)) or not os.path.isfile(real):
            return self._json(403, {"error": "bad artifact path"})
        try:
            with open(real, "rb") as f:
                data = f.read()
        except Exception as e:
            return self._json(500, {"error": str(e)})
        mime = row.get("mime") or mimetypes.guess_type(real)[0] or "application/octet-stream"
        self.send_response(200)
        self.send_header("Content-Type", mime)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("X-Content-SHA256", row.get("sha256") or "")
        self.send_header("X-Content-Type-Options", "nosniff")
        safe_name = os.path.basename(row.get("name") or os.path.basename(real))             .replace('"', "_")
        self.send_header("Content-Disposition", f'inline; filename="{safe_name}"')
        self.end_headers()
        self.wfile.write(data)

    def _sse(self, parsed=None):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "keep-alive")
        self.end_headers()
        cur = 0
        try:
            cur = int(self.headers.get("Last-Event-ID") or 0)
        except Exception:
            pass
        try:
            qs = parse_qs((parsed.query if parsed else ""))
            if qs.get("since"):
                cur = max(cur, int(qs["since"][0]))
        except Exception:
            pass
        try:
            while True:
                for r in q("SELECT * FROM events WHERE id>? ORDER BY id LIMIT 200", (cur,)):
                    cur = r["id"]
                    self.wfile.write(f"id: {cur}\nevent: {r['type']}\ndata: {r['payload']}\n\n".encode())
                    self.wfile.flush()
                self.wfile.write(b": ping\n\n")
                self.wfile.flush()
                time.sleep(1.0)
        except (BrokenPipeError, ConnectionResetError, OSError):
            return

    # ---- POST
    def do_POST(self):
        parsed = urlparse(self.path)
        # ---- worker REST：先读 name，才能做每 worker token 校验
        if parsed.path.startswith("/worker/"):
            raw = self._body()
            try:
                a = json.loads(raw or b"{}")
            except Exception:
                if not self._guard("worker"):
                    return
                return self._json(400, {"error": "bad json"})
            if not self._guard("worker", a.get("name")):
                return
            try:
                return self._json(200, worker_api(parsed.path[8:], a))
            except Exception as e:
                return self._json(400, {"error": str(e)})
        if not self._guard("agent"):
            return
        raw = self._body()
        # ---- REST convenience for scripts/GUI clients without MCP
        if parsed.path == "/ask":
            try:
                a = json.loads(raw or b"{}")
            except Exception:
                return self._json(400, {"error": "bad json"})
            try:
                return self._json(200, t_ask(a))
            except Exception as e:
                return self._json(400, {"error": str(e)})
        if parsed.path in ("/chat", "/api/chat"):
            try:
                a = json.loads(raw or b"{}")
            except Exception:
                return self._json(400, {"error": "bad json"})
            try:
                return self._json(200, t_chat(a))
            except Exception as e:
                return self._json(400, {"error": str(e)})
        # ---- MCP
        if parsed.path.startswith("/mcp"):
            try:
                msg = json.loads(raw or b"{}")
            except Exception:
                return self._json(400, {"jsonrpc": "2.0", "id": None,
                                        "error": {"code": -32700, "message": "parse error"}})
            resp = mcp_handle(msg)
            if resp is None:                       # notification
                self.send_response(202)
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
            return self._json(200, resp)
        self._json(404, {"error": "not found"})


# -------------------------------------------------------------------- worker api

def _task_for_worker(tid, name, claim_token):
    r = q("SELECT * FROM tasks WHERE id=?", (tid,), one=True)
    if not r:
        return None, {"ok": False, "error": "task not found"}
    if r.get("worker") != name:
        return None, {"ok": False, "stale": True, "error": "worker mismatch",
                      "status": r.get("status")}
    if r.get("claim_token") and claim_token and r["claim_token"] != claim_token:
        return None, {"ok": False, "stale": True, "error": "claim token mismatch",
                      "status": r.get("status")}
    if r.get("claim_token") and not claim_token:
        return None, {"ok": False, "stale": True, "error": "claim token required",
                      "status": r.get("status")}
    return r, None


def _cancel_ids(name):
    rows = q("""SELECT id FROM tasks WHERE worker=? AND cancel_requested=1
                AND status NOT IN ('approved','failed','cancelled')""", (name,))
    return [r["id"] for r in rows]


def worker_api(op, a):
    name = a.get("name") or "anonymous"
    if op == "register":
        env = (a.get("env") or "").strip()
        if ALLOWED_ENVS and env not in ALLOWED_ENVS:
            return {"ok": False, "error": f"env {env!r} not allowed",
                    "allowed": sorted(ALLOWED_ENVS)}
        caps = _json_list(a.get("caps"))
        prev = q("SELECT current_task FROM workers WHERE name=?", (name,), one=True)
        x("""INSERT INTO workers(name,env,agent,caps,started_at,last_seen,current_task,
                                 version,pid,hostname)
             VALUES(?,?,?,?,?,?,NULL,?,?,?)
             ON CONFLICT(name) DO UPDATE SET env=?,agent=?,caps=?,last_seen=?,
                 current_task=NULL,version=?,pid=?,hostname=?""",
          (name, env, a.get("agent", ""), json.dumps(caps), now(), now(),
           a.get("version", ""), a.get("pid"), a.get("hostname", ""),
           env, a.get("agent", ""), json.dumps(caps), now(),
           a.get("version", ""), a.get("pid"), a.get("hostname", "")))
        emit("worker.online", {"name": name, "env": env, "caps": caps})
        return {"ok": True, "now": now(),
                "previous_task": (prev or {}).get("current_task"),
                "config": {"lease_s": LEASE_S, "progress_s": 3,
                           "max_artifact_bytes": MAX_ARTIFACT_BYTES}}
    if op == "heartbeat":
        x("UPDATE workers SET last_seen=? WHERE name=?", (now(), name))
        return {"ok": True, "now": now(), "cancel": _cancel_ids(name)}
    if op == "claim":
        env = a.get("env") or ""
        task = None
        with _lock:                                  # 临界区：取一条 + 原子置 running
            c = db()
            try:
                w = c.execute("SELECT * FROM workers WHERE name=?", (name,)).fetchone()
                caps = _json_list(dict(w)["caps"] if w else [])
                rows = c.execute(
                    """SELECT * FROM tasks
                       WHERE status='queued' AND (env='' OR ?='' OR env=?)
                       ORDER BY priority DESC, retry ASC, created_at ASC LIMIT 200""",
                    (env, env)).fetchall()
                for row in rows:
                    req = _json_list(dict(row).get("required_caps"))
                    if not _caps_match(caps, req):
                        continue
                    token = uuid.uuid4().hex
                    lease = int(dict(row).get("lease_s") or LEASE_S)
                    cur = c.execute(
                        """UPDATE tasks SET status='running', worker=?, claim_token=?,
                               last_claim_token=NULL,
                               started_at=?, heartbeat_at=?, lease_expires_at=?,
                               cancel_requested=0, cancel_reason=NULL,
                               recoveries=COALESCE(recoveries,0), updated_at=?
                           WHERE id=? AND status='queued'""",
                        (name, token, now(), now(), now(lease), now(), row["id"]))
                    c.commit()
                    if cur.rowcount:
                        task = dict(row)
                        task.update(status="running", worker=name, claim_token=token,
                                    lease_expires_at=now(lease))
                        break
            finally:
                c.close()
        if not task:
            x("UPDATE workers SET last_seen=? WHERE name=?", (now(), name))
            return {"task": None}
        emit("task.claimed", {"id": task["id"], "worker": name})
        emit("task.running", {"id": task["id"], "worker": name})
        x("UPDATE workers SET last_seen=?,current_task=? WHERE name=?",
          (now(), task["id"], name))
        task["log_seq"] = int(task.get("log_seq") or 0)
        return {"task": task}
    if op == "progress":
        tid = a.get("task_id")
        r, err = _task_for_worker(tid, name, a.get("claim_token"))
        if err:
            return err
        if r["status"] not in ACTIVE:
            return {"ok": False, "invalid": True, "status": r["status"],
                    "cancel": [tid] if r["status"] == "cancelled" else []}
        lease = int(r.get("lease_s") or LEASE_S)
        x("""UPDATE tasks SET heartbeat_at=?, lease_expires_at=?, updated_at=?
              WHERE id=?""", (now(), now(lease), now(), tid))
        x("UPDATE workers SET last_seen=? WHERE name=?", (now(), name))
        cancelled = bool(r.get("cancel_requested")) or r["status"] == "cancelled"
        return {"ok": True, "now": now(), "cancel": [tid] if cancelled else [],
                "status": r["status"],
                "lease_expires_at": now(lease)}
    if op == "log":
        tid = a.get("task_id")
        r, err = _task_for_worker(tid, name, a.get("claim_token"))
        if err:
            return err
        if r["status"] not in ACTIVE:
            return {"ok": False, "stale": True, "status": r["status"]}
        lines = a.get("lines") or []
        if not isinstance(lines, list):
            lines = [{"seq": 0, "text": str(lines)}]
        n = 0
        last_seq = int(r.get("log_seq") or 0)
        with _lock:
            c = db()
            try:
                for ln in lines:
                    if not isinstance(ln, dict):
                        ln = {"text": str(ln)}
                    try:
                        seq = int(ln.get("seq") or 0)
                    except Exception:
                        seq = 0
                    if seq <= 0:
                        seq = last_seq + 1
                    text = str(ln.get("text") or "")[:20000]
                    stream = str(ln.get("stream") or "stdout")[:32]
                    cur = c.execute(
                        "INSERT OR IGNORE INTO task_logs(task_id,seq,stream,text,created_at) "
                        "VALUES(?,?,?,?,?)", (tid, seq, stream, text, now()))
                    n += max(0, cur.rowcount)
                    last_seq = max(last_seq, seq)
                c.execute("""UPDATE tasks SET log_seq=?, log_lines=COALESCE(log_lines,0)+?,
                                  last_log_at=?, heartbeat_at=?, lease_expires_at=?,
                                  updated_at=? WHERE id=?""",
                          (last_seq, n, now(), now(),
                           now(int(r.get("lease_s") or LEASE_S)), now(), tid))
                c.commit()
            finally:
                c.close()
        if n:
            tail = (lines[-1].get("text") if isinstance(lines[-1], dict) else str(lines[-1])) or ""
            emit("task.log", {"id": tid, "seq": last_seq,
                              "stream": (lines[-1].get("stream", "stdout")
                                         if isinstance(lines[-1], dict) else "stdout"),
                              "text": tail[:1000]})
        return {"ok": True, "next_seq": last_seq}
    if op == "submit":
        tid = a["task_id"]
        status = a.get("status", "submitted")
        if status not in ("submitted", "failed", "cancelled"):
            return {"error": "bad status"}
        r = q("SELECT * FROM tasks WHERE id=?", (tid,), one=True)
        if not r:
            return {"error": "not found", "task_id": tid}
        claim = a.get("claim_token") or ""
        if r["status"] in ("approved", "failed"):
            return {"ok": True, "ignored": f"task already {r['status']}",
                    "task_id": tid}
        if r["status"] == "cancelled" or r.get("cancel_requested"):
            if r["status"] == "cancelled":
                _append_hub_log(tid, "[hub] 已取消，拒绝 worker 的迟到提交")
                return {"ok": True, "ignored": "cancelled", "task_id": tid}
        if r["status"] == "submitted":
            # 提交幂等：迟到/重试的同一份提交不能把 submitted 改成别的状态
            return {"ok": True, "ignored": "already submitted", "task_id": tid}

        # 租约恢复后的“迟到提交”：任务已回到 queued、尚未被新 worker 领取，
        # 且携带恢复前最后一次 claim token，则接受这份已经跑完的结果，避免白跑。
        recovered = False
        if r["status"] == "queued":
            old = r.get("last_claim_token") or ""
            recovered = bool(claim and old and not r.get("worker")
                             and hmac.compare_digest(str(claim), str(old)))
            if not recovered:
                return {"ok": True, "ignored": "task is queued (rework/recovered)",
                        "task_id": tid}
            if status == "cancelled":
                return {"ok": True, "ignored": "recovered task awaits re-claim",
                        "task_id": tid}

        if not recovered:
            if r.get("worker") != name:
                return {"ok": True, "ignored": "worker mismatch", "task_id": tid}
            if r.get("claim_token") and claim and r["claim_token"] != claim:
                return {"ok": True, "ignored": "stale claim token", "task_id": tid}
            if r.get("claim_token") and not claim:
                return {"ok": True, "ignored": "claim token required", "task_id": tid}
            if r["status"] not in ACTIVE:
                return {"ok": True, "ignored": f"task is {r['status']}", "task_id": tid}

        files = []
        if status != "cancelled":
            files = save_artifacts(tid, a.get("artifacts") or [])
        result = (a.get("result") or "")[:20000]
        verify_out = (a.get("verify_out") or "")[:20000]
        diff = (a.get("diff") or "")[:200000]
        changed = (a.get("changed") or "")[:8000]
        composed = (a.get("composed_prompt") or "")[:40000]
        # 条件更新：与 cancel/rework/reaper 竞争时，谁先落库谁赢；
        # 绝不能用无条件 UPDATE 把 cancelled 又改回 submitted。
        if recovered:
            cur = x("""UPDATE tasks SET status=?, worker=?, result=?, exit_code=?,
                               verify_ok=?, verify_out=?, diff=?, changed=?,
                               composed_prompt=?, claim_token=NULL,
                               last_claim_token=NULL, updated_at=?, finished_at=?,
                               lease_expires_at=NULL, heartbeat_at=?
                       WHERE id=? AND status='queued' AND worker IS NULL
                         AND COALESCE(last_claim_token,'')=?""",
                    (status, name, result, a.get("exit_code"),
                     1 if a.get("verify_ok") else 0, verify_out, diff, changed,
                     composed or None, now(), now(), now(), tid, claim))
        else:
            cur = x("""UPDATE tasks SET status=?, worker=?, result=?, exit_code=?,
                               verify_ok=?, verify_out=?, diff=?, changed=?,
                               composed_prompt=?, claim_token=NULL,
                               last_claim_token=NULL, updated_at=?, finished_at=?,
                               lease_expires_at=NULL, heartbeat_at=?
                       WHERE id=? AND status IN ('claimed','running')
                         AND worker=? AND COALESCE(claim_token,'')=?""",
                    (status, name, result, a.get("exit_code"),
                     1 if a.get("verify_ok") else 0, verify_out, diff, changed,
                     composed or None, now(), now(), now(), tid, name, claim))
        if not cur.rowcount:
            for f in files:
                try:
                    x("DELETE FROM artifacts WHERE task_id=? AND name=?",
                      (tid, f["name"]))
                except Exception:
                    pass
            return {"ok": True, "ignored": "task state changed during submit",
                    "task_id": tid}
        emit("task." + status, {"id": tid, "worker": name,
                                "exit_code": a.get("exit_code"),
                                "verify_ok": 1 if a.get("verify_ok") else 0})
        x("UPDATE workers SET current_task=NULL,last_seen=? WHERE name=?", (now(), name))
        return {"ok": True, "artifacts": files, "task_id": tid, "status": status}
    if op == "recover":
        # worker 重启后主动交还自己遗留的 claimed/running 任务；租约过期的也一并处理
        rows = q("SELECT * FROM tasks WHERE worker=? AND status IN ('claimed','running')",
                 (name,))
        done, failed = [], []
        for r in rows:
            age = time.time() - parse_ts(r.get("heartbeat_at") or r.get("started_at") or "")
            if a.get("force") or age > float(a.get("max_age_s", 15) or 15) \
                    or parse_ts(r.get("lease_expires_at") or "") < time.time():
                ok = _recover_task(r, f"worker {name} restarted / lost")
                (failed if ok == "failed" else done).append(r["id"])
        return {"ok": True, "recovered": done, "failed": failed}
    if op == "leave":
        x("UPDATE workers SET last_seen=?,current_task=NULL WHERE name=?",
          (now(), name))
        return {"ok": True}
    return {"error": "unknown op"}


def _safe_relpath(name, fallback="artifact.bin"):
    raw = str(name or "").replace("\\", "/")
    parts = []
    for p in raw.split("/"):
        if p in ("", ".", ".."):
            continue
        if re.match(r"^[A-Za-z]:$", p):
            continue
        p = p.replace(":", "_")
        parts.append(p)
    if not parts:
        parts = [fallback]
    return "/".join(parts)


def save_artifacts(tid, items):
    """接受三种产物：
       - "C:\\path\\file.png"（hub 与 worker 共享文件系统的旧用法）
       - {"path": "...", "name": "..."}
       - {"name": "...", "data": "<base64>", "mime": "...", "sha256": "..."}（跨机上传）
    """
    d = os.path.join(ART_DIR, tid)
    os.makedirs(d, exist_ok=True)
    root = os.path.realpath(d)
    out = []
    for it in (items or [])[:MAX_ARTIFACT_COUNT]:
        try:
            if isinstance(it, str):
                info = {"path": it}
            elif isinstance(it, dict):
                info = it
            else:
                continue
            data = None
            if info.get("data"):
                data = base64.b64decode(info["data"], validate=False)
            else:
                p = os.path.abspath(str(info.get("path") or ""))
                if not os.path.isfile(p):
                    continue
                if os.path.getsize(p) > MAX_ARTIFACT_BYTES:
                    continue
                with open(p, "rb") as f:
                    data = f.read()
            if data is None or len(data) > MAX_ARTIFACT_BYTES:
                continue
            sha = hashlib.sha256(data).hexdigest()
            if info.get("sha256") and str(info["sha256"]).lower() != sha:
                continue
            if info.get("name"):
                rel = _safe_relpath(info["name"])
            elif info.get("path"):
                rel = _safe_relpath(os.path.basename(str(info["path"])))
            else:
                rel = _safe_relpath("artifact.bin")
            dst = os.path.realpath(os.path.join(d, *rel.split("/")))
            if not (dst == root or dst.startswith(root + os.sep)):
                continue
            os.makedirs(os.path.dirname(dst), exist_ok=True)
            with open(dst, "wb") as f:
                f.write(data)
            mime = info.get("mime") or _guess_mime(dst)
            with _lock:
                c = db()
                try:
                    c.execute("DELETE FROM artifacts WHERE task_id=? AND name=?",
                              (tid, rel))
                    c.execute("""INSERT INTO artifacts(task_id,name,relpath,path,mime,
                                                       sha256,size,created_at)
                                 VALUES(?,?,?,?,?,?,?,?)""",
                              (tid, rel, rel, dst, mime, sha, len(data), now()))
                    c.commit()
                finally:
                    c.close()
            out.append({"name": rel, "path": dst, "mime": mime, "sha256": sha,
                        "size": len(data)})
        except Exception:
            continue
    return out


def _recover_task(r, reason):
    """把丢失 worker 的任务重排或判死。返回 queued|failed|None。"""
    tid = r["id"]
    with _lock:
        fresh = q("SELECT * FROM tasks WHERE id=?", (tid,), one=True)
        if not fresh or fresh["status"] not in ACTIVE:
            return None
        rec = int(fresh.get("recoveries") or 0) + 1
        max_rec = int(fresh.get("max_recoveries") or MAX_RECOVERIES)
        if rec > max_rec:
            x("""UPDATE tasks SET status='failed', recoveries=?, worker=NULL,
                      claim_token=NULL, lease_expires_at=NULL, heartbeat_at=NULL,
                      review_note=?, finished_at=?, updated_at=? WHERE id=?""",
              (rec, f"[执行租约丢失] {reason}", now(), now(), tid))
            emit("task.failed", {"id": tid, "recoveries": rec, "reason": reason})
            _append_hub_log(tid, f"[hub] 执行租约丢失，已超过恢复上限：{reason}")
            return "failed"
        x("""UPDATE tasks SET status='queued', recoveries=?, worker=NULL,
                  last_claim_token=claim_token, claim_token=NULL,
                  lease_expires_at=NULL, heartbeat_at=NULL,
                  cancel_requested=0, cancel_reason=NULL, composed_prompt=NULL,
                  review_note=?, updated_at=? WHERE id=?""",
          (rec, f"[自动重排 {rec}/{max_rec}] {reason}", now(), tid))
        emit("task.recovered", {"id": tid, "recoveries": rec, "reason": reason,
                                "status": "queued"})
        _append_hub_log(tid, f"[hub] worker 失联，任务自动重排：{reason}")
    return "queued"


def reap_once():
    rows = q("SELECT * FROM tasks WHERE status IN ('claimed','running')")
    ts = time.time()
    for r in rows:
        lease = r.get("lease_expires_at")
        expired = (not lease) or parse_ts(lease) < ts
        if not expired:
            continue
        _recover_task(r, f"lease expired at {lease or 'never'}")
    return len(rows)


def reaper_loop():
    while not _reaper_stop.is_set():
        try:
            reap_once()
        except Exception as e:
            print(f"[hub] reaper error: {e}", flush=True)
        _reaper_stop.wait(REAP_S)


# -------------------------------------------------------------------------- mcp

def _tool_result(name, out):
    out = dict(out or {})
    images = out.pop("_images", None)
    content = [{"type": "text",
                "text": json.dumps(out, ensure_ascii=False, default=str)}]
    for img in (images or []):
        if img.get("data"):
            content.append({"type": "image", "data": img["data"],
                            "mimeType": img.get("mimeType") or "image/png"})
    return {"content": content, "isError": False}


def mcp_handle(msg):
    if isinstance(msg, list):
        res = [mcp_handle(m) for m in msg]
        return [r for r in res if r]
    mid = msg.get("id")
    method = msg.get("method")
    params = msg.get("params") or {}
    if mid is None:
        return None                                # notification，不回复
    if method == "initialize":
        want = params.get("protocolVersion") or "2025-06-18"
        return {"jsonrpc": "2.0", "id": mid, "result": {
            "protocolVersion": want,
            "capabilities": {"tools": {"listChanged": False}},
            "serverInfo": {"name": "lanbus", "version": VERSION},
            "instructions": ("LAN 跨环境 agent 编排中枢。先 lan_envs 看在线环境与能力，"
                             "再 lan_dispatch 派单（用 context 交接背景）；"
                             "用 lan_logs 看实时执行日志；用 lan_discover 侦查局域网里已打开的 AI agent；"
                             "结果以 submitted 回来时用 lan_result 看 diff、verify 与图片产物，"
                             "用 lan_review 决定 approve 或 rework；lan_cancel 会真正终止远端进程。")}}
    if method == "ping":
        return {"jsonrpc": "2.0", "id": mid, "result": {}}
    if method == "tools/list":
        return {"jsonrpc": "2.0", "id": mid, "result": {"tools": [
            {"name": n, "description": desc.strip(), "inputSchema": schema}
            for n, (f, desc, schema) in TOOLS.items()]}}
    if method == "tools/call":
        name = params.get("name")
        args = params.get("arguments") or {}
        if name not in TOOLS:
            return {"jsonrpc": "2.0", "id": mid,
                    "error": {"code": -32602, "message": f"unknown tool {name}"}}
        fn, desc, schema = TOOLS[name]
        miss = [k for k in schema.get("required", []) if k not in args or args[k] in (None, "")]
        if miss:
            return {"jsonrpc": "2.0", "id": mid,
                    "error": {"code": -32602, "message": f"missing required: {miss}"}}
        try:
            out = fn(args)
        except Exception as e:
            return {"jsonrpc": "2.0", "id": mid,
                    "result": {"content": [{"type": "text",
                                            "text": json.dumps({"error": str(e)}, ensure_ascii=False)}],
                               "isError": True}}
        return {"jsonrpc": "2.0", "id": mid,
                "result": _tool_result(name, out)}
    return {"jsonrpc": "2.0", "id": mid,
            "error": {"code": -32601, "message": f"unknown method {method}"}}


# ------------------------------------------------------------------------- main

def _parse_worker_tokens(text, from_file=False):
    text = (text or "").strip()
    if not text:
        return {}
    try:
        obj = json.loads(text)
        if isinstance(obj, dict):
            return {str(k): str(v) for k, v in obj.items() if k and v}
    except Exception:
        pass
    out = {}
    for line in text.splitlines():
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        if "=" in line:
            k, v = line.split("=", 1)
        elif ":" in line:
            k, v = line.split(":", 1)
        else:
            continue
        k, v = k.strip(), v.strip()
        if k and v:
            out[k] = v
    if not out and from_file:
        raise ValueError("no name=token entries")
    return out


def _read_or_create(path):
    if os.path.isfile(path):
        t = open(path, encoding="utf-8").read().strip()
        if t:
            return t
    t = uuid.uuid4().hex
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(t)
    try:
        os.chmod(path, 0o600)
    except Exception:
        pass
    return t


def main():
    global TOKEN, AGENT_TOKEN, WORKER_TOKEN, WORKER_TOKENS, HOSTS
    global DATA_DIR, DB_PATH, ART_DIR, TOKEN_FILE, WORKER_TOKEN_FILE
    global ALLOWED_ENVS, MAX_ARTIFACT_BYTES, LEASE_S, REAP_S, MAX_RECOVERIES

    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=8788)
    ap.add_argument("--bind", default="0.0.0.0")
    ap.add_argument("--token", default=os.environ.get("LANBUS_TOKEN", ""),
                    help="主 agent token（MCP/读接口）")
    ap.add_argument("--worker-token", default=os.environ.get("LANBUS_WORKER_TOKEN", ""),
                    help="worker 共用 token；留空则沿用 --token（向后兼容）")
    ap.add_argument("--worker-tokens", default=os.environ.get("LANBUS_WORKER_TOKENS", ""),
                    help='每 worker 独立 token，JSON：{"win-ui":"token1","wsl-core":"token2"}')
    ap.add_argument("--worker-tokens-file", default=os.environ.get("LANBUS_WORKER_TOKENS_FILE", ""),
                    help="每 worker token 文件（JSON 或 name=token 行）")
    ap.add_argument("--hosts", default=os.environ.get("LANBUS_HOSTS", ""))
    ap.add_argument("--allowed-envs", default=os.environ.get("LANBUS_ALLOWED_ENVS", ""))
    ap.add_argument("--lease-s", type=int, default=int(os.environ.get("LANBUS_LEASE_S", "120")))
    ap.add_argument("--reap-s", type=float, default=float(os.environ.get("LANBUS_REAP_S", "10")))
    ap.add_argument("--max-recoveries", type=int,
                    default=int(os.environ.get("LANBUS_MAX_RECOVERIES", "3")))
    ap.add_argument("--max-artifact-mb", type=int,
                    default=int(os.environ.get("LANBUS_MAX_ARTIFACT_MB", "32")))
    ap.add_argument("--tls-cert", default=os.environ.get("LANBUS_TLS_CERT", ""))
    ap.add_argument("--tls-key", default=os.environ.get("LANBUS_TLS_KEY", ""))
    ap.add_argument("--no-reaper", action="store_true")
    ap.add_argument("--version", action="version", version=f"lanbus {VERSION}")
    ap.add_argument("--data-dir", default="", help="覆盖 LANBUS_DATA_DIR")
    a = ap.parse_args()

    if a.data_dir:
        DATA_DIR = os.path.abspath(a.data_dir)
        DB_PATH = os.path.join(DATA_DIR, "bus.db")
        ART_DIR = os.path.join(DATA_DIR, "artifacts")
        TOKEN_FILE = os.path.join(DATA_DIR, "token.txt")
        WORKER_TOKEN_FILE = os.path.join(DATA_DIR, "worker-token.txt")

    LEASE_S = max(15, a.lease_s)
    REAP_S = max(1.0, a.reap_s)
    MAX_RECOVERIES = max(0, a.max_recoveries)
    MAX_ARTIFACT_BYTES = max(1, a.max_artifact_mb) * 1024 * 1024
    ALLOWED_ENVS = {s.strip() for s in re.split(r"[,;\s]+", a.allowed_envs or "") if s.strip()}

    init_db()

    if a.token:
        AGENT_TOKEN = a.token
    else:
        AGENT_TOKEN = _read_or_create(TOKEN_FILE)
    if a.worker_token:
        WORKER_TOKEN = a.worker_token
    elif os.path.isfile(WORKER_TOKEN_FILE):
        WORKER_TOKEN = open(WORKER_TOKEN_FILE, encoding="utf-8").read().strip()
    else:
        WORKER_TOKEN = AGENT_TOKEN           # 单口令：向后兼容
    TOKEN = AGENT_TOKEN

    WORKER_TOKENS = _parse_worker_tokens(a.worker_tokens)
    tf = a.worker_tokens_file
    if not tf and os.path.isfile(os.path.join(DATA_DIR, "worker-tokens.json")):
        tf = os.path.join(DATA_DIR, "worker-tokens.json")
    if tf:
        try:
            WORKER_TOKENS.update(_parse_worker_tokens(
                open(tf, encoding="utf-8").read(), from_file=True))
        except Exception as e:
            print(f"[hub] worker-tokens-file error: {e}", flush=True)

    if a.hosts:
        HOSTS |= {h.strip() for h in a.hosts.split(",") if h.strip()}

    srv = ThreadingHTTPServer((a.bind, a.port), H)
    srv.daemon_threads = True
    scheme = "http"
    if a.tls_cert and a.tls_key:
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(a.tls_cert, a.tls_key)
        srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
        scheme = "https"

    if not a.no_reaper:
        threading.Thread(target=reaper_loop, daemon=True,
                         name="lanbus-reaper").start()

    print(f"[hub] listening on {scheme}://{a.bind}:{a.port}   "
          f"agent_token={AGENT_TOKEN[:6]}…", flush=True)
    if WORKER_TOKEN != AGENT_TOKEN:
        print(f"[hub] worker token 独立配置（worker-token.txt）", flush=True)
    if WORKER_TOKENS:
        print(f"[hub] per-worker tokens: {len(WORKER_TOKENS)} 个身份", flush=True)
    print(f"[hub] MCP   {scheme}://<主机IP>:{a.port}/mcp", flush=True)
    print(f"[hub] SSE   {scheme}://<主机IP>:{a.port}/events", flush=True)
    print(f"[hub] logs  {scheme}://<主机IP>:{a.port}/tasks/<id>/logs", flush=True)
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        _reaper_stop.set()


if __name__ == "__main__":
    main()
