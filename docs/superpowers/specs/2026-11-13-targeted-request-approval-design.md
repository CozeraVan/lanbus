# 定向请求通知与逐次人工审批设计

日期：2026-11-13

## 背景与问题

当前直连协议仅在首次配对时产生待审批记录。请求写入目标 peer 的 HTTP inbox，并在 worker 终端打印一行日志；正在运行的 AI agent 页面不会收到任何提示。持续配对后的对话和项目任务会直接执行，不再展示审批。多个 agent 或同一 agent 的多个窗口共用相似名称时，操作者也无法从 `go-agent` 之类的标识判断实际目标。

本设计解决两个独立问题：

1. 请求必须到达一个可辨识的目标实例，并让目标机器操作者及时看到。
2. 新版节点之间，持续配对只复用身份和密钥，不再隐式授权后续每一次执行。

## 目标

- 新请求到达时，在目标机器显示明确的本机审批界面和系统通知。
- 页面同时展示来源、目标实例、agent 类型、项目目录、请求类型和请求正文。
- 同一机器多个 agent、同一 agent 多个实例均可区分；发起端遇到重名时必须明确选择。
- 新版节点之间，每次对话或项目任务默认都需要目标端批准一次或拒绝。
- 保留 v1.9.1/v1.9.2 旧协议互操作性，并明确标识旧版的持续授权语义。
- 未批准前不得调用 agent、读取项目或执行命令；远程来源不能伪造本地审批。

## 非目标

- 不通过模拟键鼠向任意 AI 窗口注入审批文字。
- 不在第一阶段为 Claude、Kimi、Codex 等分别实现私有页面插件。
- 不改变现有加密身份、grant 密钥派生和 cwd 边界算法。
- 不自动批准任何请求，不把 agent token 变成审批凭证。

## 方案选择

采用“目标实例定向审批助手”。 peer 收到请求后，通过本机通知适配器打开该实例的审批页，并发送操作系统通知。审批页是唯一执行闸门，不依赖具体 AI 产品的前端接口。

不采用 GUI 键鼠注入：多窗口环境下无法可靠证明文字进入了正确会话，还可能把控制请求误当作用户消息执行。私有 agent 插件可作为后续增强，不是跨 agent 基础能力的前提。

## 实例身份与发现

`PeerServer`/`ServerConfig` 增加可选 `DisplayName`、`InstanceLabel` 和已有 `CWD` 的公开元数据。默认显示名称仍为 `Name`，实例标签为空；因此旧启动命令保持有效。

公开 `/peer/health` 在保留现有字段的基础上增加：

```json
{
  "display_name": "设计窗口 2",
  "instance_label": "lanbus-frontend",
  "cwd_display": "C:/work/lanbus",
  "approval_features": ["request_v2"],
  "request_policy": "each"
}
```

`cwd_display` 仅用于目标选择；项目请求仍由目标端审批时确认 canonical cwd。发现报告和 CLI 目标候选展示这些字段。若同一目标名对应多个 peer，发起端输出编号、hostname、agent、display_name、instance_label、cwd_display 和 endpoint；非交互模式下必须重新使用唯一名称或显式 `IP:PORT`，不得默认选择第一个。

worker 增加 `--display-name` 和 `--instance-label`，Python 与 Go 使用相同名称和语义。

## 本机通知与审批助手

新增独立的 `notifier` 边界：

- Python：`peer` 依赖接收 pending/approval 事件后调用的通知回调。
- Go：`peer.Notifier` 接口，worker 注入平台实现；peer 包不直接操作桌面。
- macOS：使用系统通知；点击或自动打开的 URL 仅指向 `http://127.0.0.1:<port>/peer/ui`。
- Windows：优先使用现有 Wails/tray 所在进程可用的通知能力；无托盘时使用系统通知或安全的本机浏览器打开作为回退。
- 通知失败不丢弃请求，也不影响 inbox；worker 日志记录脱敏后的失败原因。
- 默认在新待审批请求产生时自动打开一次专属审批页；同一 request_id 不重复弹出。提供 `--no-open-approval` 仅关闭自动打开，不关闭审批要求。

审批 URL 的 secret 仍只通过本机 URL fragment 注入，不进入 HTTP 请求、日志或远端响应。审批接口继续要求 loopback 和 `X-Lanbus-Approval`。

## 新版逐次审批协议

### 能力协商

目标健康元数据声明 `approval_features=["request_v2"]` 和 `request_policy="each"`。新发起端识别后使用 request v2。未声明这些字段的目标走旧协议。

持续 pair grant 在新版中只证明来源身份、保存密钥并授权发起审批请求，不再直接执行。目标始终可以撤销 pair；撤销后后续请求重新进入 pairing。

### 请求流程

1. 发起端使用已有 grant 加密请求，新增 `request_version=2`；正文仍受现有 64 KiB 限制。
2. 目标验证 grant、scope、cwd、签名和防重放字段后，只创建 `execution_pending`，不调用 executor。
3. 目标返回 `202 {status:"approval_required", request_id}`；新发起端轮询现有 pair status 通道，等待本地决定。
4. 本机审批页展示来源与目标实例，提供“批准一次”和“拒绝”。项目任务不能用 chat 审批提升权限。
5. 批准后状态查询返回 `approved`（不返回新 grant）。发起端使用同一 grant、request_id 和完全相同的密文重发 direct request；目标校验它与已批准请求一致后才调用 executor，并按现有 direct request 通道返回结果。
6. 执行中的相同重发返回 `202 execution_running`；完成后的相同重发返回缓存结果，不再次执行。request_id、密文摘要、grant_id、审批和执行结果绑定，任何字段变化都视为新请求并要求新的审批。
7. 超时、目标重启、撤销和重复点击均返回确定状态；超时不执行，新的正文或新的 request_id 必须重新审批。

实现时复用现有 pairing pending/completed 的状态机模式，但 pairing 与 execution pending 使用不同类型和存储键，避免同一 request_id 混淆。执行结果只对持有对应 grant、匹配密文摘要和 request_id 的来源可见。

### 默认策略与配置

新版默认 `each`。可以提供显式 `--request-policy=pair` 恢复“持续配对后免逐次审批”，健康元数据必须如实公布；不提供隐藏的全局绕过开关。旧目标没有 request v2 能力时维持旧行为，新审批页对旧来源显示“旧版兼容模式：持续配对可能允许后续请求直接执行”。

## 审批页面

页面首屏固定显示两张身份卡：

- 来源：hostname、来源 agent/peer_id、请求类型、发起时间。
- 目标：display_name、Name、agent、instance_label、项目目录、hostname、端口。

标题使用“<来源> 请求指挥 <目标实例>，是否批准？”。正文保留纯文本转义。按钮为“批准一次”和“拒绝”；持续配对管理位于独立区域，不与本次执行批准混为一谈。

页面轮询 inbox 时在出现新 pending 时更新标题并可选播放本机提示音；不依赖页面已经打开，因为 notifier 会在请求到达时主动唤起。

## 兼容性与迁移

- 旧发起端 → 新目标：旧 pairing 和 direct request 格式继续可用；按旧 grant 策略执行，并在审批页标明兼容模式。
- 新发起端 → 旧目标：未发现 request v2 能力时使用旧流程；CLI 在持续配对复用时提示旧目标可能不逐次确认。
- 新发起端 → 新目标：默认每次执行审批。
- 不修改 v1.9.1/v1.9.2 的历史 Release；功能进入下一版本。

## 测试与验收

### 单元与协议测试

- Python/Go 健康元数据兼容旧字段，实例信息正确转义。
- 重名候选不会自动选错；显式 endpoint 行为不变。
- request v2 未批准不调用 executor；批准一次仅调用一次；拒绝、超时、撤销和重放均不执行。
- chat 不能提升为 project；cwd 逃逸仍被拒绝。
- notifier 失败不影响 inbox 和审批；相同 request 不重复弹窗。
- Python ↔ Go 的 request v2 测试向量一致。

### 黑盒矩阵

- 新版 Python → 新版 Python、新版 Python → 新版 Go、反向组合：首次 pairing、第二次对话仍需批准、拒绝不执行。
- v1.9.1/v1.9.2 与新版双向连接：旧协议仍可配对和对话，并显示兼容提示。
- 同机两个相同 agent、不同实例标签：发起端能区分，目标通知打开正确实例的审批页。

### 真实桌面验收

- Windows 两个 Claude 工作窗口分别绑定不同实例标签，请求只打开目标实例审批页。
- macOS 与 Windows 互发对话和项目请求，逐次批准、拒绝和撤销均符合预期。
- 不宣称审批消息已嵌入 AI 私有页面；验收对象是 Lanbus 的本机定向审批窗口。

## 已知边界

- 自动打开窗口受操作系统通知权限、专注模式和桌面会话限制；失败时请求仍在 inbox，不能因此自动执行。
- 一个交互式 AI 窗口要获得稳定实例标签，需要用户启动对应 peer 时设置标签；Lanbus 不猜测窗口标题或会话 ID。
- 旧版本的持续配对语义无法在不升级目标端的情况下改变。
