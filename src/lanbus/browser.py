#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
lanbus browser helper —— worker 内置的轻量浏览器执行器。

优先使用 Playwright（可点击、填表、执行 JS、整页截图）；
没装 Playwright 时退化为本机 Chrome/Edge/Chromium 的 headless 截图。
零硬依赖：Playwright 是可选能力。

支持的动作（spec["actions"]）：
  {"goto": "http://..."}                打开页面
  {"click": "selector"}                 点击
  {"fill": ["selector", "value"]}       填表
  {"press": ["selector", "Enter"]}      按键
  {"select": ["selector", "value"]}     下拉选择
  {"check": "selector"}                 勾选
  {"wait": 1000}                        等待毫秒
  {"wait_for": "selector"}              等到元素出现
  {"wait_for_load": true}               等待网络空闲
  {"screenshot": "path.png"}            保存截图
  {"pdf": "path.pdf"}                   保存 PDF（Playwright/Chromium）
  {"evaluate": "js expression"}         执行 JS 并记录结果
  {"reload": true}                      刷新
"""

import glob
import json
import os
import shutil
import tempfile
import subprocess
import sys
import time


def _which_any(names):
    for n in names:
        p = shutil.which(n)
        if p:
            return p
    return ""


def find_browser():
    """找本机 Chrome/Edge/Chromium 可执行文件。"""
    env = os.environ.get("LANBUS_BROWSER")
    if env and os.path.isfile(env):
        return env
    names = ["msedge", "msedge.exe", "chrome", "chrome.exe", "chromium",
             "chromium-browser", "google-chrome", "google-chrome-stable"]
    p = _which_any(names)
    if p:
        return p
    candidates = []
    if sys.platform.startswith("win"):
        roots = [os.environ.get("ProgramFiles", r"C:\Program Files"),
                 os.environ.get("ProgramFiles(x86)", r"C:\Program Files (x86)"),
                 os.environ.get("LOCALAPPDATA", "")]
        for root in roots:
            if not root:
                continue
            candidates += [
                os.path.join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
                os.path.join(root, "Google", "Chrome", "Application", "chrome.exe"),
                os.path.join(root, "Chromium", "Application", "chrome.exe"),
            ]
    elif sys.platform == "darwin":
        candidates += [
            "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
            "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
            "/Applications/Chromium.app/Contents/MacOS/Chromium",
        ]
    else:
        candidates += ["/usr/bin/chromium", "/usr/bin/chromium-browser",
                       "/usr/bin/google-chrome", "/usr/bin/microsoft-edge"]
    for c in candidates:
        if c and os.path.isfile(c):
            return c
    # Playwright 下载的 chromium
    base = os.environ.get("PLAYWRIGHT_BROWSERS_PATH") or os.path.join(
        os.path.expanduser("~"), ".cache", "ms-playwright")
    for pat in ("chromium-*/chrome-linux/chrome",
                "chromium-*/chrome-linux64/chrome",
                "chromium-*/chrome-win/chrome.exe",
                "chromium-*/chrome-mac/Chromium.app/Contents/MacOS/Chromium"):
        hits = glob.glob(os.path.join(base, pat))
        if hits:
            return hits[0]
    return ""


def detect():
    """返回当前 worker 的浏览器能力。"""
    pw = False
    pw_version = ""
    try:
        import playwright  # noqa: F401
        pw = True
        try:
            from importlib.metadata import version
            pw_version = version("playwright")
        except Exception:
            pass
    except Exception:
        pass
    browser = find_browser()
    caps = []
    if pw:
        caps.append("browser:playwright")
    if browser:
        caps.append("browser:headless")
    if caps:
        caps.insert(0, "browser")
    return {"ok": bool(caps), "playwright": pw, "playwright_version": pw_version,
            "browser": browser, "caps": caps}


def _abs(cwd, p, default):
    p = p or default
    if not os.path.isabs(p):
        p = os.path.join(cwd or os.getcwd(), p)
    return os.path.abspath(p)


def _log(log, msg):
    if log:
        try:
            log(str(msg))
        except Exception:
            pass


def _run_playwright(spec, cwd, out, log):
    from playwright.sync_api import sync_playwright
    url = spec.get("url") or ""
    timeout = int(spec.get("timeout_ms") or 30000)
    viewport = spec.get("viewport") or {"width": 1440, "height": 900}
    full_page = bool(spec.get("full_page", True))
    actions = spec.get("actions") or []
    shot_taken = False
    with sync_playwright() as p:
        browser = None
        last = None
        for kwargs in ({"headless": True, "args": ["--no-sandbox", "--disable-dev-shm-usage"]},
                       {"headless": True, "channel": "msedge"},
                       {"headless": True, "channel": "chrome"}):
            try:
                browser = p.chromium.launch(**kwargs)
                break
            except Exception as e:
                last = e
        if browser is None:
            raise RuntimeError(f"Playwright 无法启动 Chromium: {last}")
        ctx = browser.new_context(viewport={"width": int(viewport.get("width", 1440)),
                                           "height": int(viewport.get("height", 900))},
                                  device_scale_factor=1,
                                  ignore_https_errors=True)
        page = ctx.new_page()
        page.set_default_timeout(timeout)
        if url:
            _log(log, f"[playwright] goto {url}")
            try:
                page.goto(url, wait_until="networkidle", timeout=timeout)
            except Exception:
                page.goto(url, wait_until="load", timeout=timeout)
        for i, act in enumerate(actions):
            if not isinstance(act, dict):
                continue
            _log(log, f"[playwright] action {i + 1}/{len(actions)}: {json.dumps(act, ensure_ascii=False)[:300]}")
            if act.get("goto"):
                page.goto(str(act["goto"]), wait_until="load", timeout=timeout)
            elif act.get("click"):
                page.click(str(act["click"]), timeout=timeout)
            elif act.get("fill"):
                v = act["fill"]
                if isinstance(v, (list, tuple)) and len(v) >= 2:
                    page.fill(str(v[0]), str(v[1]), timeout=timeout)
                elif isinstance(v, dict):
                    page.fill(str(v.get("selector")), str(v.get("value", "")), timeout=timeout)
            elif act.get("press"):
                v = act["press"]
                if isinstance(v, (list, tuple)) and len(v) >= 2:
                    page.press(str(v[0]), str(v[1]), timeout=timeout)
            elif act.get("select"):
                v = act["select"]
                if isinstance(v, (list, tuple)) and len(v) >= 2:
                    page.select_option(str(v[0]), str(v[1]), timeout=timeout)
            elif act.get("check"):
                page.check(str(act["check"]), timeout=timeout)
            elif act.get("wait") is not None:
                page.wait_for_timeout(int(act["wait"]))
            elif act.get("wait_for"):
                page.wait_for_selector(str(act["wait_for"]), timeout=timeout)
            elif act.get("wait_for_load"):
                page.wait_for_load_state("networkidle", timeout=timeout)
            elif act.get("evaluate"):
                val = page.evaluate(str(act["evaluate"]))
                _log(log, f"[playwright] evaluate => {str(val)[:1000]}")
            elif act.get("reload"):
                page.reload(wait_until="load", timeout=timeout)
            elif act.get("screenshot"):
                sp = _abs(cwd, act["screenshot"], out)
                os.makedirs(os.path.dirname(sp), exist_ok=True)
                page.screenshot(path=sp, full_page=full_page)
                shot_taken = True
                _log(log, f"[playwright] screenshot -> {sp}")
            elif act.get("pdf"):
                pp = _abs(cwd, act["pdf"], os.path.splitext(out)[0] + ".pdf")
                os.makedirs(os.path.dirname(pp), exist_ok=True)
                page.pdf(path=pp, print_background=True)
                _log(log, f"[playwright] pdf -> {pp}")
        if spec.get("wait_ms"):
            page.wait_for_timeout(int(spec["wait_ms"]))
        if spec.get("pdf"):
            pp = _abs(cwd, spec["pdf"], os.path.splitext(out)[0] + ".pdf")
            os.makedirs(os.path.dirname(pp), exist_ok=True)
            page.pdf(path=pp, print_background=True)
            _log(log, f"[playwright] pdf -> {pp}")
        if not shot_taken and out:
            os.makedirs(os.path.dirname(out), exist_ok=True)
            page.screenshot(path=out, full_page=full_page)
            _log(log, f"[playwright] screenshot -> {out}")
        ctx.close()
        browser.close()
    return 0, f"playwright ok: {out}"


def _run_headless(spec, cwd, out, log):
    exe = find_browser()
    if not exe:
        return 127, ("没有可用的浏览器：请 pip install playwright && playwright install chromium，"
                     "或安装 Chrome/Edge，或设置 LANBUS_BROWSER")
    url = spec.get("url") or ""
    if not url:
        return 2, "browser: url required"
    actions = spec.get("actions") or []
    allowed = {"goto", "wait", "wait_for_load", "screenshot", "reload"}
    for act in actions:
        if isinstance(act, dict) and not set(act.keys()).issubset(allowed):
            return 3, ("headless 降级只支持 goto/wait/screenshot；"
                       "点击/填表/JS 需要 Playwright：pip install playwright && playwright install chromium")
    viewport = spec.get("viewport") or {"width": 1440, "height": 900}
    width = int(viewport.get("width", 1440))
    height = int(viewport.get("height", 900))
    if spec.get("full_page"):
        height = max(height, 2600)
    os.makedirs(os.path.dirname(out), exist_ok=True)
    profile = tempfile.mkdtemp(prefix="lanbus-chrome-")
    base = [exe, "--headless=new", "--disable-gpu", "--no-sandbox",
            "--hide-scrollbars", "--no-first-run", "--no-default-browser-check",
            "--disable-extensions", f"--user-data-dir={profile}",
            f"--window-size={width},{height}", f"--screenshot={out}", url]
    try:
        for args in (base,
                     [a if a != "--headless=new" else "--headless" for a in base]):
            _log(log, f"[headless] {exe} {' '.join(args[:5])} ...")
            try:
                p = subprocess.run(args, capture_output=True, text=True, timeout=90,
                                   encoding="utf-8", errors="replace")
            except Exception as e:
                return 125, f"headless 启动失败: {e}"
            if os.path.isfile(out) and os.path.getsize(out) > 0:
                return 0, f"headless ok: {out}"
            if p.returncode == 0:
                time.sleep(1)
                if os.path.isfile(out) and os.path.getsize(out) > 0:
                    return 0, f"headless ok: {out}"
        return 1, f"headless 截图失败 rc={p.returncode}: {(p.stderr or p.stdout or '')[-2000:]}"
    finally:
        shutil.rmtree(profile, ignore_errors=True)


def run_browser(spec, cwd, log=None):
    """执行 browser spec，返回 (exit_code, output)。"""
    spec = spec or {}
    cwd = os.path.abspath(cwd or os.getcwd())
    out = _abs(cwd, spec.get("out"), ".lanbus-shot.png")
    os.makedirs(os.path.dirname(out), exist_ok=True)
    try:
        import playwright  # noqa: F401
        try:
            return _run_playwright(spec, cwd, out, log)
        except Exception as e:
            _log(log, f"[browser] Playwright 失败: {e}")
            rc, msg = _run_headless(spec, cwd, out, log)
            if rc == 0:
                return rc, msg + f"（Playwright 失败后降级：{e}）"
            return rc, f"{msg}\nPlaywright 错误: {e}"
    except ImportError:
        return _run_headless(spec, cwd, out, log)


def main(argv=None):
    import argparse
    ap = argparse.ArgumentParser(description="lanbus browser probe / screenshot")
    ap.add_argument("--url", default="")
    ap.add_argument("--out", default=".lanbus-shot.png")
    ap.add_argument("--cwd", default="")
    ap.add_argument("--spec-file", default="",
                    help="JSON spec file written by the worker")
    ap.add_argument("--spec-json", default="")
    ap.add_argument("--full-page", action="store_true", default=True)
    ap.add_argument("--wait-ms", type=int, default=0)
    ap.add_argument("--detect", action="store_true")
    a = ap.parse_args(argv)
    if a.detect:
        print(json.dumps(detect(), ensure_ascii=False, indent=2))
        return 0
    spec = None
    if a.spec_file:
        with open(a.spec_file, encoding="utf-8") as f:
            spec = json.load(f)
    elif a.spec_json:
        spec = json.loads(a.spec_json)
    if spec is None:
        if not a.url:
            ap.error("--url required unless --detect/--spec-file")
        spec = {"url": a.url, "out": a.out, "full_page": a.full_page,
                "wait_ms": a.wait_ms}
    spec.setdefault("out", a.out)
    rc, msg = run_browser(spec, a.cwd or os.getcwd(),
                          lambda line: print(line, flush=True))
    print(msg, flush=True)
    return rc


if __name__ == "__main__":
    sys.exit(main())
