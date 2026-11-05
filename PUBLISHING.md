# 发布到 GitHub

本目录已经是一个完整可构建、可测试的项目。下面是把 `D:\nixiang\lanbus` 变成公开仓库的全部步骤。**我没有替你执行 git 提交或推送**，命令由你自己跑。

## 1. 先替换 4 处占位符

| 文件 | 行 | 现值 | 改成 |
|---|---|---|---|
| `pyproject.toml` | 36 | `https://github.com/CozeraVan/lanbus` | https://github.com/CozeraVan/lanbus |
| `pyproject.toml` | 37 | `https://github.com/CozeraVan/lanbus/issues` | 同上 + `/issues` |
| `CONTRIBUTING.md` | 6 | `git clone https://github.com/CozeraVan/lanbus` | https://github.com/CozeraVan/lanbus |
| `examples/wsl/lanbus-worker@.service` | 11 | `https://github.com/CozeraVan/lanbus` | https://github.com/CozeraVan/lanbus |

```powershell
# 一次性替换（把 CozeraVan 换成你的 GitHub 用户名）
Get-ChildItem -Recurse -File -Include *.toml,*.md,*.service |
  ForEach-Object { (Get-Content $_ -Raw) -replace 'github\.com/USER/lanbus','github.com/CozeraVan/lanbus' |
    Set-Content $_ -NoNewline }
```

`Dockerfile` 里的 `USER lanbus` 是容器用户，不要动。

## 2. 初始化并推送

```bash
cd D:\nixiang\lanbus
git init -b main
git add -A
git commit -m "lanbus 1.9.0: native Go hub subset + dual-run"

# 在 GitHub 上先建一个空仓库（不要勾选 README/LICENSE/gitignore）
git remote add origin https://github.com/CozeraVan/lanbus.git
git push -u origin main
```

## 3. 打标签与 Release

```bash
git tag -a v1.9.0 -m "lanbus 1.9.0"
git push origin v1.9.0
```

在 GitHub 上新建 Release，选 `v1.9.0`，正文可直接取 `CHANGELOG.md` 的 `[1.9.0]` 段落。附件上传构建产物：

```bash
python -m pip install build
python -m build            # 生成 dist/lanbus-1.9.0-py3-none-any.whl 与 .tar.gz
```

## 4. 可选：发布到 PyPI

```bash
python -m twine upload dist/*
```

`lanbus` 这个名字在 PyPI 上可能已被占用。若被占用，改 `pyproject.toml` 的 `name`（例如 `lanbus-agent`），同时保留命令名 `lanbus`（`[project.scripts]` 不受包名影响）。

## 5. 推送前检查清单

```bash
pytest -v                                        # 39 项必须全绿
python -m pip wheel . --no-deps --no-build-isolation -w dist
git status --porcelain                           # 确认没有下面这些
```

**绝不能入库**（`.gitignore` 已挡住，仍需人工确认）：

- `token.txt` / `.mcp.json`（含真实口令）
- `bus.db*` / `artifacts/`（任务数据与截图）
- `*.egg-info/` / `dist/` / `__pycache__/` / `.pytest_cache/`

```bash
git ls-files | Select-String "token.txt|\.mcp\.json$|bus\.db|artifacts/"   # 应该没有输出
```

## 6. CI 会做什么

`.github/workflows/ci.yml`：

- **test**：Ubuntu / Windows / macOS × Python 3.9 / 3.12（每个 OS 取两个版本，矩阵已裁剪），`pip install -e ".[dev]"` + `pytest`。测试会真起 hub 与两个 worker 并驱动真实 MCP，不依赖网络服务，所以在三个 OS 上都能跑。
- **build**：`python -m build` + `twine check` + 装进干净 venv 跑 `lanbus --version` 与 `lanbus-hub --help`，并上传 `dist/` 工件。

首次推送后看 Actions 页签；Windows runner 上 `tests` 会走 `cmd.exe` 执行验收命令，这也是本机（Windows）已经验证过的路径。

## 7. 建议的仓库设置

- Description：`One agent entry, sub-agents on your LAN — dispatch, verify, rework. Zero deps.`
- Topics：`mcp`、`claude-code`、`multi-agent`、`orchestration`、`wsl`、`lan`、`python`
- 关掉 Wiki / Projects，Discussions 可选
- 分支保护：要求 `ci / test` 全绿才可合并