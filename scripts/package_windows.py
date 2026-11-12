#!/usr/bin/env python3
"""Build a portable Windows package from the already-built Go binaries."""
import os
import re
import shutil
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
VERSION = re.search(r'__version__\s*=\s*"([^"]+)"',
                   (ROOT / "src/lanbus/__init__.py").read_text(encoding="utf-8")).group(1)
OUT = ROOT / "dist" / f"lanbus-{VERSION}-windows-amd64"
BINS = ["lanbus.exe", "lanbusd.exe", "lanbus-worker.exe", "lanbus-scan.exe", "lanbus-gui.exe"]

for b in BINS:
    if not (ROOT / "bin" / b).is_file():
        raise SystemExit(f"missing bin/{b}; run make go-hub go-worker go-gui")

if OUT.exists():
    shutil.rmtree(OUT)
(OUT / "bin").mkdir(parents=True)
(OUT / "examples").mkdir()
for b in BINS:
    shutil.copy2(ROOT / "bin" / b, OUT / "bin" / b)

(OUT / "start-hub.cmd").write_text(r"""@echo off
cd /d "%~dp0"
set "LANBUS_DATA=%USERPROFILE%\.lanbus"
if not exist "%LANBUS_DATA%" mkdir "%LANBUS_DATA%"
if not exist "%LANBUS_DATA%\token.txt" powershell -NoProfile -Command "[guid]::NewGuid().ToString('N') | Set-Content -NoNewline -Encoding ASCII '%LANBUS_DATA%\token.txt'"
bin\lanbusd.exe --bind 0.0.0.0 --port 8788 --data-dir "%LANBUS_DATA%" --token-file "%LANBUS_DATA%\token.txt" --worker-token-file "%LANBUS_DATA%\token.txt" --reap-s 5
""", encoding="utf-8")

(OUT / "start-gui.cmd").write_text("""@echo off
cd /d "%~dp0"
bin\lanbus-gui.exe --hub http://127.0.0.1:8788
""", encoding="utf-8")

(OUT / "start-local-workers.cmd").write_text("""@echo off
cd /d "%~dp0"
rem Requires Python for `lanbus local`; edit examples\local.json for this machine.
python -m lanbus local up
""", encoding="utf-8")

(OUT / "lanbus.cmd").write_text("""@echo off
python -m lanbus %*
""", encoding="utf-8")

(OUT / "examples" / "local.json").write_text("""{
  "hub": "http://127.0.0.1:8788",
  "token_file": "C:/Users/me/.lanbus/token.txt",
  "workers": [
    {"name": "win-claude", "env": "win-claude", "agent": "claude",
     "cwd": "D:/proj", "binary": "D:/lanbus/bin/lanbus-worker.exe"},
    {"name": "win-browser", "env": "win-browser", "kind": "python",
     "agent": "shell", "cwd": "D:/proj"}
  ]
}
""", encoding="utf-8")

(OUT / "README.txt").write_text(f"""lanbus {VERSION} - portable Windows package

1. Run start-hub.cmd once (or install as a startup task).
2. Run start-gui.cmd for the Wails/tray desktop shell.
3. Optional: copy examples\local.json to %USERPROFILE%\.lanbus\local.json,
   edit paths/token, then run `python -m lanbus local up` to keep agents online.

Binaries:
  bin\lanbus.exe        native product CLI (version/healthcheck/backup/restore/daemon)
  bin\lanbusd.exe       Go hub (default, port 8788)
  bin\lanbus-gui.exe    Wails/WebView2 window + tray
  bin\lanbus-worker.exe Go worker (claude/codex/kimi/deepseek/discover/ask/pty)
  bin\lanbus-scan.exe   native LAN scanner

Python is only needed for the optional local supervisor and fallback Python hub.
""", encoding="utf-8")

(OUT / "install-startup.ps1").write_text(r'''$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$home = Join-Path $env:USERPROFILE ".lanbus"
New-Item -ItemType Directory -Force -Path $home | Out-Null
Copy-Item (Join-Path $root "start-hub.cmd") (Join-Path $home "start-hub.cmd") -Force
$vbs = @'
Set sh = CreateObject("WScript.Shell")
sh.Run """%USERPROFILE%\.lanbus\start-hub.cmd""", 0, False
'@
$startup = [Environment]::GetFolderPath("Startup")
$vbs | Set-Content -Encoding ASCII (Join-Path $startup "lanbus-hub.vbs")
Write-Host "Installed startup shortcut: $startup\lanbus-hub.vbs"
''', encoding="utf-8")

zip_path = ROOT / "dist" / f"lanbus-{VERSION}-windows-amd64.zip"
with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as z:
    for p in OUT.rglob("*"):
        z.write(p, p.relative_to(OUT.parent))
print(zip_path)
print(OUT)
