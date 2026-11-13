#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
lanbus discovery —— 不需要对方预装 lanbus/MCP 的局域网 AI agent 侦查。

三种证据源：
1. 本机进程：识别 Claude Code / Codex / Cursor / Windsurf / Ollama / LM Studio 等；
2. 本机监听端口：把 PID、进程名、端口对应起来；
3. 局域网扫描 + HTTP 指纹：Ollama、OpenAI-compatible、Open WebUI、ComfyUI、
   llama.cpp、vLLM、Gradio、Jupyter、Chrome DevTools、MCP server、lanbus hub。

扫描只做 TCP connect 和少量 HTTP GET/POST，不利用漏洞、不修改远端。
"""
import concurrent.futures
import http.client
import ipaddress
import json
import locale
import os
import re
import shutil
import socket
import ssl
import subprocess
import sys
import time

MAX_HOSTS = 1024
MAX_PORTS = 2048
DEFAULT_TIMEOUT = 0.22
HTTP_TIMEOUT = 1.5
THREADS = 256
READ_LIMIT = 256 * 1024
_TLS_CTX = ssl.create_default_context()
_TLS_CTX.check_hostname = False
_TLS_CTX.verify_mode = ssl.CERT_NONE

# 常用 AI / agent / 开发端口。默认只扫这些，避免把 /24 扫成端口全量。
BASE_PORTS = [
    22, 80, 443,
    1234,              # LM Studio
    3000, 3001,        # Open WebUI / dev server
    4000, 5000, 5001,  # LocalAI / Gradio / text-generation-webui
    5173, 5678,        # Vite / n8n
    7000, 7860, 7861,  # Gradio / ComfyUI variants
    8000, 8001, 8008,  # vLLM / llama.cpp / generic
    8080, 8081, 8082, 8083, 8084, 8088, 8090, 8095,
    8188,              # ComfyUI
    8501,              # Streamlit
    8788, 8789,        # lanbus hub
    8790, 8791,        # lanbus direct peer
    8888,              # Jupyter
    9000, 9090,
    11434,             # Ollama
    12345, 18080, 18789, 30000,
]


AI_KINDS = {
    "ollama", "lmstudio", "openai", "vllm", "localai", "llamacpp",
    "openwebui", "comfyui", "gradio", "jupyter", "chrome-devtools",
    "mcp", "lanbus", "lanbus-peer", "anythingllm", "dify", "n8n", "langflow",
    "textgen", "sillytavern", "koboldcpp", "streamlit", "ai-api",
}

PROCESS_PATTERNS = [
    ("claude", r"claude"),
    ("codex", r"codex"),
    ("cursor", r"cursor"),
    ("windsurf", r"windsurf"),
    ("chatgpt", r"chatgpt|openai desktop"),
    ("deepseek", r"deepseek|deepseek harness"),
    ("copilot", r"copilot|github copilot"),
    ("aider", r"\baider\b"),
    ("cline", r"\bcline\b"),
    ("roo", r"roo[- ]?code|roocode"),
    ("goose", r"\bgoose\b"),
    ("opencode", r"opencode"),
    ("gemini", r"gemini"),
    ("qwen", r"qwen"),
    ("kimi", r"kimi"),
    ("perplexity", r"perplexity"),
    ("manus", r"\bmanus\b"),
    ("poe", r"\bpoe\b"),
    ("monica", r"\bmonica\b"),
    ("sider", r"\bsider\b"),
    ("trae", r"\btrae\b"),
    ("continue", r"continue\.dev|continueai|continue-ai"),
    ("vscode", r"visual studio code|code\.exe|\bcode\b"),
    ("zed", r"\bzed\b"),
    ("ollama", r"ollama"),
    ("lmstudio", r"lm ?studio|lmstudio"),
    ("localai", r"localai"),
    ("llamacpp", r"llama[-_. ]?(cpp|server)"),
    ("vllm", r"vllm"),
    ("textgen", r"text-generation-webui|oobabooga"),
    ("openwebui", r"open[- ]?webui"),
    ("comfyui", r"comfyui|comfy"),
    ("jan", r"^jan(\.exe)?$|\bjan\.ai\b"),
    ("gpt4all", r"gpt4all"),
    ("msty", r"msty"),
    ("anythingllm", r"anythingllm"),
    ("dify", r"\bdify\b"),
    ("n8n", r"\bn8n\b"),
    ("langflow", r"langflow"),
    ("sillytavern", r"sillytavern"),
    ("koboldcpp", r"koboldcpp"),
    ("browser-use", r"browser-use"),
    ("playwright", r"playwright"),
    ("selenium", r"selenium"),
    ("jupyter", r"jupyter|ipykernel"),
]

LOCAL_AGENT_LABEL = {
    "claude": "Claude Code / Claude Desktop",
    "codex": "Codex CLI / Codex desktop",
    "cursor": "Cursor",
    "windsurf": "Windsurf",
    "chatgpt": "ChatGPT desktop",
    "copilot": "GitHub Copilot",
    "aider": "Aider",
    "cline": "Cline",
    "roo": "Roo Code",
    "goose": "Goose",
    "opencode": "OpenCode",
    "gemini": "Gemini CLI",
    "qwen": "Qwen agent",
    "kimi": "Kimi",
    "deepseek": "DeepSeek",
    "perplexity": "Perplexity",
    "manus": "Manus",
    "poe": "Poe",
    "monica": "Monica",
    "sider": "Sider",
    "trae": "Trae",
    "continue": "Continue",
    "vscode": "VS Code + AI extension",
    "zed": "Zed",
    "ollama": "Ollama",
    "lmstudio": "LM Studio",
    "localai": "LocalAI",
    "llamacpp": "llama.cpp",
    "vllm": "vLLM",
    "textgen": "text-generation-webui",
    "openwebui": "Open WebUI",
    "comfyui": "ComfyUI",
    "jan": "Jan",
    "gpt4all": "GPT4All",
    "msty": "Msty",
    "anythingllm": "AnythingLLM",
    "dify": "Dify",
    "n8n": "n8n",
    "langflow": "Langflow",
    "sillytavern": "SillyTavern",
    "koboldcpp": "KoboldCpp",
    "browser-use": "browser-use",
    "playwright": "Playwright",
    "selenium": "Selenium",
    "jupyter": "Jupyter",
}


# ------------------------------------------------------------------ platform



def _preferred_encoding():
    try:
        return locale.getpreferredencoding(False) or "utf-8"
    except Exception:
        return "utf-8"


def _cmd(cmd, timeout=8):
    try:
        p = subprocess.run(cmd, capture_output=True, timeout=timeout,
                           stdin=subprocess.DEVNULL)
        return p.returncode, (p.stdout or b"").decode(_preferred_encoding(),
                                                      "replace")
    except Exception as e:
        return 127, str(e)


def local_interfaces():
    """返回 [{ip,prefix,network,iface,mask}]，跨平台尽力解析。"""
    out = []
    seen = set()

    def add(ip, prefix, iface, mask=""):
        try:
            ipaddress.ip_address(ip)
            net = ipaddress.ip_network(f"{ip}/{int(prefix)}", strict=False)
        except Exception:
            return
        if ip.startswith("127.") or ip == "0.0.0.0" or ip.startswith("169.254."):
            return
        key = (ip, int(prefix), iface)
        if key in seen:
            return
        seen.add(key)
        out.append({"ip": ip, "prefix": int(prefix), "network": str(net),
                    "iface": iface, "mask": mask})

    if sys.platform.startswith("win"):
        rc, text = _cmd(["ipconfig"], timeout=10)
        iface = ""
        ip = ""
        for line in text.splitlines():
            m = re.match(r"^\S.*(?:adapter|适配器)\s+(.+):$", line.strip(), re.I)
            if m:
                iface = m.group(1).strip()
                ip = ""
            if re.search(r"IPv4", line) or "IPv4" in line:
                m = re.search(r"(\d{1,3}(?:\.\d{1,3}){3})", line)
                if m:
                    ip = m.group(1)
            if ip and ("Mask" in line or "掩码" in line or
                       re.search(r"\b255(?:\.\d{1,3}){3}\b", line)):
                m = re.search(r"(\d{1,3}(?:\.\d{1,3}){3})", line)
                if m:
                    mask = m.group(1)
                    try:
                        prefix = ipaddress.IPv4Network(f"0.0.0.0/{mask}").prefixlen
                    except Exception:
                        prefix = 24
                    add(ip, prefix, iface, mask)
                    ip = ""
        if not out:
            rc, text = _cmd(["ipconfig", "/all"], timeout=10)
            for line in text.splitlines():
                m = re.search(r"IPv4[^:]*:\s*(\d{1,3}(?:\.\d{1,3}){3})", line)
                if m:
                    ip = m.group(1)
                    add(ip, 24, iface or "windows")
    elif sys.platform == "darwin":
        rc, text = _cmd(["ifconfig"], timeout=10)
        iface = ""
        for line in text.splitlines():
            m = re.match(r"^(\w+):", line)
            if m:
                iface = m.group(1)
            m = re.search(r"inet\s+(\d{1,3}(?:\.\d{1,3}){3})\s+netmask\s+0x([0-9a-fA-F]+)", line)
            if m:
                ip = m.group(1)
                try:
                    mask_int = int(m.group(2), 16)
                    mask = str(ipaddress.IPv4Address(mask_int))
                    prefix = ipaddress.IPv4Network(f"0.0.0.0/{mask}").prefixlen
                except Exception:
                    prefix = 24
                    mask = "255.255.255.0"
                add(ip, prefix, iface, mask)
    else:
        rc, text = _cmd(["ip", "-o", "-4", "addr", "show"], timeout=10)
        for line in text.splitlines():
            m = re.search(r"\d+:\s+(\S+)\s+inet\s+(\d{1,3}(?:\.\d{1,3}){3})/(\d+)", line)
            if m:
                add(m.group(2), int(m.group(3)), m.group(1))
        if not out:
            rc, text = _cmd(["ifconfig"], timeout=10)
            iface = ""
            for line in text.splitlines():
                m = re.match(r"^(\w+):", line)
                if m:
                    iface = m.group(1)
                m = re.search(r"inet\s+(\d{1,3}(?:\.\d{1,3}){3})\s+netmask\s+(\S+)", line)
                if m:
                    ip = m.group(1)
                    mask = m.group(2)
                    try:
                        if mask.startswith("0x"):
                            mask = str(ipaddress.IPv4Address(int(mask, 16)))
                        prefix = ipaddress.IPv4Network(f"0.0.0.0/{mask}").prefixlen
                    except Exception:
                        prefix = 24
                    add(ip, prefix, iface, mask)

    if not out:
        ip = ""
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.settimeout(0.5)
            s.connect(("8.8.8.8", 80))
            ip = s.getsockname()[0]
            s.close()
        except Exception:
            pass
        if ip:
            add(ip, 24, "default")
    return out


def arp_table():
    """返回 {ip: {mac, iface}}。"""
    out = {}
    if os.path.isfile("/proc/net/arp"):
        try:
            with open("/proc/net/arp", encoding="utf-8", errors="replace") as f:
                for line in f.readlines()[1:]:
                    parts = line.split()
                    if len(parts) >= 6 and re.match(r"\d{1,3}(?:\.\d{1,3}){3}", parts[0]):
                        out[parts[0]] = {"mac": parts[3], "iface": parts[5]}
            if out:
                return out
        except Exception:
            pass
    rc, text = _cmd(["arp", "-a"], timeout=8)
    if rc != 0 and sys.platform.startswith("win"):
        rc, text = _cmd(["arp", "-a"], timeout=8)
    for line in text.splitlines():
        m = re.search(r"(\d{1,3}(?:\.\d{1,3}){3})", line)
        if not m:
            continue
        ip = m.group(1)
        mm = re.search(r"([0-9a-fA-F]{1,2}[:-]){5}[0-9a-fA-F]{1,2}", line)
        if not mm:
            mm = re.search(r"\b([0-9a-fA-F]{4}\.[0-9a-fA-F]{4}\.[0-9a-fA-F]{4})\b", line)
        mac = mm.group(0) if mm else ""
        if mac.lower() in ("", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff"):
            continue
        iface = ""
        im = re.search(r"\bon\s+(\w+)", line)
        if im:
            iface = im.group(1)
        out[ip] = {"mac": mac, "iface": iface}
    return out


def _path_from_command(command):
    cmd = str(command or "").strip()
    if not cmd:
        return ""
    m = re.match(r'^"([^"]+)"', cmd) or re.match(r"^(\S+)", cmd)
    if not m:
        return ""
    p = m.group(1).strip()
    if re.search(r"\.(exe|app|bin)$", p, re.I) or os.path.isabs(p):
        return p
    return ""


def wsl_running_distros():
    """Windows: 列出正在运行的 WSL 发行版（不主动启动已停止的实例）。"""
    if not sys.platform.startswith("win") or not shutil.which("wsl"):
        return []
    try:
        p = subprocess.run(["wsl.exe", "-l", "-v"], capture_output=True, timeout=10,
                           stdin=subprocess.DEVNULL)
        raw = p.stdout or b""
        for enc in ("utf-16-le", "utf-8"):
            try:
                text = raw.decode(enc, "ignore")
            except Exception:
                continue
            out = []
            for line in text.replace("\x00", "").splitlines():
                m = re.match(r"^\s*\*?\s*(\S+)\s+Running\b", line, re.I)
                if m:
                    out.append(m.group(1))
            if out:
                return out
    except Exception:
        pass
    return []


def wsl_processes():
    """Windows: 读取正在运行的 WSL 内进程，返回带 scope/path 的进程列表。"""
    out = []
    for distro in wsl_running_distros():
        script = (
            "ps -eo pid=,ppid=,comm=,args= | while read pid ppid comm args; do "
            "exe=$(readlink -f /proc/$pid/exe 2>/dev/null); "
            "printf '%s\t%s\t%s\t%s\t%s\n' \"$pid\" \"$ppid\" \"$comm\" \"$exe\" \"$args\"; "
            "done"
        )
        try:
            p = subprocess.run(["wsl.exe", "-d", distro, "--exec", "sh", "-lc", script],
                               capture_output=True, timeout=15,
                               stdin=subprocess.DEVNULL)
            text = (p.stdout or b"").decode("utf-8", "replace")
        except Exception:
            continue
        for line in text.splitlines():
            parts = line.split("	", 4)
            if len(parts) < 4:
                continue
            try:
                pid = int(parts[0])
                ppid = int(parts[1])
            except Exception:
                continue
            out.append({
                "pid": pid, "ppid": ppid,
                "name": parts[2].strip() or os.path.basename(parts[3] or ""),
                "path": parts[3].strip(),
                "command": parts[4].strip() if len(parts) > 4 else "",
                "scope": f"wsl:{distro}",
            })
    return out


def local_processes():
    """返回本机进程列表（pid/ppid/name/command/path）。"""
    procs = []
    if sys.platform.startswith("win"):
        # 优先拿命令行；PowerShell 不可用或太慢时退回 tasklist
        rc, text = _cmd(["powershell", "-NoProfile", "-Command",
                         "Get-CimInstance Win32_Process | "
                         "Select-Object ProcessId,ParentProcessId,Name,CommandLine,ExecutablePath | "
                         "ConvertTo-Json -Compress"],
                        timeout=12)
        if rc == 0 and text.strip().startswith(("{", "[")):
            try:
                data = json.loads(text)
                if isinstance(data, dict):
                    data = [data]
                for p in data:
                    cmd = p.get("CommandLine") or ""
                    procs.append({"pid": int(p.get("ProcessId") or 0),
                                  "ppid": int(p.get("ParentProcessId") or 0),
                                  "name": p.get("Name") or "",
                                  "command": cmd,
                                  "path": p.get("ExecutablePath") or
                                          _path_from_command(cmd)})
            except Exception:
                pass
        if procs and any(not x.get("path") for x in procs):
            # 某些服务进程（如 Codex sandbox service）拿不到 ExecutablePath，
            # 用 Win32_Service 的 PathName 补全。
            rc, text = _cmd(["powershell", "-NoProfile", "-Command",
                             "Get-CimInstance Win32_Service | "
                             "Select-Object ProcessId,PathName | ConvertTo-Json -Compress"],
                            timeout=12)
            if rc == 0 and text.strip().startswith(("{", "[")):
                try:
                    data = json.loads(text)
                    if isinstance(data, dict):
                        data = [data]
                    smap = {}
                    for svc in data:
                        try:
                            pid = int(svc.get("ProcessId") or 0)
                        except Exception:
                            continue
                        raw = str(svc.get("PathName") or "")
                        m = re.match(r'^"([^"]+)"', raw) or re.match(r"^(\S+)", raw)
                        if pid and m:
                            smap[pid] = m.group(1)
                    for x in procs:
                        if not x.get("path") and x.get("pid") in smap:
                            x["path"] = smap[x["pid"]]
                except Exception:
                    pass
        if not procs:
            rc, text = _cmd(["tasklist", "/FO", "CSV", "/NH"], timeout=10)
            for line in text.splitlines():
                parts = [x.strip('"') for x in line.split('","')]
                if len(parts) >= 2:
                    try:
                        procs.append({"pid": int(parts[1]), "ppid": 0,
                                      "name": parts[0], "command": parts[0],
                                      "path": ""})
                    except Exception:
                        pass
    elif sys.platform == "darwin":
        rc, text = _cmd(["ps", "-axo", "pid=,comm=,args="], timeout=10)
        for line in text.splitlines():
            m = re.match(r"\s*(\d+)\s+(\S+)\s*(.*)", line)
            if m:
                comm = m.group(2)
                procs.append({"pid": int(m.group(1)), "ppid": 0,
                              "name": os.path.basename(comm),
                              "command": m.group(3),
                              "path": comm if os.path.isabs(comm) else ""})
    else:
        for name in os.listdir("/proc"):
            if not name.isdigit():
                continue
            pid = int(name)
            try:
                with open(f"/proc/{pid}/comm", encoding="utf-8", errors="replace") as f:
                    comm = f.read().strip()
            except Exception:
                continue
            cmd = ""
            try:
                with open(f"/proc/{pid}/cmdline", "rb") as f:
                    cmd = f.read().replace(b"\0", b" ").decode("utf-8", "replace").strip()
            except Exception:
                pass
            procs.append({"pid": pid, "name": comm, "command": cmd})
    return procs


def listening_ports(procs=None):
    """返回 [{port,addr,pid,process}]。"""
    out = []
    pid_name = {p["pid"]: p.get("name") or "" for p in (procs or [])}
    if sys.platform.startswith("win"):
        rc, text = _cmd(["netstat", "-ano", "-p", "tcp"], timeout=10)
        for line in text.splitlines():
            parts = line.split()
            if len(parts) >= 5 and parts[0].upper() == "TCP" and parts[3].upper() == "LISTENING":
                addr = parts[1]
                port = addr.rsplit(":", 1)[-1]
                try:
                    port = int(port)
                    pid = int(parts[4])
                except Exception:
                    continue
                out.append({"port": port, "addr": addr, "pid": pid,
                            "process": pid_name.get(pid, "")})
    elif sys.platform == "darwin":
        rc, text = _cmd(["lsof", "-nP", "-iTCP", "-sTCP:LISTEN"], timeout=10)
        for line in text.splitlines()[1:]:
            parts = line.split()
            if len(parts) < 9:
                continue
            port = None
            for x in parts:
                if ":" in x:
                    try:
                        port = int(x.rsplit(":", 1)[1])
                        break
                    except Exception:
                        pass
            try:
                pid = int(parts[1])
            except Exception:
                pid = 0
            if port:
                out.append({"port": port, "addr": parts[8] if len(parts) > 8 else "",
                            "pid": pid, "process": parts[0]})
    else:
        rc, text = _cmd(["ss", "-ltnp"], timeout=10)
        if rc == 0 and text.strip():
            for line in text.splitlines()[1:]:
                parts = line.split()
                if len(parts) < 4:
                    continue
                local = parts[3]
                try:
                    port = int(local.rsplit(":", 1)[-1])
                except Exception:
                    continue
                pid = 0
                m = re.search(r"pid=(\d+)", line)
                if m:
                    pid = int(m.group(1))
                pm = re.search(r'users:\(\("([^"]+)"', line)
                proc = pm.group(1) if pm else pid_name.get(pid, "")
                out.append({"port": port, "addr": local, "pid": pid, "process": proc})
        else:
            try:
                with open("/proc/net/tcp", encoding="utf-8", errors="replace") as f:
                    for line in f.readlines()[1:]:
                        parts = line.split()
                        if len(parts) < 4 or parts[3] != "0A":
                            continue
                        local = parts[1]
                        try:
                            port = int(local.rsplit(":", 1)[-1], 16)
                        except Exception:
                            continue
                        out.append({"port": port, "addr": local, "pid": 0,
                                    "process": ""})
            except Exception:
                pass
    # 去重
    seen = set()
    uniq = []
    for x in out:
        key = (x["port"], x.get("pid") or 0)
        if key in seen:
            continue
        seen.add(key)
        uniq.append(x)
    return sorted(uniq, key=lambda x: x["port"])


def _redact(s):
    s = str(s or "")
    s = re.sub(r"(?i)((?:token|api[_-]?key|secret|password|passwd)\s*[=:]\s*)\S+",
               r"\1***", s)
    return s[:400]


def _kind_for_process(name, command):
    """先看可执行文件名；通用解释器再看命令行里的模块/脚本。"""
    name_l = os.path.basename(str(name or "")).lower()
    cmd_l = str(command or "").lower()
    for kind, pat in PROCESS_PATTERNS:
        if re.search(pat, name_l, re.I):
            if kind == "vscode" and not re.search(
                    r"copilot|continue|cline|roo|codeium|tabnine|ai", cmd_l):
                continue
            return kind, True
    generic = re.match(
        r"^(node|node\.exe|python\d*(\.exe)?|pythonw\.exe|uv|uv\.exe|"
        r"bun|bun\.exe|deno|deno\.exe|java|java\.exe|ruby|go|go\.exe|"
        r"npx|npx\.cmd|npm|npm\.cmd|pnpm|yarn)$", name_l)
    if not generic:
        return None
    # 只在这些“运行时”进程里用命令行特征，避免 shell 里随便出现 claude/codex 就误报
    rules = [
        ("claude", r"claude-code|@anthropic-ai/claude|/claude-code/|\bclaude\.js\b"),
        ("codex", r"@openai/codex|/codex(?:-cli)?/|\bcodex\.js\b|\bcodex\b.*(?:exec|--)"),
        ("aider", r"(^|[/\ ])aider(?:\.py)?([/\ ]|$)"),
        ("goose", r"\bgoose\b"),
        ("open-interpreter", r"open[-_]interpreter|\binterpreter\b"),
        ("opencode", r"\bopencode\b"),
        ("gemini", r"gemini-cli|@google/gemini"),
        ("qwen", r"qwen[-_]?agent|qwen-code"),
        ("kimi", r"kimi[-_]?(?:cli|bridge|desktop)"),
        ("deepseek", r"deepseek"),
        ("continue", r"continue\.dev|continueai|continue-ai"),
        ("browser-use", r"browser[-_]use"),
        ("playwright", r"playwright"),
        ("selenium", r"selenium"),
        ("jupyter", r"jupyter|ipykernel"),
        ("vllm", r"\bvllm\b"),
        ("textgen", r"text[-_]generation[-_]webui|oobabooga"),
        ("comfyui", r"comfyui|comfy"),
        ("openwebui", r"open[-_]?webui"),
    ]
    for kind, pat in rules:
        if re.search(pat, cmd_l, re.I):
            return kind, False
    return None


HELPER_NAME_RE = re.compile(
    r"(service|helper|crashpad|renderer|gpu-process|utility|updater|update|"
    r"sandbox|bridge|daemon)", re.I)

CLI_NAMES = {
    "claude": ["claude"], "codex": ["codex"], "ollama": ["ollama"],
    "lmstudio": ["lms", "lm-studio"], "cursor": ["cursor"],
    "windsurf": ["windsurf"], "aider": ["aider"], "goose": ["goose"],
    "opencode": ["opencode"], "gemini": ["gemini"], "qwen": ["qwen"],
    "kimi": ["kimi"], "deepseek": ["deepseek"], "chatgpt": ["chatgpt"],
    "trae": ["trae"], "jan": ["jan"], "gpt4all": ["gpt4all"],
    "anythingllm": ["anythingllm"], "dify": ["dify"], "n8n": ["n8n"],
    "langflow": ["langflow"], "comfyui": ["comfy"], "jupyter": ["jupyter"],
}


def detect_local_agents(procs=None):
    """从进程列表提取 AI agent；按应用类型聚合，避免 Electron 子进程刷屏。"""
    procs = procs if procs is not None else local_processes()
    groups = {}
    for p in procs:
        name = p.get("name") or ""
        command = p.get("command") or ""
        found = _kind_for_process(name, command)
        if not found:
            continue
        kind, direct = found
        scope = p.get("scope") or "local"
        key = (kind, scope)
        g = groups.setdefault(key, {
            "kind": kind,
            "name": LOCAL_AGENT_LABEL.get(kind, kind),
            "scope": scope,
            "pids": [],
            "processes": [],
            "helpers": [],
            "direct": [],
            "paths": [],
            "helper_paths": [],
            "path": "",
            "command": "",
            "instances": 0,
            "source": "process",
        })
        try:
            pid = int(p.get("pid") or 0)
        except Exception:
            pid = 0
        if pid and pid not in g["pids"]:
            g["pids"].append(pid)
        proc = name or ""
        if proc:
            is_helper = bool(HELPER_NAME_RE.search(proc))
            exe = p.get("path") or _path_from_command(command)
            if direct and not is_helper:
                if proc.lower() not in {x.lower() for x in g["processes"]}:
                    g["processes"].append(proc)
                if proc.lower() not in {x.lower() for x in g["direct"]}:
                    g["direct"].append(proc)
                if exe and exe not in g["paths"]:
                    g["paths"].append(exe)
            else:
                if proc.lower() not in {x.lower() for x in g["helpers"]}:
                    g["helpers"].append(proc)
                if exe and exe not in g["helper_paths"]:
                    g["helper_paths"].append(exe)
        if not g["command"] and command:
            g["command"] = _redact(command)
    out = []
    for g in groups.values():
        g["pid"] = g["pids"][0] if g["pids"] else None
        g["process"] = ", ".join(g["processes"][:4])
        g["instances"] = max(1, len(g["direct"]))
        if not g["paths"]:
            for exe_name in CLI_NAMES.get(g["kind"], []):
                found = shutil.which(exe_name)
                if found and found not in g["paths"]:
                    g["paths"].append(found)
                    break
        g["path"] = g["paths"][0] if g["paths"] else (
            g["helper_paths"][0] if g["helper_paths"] else "")
        if not g["process"]:
            g["process"] = ", ".join(g["helpers"][:4])
        if len(g["pids"]) > 20:
            g["pids"] = g["pids"][:20]
        out.append(g)
    return sorted(out, key=lambda x: (x["name"].lower(), x.get("pid") or 0))

# --------------------------------------------------------------------- HTTP

def _http(ip, port, method, path, body=None, timeout=HTTP_TIMEOUT, api_key="",
          tls=False):
    conn = (http.client.HTTPSConnection(ip, port, timeout=timeout, context=_TLS_CTX)
            if tls else http.client.HTTPConnection(ip, port, timeout=timeout))
    headers = {"User-Agent": "lanbus-discovery/1.1",
               "Accept": "application/json, text/plain, */*"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    try:
        conn.request(method, path, body=body, headers=headers)
        r = conn.getresponse()
        data = r.read(READ_LIMIT)
        return r.status, dict(r.getheaders()), data
    except Exception:
        return 0, {}, b""
    finally:
        try:
            conn.close()
        except Exception:
            pass


def _json_body(data):
    try:
        return json.loads(data.decode("utf-8", "replace") or "{}")
    except Exception:
        return None


def _models_from(data):
    obj = _json_body(data)
    if isinstance(obj, dict):
        for key in ("models", "data"):
            arr = obj.get(key)
            if isinstance(arr, list):
                out = []
                for x in arr:
                    if isinstance(x, dict):
                        out.append(str(x.get("name") or x.get("id") or x.get("model") or ""))
                    else:
                        out.append(str(x))
                return [x for x in out if x]
    elif isinstance(obj, list):
        return [str(x.get("name") or x.get("id") or x) for x in obj if isinstance(x, dict)]
    return []


def probe_http(ip, port, timeout=HTTP_TIMEOUT, api_key="", tls=False):
    """对一个已开放端口做 HTTP(S) 指纹，返回 service 列表。"""
    services = []
    seen = set()
    auth_seen = False
    scheme = "https" if tls else "http"

    # 先确认它真的会说 HTTP；SSH/数据库等端口只做一次探测就跳过，
    # 否则每个开放端口会白等 9 次超时。
    st0, _, _ = _http(ip, port, "GET", "/", timeout=timeout, api_key=api_key,
                      tls=tls)
    if st0 == 0:
        return []

    def add(kind, **kw):
        if kind in seen:
            return
        seen.add(kind)
        d = {"kind": kind, "name": kind, "port": port,
             "transport": "http", "ai": kind in AI_KINDS}
        d.update(kw)
        services.append(d)

    # Ollama 原生
    st, hdr, data = _http(ip, port, "GET", "/api/tags", timeout=timeout, api_key=api_key, tls=tls)
    auth_seen = auth_seen or st in (401, 403)
    models = _models_from(data) if st == 200 else []
    if st == 200 and isinstance(_json_body(data), dict) and "models" in (_json_body(data) or {}):
        add("ollama", models=models, endpoint=f"{scheme}://{ip}:{port}",
            version=((_json_body(data) or {}).get("version") or ""))
    st2, hdr2, data2 = _http(ip, port, "GET", "/api/version", timeout=timeout, api_key=api_key, tls=tls)
    auth_seen = auth_seen or st2 in (401, 403)
    obj2 = _json_body(data2)
    if st2 == 200 and isinstance(obj2, dict) and ("version" in obj2 or "name" in obj2):
        add("ollama", models=models, endpoint=f"{scheme}://{ip}:{port}",
            version=str(obj2.get("version") or ""))

    # OpenAI-compatible
    st, hdr, data = _http(ip, port, "GET", "/v1/models", timeout=timeout, api_key=api_key, tls=tls)
    auth_seen = auth_seen or st in (401, 403)
    obj = _json_body(data)
    if st == 200 and isinstance(obj, dict) and isinstance(obj.get("data"), list):
        product = "openai"
        server = (hdr.get("Server") or "").lower()
        if port == 1234:
            product = "lmstudio"
        elif port == 11434:
            product = "ollama"
        elif "llama" in server:
            product = "llamacpp"
        elif "uvicorn" in server and port in (8000, 8001):
            product = "vllm"
        add(product, models=_models_from(data), endpoint=f"{scheme}://{ip}:{port}/v1",
            server=hdr.get("Server") or "", api="openai")

    # Open WebUI
    st, hdr, data = _http(ip, port, "GET", "/api/config", timeout=timeout, api_key=api_key, tls=tls)
    auth_seen = auth_seen or st in (401, 403)
    obj = _json_body(data)
    if st == 200 and isinstance(obj, dict) and "version" in obj and any(
            k in obj for k in ("features", "default_models", "onboarding",
                               "oauth", "default_locale")):
        add("openwebui", endpoint=f"{scheme}://{ip}:{port}",
            version=str(obj.get("version") or ""))

    # ComfyUI
    st, hdr, data = _http(ip, port, "GET", "/system_stats", timeout=timeout, api_key=api_key, tls=tls)
    auth_seen = auth_seen or st in (401, 403)
    obj = _json_body(data)
    if st == 200 and isinstance(obj, dict) and ("system" in obj or "devices" in obj):
        add("comfyui", endpoint=f"{scheme}://{ip}:{port}")

    # llama.cpp server
    st, hdr, data = _http(ip, port, "GET", "/props", timeout=timeout, api_key=api_key, tls=tls)
    auth_seen = auth_seen or st in (401, 403)
    obj = _json_body(data)
    if st == 200 and isinstance(obj, dict) and (
            "default_generation_settings" in obj or "total_slots" in obj):
        add("llamacpp", endpoint=f"{scheme}://{ip}:{port}")

    # Jupyter
    st, hdr, data = _http(ip, port, "GET", "/api", timeout=timeout, api_key=api_key, tls=tls)
    auth_seen = auth_seen or st in (401, 403)
    obj = _json_body(data)
    if st == 200 and isinstance(obj, dict) and "version" in obj:
        add("jupyter", endpoint=f"{scheme}://{ip}:{port}", version=str(obj.get("version") or ""))

    # Gradio
    st, hdr, data = _http(ip, port, "GET", "/config", timeout=timeout, api_key=api_key, tls=tls)
    auth_seen = auth_seen or st in (401, 403)
    obj = _json_body(data)
    if st == 200 and isinstance(obj, dict) and (
            "dependencies" in obj or ("version" in obj and "components" in obj)):
        add("gradio", endpoint=f"{scheme}://{ip}:{port}", version=str(obj.get("version") or ""))

    # Chrome / Edge DevTools —— 常见于 agent 的浏览器控制
    st, hdr, data = _http(ip, port, "GET", "/json/version", timeout=timeout, api_key=api_key, tls=tls)
    auth_seen = auth_seen or st in (401, 403)
    obj = _json_body(data)
    if st == 200 and isinstance(obj, dict) and "Browser" in obj:
        add("chrome-devtools", endpoint=f"{scheme}://{ip}:{port}",
            version=str(obj.get("Browser") or ""))

    # FastAPI / OpenAPI — 很多自建 AI agent 只暴露 OpenAPI 文档，不暴露 /v1
    st, hdr, data = _http(ip, port, "GET", "/openapi.json", timeout=timeout,
                          api_key=api_key, tls=tls)
    auth_seen = auth_seen or st in (401, 403)
    obj = _json_body(data)
    if st == 200 and isinstance(obj, dict) and obj.get("openapi") and isinstance(obj.get("info"), dict):
        info = obj.get("info") or {}
        title = str(info.get("title") or "")
        paths = obj.get("paths") or {}
        ai_hint = bool(re.search(r"(?i)(^|[^a-z])(ai|agent|llm|model|chat|inference|assistant|copilot)([^a-z]|$)", title))
        ai_paths = any(re.search(r"(?i)(chat|agent|llm|model|ai)", str(p)) for p in paths.keys())
        if ai_hint or ai_paths:
            add("ai-api", title=title or "AI API",
                name=title or "AI API",
                endpoint=f"{scheme}://{ip}:{port}",
                docs=f"{scheme}://{ip}:{port}/docs",
                version=str(info.get("version") or ""),
                paths=len(paths), api="openapi")

    # lanbus direct peer（公开健康和身份元数据，不代表已授权）
    st, hdr, data = _http(ip, port, "GET", "/peer/health", timeout=timeout,
                          api_key=api_key, tls=tls)
    obj = _json_body(data)
    if st == 200 and isinstance(obj, dict) and obj.get("direct_pairing") and obj.get("peer_id"):
        add("lanbus-peer", name=str(obj.get("name") or "lanbus-peer"),
            endpoint=f"{scheme}://{ip}:{port}", peer_id=obj["peer_id"],
            hostname=obj.get("hostname") or "", agent=obj.get("agent") or "",
            display_name=obj.get("display_name") or "",
            instance_label=obj.get("instance_label") or "",
            cwd_display=obj.get("cwd_display") or "",
            request_policy=obj.get("request_policy") or "",
            approval_features=obj.get("approval_features") or [],
            direct_pairing=True)

    # lanbus hub
    st, hdr, data = _http(ip, port, "GET", "/health", timeout=timeout, api_key=api_key, tls=tls)
    auth_seen = auth_seen or st in (401, 403)
    obj = _json_body(data)
    if st == 200 and isinstance(obj, dict) and obj.get("ok") and "counts" in obj:
        add("lanbus", endpoint=f"{scheme}://{ip}:{port}",
            version=str(obj.get("version") or ""), mcp=f"{scheme}://{ip}:{port}/mcp")

    # MCP streamable HTTP
    st, hdr, data = _http(
        ip, port, "POST", "/mcp",
        body=json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                         "params": {"protocolVersion": "2025-06-18",
                                    "capabilities": {}}}).encode(),
        timeout=timeout, api_key=api_key, tls=tls)
    obj = _json_body(data)
    if st in (200, 202) and isinstance(obj, dict):
        info = (obj.get("result") or {}).get("serverInfo") or {}
        if info.get("name"):
            add("mcp", endpoint=f"{scheme}://{ip}:{port}/mcp",
                server=str(info.get("name")), version=str(info.get("version") or ""))

    # 401/403 也说明这里有一个需要鉴权的服务
    if not services and auth_seen:
        add("http-auth", endpoint=f"{scheme}://{ip}:{port}", auth_required=True)
    return services


def guess_os(ports, services=None):
    """基于端口/服务做粗粒度 OS 猜测，仅用于提示。"""
    ps = set(ports or [])
    if ps & {5000, 7000, 62078, 548, 3689}:
        return "macos"
    if ps & {135, 139, 445, 3389, 5985, 5986}:
        return "windows"
    if 22 in ps:
        return "unix"
    return ""


def _tcp_open(ip, port, timeout):
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(timeout)
    try:
        return s.connect_ex((ip, port)) == 0
    except Exception:
        return False
    finally:
        try:
            s.close()
        except Exception:
            pass


def scan_host(ip, ports, timeout=DEFAULT_TIMEOUT, http_timeout=HTTP_TIMEOUT,
              api_key=""):
    open_ports = []
    services = []
    for port in ports:
        if _tcp_open(ip, port, timeout):
            open_ports.append(port)
            if port == 22:
                services.append({"kind": "ssh", "name": "ssh", "port": 22,
                                 "transport": "tcp", "ai": False})
                continue
            try:
                svcs = probe_http(ip, port, timeout=http_timeout, api_key=api_key,
                                  tls=(port == 443))
                if not svcs:
                    kind = "tls" if port == 443 else "tcp"
                    services.append({"kind": kind, "name": kind, "port": port,
                                     "transport": "tcp", "ai": False})
                for svc in svcs:
                    svc["port"] = port
                    services.append(svc)
            except Exception:
                services.append({"kind": "tcp", "name": "tcp", "port": port,
                                 "transport": "tcp", "ai": False})
    agents = [s for s in services if s.get("ai")]
    return {"ip": ip, "alive": bool(open_ports),
            "os_guess": guess_os(open_ports, services),
            "ports": open_ports, "services": services,
            "agents": agents}


# ------------------------------------------------------------------ scanning

def parse_ports(value=None, deep=False):
    ports = set(BASE_PORTS)
    if deep:
        ports |= set(range(1, 1025))
    if value:
        if isinstance(value, (list, tuple, set)):
            parts = [str(x) for x in value]
        else:
            parts = re.split(r"[,;\s]+", str(value))
        for part in parts:
            if not part:
                continue
            m = re.match(r"^(\d+)-(\d+)$", part)
            if m:
                a, b = int(m.group(1)), int(m.group(2))
                if a <= b:
                    ports |= set(range(a, min(b, 65535) + 1))
            elif part.isdigit():
                p = int(part)
                if 0 < p < 65536:
                    ports.add(p)
    return sorted(ports)[:MAX_PORTS]


def _targets(cidr, interfaces, arp, max_hosts=MAX_HOSTS):
    nets = []
    if cidr:
        for part in re.split(r"[,;\s]+", str(cidr)):
            if not part:
                continue
            try:
                nets.append(ipaddress.ip_network(part, strict=False))
            except Exception:
                pass
    else:
        virtual = re.compile(
            r"^(docker|veth|br-|virbr|vmnet|vboxnet|vethernet|lo)", re.I)
        for i in interfaces:
            iface = str(i.get("iface") or "")
            if virtual.search(iface):
                continue
            try:
                net = ipaddress.ip_network(i["network"], strict=False)
            except Exception:
                continue
            if net.num_addresses > 256:
                # 大网段只取本机所在 /24，避免 /16 扫爆
                try:
                    net = ipaddress.ip_network(f"{i['ip']}/24", strict=False)
                except Exception:
                    continue
            nets.append(net)
    ips = set()
    for net in nets:
        if net.num_addresses <= 2:
            ips.add(str(net.network_address))
        else:
            for h in net.hosts():
                ips.add(str(h))
    if not cidr:
        # ARP 表只在“自动发现”时并入；显式 cidr 时严格按用户范围扫。
        for ip in arp:
            ips.add(ip)
    out = []
    truncated = False
    for ip in sorted(ips, key=lambda x: int(ipaddress.ip_address(x))):
        if len(out) >= max_hosts:
            truncated = True
            break
        out.append(ip)
    return out, [str(n) for n in nets], truncated


def _local_services_listeners(listeners, api_key="", interesting=None):
    by_port = {}
    for x in listeners:
        if interesting is not None and x["port"] not in interesting:
            continue
        by_port.setdefault(x["port"], []).append(x)
    out = []
    for port in sorted(by_port):
        if port == 22:
            out.append({"kind": "ssh", "name": "ssh", "port": 22,
                        "transport": "tcp", "ai": False,
                        "pid": by_port[port][0].get("pid"),
                        "process": by_port[port][0].get("process")})
            continue
        try:
            svcs = probe_http("127.0.0.1", port, api_key=api_key,
                              tls=(port == 443))
        except Exception:
            svcs = []
        for s in svcs:
            s["pid"] = by_port[port][0].get("pid")
            s["process"] = by_port[port][0].get("process")
            out.append(s)
        if not svcs:
            kind = "tls" if port == 443 else "tcp"
            out.append({"kind": kind, "name": kind, "port": port,
                        "transport": "tcp", "ai": False,
                        "pid": by_port[port][0].get("pid"),
                        "process": by_port[port][0].get("process")})
    return out


def default_scanner_bin():
    """查找 Go lanbus-scan 二进制（环境变量、PATH、源码 bin/、数据目录 bin/）。"""
    env = os.environ.get("LANBUS_SCANNER_BIN")
    if env and os.path.isfile(env):
        return env
    found = shutil.which("lanbus-scan")
    if found:
        return found
    machine = os.uname().machine if hasattr(os, "uname") else ""
    arch = {"AMD64": "amd64", "x86_64": "amd64", "arm64": "arm64",
            "aarch64": "arm64", "x86": "386", "i686": "386"}.get(machine, "")
    names = []
    if sys.platform.startswith("win"):
        names = ["lanbus-scan.exe", "lanbus-scan-windows-amd64.exe"]
    elif sys.platform == "darwin":
        names = [f"lanbus-scan-darwin-{arch or 'arm64'}", "lanbus-scan"]
    else:
        release = os.uname().release.lower() if hasattr(os, "uname") else ""
        if "microsoft" in release or "wsl" in release:
            # WSL 下优先 Windows 二进制（binfmt 互操作）：Linux 二进制只能看到
            # WSL NAT 子网，Windows 侧才能看到真实局域网。
            names = ["lanbus-scan.exe", "lanbus-scan-windows-amd64.exe",
                     f"lanbus-scan-linux-{arch or 'amd64'}", "lanbus-scan"]
        else:
            names = [f"lanbus-scan-linux-{arch or 'amd64'}", "lanbus-scan"]
    here = os.path.dirname(os.path.abspath(__file__))
    roots = [os.path.join(here, "..", "..", "bin"),
             os.path.join(DATA_DIR if "DATA_DIR" in globals() else
                          os.path.expanduser("~/.lanbus"), "bin"),
             os.path.join(os.path.expanduser("~"), ".lanbus", "bin")]
    for root in roots:
        for n in names:
            p = os.path.abspath(os.path.join(root, n))
            if os.path.isfile(p):
                return p
    return ""


def _run_go_scanner(scanner, spec, log=None):
    try:
        p = subprocess.run([scanner], input=json.dumps(spec, ensure_ascii=False),
                           capture_output=True, text=True, encoding="utf-8",
                           errors="replace", timeout=180)
        if p.returncode != 0:
            if log:
                log(f"[discover] go scanner rc={p.returncode}: {(p.stderr or '')[-500:]}")
            return None
        rep = json.loads(p.stdout)
        if isinstance(rep, dict):
            rep["engine"] = "go"
            return rep
    except Exception as e:
        if log:
            log(f"[discover] go scanner failed: {e}")
    return None


PEER_PORTS = (8790, 8791)


def _merge_lanbus_peers(rep, http_timeout=HTTP_TIMEOUT, peer_ports=PEER_PORTS):
    """Go 扫描器（<=0.1.0）缺少 /peer/health 探测；Python 侧对报告中的主机
    补探直连 peer 端口并合并进 hosts/agents/summary，保持与 Python 引擎同等的
    lanbus-peer 发现能力（不修改 Go 二进制）。"""
    if not isinstance(rep, dict):
        return rep
    agents = rep.setdefault("agents", [])
    seen = {(a.get("host"), a.get("port")) for a in agents
            if isinstance(a, dict) and a.get("kind") == "lanbus-peer"}
    for host in rep.get("hosts") or []:
        if not isinstance(host, dict):
            continue
        ip = host.get("ip")
        if not ip:
            continue
        for port in peer_ports:
            if (ip, port) in seen:
                continue
            status, _, data = _http(ip, port, "GET", "/peer/health", timeout=http_timeout)
            obj = _json_body(data)
            if status != 200 or not isinstance(obj, dict) or not (
                    obj.get("direct_pairing") and obj.get("peer_id")):
                continue
            service = {"kind": "lanbus-peer",
                       "name": str(obj.get("name") or "lanbus-peer"),
                       "port": port, "transport": "http", "ai": True,
                       "endpoint": f"http://{ip}:{port}", "peer_id": obj["peer_id"],
                       "hostname": obj.get("hostname") or "",
                       "agent": obj.get("agent") or "", "direct_pairing": True,
                       "display_name": obj.get("display_name") or "",
                       "instance_label": obj.get("instance_label") or "",
                       "cwd_display": obj.get("cwd_display") or "",
                       "request_policy": obj.get("request_policy") or "",
                       "approval_features": obj.get("approval_features") or []}
            host.setdefault("services", []).append(service)
            host.setdefault("agents", []).append(service)
            if port not in host.setdefault("ports", []):
                host["ports"].append(port)
            agents.append({"host": ip, "port": port, "kind": "lanbus-peer",
                           "name": service["name"], "os_guess": host.get("os_guess") or "",
                           "models": [], "endpoint": service["endpoint"],
                           "peer_id": obj["peer_id"], "transport": "http",
                           "hostname": service["hostname"], "agent": service["agent"],
                           "display_name": service["display_name"],
                           "instance_label": service["instance_label"],
                           "cwd_display": service["cwd_display"],
                           "request_policy": service["request_policy"],
                           "approval_features": service["approval_features"]})
            seen.add((ip, port))
    summary = rep.get("summary")
    if isinstance(summary, dict):
        remote = [a for a in agents if isinstance(a, dict) and a.get("host") != "local"]
        summary["agent_endpoints"] = len(remote)
        summary["hosts_with_agents"] = len({a["host"] for a in remote if a.get("host")})
        kinds = summary.get("agent_kinds") or []
        if any(a.get("kind") == "lanbus-peer" for a in remote):
            summary["agent_kinds"] = sorted(set(kinds) | {"lanbus-peer"})
    return rep


def discover(spec=None, log=None, cancel_event=None):
    """执行一次局域网 AI agent 侦查，返回可 JSON 序列化的 report。"""
    spec = spec or {}
    scanner = spec.get("scanner_bin") or os.environ.get("LANBUS_SCANNER_BIN")         or default_scanner_bin()
    if scanner:
        rep = _run_go_scanner(scanner, spec, log)
        if rep is not None:
            return _merge_lanbus_peers(
                rep, http_timeout=float(spec.get("http_timeout") or HTTP_TIMEOUT),
                peer_ports=tuple(spec.get("peer_ports") or PEER_PORTS))
    started = time.time()

    def say(msg):
        if log:
            try:
                log(str(msg))
            except Exception:
                pass

    timeout = float(spec.get("timeout") or DEFAULT_TIMEOUT)
    http_timeout = float(spec.get("http_timeout") or HTTP_TIMEOUT)
    timeout = max(0.05, min(5.0, timeout))
    http_timeout = max(0.2, min(15.0, http_timeout))
    deep = bool(spec.get("deep"))
    ports = parse_ports(spec.get("ports"), deep=deep)
    max_hosts = max(1, min(MAX_HOSTS, int(spec.get("max_hosts") or MAX_HOSTS)))
    include_local = spec.get("include_local", True)
    include_processes = spec.get("include_processes", True)
    include_wsl = spec.get("include_wsl", True)
    # 侦查阶段绝不把用户 API key 发给全网开放端口；需要鉴权的端点只做识别。
    api_key = ""
    if cancel_event is not None and cancel_event.is_set():
        return {"ok": False, "cancelled": True, "generated_at": _now(),
                "summary": {}, "hosts": [], "agents": [], "local_agents": []}

    say("[discover] interfaces / processes / listeners ...")
    interfaces = local_interfaces()
    base_procs = local_processes() if (include_local or include_processes) else []
    wsl_procs = []
    if include_processes and include_wsl and sys.platform.startswith("win"):
        wsl_procs = wsl_processes()
        if wsl_procs:
            say(f"[discover] wsl processes={len(wsl_procs)}")
    agents_local = detect_local_agents(base_procs + wsl_procs) if include_processes else []
    listeners = listening_ports(base_procs) if include_local else []
    local_services = (_local_services_listeners(listeners, api_key, set(ports))
                      if include_local else [])

    arp = arp_table()
    targets, nets, truncated = _targets(spec.get("cidr"), interfaces, arp, max_hosts)
    say(f"[discover] targets={len(targets)} nets={nets} ports={len(ports)} "
        f"timeout={timeout}")

    hosts = []
    cancelled = False
    done = 0
    try:
        with concurrent.futures.ThreadPoolExecutor(
                max_workers=min(THREADS, max(1, len(targets)))) as ex:
            futs = {ex.submit(scan_host, ip, ports, timeout, http_timeout,
                              api_key): ip for ip in targets}
            for fut in concurrent.futures.as_completed(futs):
                ip = futs[fut]
                if cancel_event is not None and cancel_event.is_set():
                    cancelled = True
                    for f in futs:
                        f.cancel()
                    break
                try:
                    h = fut.result()
                except Exception as e:
                    h = {"ip": ip, "alive": False, "ports": [], "services": [],
                         "agents": [], "error": str(e)}
                h["mac"] = (arp.get(ip) or {}).get("mac", "")
                h["iface"] = (arp.get(ip) or {}).get("iface", "")
                # 本机通过 127.0.0.1 探测到的服务也合并进本机记录
                if ip in [i.get("ip") for i in interfaces]:
                    h["local"] = True
                if h["alive"] or h["mac"]:
                    hosts.append(h)
                done += 1
                if done % 32 == 0:
                    say(f"[discover] scanned {done}/{len(targets)}")
    except Exception as e:
        say(f"[discover] scan error: {e}")

    hosts.sort(key=lambda x: int(ipaddress.ip_address(x["ip"])))
    agents = []
    for h in hosts:
        for s in h.get("agents") or []:
            agents.append({"host": h["ip"], "port": s.get("port"),
                           "kind": s.get("kind"), "name": s.get("name"),
                           "os_guess": h.get("os_guess") or "",
                           "models": s.get("models") or [],
                           "endpoint": s.get("endpoint"),
                           "transport": s.get("transport")})
    for a in agents_local:
        agents.append({"host": "local", "kind": a.get("kind"),
                       "name": a.get("name"), "pid": a.get("pid"),
                       "pids": a.get("pids") or [],
                       "process": a.get("process") or "",
                       "paths": a.get("paths") or [],
                       "path": a.get("path") or "",
                       "scope": a.get("scope") or "local",
                       "helper_paths": a.get("helper_paths") or [],
                       "instances": a.get("instances", 1),
                       "models": [], "endpoint": None,
                       "transport": "process"})

    agent_hosts = {a["host"] for a in agents if a["host"] != "local"}
    kinds = sorted({a["kind"] for a in agents if a.get("kind")})
    summary = {
        "hosts_in_scope": len(targets),
        "hosts_alive": len([h for h in hosts if h.get("alive")]),
        "hosts_with_agents": len(agent_hosts),
        "agent_endpoints": len([a for a in agents if a["host"] != "local"]),
        "local_agents": len(agents_local),
        "local_agent_instances": sum(int(a.get("instances") or 1)
                                     for a in agents_local),
        "agent_kinds": kinds,
        "ports_scanned": len(ports),
        "truncated": truncated,
        "deep": deep,
    }
    report = {
        "ok": True,
        "engine": "python",
        "generated_at": _now(),
        "duration_s": round(time.time() - started, 2),
        "cancelled": cancelled,
        "host": socket.gethostname(),
        "platform": sys.platform,
        "scope": {"networks": nets, "cidr": spec.get("cidr") or "",
                  "ports": ports, "max_hosts": max_hosts},
        "interfaces": interfaces,
        "local_agents": agents_local,
        "local_services": local_services,
        "hosts": hosts,
        "agents": agents,
        "summary": summary,
        "notes": [
            "进程证据只在本机有效；远端主机只能通过开放端口/HTTP 指纹发现。",
            "GUI-only agent 如果没有监听端口，无法从其它机器直接嗅探；"
            "在该主机运行 lanbus worker 或 lanbus discover 才能看到进程。",
        ],
    }
    say(f"[discover] done: {summary}")
    return report


def _now():
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


# ----------------------------------------------------------------------- ask

def ask(ip, port, prompt, model="", service="", api_key="", timeout=120,
        max_tokens=1024, tls=False):
    """向发现的 OpenAI-compatible / Ollama 端点发一条消息。无需对方装插件。"""
    ip = str(ip).strip()
    port = int(port)
    prompt = str(prompt or "")
    if not prompt:
        return {"ok": False, "error": "prompt required"}

    scheme = "https" if tls else "http"

    def jpost(path, payload):
        return _http(ip, port, "POST", path, body=json.dumps(payload).encode(),
                     timeout=timeout, api_key=api_key, tls=tls)

    def jget(path):
        return _http(ip, port, "GET", path, timeout=min(timeout, HTTP_TIMEOUT),
                     api_key=api_key, tls=tls)

    # 1) OpenAI-compatible
    if service not in ("ollama",):
        st, hdr, data = jget("/v1/models")
        models = _models_from(data) if st == 200 else []
        use_model = model or (models[0] if models else "")
        if use_model:
            st, hdr, data = jpost("/v1/chat/completions", {
                "model": use_model,
                "messages": [{"role": "user", "content": prompt}],
                "max_tokens": int(max_tokens),
                "temperature": 0.2,
                "stream": False,
            })
            obj = _json_body(data)
            if st == 200 and isinstance(obj, dict):
                try:
                    reply = obj["choices"][0]["message"]["content"]
                except Exception:
                    reply = ""
                if reply:
                    return {"ok": True, "endpoint": f"{scheme}://{ip}:{port}/v1",
                            "api": "openai", "model": use_model,
                            "reply": reply, "raw": obj}
            if st in (401, 403):
                return {"ok": False, "auth_required": True, "status": st,
                        "endpoint": f"{scheme}://{ip}:{port}/v1",
                        "error": "endpoint requires authentication"}

    # 2) Ollama
    st, hdr, data = jget("/api/tags")
    models = _models_from(data) if st == 200 else []
    use_model = model or (models[0] if models else "")
    if use_model:
        st, hdr, data = jpost("/api/chat", {
            "model": use_model,
            "messages": [{"role": "user", "content": prompt}],
            "stream": False,
        })
        obj = _json_body(data)
        if st == 200 and isinstance(obj, dict):
            reply = ((obj.get("message") or {}).get("content") or "")
            if reply:
                return {"ok": True, "endpoint": f"{scheme}://{ip}:{port}",
                        "api": "ollama", "model": use_model,
                        "reply": reply, "raw": obj}
        st, hdr, data = jpost("/api/generate", {
            "model": use_model, "prompt": prompt, "stream": False,
        })
        obj = _json_body(data)
        if st == 200 and isinstance(obj, dict) and obj.get("response"):
            return {"ok": True, "endpoint": f"{scheme}://{ip}:{port}",
                    "api": "ollama-generate", "model": use_model,
                    "reply": obj.get("response"), "raw": obj}
    return {"ok": False, "endpoint": f"{scheme}://{ip}:{port}",
            "error": "no compatible model endpoint or no model available",
            "models": models}


def summary_text(report):
    """给 CLI 的人类可读摘要。"""
    s = report.get("summary") or {}
    lines = [f"host {report.get('host')} ({report.get('platform')})  "
             f"{report.get('duration_s')}s  scope={','.join((report.get('scope') or {}).get('networks') or [])}",
             f"hosts: {s.get('hosts_alive')}/{s.get('hosts_in_scope')} alive, "
             f"{s.get('hosts_with_agents')} with AI endpoints, "
             f"{s.get('agent_endpoints')} endpoints, {s.get('local_agents')} local agents"]
    for a in report.get("agents") or []:
        host = a.get("host")
        if host == "local":
            pids = ", ".join(str(x) for x in (a.get("pids") or [])[:6])
            scope = a.get("scope") or "local"
            lines.append(f"  [{scope}] {a.get('name')} instances={a.get('instances', 1)} "
                         f"pids=[{pids}] path={a.get('path') or '(unknown)'}")
        else:
            models = ",".join((a.get("models") or [])[:4])
            suffix = f"  models={models}" if models else ""
            lines.append(f"  {host}:{a.get('port')}  {a.get('kind')}{suffix}")
    if not report.get("agents"):
        lines.append("  (no AI endpoints found)")
    return "\n".join(lines)


if __name__ == "__main__":
    import argparse
    ap = argparse.ArgumentParser(description="lanbus LAN AI agent discovery")
    ap.add_argument("--cidr", default="")
    ap.add_argument("--ports", default="")
    ap.add_argument("--deep", action="store_true")
    ap.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT)
    ap.add_argument("--json", action="store_true")
    a = ap.parse_args()
    rep = discover({"cidr": a.cidr, "ports": a.ports, "deep": a.deep,
                    "timeout": a.timeout}, log=lambda x: print(x, file=sys.stderr))
    print(json.dumps(rep, ensure_ascii=False, indent=2) if a.json
          else summary_text(rep))
