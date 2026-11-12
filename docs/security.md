# 安全模型

## 认证与令牌

- hub 三类令牌：**agent token**（任务派发）、**worker token**（worker 协议）、**operator token**（人工审批）。
- 常量时间比较（`crypto/subtle`），令牌经 `Authorization: Bearer` / `X-API-Key` / `?token=` 传递。
- **强 token 默认**：`lanbusd` 未提供 token 时自动生成 24 字节随机令牌并持久化到 `<data-dir>/token.txt`（0600）。仅 `--allow-dev-token` 才允许不安全的 `devtoken`（开发用）。
- operator token 缺省自动生成并写入 `operator-token.txt`（0600）。

## 传输加密（TLS）

```bash
lanbusd --tls-cert cert.pem --tls-key key.pem
```
提供证书/密钥后 hub 以 HTTPS 监听。LAN 内可用自签证书；worker/CLI 侧需信任该证书。未配置时为 HTTP（仅限可信 LAN）。

## 主机校验

非公开端点要求 `Host` 为 localhost 或 IP（`validHost`），抵御 DNS rebinding。公开端点仅 `/health`、`/`、`/ui`。

## 直连 peering（零配置）

- Ed25519 身份（peer_id = sha256(raw pub)[:32]），X25519 ECDH → HKDF-SHA256 → AES-256-GCM 加密请求/回复。
- **目标侧人工审批**：配对/请求须经 `lanbus inbox/allow` 或审批页（loopback-only `/peer/ui`，密钥经 URL fragment，不落日志）显式批准；可 revoke。
- 审批/管理端点仅 loopback + `X-Lanbus-Approval` 密钥。

## 可观测性（pprof）

```bash
lanbusd --pprof 127.0.0.1:6060
```
pprof 独立监听，**强制 loopback**（非环回地址拒绝启用），不暴露在 hub 公网端口。

## 备份

`lanbus backup` 仅归档已知状态文件（bus.db/WAL、token、local.json），zip-slip 安全恢复；token 文件 0600。备份含敏感令牌，须妥善保管。

## 已知边界

- HTTP 默认仅适用于可信 LAN；跨不可信网络务必启用 TLS。
- 自签证书需客户端显式信任。
- 签名/公证证书为用户侧机密，不入库（见 `docs/release.md`）。