# 跨机连接与对话便捷性设计

日期：2026-11-12
分支：`fix/hub-state-parity`

## 目标

将 macOS／Windows 主 agent 连接局域网 Hub 并发起多 agent 对话的操作，从重复填写 Hub URL、token 和 env 列表，简化为首次 `lanbus connect`、后续 `lanbus chat --message`。保持现有 token 认证、远程人工确认和显式参数兼容性，不通过局域网免认证换取便利。

## 范围

本轮修改 Python CLI、发现默认端口、连接配置存储及相关测试和文档。不修改 Go／Python Hub API、MCP 工具 schema、数据库结构、Worker 注册协议或远程确认规则，不要求其他 LAN 节点同步升级。

不实现自动启动本地 Worker、系统钥匙串、二维码配对、免 token 配对或跨机自动安装。

## 用户流程

### 首次连接

```bash
lanbus connect 192.168.1.236
```

CLI 将地址规范化为 `http://192.168.1.236:8788`，请求 `/health`，确认是兼容 Hub，然后按以下顺序取得 agent token：

1. `--token`；
2. `LANBUS_TOKEN`；
3. 已保存的该 Hub token；
4. 终端隐藏输入。

连接成功后保存默认连接并输出在线 env 数量，不打印 token。非交互终端缺少 token 时返回明确错误，不等待输入。

`lanbus connect` 不带地址时运行 LAN 发现，列出检测到的 lanbus Hub。只有一个时自动选择；多个时返回编号列表并要求用 `lanbus connect HOST` 明确选择，避免误连。

`--no-save` 只验证并输出连接结果，不写配置；本次进程结束后不保留 token。

### 后续对话

```bash
lanbus chat --message "请互相介绍并给出协作方案"
```

未传 `--hub` 时使用默认连接及其 token。未传 `--envs` 且不是续聊时，通过 `lan_envs` 获取在线 Worker，按 Hub 返回顺序选择前 4 个，至少 2 个才发起对话。`--envs` 始终优先，不改变现有行为。

续聊只要求 `conversation_id`，不自动补充或改变 env。`chat --list` 同样使用默认连接。

### 状态与断开

```bash
lanbus connect --status
lanbus disconnect
```

状态命令显示默认 Hub、可达性、版本、在线 env 和是否保存 token，不显示 token 本身。`disconnect` 删除默认连接及其已保存 token；支持指定 Hub 时仅删除该档案，不影响其他连接。

## 配置模型

新增模块 `src/lanbus/connections.py`，集中处理地址、档案、token 和默认连接，避免在多个 CLI 命令中复制路径和优先级逻辑。

默认文件：`~/.lanbus/connections.json`；测试可通过 `LANBUS_CONNECTIONS_FILE` 指定隔离路径。

示例结构：

```json
{
  "version": 1,
  "default": "http://192.168.1.236:8788",
  "connections": {
    "http://192.168.1.236:8788": {
      "token": "stored-secret",
      "created_at": "ISO-8601",
      "last_used_at": "ISO-8601"
    }
  }
}
```

写入时：

- 创建父目录，POSIX 权限为 `0700`；文件权限为 `0600`。
- 使用同目录临时文件、`os.replace` 原子替换，避免半写配置。
- 若现有文件权限过宽，POSIX 下修正为 `0600`。
- Windows 使用当前用户目录和原子替换；不声称实现 POSIX 位权限。
- JSON 损坏时保留原文件并给出明确错误，不自动清空。

该格式沿用项目现有本地 token 文件的安全级别，不宣称加密存储。文档提醒用户不要使用 shell 历史记录保存 token。

## 地址与认证行为

`normalize_hub` 支持：

- `192.168.1.236` → `http://192.168.1.236:8788`
- `192.168.1.236:8789` → `http://192.168.1.236:8789`
- `http://...`／`https://...` 保留协议并移除末尾斜杠
- IPv6 字面量使用合法 URL 方括号形式

拒绝包含路径、query、fragment 或缺失主机名的地址，避免把 `/mcp` 等端点误当 Hub 根地址。

认证失败返回“该 Hub 需要有效 agent token，请重新 connect”，不尝试默认密码或批量猜测 token。健康检查失败区分连接超时、非 200 和非 Hub 响应。

## 默认连接解析

新增统一解析函数，供 `chat`、`logs`、`interrupt`、`shot`、`ask`、`web` 和 `confirm` 逐步复用。第一轮至少覆盖 `chat`、`connect`、`disconnect`，其他命令保持现有行为，避免无关扩张。

优先级：

1. 显式命令行参数；
2. `LANBUS_HUB`／`LANBUS_TOKEN`；
3. 默认连接档案；
4. 既有本机默认 `http://127.0.0.1:8788` 和 `~/.lanbus/token.txt`。

显式 `--hub` 与默认档案 Hub 相同时可使用该档案 token；显式连接到不同 Hub 时不得误用默认 Hub token。

## 自动选择 Worker

新增 `select_online_envs(envs_response, limit=4)`：

- 仅选择 `online=true` 且 env 非空的 Worker；
- 按 Hub 返回顺序去重；
- 最多 4 个；
- 返回空或 1 个时 CLI 给出当前在线数量和操作提示；
- 不改变用户显式 env 顺序，不自动加入本机 Worker。

远程人工确认仍由 Hub 判断。若返回 `human_confirmation_required`，CLI 输出目标 env 和 `lanbus web` 授权提示，不自动调用 operator token 或绕过确认。

## 发现改善

将 8788 加入默认 TCP 扫描端口，确保标准 lanbus Hub 能被默认 `lanbus discover` 检出。保留现有自定义 `--ports` 合并行为。

## 错误与退出码

- 参数／用法错误：2。
- 连接、认证、配置或对话失败：1。
- 成功：0。
- 所有错误使用简洁中文，并保留下一步命令；不输出 token、完整鉴权头或配置原文。

## 测试策略

### 单元测试

- 地址规范化：IPv4、端口、协议、IPv6、非法路径。
- 配置读写、默认连接、按 Hub 隔离 token、原子替换、POSIX 权限、损坏 JSON。
- 参数优先级和不同 Hub token 防串用。
- 在线 env 去重、排序、数量限制和不足两个错误。
- 非交互输入缺少 token 时不提示等待。

### 集成测试

- 使用现有真实 Python／Go Hub 与两个 fake Worker：连接一次后，无 `--hub`、无 `--envs` 完成对话。
- 显式参数覆盖默认档案。
- 未授权、待人工确认和离线 Worker 的提示。
- 默认发现端口包含 8788。
- 全量 Python／Go 回归，确认公共协议未改变。

### 实机验证

在当前 macOS 对 `192.168.1.236:8788` 只做健康检查和经过用户有效 token 授权的连接／对话；没有 token 时明确记录未验证，不尝试猜测。Windows 实机不在本轮冒充验证。

## 文档

更新 README Quick Start、中文说明和连接故障排查，展示两条命令的默认流程以及显式参数覆盖方式。保留安全配置、operator 确认和本地 Worker 手册，不删除高级用法。
