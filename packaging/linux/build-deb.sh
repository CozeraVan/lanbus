#!/bin/bash
# Build a Debian .deb from the cross-built linux binaries. Run on Linux (dpkg-deb).
# Usage: packaging/linux/build-deb.sh [VERSION]
set -euo pipefail
VERSION="${1:-1.9.0}"
ARCH=amd64
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
STAGE="$(mktemp -d)"
PKG="$STAGE/lanbus_${VERSION}_${ARCH}"
mkdir -p "$PKG/DEBIAN" "$PKG/usr/bin" "$PKG/lib/systemd/user"
for b in lanbus lanbusd lanbus-worker lanbus-scan; do
  src="$ROOT/bin/${b}-linux-${ARCH}"
  [ -f "$src" ] || { echo "missing $src; run make go-hub-all go-worker-all go-scan-all go-cli-all" >&2; exit 1; }
  cp "$src" "$PKG/usr/bin/${b}"
  chmod +x "$PKG/usr/bin/${b}"
done
cp "$ROOT/packaging/linux/lanbus-hub.service" "$PKG/lib/systemd/user/lanbus-hub.service"
cat > "$PKG/DEBIAN/control" <<EOF
Package: lanbus
Version: ${VERSION}
Section: utils
Priority: optional
Architecture: ${ARCH}
Maintainer: lanbus contributors <noreply@github.com>
Description: LAN multi-agent bus (Go hub, worker, scanner, CLI)
 Zero-config direct agent peering with target-side human approval.
EOF
mkdir -p "$ROOT/dist"
dpkg-deb --build "$PKG" "$ROOT/dist/lanbus_${VERSION}_${ARCH}.deb"
rm -rf "$STAGE"
echo "built dist/lanbus_${VERSION}_${ARCH}.deb"