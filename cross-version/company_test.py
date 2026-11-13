#!/usr/bin/env python3
"""Black-box symmetric company membership and Python/Go interoperability test."""
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parent
PROJECT = ROOT.parent
PYTHON = PROJECT / ".venv/bin/python"
GO = PROJECT / ".tools/go/bin/go"
GO_WORKER = ROOT / "current-bin/lanbus-worker"


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def cli(*args):
    return [str(PYTHON), "-m", "lanbus", *args]


def run(args, env, timeout=60, expected=0):
    result = subprocess.run(args, env=env, text=True, capture_output=True, timeout=timeout)
    assert result.returncode == expected, (result.returncode, result.stdout, result.stderr)
    return result


def environment(case_dir, data_dir):
    env = os.environ.copy()
    env.update(HOME=str(case_dir / "home"), LANBUS_PEER_DIR=str(data_dir),
               PYTHONPATH=str(PROJECT / "src"), PYTHONUTF8="1")
    env.pop("LANBUS_HUB", None)
    env.pop("LANBUS_TOKEN", None)
    return env


def output_json(result):
    return json.loads(result.stdout[result.stdout.index("{"):])


class Node:
    def __init__(self, root, name, kind="python", policy="company", company_from=None):
        self.name = name
        self.kind = kind
        self.port = free_port()
        self.url = f"http://127.0.0.1:{self.port}"
        self.dir = root / name
        self.data_dir = self.dir / "peer"
        self.data_dir.mkdir(parents=True, exist_ok=True)
        self.env = environment(self.dir, self.data_dir)
        if company_from and company_from != self.data_dir:
            shutil.copy2(company_from / "company.json", self.data_dir / "company.json")
        if kind == "go":
            command = [str(GO_WORKER), "--name", name, "--agent", "echo", "--direct",
                       "--direct-bind", "127.0.0.1", "--direct-port", str(self.port),
                       "--direct-data-dir", str(self.data_dir), "--request-policy", policy,
                       "--no-open-approval", "--cwd", str(self.dir)]
        else:
            command = cli("agent", "--name", name, "--env", "company-test", "--agent", "echo", "--direct",
                          "--direct-bind", "127.0.0.1", "--direct-port", str(self.port),
                          "--direct-data-dir", str(self.data_dir), "--request-policy", policy,
                          "--no-open-approval", "--cwd", str(self.dir))
        self.log = (self.dir / "node.log").open("w")
        self.process = subprocess.Popen(command, env=self.env, stdout=self.log,
                                        stderr=subprocess.STDOUT, text=True)
        deadline = time.time() + 15
        while time.time() < deadline:
            if self.process.poll() is not None:
                raise AssertionError((self.dir / "node.log").read_text())
            try:
                health = run(cli("peers", "--url", self.url), self.env, timeout=5)
                self.health = output_json(health)
                return
            except (AssertionError, ValueError, subprocess.TimeoutExpired):
                time.sleep(0.15)
        raise AssertionError("node did not become healthy: " + name)

    def stop(self):
        self.process.terminate()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)
        self.log.close()


def pending(node, kind=""):
    deadline = time.time() + 10
    while time.time() < deadline:
        result = run(cli("inbox", "--url", node.url, "--data-dir", str(node.data_dir)), node.env)
        requests = [item for item in output_json(result).get("requests", [])
                    if not kind or item.get("kind") == kind]
        if requests:
            return requests[0]
        time.sleep(0.15)
    return None


def approve(node, request_id, pair=False):
    args = ["allow", request_id, "--url", node.url, "--data-dir", str(node.data_dir)]
    if pair:
        args += ["--pair", "--scope", "chat"]
    run(cli(*args), node.env)


def join(caller_env, caller_dir, target):
    process = subprocess.Popen(cli("company", "join", "--to", target.url,
        "--data-dir", str(caller_dir), "--pair-timeout", "20"), env=caller_env,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    request = pending(target, "company")
    assert request, "company join did not require human approval"
    approve(target, request["request_id"])
    stdout, stderr = process.communicate(timeout=30)
    assert process.returncode == 0, stdout + stderr
    company = json.loads((caller_dir / "company.json").read_text())
    health = output_json(run(cli("peers", "--url", target.url), caller_env))
    assert company["company_id"] == health["company_id"]
    assert "secret" not in health
    return company


def chat(env, to, message, expected=0, no_wait=False):
    args = ["chat", "--to", to.url, "--message", message, "--pair-timeout", "15", "--timeout", "10"]
    if no_wait:
        args.append("--no-wait")
    return run(cli(*args), env, expected=expected, timeout=40)


def main():
    case_dir = ROOT / "case-company-network"
    shutil.rmtree(case_dir, ignore_errors=True)
    case_dir.mkdir(parents=True)
    GO_WORKER.parent.mkdir(exist_ok=True)
    subprocess.run([str(GO), "build", "-o", str(GO_WORKER), "./cmd/lanbus-worker"],
                   cwd=PROJECT / "go", check=True)
    nodes = []
    results = []
    try:
        b_dir = case_dir / "b" / "peer"
        b_dir.mkdir(parents=True)
        b_env = environment(case_dir / "b", b_dir)
        run(cli("company", "create", "--data-dir", str(b_dir), "--company-name", "测试公司"), b_env)
        b = Node(case_dir, "b", company_from=b_dir)
        nodes.append(b)
        company = json.loads((b.data_dir / "company.json").read_text())

        a_dir = case_dir / "a-client"
        a_env = environment(case_dir, a_dir)
        join(a_env, a_dir, b)
        assert output_json(chat(a_env, b, "A-to-B"))["reply"] == "A-to-B"
        assert pending(b) is None

        a = Node(case_dir, "a-node", company_from=a_dir)
        nodes.append(a)
        c_dir = case_dir / "c-client"
        c_env = environment(case_dir, c_dir)
        join(c_env, c_dir, a)
        c = Node(case_dir, "c-node", company_from=c_dir)
        nodes.append(c)
        for source_env, target, message in [
                (a_env, c, "A-to-C"), (c_env, a, "C-to-A"),
                (c_env, b, "C-to-B"), (a_env, c, "A-to-C-second")]:
            assert output_json(chat(source_env, target, message))["reply"] == message
            assert pending(target) is None, "member chat unexpectedly required approval"
        results.append({"three_nodes": True, "directions": ["A->C", "C->A", "C->B"],
                        "second_chat_without_approval": True})

        go_node = Node(case_dir, "go-node", kind="go", company_from=b.data_dir)
        nodes.append(go_node)
        assert output_json(chat(a_env, go_node, "Python-to-Go-company"))["reply"] == "Python-to-Go-company"
        d_dir = case_dir / "d-client"
        d_env = environment(case_dir, d_dir)
        join(d_env, d_dir, go_node)
        assert output_json(chat(d_env, a, "Go-joined-member-to-Python"))["reply"] == "Go-joined-member-to-Python"
        results.append({"python_to_go_auto_company": True, "python_join_go_company": True})

        outsider_dir = case_dir / "outsider"
        outsider_env = environment(case_dir, outsider_dir)
        response = chat(outsider_env, c, "outsider-must-not-run", expected=2, no_wait=True)
        assert output_json(response)["status"] == "pending"
        assert pending(c, "execution") is None
        bad_dir = case_dir / "bad-member"
        shutil.copytree(a_dir, bad_dir)
        bad_company = json.loads((bad_dir / "company.json").read_text())
        bad_company["secret"] = "00" * 32
        (bad_dir / "company.json").write_text(json.dumps(bad_company))
        (bad_dir / "credentials.json").unlink()
        response = chat(environment(case_dir, bad_dir), c, "forged-member", expected=1)
        assert "公司成员证明无效" in response.stdout + response.stderr
        results.append({"outsider_needs_approval": True, "forged_proof_rejected": True})
        print(json.dumps({"ok": True, "company_id": company["company_id"], "results": results},
                         ensure_ascii=False, indent=2))
        return 0
    finally:
        for node in reversed(nodes):
            if node.process.poll() is None:
                node.stop()
            else:
                node.log.close()


if __name__ == "__main__":
    sys.exit(main())
