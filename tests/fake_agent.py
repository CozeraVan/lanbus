#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""自测用的假 agent：把 prompt 当成活干。
规则：prompt 里出现 REWORK 就写 done.txt（模拟"按验收意见修好了"），否则不写。
"""
import sys

p = open(sys.argv[1], encoding="utf-8").read()
print("[fake-agent] received prompt:")
print(p[:400])
if "REWORK" in p:
    with open("done.txt", "w", encoding="utf-8") as f:
        f.write("fixed per review feedback\n")
    print("[fake-agent] wrote done.txt")
else:
    print("[fake-agent] did nothing (no feedback yet)")
sys.exit(0)