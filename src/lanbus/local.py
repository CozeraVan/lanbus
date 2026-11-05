"""Local worker supervisor: keep this machine's agents online across reboots.

Config: ~/.lanbus/local.json
PIDs/logs: ~/.lanbus/local/
"""

import json
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.request

HOME = os.path.expanduser("~")
DATA = os.path.join(HOME, ".lanbus")
CONFIG = os.path.join(DATA, "local.json")
RUNDIR = os.path.join(DATA, "local")


def _mkdir():
    os.makedirs(RUNDIR, exist_ok=True)


def default_config():
    here = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
    go = os.path.join(here, "bin", "lanbus-worker")
    if os.name == "nt":
        go += ".exe"
    return {
        "hub": "http://127.0.0.1:8788",
        "token_file": os.path.join(DATA, "token.txt"),
        "workers": [
            {"name": "win-claude", "env": "win-claude", "agent": "claude",
             "cwd": here, "binary": go},
            {"name": "win-deepseek", "env": "win-deepseek", "agent": "deepseek",
             "cwd": here, "binary": go, "cmd": "dsh headless -"},
            {"name": "win-kimi", "env": "win-kimi", "agent": "kimi",
             "cwd": here, "binary": go},
        ],
    }


def load_config(path=CONFIG):
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def write_config(cfg, path=CONFIG):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(cfg, f, ensure_ascii=False, indent=2)
        f.write("\n")


def _token(cfg):
    tf = cfg.get("token_file") or os.path.join(DATA, "token.txt")
    try:
        return open(tf, encoding="utf-8").read().strip()
    except Exception:
        return cfg.get("token") or os.environ.get("LANBUS_TOKEN", "")


def _hub(cfg):
    return (cfg.get("hub") or os.environ.get("LANBUS_HUB") or
            "http://127.0.0.1:8788").rstrip("/")


def hub_online_envs(cfg):
    """Return {env: online} from /api/envs; empty on error."""
    try:
        req = urllib.request.Request(_hub(cfg) + "/api/envs",
                                     headers={"Authorization": "Bearer " + _token(cfg)})
        with urllib.request.urlopen(req, timeout=4) as r:
            data = json.loads(r.read().decode() or "{}")
        return {w.get("env"): bool(w.get("online")) for w in data.get("workers") or []}
    except Exception:
        return {}


def pid_path(name):
    return os.path.join(RUNDIR, name + ".pid")


def log_path(name):
    return os.path.join(RUNDIR, name + ".log")


def _alive(pid):
    if not pid or pid <= 0:
        return False
    if os.name == "nt":
        try:
            import ctypes
            h = ctypes.windll.kernel32.OpenProcess(0x1000, False, int(pid))
            if not h:
                return False
            ctypes.windll.kernel32.CloseHandle(h)
            return True
        except Exception:
            return False
    try:
        os.kill(pid, 0)
        return True
    except Exception:
        return False


def _read_pid(name):
    try:
        return int(open(pid_path(name), encoding="utf-8").read().strip())
    except Exception:
        return 0


def _worker_args(w, cfg, token):
    binary = w.get("binary") or ""
    kind = (w.get("kind") or "go").lower()
    args = [binary] if kind == "go" else [sys.executable, "-m", "lanbus", "worker"]
    args += ["--name", w["name"], "--env", w["env"],
             "--hub", w.get("hub") or _hub(cfg), "--token", token,
             "--agent", w.get("agent") or "claude"]
    if w.get("cwd"):
        args += ["--cwd", w["cwd"]]
    if w.get("cmd"):
        args += ["--cmd", w["cmd"]]
    if w.get("pty"):
        args += ["--pty"]
    if w.get("caps"):
        args += ["--caps", w["caps"]]
    return args


def _redact(args):
    out = list(args)
    for i, x in enumerate(out[:-1]):
        if x == "--token":
            out[i + 1] = "***"
    return out


def _wsl_command(w, cfg, token):
    args = _worker_args(w, cfg, token)
    # The command runs inside the distro; quote each argv for sh.
    import shlex
    line = "exec " + " ".join(shlex.quote(str(a)) for a in args)
    return ["wsl.exe", "-d", w["wsl"], "-e", "bash", "-lc", line]


def start_worker(w, cfg, token, dry_run=False):
    name = w.get("name")
    if not name or not w.get("env"):
        return {"name": name, "ok": False, "error": "name/env required"}
    pid = _read_pid(name)
    if _alive(pid):
        return {"name": name, "ok": True, "skipped": "pid " + str(pid)}
    if w.get("wsl"):
        args = _wsl_command(w, cfg, token)
    else:
        args = _worker_args(w, cfg, token)
    if dry_run:
        return {"name": name, "ok": True, "dry_run": _redact(args)}
    _mkdir()
    log = open(log_path(name), "ab")
    kwargs = {"stdin": subprocess.DEVNULL, "stdout": log, "stderr": subprocess.STDOUT,
              "close_fds": True}
    if os.name == "nt":
        # wsl.exe is killed by DETACHED_PROCESS; a new process group survives.
        kwargs["creationflags"] = 0x00000200 if w.get("wsl") else 0x00000008 | 0x00000200
    else:
        kwargs["start_new_session"] = True
    try:
        p = subprocess.Popen(args, **kwargs)
    except Exception as e:
        log.close()
        return {"name": name, "ok": False, "error": str(e)}
    with open(pid_path(name), "w", encoding="utf-8") as f:
        f.write(str(p.pid))
    return {"name": name, "ok": True, "pid": p.pid, "log": log_path(name)}


def stop_wsl(w, dry_run=False):
    name = w.get("name") or ""
    distro = w.get("wsl")
    if not distro or not name:
        return {"name": name, "ok": False, "error": "wsl/name required"}
    line = "pkill -f " + repr("lanbus-worker-go.*--name " + name) + " || true"
    if dry_run:
        return {"name": name, "ok": True, "dry_run": ["wsl.exe", "-d", distro, "bash", "-lc", line]}
    subprocess.run(["wsl.exe", "-d", distro, "-e", "bash", "-lc", line],
                   capture_output=True)
    try:
        os.unlink(pid_path(name))
    except Exception:
        pass
    return {"name": name, "ok": True, "wsl": distro}


def stop_worker(name, dry_run=False):
    pid = _read_pid(name)
    if not _alive(pid):
        try:
            os.unlink(pid_path(name))
        except Exception:
            pass
        return {"name": name, "ok": True, "skipped": "not running"}
    if dry_run:
        return {"name": name, "ok": True, "dry_run": "kill " + str(pid)}
    try:
        if os.name == "nt":
            subprocess.run(["taskkill", "/F", "/T", "/PID", str(pid)],
                           capture_output=True)
        else:
            os.killpg(os.getpgid(pid), signal.SIGTERM)
    except Exception:
        try:
            os.kill(pid, signal.SIGTERM)
        except Exception:
            pass
    for _ in range(20):
        if not _alive(pid):
            break
        time.sleep(0.15)
    try:
        os.unlink(pid_path(name))
    except Exception:
        pass
    return {"name": name, "ok": True, "stopped": pid}


def status(cfg):
    envs = hub_online_envs(cfg)
    out = []
    for w in cfg.get("workers") or []:
        name = w.get("name")
        pid = _read_pid(name)
        online = bool(envs.get(w.get("env")))
        out.append({"name": name, "env": w.get("env"), "pid": pid,
                    "running": _alive(pid) or (bool(w.get("wsl")) and online),
                    "hub_online": online, "log": log_path(name)})
    return {"hub": _hub(cfg), "workers": out}


def command(argv):
    action = argv[0] if argv else "status"
    path = CONFIG
    if "--config" in argv:
        try:
            path = argv[argv.index("--config") + 1]
        except Exception:
            pass
    cfg = load_config(path)
    if action == "init":
        if cfg and "--force" not in argv:
            print(json.dumps({"ok": False, "error": "config exists", "path": path},
                             ensure_ascii=False))
            return 1
        cfg = default_config()
        write_config(cfg, path)
        print(json.dumps({"ok": True, "path": path, "config": cfg},
                         ensure_ascii=False, indent=2))
        return 0
    if not cfg:
        print(json.dumps({"ok": False, "error": "missing config; run lanbus local init",
                          "path": path}, ensure_ascii=False))
        return 1
    token = _token(cfg)
    dry = "--dry-run" in argv
    only = None
    if "--only" in argv:
        try:
            only = argv[argv.index("--only") + 1]
        except Exception:
            pass
    workers = [w for w in cfg.get("workers") or []
               if not only or w.get("name") == only]
    if action == "up":
        envs = hub_online_envs(cfg)
        out = []
        for w in workers:
            if envs.get(w.get("env")) and "--force" not in argv:
                out.append({"name": w.get("name"), "ok": True,
                            "skipped": "hub already has online env"})
                continue
            out.append(start_worker(w, cfg, token, dry))
        print(json.dumps({"ok": all(x.get("ok") for x in out), "workers": out},
                         ensure_ascii=False, indent=2))
        return 0 if all(x.get("ok") for x in out) else 1
    if action == "down":
        out = [(stop_wsl(w, dry) if w.get("wsl") else stop_worker(w.get("name"), dry))
               for w in workers]
        print(json.dumps({"ok": True, "workers": out}, ensure_ascii=False, indent=2))
        return 0
    if action in ("status", "ls"):
        print(json.dumps(status(cfg), ensure_ascii=False, indent=2))
        return 0
    print("usage: lanbus local {init|up|down|status} [--config FILE] [--only NAME] [--force] [--dry-run]",
          file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(command(sys.argv[1:]))
