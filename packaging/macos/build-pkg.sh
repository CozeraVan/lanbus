#!/bin/bash
# Build a macOS .pkg from the cross-built darwin binaries. Run on macOS.
# Usage: packaging/macos/build-pkg.sh [VERSION] [arm64|amd64]
# Signing/notarization (codesign/productsign + notarytool) is a separate step
# requiring an Apple Developer ID certificate; see docs/release.md.
set -euo pipefail
VERSION="${1:-1.9.0}"
ARCH="${2:-arm64}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
STAGE="$(mktemp -d)"
PAYLOAD="$STAGE/payload"
mkdir -p "$PAYLOAD/usr/local/bin" "$PAYLOAD/Library/LaunchDaemons"
for b in lanbus lanbusd lanbus-worker lanbus-scan; do
  src="$ROOT/bin/${b}-darwin-${ARCH}"
  [ -f "$src" ] || { echo "missing $src; run make go-hub-all go-worker-all go-scan-all go-cli-all" >&2; exit 1; }
  cp "$src" "$PAYLOAD/usr/local/bin/${b}"
  chmod +x "$PAYLOAD/usr/local/bin/${b}"
done
cp "$ROOT/packaging/macos/com.lanbus.hub.plist" "$PAYLOAD/Library/LaunchDaemons/com.lanbus.hub.plist"
mkdir -p "$ROOT/dist"
pkgbuild --root "$PAYLOAD" --identifier com.lanbus.pkg --version "$VERSION" \
  --install-location / "$STAGE/lanbus-component.pkg"
productbuild --package "$STAGE/lanbus-component.pkg" \
  "$ROOT/dist/lanbus-${VERSION}-macos-${ARCH}.pkg"
rm -rf "$STAGE"
echo "built dist/lanbus-${VERSION}-macos-${ARCH}.pkg"