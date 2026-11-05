#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""测试用多行为 agent：根据 prompt 关键字模拟慢任务/取消/产物/上下文捕获。"""
import base64
import os
import sys
import time

p = open(sys.argv[1], encoding="utf-8").read() if len(sys.argv) > 1 else sys.stdin.read()
print("[flex] start", flush=True)

PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")

if "SLEEP_UNTIL_CANCEL" in p:
    print("FLEX-READY", flush=True)
    for i in range(120):
        time.sleep(1)
        if i % 5 == 0:
            print(f"FLEX-WAIT {i}", flush=True)
    with open("late.txt", "w", encoding="utf-8") as f:
        f.write("this must never appear when cancelled\n")
elif "SLEEP_SHORT" in p:
    print("FLEX-EARLY", flush=True)
    for i in range(3):
        time.sleep(1.2)
        print(f"FLEX-TICK {i}", flush=True)
    with open("short-done.txt", "w", encoding="utf-8") as f:
        f.write("short task done\n")
elif "WRITE_ARTIFACTS" in p:
    with open("build.log", "w", encoding="utf-8") as f:
        f.write("build ok\n")
    with open("shot.png", "wb") as f:
        f.write(PNG)
    os.makedirs("dist", exist_ok=True)
    with open(os.path.join("dist", "icon.png"), "wb") as f:
        f.write(PNG)
    print("FLEX-ARTIFACTS", flush=True)
elif "CAPTURE_CONTEXT" in p:
    with open("seen-prompt.txt", "w", encoding="utf-8") as f:
        f.write(p)
    print("FLEX-CONTEXT-CAPTURED", flush=True)
elif "FAIL_AGENT" in p:
    print("FLEX-FAIL", flush=True)
    sys.exit(2)
else:
    print("FLEX-DEFAULT", flush=True)

print("[flex] end", flush=True)
