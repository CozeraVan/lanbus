#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""把 prompt 文件作为单个 argv 传给只接受 `--prompt <text>` 的 CLI。

例如 Kimi Code：
  python -m lanbus.prompt_cli --cmd kimi --arg -p --prompt-file C:\\Temp\\p.txt

这样可以避免 Windows cmd/PowerShell 对长 prompt、换行和引号的二次解释。
"""
import argparse
import os
import subprocess
import sys


def main(argv=None):
    ap = argparse.ArgumentParser()
    ap.add_argument("--cmd", required=True, help="agent CLI executable")
    ap.add_argument("--arg", action="append", default=[],
                    help="argument before prompt (repeatable)")
    ap.add_argument("--prompt-file", required=True)
    ap.add_argument("--cwd", default="")
    ap.add_argument("--timeout", type=int, default=1800)
    ap.add_argument("--env", action="append", default=[],
                    help="KEY=VALUE (repeatable)")
    a = ap.parse_args(argv)
    try:
        with open(a.prompt_file, encoding="utf-8") as f:
            prompt = f.read()
    except Exception as e:
        print(f"prompt_cli: read prompt failed: {e}", file=sys.stderr)
        return 2
    env = dict(os.environ)
    for pair in a.env:
        if "=" in pair:
            k, v = pair.split("=", 1)
            env[k] = v
    cmd = [a.cmd] + list(a.arg) + [prompt]
    try:
        p = subprocess.run(cmd, cwd=a.cwd or None, env=env,
                           stdin=subprocess.DEVNULL, timeout=a.timeout)
        return p.returncode
    except FileNotFoundError:
        print(f"prompt_cli: executable not found: {a.cmd}", file=sys.stderr)
        return 127
    except subprocess.TimeoutExpired:
        print(f"prompt_cli: timeout after {a.timeout}s", file=sys.stderr)
        return 124
    except Exception as e:
        print(f"prompt_cli: {e}", file=sys.stderr)
        return 125


if __name__ == "__main__":
    sys.exit(main())
