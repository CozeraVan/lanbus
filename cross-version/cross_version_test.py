#!/usr/bin/env python3
"""Black-box bidirectional legacy/current direct-peer compatibility test."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parent
PROJECT = ROOT.parent
BINS = {"1.9.1": ROOT / "v191/bin/lanbus", "1.9.2": ROOT / "v192/bin/lanbus",
        "current": PROJECT / ".venv/bin/python"}
GO_BINS = {"1.9.1": Path("/usr/local/bin/lanbus-worker"),
           "1.9.2": PROJECT / "release-v1.9.2-validation/lanbus-worker-darwin-arm64",
           "current": ROOT / "current-bin/lanbus-worker"}
CASES = [
    ("1.9.1", "1.9.2", 18792, "python"),
    ("1.9.2", "1.9.1", 18791, "python"),
    ("1.9.2", "1.9.1", 18794, "go"),
    ("1.9.1", "1.9.2", 18795, "go"),
    ("current", "current", 18810, "python"),
    ("current", "current", 18811, "go"),
    ("1.9.2", "current", 18812, "python"),
    ("1.9.2", "current", 18813, "go"),
    ("current", "1.9.2", 18814, "python"),
    ("current", "1.9.2", 18815, "go"),
]


def cli(version, *args):
    if version == "current":
        return [str(BINS[version]), "-m", "lanbus", *args]
    return [str(BINS[version]), *args]


def run(cmd, env, timeout=90):
    return subprocess.run(cmd, env=env, text=True, capture_output=True, timeout=timeout)


def pending_request(version, port, target_dir, env, kind=""):
    deadline = time.time() + 15
    while time.time() < deadline:
        inbox = run(cli(version, "inbox", "--url", f"http://127.0.0.1:{port}",
                        "--data-dir", str(target_dir)), env, timeout=10)
        if inbox.returncode == 0:
            requests = json.loads(inbox.stdout).get("requests") or []
            matching = [item for item in requests if not kind or item.get("kind") == kind]
            if matching:
                return matching[0]
        time.sleep(0.2)
    return None


def approve(version, port, target_dir, env, request_id, pairing=False):
    args = ["allow", request_id, "--url", f"http://127.0.0.1:{port}",
            "--data-dir", str(target_dir)]
    if pairing:
        args += ["--pair", "--scope", "chat"]
    result = run(cli(version, *args), env, timeout=15)
    assert result.returncode == 0, result.stdout + result.stderr


def main():
    GO_BINS["current"].parent.mkdir(exist_ok=True)
    subprocess.run([str(PROJECT / ".tools/go/bin/go"), "build", "-o", str(GO_BINS["current"]),
                    "./cmd/lanbus-worker"], cwd=PROJECT / "go", check=True)
    results = []
    processes = []
    logs = []
    try:
        for caller_version, target_version, port, target_kind in CASES:
            name = f"compat-{caller_version.replace('.', '')}-to-{target_version.replace('.', '')}-{target_kind}"
            case_dir = ROOT / f"case-{name}"
            shutil.rmtree(case_dir, ignore_errors=True)
            target_dir = case_dir / "target"
            caller_dir = case_dir / "caller"
            target_dir.mkdir(parents=True)
            caller_dir.mkdir(parents=True)
            target_env = os.environ.copy()
            target_env.update(HOME=str(case_dir / "home-target"), LANBUS_PEER_DIR=str(target_dir),
                              PYTHONUTF8="1")
            target_env.pop("PYTHONPATH", None)
            if target_version == "current":
                target_env["PYTHONPATH"] = str(PROJECT / "src")
            target_env.pop("LANBUS_HUB", None)
            target_env.pop("LANBUS_TOKEN", None)
            if target_kind == "go":
                command = [str(GO_BINS[target_version]), "--name", name, "--env", "macos",
                           "--agent", "echo", "--direct", "--direct-bind", "0.0.0.0",
                           "--direct-port", str(port), "--direct-data-dir", str(target_dir),
                           "--cwd", str(case_dir)]
                if target_version == "current":
                    command += ["--request-policy", "each", "--no-open-approval"]
            else:
                command = cli(target_version, "peer", "--name", name, "--agent", "echo",
                              "--bind", "0.0.0.0", "--port", str(port), "--data-dir", str(target_dir),
                              "--cwd", str(case_dir))
                if target_version == "current":
                    command += ["--request-policy", "each", "--no-open-approval"]
            log = (case_dir / "target.log").open("w")
            logs.append(log)
            target = subprocess.Popen(command, env=target_env, stdout=log,
                                      stderr=subprocess.STDOUT, text=True)
            processes.append(target)
            time.sleep(1)
            assert target.poll() is None, f"target exited: {(case_dir / 'target.log').read_text()}"

            health_env = target_env.copy()
            health_env.pop("PYTHONPATH", None)
            if caller_version == "current":
                health_env["PYTHONPATH"] = str(PROJECT / "src")
            health = None
            for _ in range(30):
                health = run(cli(caller_version, "peers", "--url", f"http://127.0.0.1:{port}"), health_env)
                if health.returncode == 0:
                    break
                assert target.poll() is None, f"target exited: {(case_dir / 'target.log').read_text()}"
                time.sleep(0.2)
            assert health.returncode == 0, health.stdout + health.stderr
            assert json.loads(health.stdout)["name"] == name

            caller_env = os.environ.copy()
            caller_env.update(HOME=str(case_dir / "home-caller"), LANBUS_PEER_DIR=str(caller_dir),
                              PYTHONUTF8="1")
            caller_env.pop("PYTHONPATH", None)
            if caller_version == "current":
                caller_env["PYTHONPATH"] = str(PROJECT / "src")
            caller_env.pop("LANBUS_HUB", None)
            caller_env.pop("LANBUS_TOKEN", None)
            message = f"cross-version-{caller_version}-to-{target_version}-{target_kind}"

            def chat(text):
                return subprocess.Popen(cli(caller_version, "chat", "--to", f"127.0.0.1:{port}",
                    "--message", text, "--pair-timeout", "45", "--timeout", "20"),
                    env=caller_env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

            caller = chat(message)
            pair = pending_request(target_version, port, target_dir, target_env)
            assert pair, "no pairing request"
            approve(target_version, port, target_dir, target_env, pair["request_id"], pairing=True)
            request_v2 = caller_version == "current" and target_version == "current"
            if request_v2:
                execution = pending_request(target_version, port, target_dir, target_env, "execution")
                assert execution, "no per-request approval"
                approve(target_version, port, target_dir, target_env, execution["request_id"])
            stdout, stderr = caller.communicate(timeout=60)
            assert caller.returncode == 0, stdout + stderr
            first = json.loads(stdout[stdout.index("{"):])
            assert first.get("ok") and message in (first.get("output") or first.get("reply") or ""), first

            second = chat(message + "-second")
            if request_v2:
                execution = pending_request(target_version, port, target_dir, target_env, "execution")
                assert execution, "second request bypassed approval"
                approve(target_version, port, target_dir, target_env, execution["request_id"])
            stdout, stderr = second.communicate(timeout=60)
            assert second.returncode == 0, stdout + stderr
            paired = json.loads(stdout[stdout.index("{"):])
            assert paired.get("ok") and message + "-second" in (paired.get("output") or paired.get("reply") or ""), paired
            results.append({"caller": caller_version, "target": target_version,
                            "target_kind": target_kind, "health_discovery": True,
                            "pair_approval": True, "first_chat": True, "second_chat": True,
                            "request_v2": request_v2})
            target.terminate()
            target.wait(timeout=10)
            processes.remove(target)
            log.close()
        print(json.dumps({"ok": True, "results": results}, indent=2))
        return 0
    finally:
        for process in processes:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
        for log in logs:
            log.close()


if __name__ == "__main__":
    sys.exit(main())
