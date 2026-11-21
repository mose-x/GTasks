#!/usr/bin/env bash
# 打 Linux 发布物：裸二进制 + .tar.gz，装了 fpm 时再产出 .deb / .rpm。
#
# 用法:   ./scripts/build-linux.sh <版本号> [架构]
#   版本号: 例如 1.0.0
#   架构:   amd64 | arm64（默认取本机 GOARCH）
#
# 产物（build/bin/）:
#   gtasks-<ver>-linux-<x64|arm64>          裸二进制
#   gtasks-<ver>-linux-<x64|arm64>.tar.gz   二进制 + LICENSE + .desktop + 图标
#   gtasks-<ver>-linux-<x64|arm64>.deb      需要 fpm（sudo gem install fpm）
#   gtasks-<ver>-linux-<x64|arm64>.rpm      需要 fpm
#
# 依赖: Go 1.26+。Wails v3 的 Linux 后端要 webkit2gtk-4.1，本仓库走 purego 通道
# （CGO_ENABLED=0 -tags purego），因此 amd64 / arm64 都能从任意平台交叉编译，
# 构建机不需要 gtk / webkit 的 dev 包；目标机只需要运行时库
# libgtk-3-0 与 libwebkit2gtk-4.1-0（Arch: gtk3 webkit2gtk-4.1）。
#
# 这个脚本是 Linux 打包步骤的唯一事实来源，.github/workflows/build.yml 直接调它。
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?需要版本号，例如 1.0.0}"
# 包管理器里的版本：apt/dnf 会把 "1.0.0-rc2" 排在正式版 "1.0.0" 之上，rc 用户就没有
# 升级路径了。波浪号在 dpkg 与 rpmvercmp（rpm >= 4.10）里都排在一切之前，所以
# "1.0.0~rc2" 能干净地升到 "1.0.0"。正式版本号不含连字符，原样透传。
PKG_VER="$(printf '%s' "$VERSION" | sed 's/-/~/')"
ARCH="${2:-$(go env GOARCH)}"
case "$ARCH" in
  amd64) ASSET_ARCH="x64";  DEB_ARCH="amd64"; RPM_ARCH="x86_64" ;;
  arm64) ASSET_ARCH="arm64"; DEB_ARCH="arm64"; RPM_ARCH="aarch64" ;;
  *) echo "不支持的架构: $ARCH（只支持 amd64|arm64）" >&2; exit 1 ;;
esac

mkdir -p build/bin
ASSET_NAME="gtasks-${VERSION}-linux-${ASSET_ARCH}"
TAR_NAME="${ASSET_NAME}.tar.gz"

GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
  go build -tags purego -trimpath -ldflags "-X main.version=${VERSION}" \
  -o "build/bin/${ASSET_NAME}" .

# --- .desktop 与安装树。图标直接用 build/app-icon.png（512x512），
# 装进 hicolor/512x512 即可，不需要任何缩放工具。
STAGING="$(mktemp -d)"
trap 'rm -rf "$STAGING"' EXIT
mkdir -p "$STAGING/usr/bin" \
         "$STAGING/usr/share/applications" \
         "$STAGING/usr/share/icons/hicolor/512x512/apps" \
         "$STAGING/usr/share/pixmaps" \
         "$STAGING/usr/share/doc/gtasks"

cp "build/bin/${ASSET_NAME}" "$STAGING/usr/bin/gtasks"
cp build/app-icon.png "$STAGING/usr/share/icons/hicolor/512x512/apps/gtasks.png"
cp build/app-icon.png "$STAGING/usr/share/pixmaps/gtasks.png"
cp LICENSE "$STAGING/usr/share/doc/gtasks/LICENSE"

cat > "$STAGING/usr/share/applications/gtasks.desktop" <<'DESKTOP'
[Desktop Entry]
Type=Application
Name=GTasks
Comment=Third-party Google Tasks desktop client
Exec=/usr/bin/gtasks
Icon=gtasks
Terminal=false
Categories=Office;
StartupNotify=true
DESKTOP

# --- tar.gz：给不用 deb/rpm 的发行版，解到 / 即完成安装（需要 root）。
tar -czf "build/bin/${TAR_NAME}" -C "$STAGING" usr

# --- .deb / .rpm：有 fpm 就打，没有就只留裸二进制与 tar.gz。
if command -v fpm >/dev/null 2>&1; then
  DEB_NAME="gtasks-${VERSION}-linux-${ASSET_ARCH}.deb"
  RPM_NAME="gtasks-${VERSION}-linux-${ASSET_ARCH}.rpm"
  fpm -s dir -t deb -n gtasks -v "$PKG_VER" -a "$DEB_ARCH" \
    --license MIT --vendor mose-x --url "https://github.com/mose-x/GTasks" \
    --description "GTasks - third-party Google Tasks desktop client (Wails v3)" \
    --depends libgtk-3-0 --depends libwebkit2gtk-4.1-0 \
    -p "build/bin/${DEB_NAME}" -C "$STAGING" usr
  fpm -s dir -t rpm -n gtasks -v "$PKG_VER" -a "$RPM_ARCH" \
    --license MIT --vendor mose-x --url "https://github.com/mose-x/GTasks" \
    --description "GTasks - third-party Google Tasks desktop client (Wails v3)" \
    --depends gtk3 --depends webkit2gtk4.1 \
    -p "build/bin/${RPM_NAME}" -C "$STAGING" usr
else
  echo "提示: 未找到 fpm，跳过 .deb / .rpm（装法: sudo gem install fpm）" >&2
fi

echo
echo "已产出:"
echo "  build/bin/${ASSET_NAME}"
echo "  build/bin/${TAR_NAME}"
command -v fpm >/dev/null 2>&1 && {
  echo "  build/bin/gtasks-${VERSION}-linux-${ASSET_ARCH}.deb"
  echo "  build/bin/gtasks-${VERSION}-linux-${ASSET_ARCH}.rpm"
}
exit 0
