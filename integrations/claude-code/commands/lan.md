---
description: 向局域网某环境的子 agent 派活，并自动跑完「执行 → 实时日志 → 验收 → 打回重做」回环
argument-hint: <env> <任务描述，例如: windows 把登录页按钮圆角改成 12px 并跑 build>
allowed-tools: mcp__lanbus__lan_envs, mcp__lanbus__lan_discover, mcp__lanbus__lan_ask, mcp__lanbus__lan_dispatch, mcp__lanbus__lan_shot, mcp__lanbus__lan_logs, mcp__lanbus__lan_await, mcp__lanbus__lan_result, mcp__lanbus__lan_artifact, mcp__lanbus__lan_review, mcp__lanbus__lan_list, mcp__lanbus__lan_interrupt, Task, Read, Bash
---

按下面流程执行，不要跳过任何一步：

参数：`$ARGUMENTS`（第一个词是目标环境，其余是任务需求）

1. 先 `lan_discover` 侦查当前局域网：看 `summary`、`agents`、`hosts`，确认目标环境/端口。再 `lan_envs` 确认目标环境有 worker 在线，记录 `caps` / `has_browser`。都没有就报告，不要在本机代做。
2. 如果发现的是 Ollama / OpenAI-compatible 端点，可直接 `lan_ask(endpoint, prompt)` 取反馈（无需对方装插件）；需要执行文件改动时才走 lanbus worker。
3. 把任务需求整理成两段：
   - `context`：项目背景、相关路径、已知约束、验收标准、不允许动的东西；
   - `prompt`：对方能独立执行的指令（目录、改什么、期望结果）。
4. `lan_dispatch(env, title, prompt, context, cwd, verify, artifact_globs, wait_s=240)`。
   若返回 `pending`，用 `lan_logs(task_id, wait_s=30)` 边看日志边等；同时可 `lan_await(task_ids, wait_s=240)`，最多等 10 分钟。
   任务长时间 `running` 且无新日志 → 先看日志判断是否卡在交互/依赖，必要时 `lan_interrupt` / `lan_cancel` 并报告。
5. `lan_result(task_id)`，读 `verify_ok` / `verify_out` / `log_tail` / `diff` / `changed` / `artifacts`。
   图片产物会作为 image content 返回；需要单看时调 `lan_artifact`。
6. **验收判定**（这是你的责任，不是子 agent 的）：
   - `verify_ok=1` 且 diff 与需求一致 → `lan_review(verdict="approve")`，进入第 8 步。
   - `verify_ok=0`，或 diff 改了不相干的东西，或改动量明显不足 → `lan_review(verdict="rework", feedback="…")`，
     feedback 里必须写清：哪条断言没过、期望什么、不许动什么。然后回到第 4 步等它重做（`retry` 会自动 +1）。
   - `retry` 已达 `max_retry` 仍不达标 → `lan_review(verdict="fail")`，向用户报告失败原因与已尝试的修改。
7. 循环上限：最多 3 轮验收，超过就停下来向用户报告卡点，不要无限重试。
8. 收尾汇报（一段话）：环境、最终状态、验收命令与结果、改动了哪些文件、产物名称/下载 URL、关键日志。

补充：

- 需要浏览器截图而未派 agent 时，可用 `lan_shot(env, url, actions, out, wait_s)`；它要求 worker 有 `browser` 能力。
- 取消必须用 `lan_cancel`，不要只把任务标成失败；取消会真正终止远端进程树。
- `lan_discover` 只能看到网络暴露的服务；纯 GUI agent 要看到进程，需要在那台机器跑 worker 或 `lanbus discover`。
