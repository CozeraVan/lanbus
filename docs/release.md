# 发行流程（release）

## 版本号

单一版本源：`src/lanbus/__init__.py` 的 `__version__`。Go 侧经 ldflags 注入
`github.com/CozeraVan/lanbus/go/internal/version.Version`，与 Python 对齐。

## 自动流水线（.github/workflows/release.yml）

推送 `v*` 标签触发：

1. **cli**：ubuntu 交叉编译 `lanbus / lanbusd / lanbus-worker / lanbus-scan` × {linux-amd64, darwin-amd64, darwin-arm64, windows-amd64}，版本取自标签。
2. **gui**：windows-latest 构建 Wails GUI（`-tags production -H=windowsgui`）。
3. **wheel**：构建 Python wheel + twine check。
4. **release**：汇总产物 → `sha256sum * > SHA256SUMS.txt` → softprops/action-gh-release 发布并生成 release notes。

```bash
git tag v1.9.1 && git push origin v1.9.1   # 触发发行
```

## 本地发行构建

```bash
make release        # 交叉编译全平台 CLI/hub/worker/scan + Windows 便携包
make package        # 仅 Windows 便携 zip（dist/）
```

## 安装器

- **Windows**：`iscc /DMyAppVersion=<ver> packaging\windows\lanbus.iss`（需 Inno Setup）。产物 `dist/lanbus-<ver>-setup.exe`。
- **macOS**：`packaging/macos/build-pkg.sh <ver> arm64`（需 macOS + pkgbuild/productbuild）。
- **Linux**：`packaging/linux/build-deb.sh <ver>`（需 dpkg-deb）。

## 签名 / 公证（需用户证书，占位）

> 以下为发行机一次性配置，证书与凭据**不入库**。

- **Windows 代码签名**：`packaging/windows/sign.ps1`（signtool + 证书）。占位：`CERT_SUBJECT`、`CERT_STORE`、`TIMESTAMP_URL`。
- **macOS 签名 + 公证**：
  ```bash
  codesign --force --options runtime --sign "Developer ID Application: <TEAM_ID>" <bin>
  productsign --sign "Developer ID Installer: <TEAM_ID>" lanbus.pkg lanbus-signed.pkg
  xcrun notarytool submit lanbus-signed.pkg --apple-id <APPLE_ID> --team-id <TEAM_ID> --password <APP_SPECIFIC_PWD> --wait
  xcrun stapler staple lanbus-signed.pkg
  ```
- **更新器**：`packaging/windows/update.ps1`（SHA-256 校验 + 替换）。跨平台更新经 GitHub Releases 的 `SHA256SUMS.txt` 校验。

## 验收清单

- [ ] 全平台二进制 `--version` 一致
- [ ] `SHA256SUMS.txt` 校验通过
- [ ] Windows 安装器安装/卸载干净
- [ ] `lanbus daemon install` 三平台自启生效
- [ ] `lanbus healthcheck` 对运行中 hub 返回 ok