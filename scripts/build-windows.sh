#!/usr/bin/env bash
# 打 Windows 发布物：裸 exe（自升级用）+ NSIS 安装包（首装 / 覆盖安装用）。
#
# 用法:   ./scripts/build-windows.sh <版本号> [架构]
#   版本号: 例如 1.0.1
#   架构:   amd64 | arm64（默认取本机 GOARCH）
#
# 产物（build/bin/）:
#   gtasks-<ver>-windows-<x64|arm64>.exe            （自升级资产，原地替换）
#   gtasks-<ver>-windows-<x64|arm64>-installer.exe  （NSIS 首装包，带图标 / 快捷方式 / 卸载器）
#
# 依赖: Go 1.26+、go-winres（图标 + 版本资源）、makensis（NSIS，缺了只出裸 exe 并告警）。
# Wails v3 在 Windows 上通过 purego 加载 WebView2，不需要 cgo，
# 所以 amd64 / arm64 都能从 macOS 或 Linux 直接交叉编译。
# 运行时需要 WebView2 Runtime（Windows 10/11 一般已内置）。
#
# 这个脚本是 Windows 打包步骤的唯一事实来源，.github/workflows/build.yml 直接调它。
# 参考 svc 的 scripts/build-windows.sh：版本三处同步 + go-winres + NSIS 安装包。
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?需要版本号，例如 1.0.1}"
ARCH="${2:-$(go env GOARCH)}"
case "$ARCH" in
  amd64) ASSET_ARCH="x64" ;;
  arm64) ASSET_ARCH="arm64" ;;
  *) echo "不支持的架构: $ARCH（只支持 amd64|arm64）" >&2; exit 1 ;;
esac

# --- 版本号同步进 winres/winres.json（资源里的 VERSIONINFO / manifest）。
# VIProductVersion 只认四段数字，预发布后缀先剥掉（v1.0.1-rc1 -> 1.0.1.0）。
BASE_VER="${VERSION%%[-+]*}"
WIN_VER=$(echo "$BASE_VER" | awk -F. '{printf "%s.%s.%s.0", $1, $2, $3}')
jq --arg v "$WIN_VER" --arg full "$BASE_VER" \
  '.RT_VERSION["#1"]["0000"].fixed.file_version = $v
   | .RT_VERSION["#1"]["0000"].fixed.product_version = $v
   | .RT_VERSION["#1"]["0000"].info["0409"].FileVersion = $full
   | .RT_VERSION["#1"]["0000"].info["0409"].ProductVersion = $full
   | .RT_MANIFEST["#1"]["0409"].identity.version = $v' \
  winres/winres.json > winres/winres.json.tmp && mv winres/winres.json.tmp winres/winres.json
# 本地构建不留脏工作区：脚本改过的 winres.json 退出时还原（只还原这一个文件）。
trap '[ -n "$VERSION" ] && git checkout winres/winres.json 2>/dev/null' EXIT
echo "版本号 $VERSION 已同步进 winres/winres.json"

# --- go-winres 生成 resource.syso（图标 + VERSIONINFO + manifest），链接器自动嵌入。
# 先清旧的，避免链接器吃到上一次构建的资源。
rm -f resource.syso
go-winres make --arch "$ARCH" --out resource --no-suffix
mv -f resource resource.syso
echo "resource.syso 已生成"

mkdir -p build/bin
ASSET_NAME="gtasks-${VERSION}-windows-${ASSET_ARCH}.exe"

# -tags production：Wails 发布标签，剔掉 dev server 代理 / devtools 等开发期代码路径。
# -H=windowsgui：GUI 子系统，双击 / 安装后运行不再挂一个 cmd 黑窗（关掉黑窗等于
# 关掉应用的根因就是默认的控制台子系统）。
GOOS=windows GOARCH="$ARCH" CGO_ENABLED=0 \
  go build -tags production -trimpath \
  -ldflags "-H=windowsgui -X main.version=${VERSION}" \
  -o "build/bin/${ASSET_NAME}" .

# --- NSIS 安装包：installer 只打包裸 exe + 图标，装完注册卸载器 / 快捷方式。
# makensis 不在 PATH 上就只发裸 exe（自升级资产），不让本地缺工具的人构建失败；
# CI 的 windows-latest 镜像自带 NSIS，build.yml 会把它加进 PATH。
INSTALLER_NAME="gtasks-${VERSION}-windows-${ASSET_ARCH}-installer.exe"
if command -v makensis >/dev/null 2>&1; then
  # BIN_PATH 用相对「启动 cwd」（仓库根）的路径：NSIS 的 File 按启动 cwd 解析，
  # 而 Git bash 会把 /d/... 形式的绝对参数转换给 Windows 原生 exe，转完反而找不到。
  makensis -V2 \
    -DAPP_VERSION="$BASE_VER" \
    -DBIN_PATH="build/bin/${ASSET_NAME}" \
    -DOUT_NAME="$INSTALLER_NAME" \
    build/windows/installer/project.nsi
else
  echo "warning: makensis 不在 PATH，跳过安装包（只出裸 exe）" >&2
fi

echo
echo "已产出:"
echo "  build/bin/${ASSET_NAME}"
[ -f "build/bin/${INSTALLER_NAME}" ] && echo "  build/bin/${INSTALLER_NAME}"
exit 0
