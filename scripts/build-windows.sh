#!/usr/bin/env bash
# 打 Windows 发布物：裸 exe。
#
# 用法:   ./scripts/build-windows.sh <版本号> [架构]
#   版本号: 例如 1.0.0
#   架构:   amd64 | arm64（默认取本机 GOARCH）
#
# 产物（build/bin/）:
#   gtasks-<ver>-windows-<x64|arm64>.exe
#
# 依赖: Go 1.26+。Wails v3 在 Windows 上通过 purego 加载 WebView2，不需要 cgo，
# 所以 amd64 / arm64 都能从 macOS 或 Linux 直接交叉编译。
# 运行时需要 WebView2 Runtime（Windows 10/11 一般已内置）。
#
# 这个脚本是 Windows 打包步骤的唯一事实来源，.github/workflows/build.yml 直接调它。
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?需要版本号，例如 1.0.0}"
ARCH="${2:-$(go env GOARCH)}"
case "$ARCH" in
  amd64) ASSET_ARCH="x64" ;;
  arm64) ASSET_ARCH="arm64" ;;
  *) echo "不支持的架构: $ARCH（只支持 amd64|arm64）" >&2; exit 1 ;;
esac

mkdir -p build/bin
ASSET_NAME="gtasks-${VERSION}-windows-${ASSET_ARCH}.exe"

# -tags production：Wails 发布标签，剔掉 dev server 代理 / devtools 等开发期代码路径。
GOOS=windows GOARCH="$ARCH" CGO_ENABLED=0 \
  go build -tags production -trimpath -ldflags "-X main.version=${VERSION}" -o "build/bin/${ASSET_NAME}" .

echo
echo "已产出:"
echo "  build/bin/${ASSET_NAME}"
