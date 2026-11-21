#!/usr/bin/env bash
#
# run-first-installed-local-mac.sh
# 本地构建的 macOS DMG 首次启动修复脚本
# First-launch fix script for locally-built macOS DMG installs
#
# 什么时候用这个脚本 / When to use this script
# ==========================================================
#
# 当你从本地构建的 DMG 安装了 GTasks 后，macOS 可能因为签名与隔离属性的问题
# 阻止应用启动（表现为「已损坏，无法打开」，或进程被 SIGKILL、退出码 137）。
# 这在严格 MDM 管控的 Mac 上尤为常见。
#
# After installing GTasks from a locally-built DMG, macOS may block the app from
# launching because of signature + quarantine attributes. You'll see "damaged
# and can't be opened", or the process gets SIGKILL'd (exit 137). This is
# especially common on strict MDM-managed Macs.
#
# 这个脚本做四件事 / This script does four things:
#   1. 去掉 .app bundle 的 ad-hoc 签名 / Strip the ad-hoc signature from the bundle
#   2. 去掉内层二进制的 ad-hoc 签名 / Strip the inner binary's ad-hoc signature
#   3. 清除所有 xattr（含浏览器下载的 quarantine）/ Clear all xattrs
#   4. 重新打一个干净的 quarantine（走 Gatekeeper 旁路）/ Re-stamp a clean quarantine
#
# 用法 / Usage:
#   ./scripts/run-first-installed-local-mac.sh
#
# 前提 / Prerequisites:
#   - GTasks 已从 DMG 拖到 /Applications / GTasks has been dragged to /Applications
#   - 在 macOS 上运行 / Run on macOS
#
# 注意 / Note:
#   - 只需首次运行一次，之后直接双击 .app 即可。
#     Only needed once on first install; afterwards double-click the .app.
#   - 从 GitHub Release 用浏览器下载的 DMG 通常不需要此脚本
#     （浏览器会自动打上正确的 quarantine）。
#     A DMG downloaded from GitHub Releases via a browser usually does not need
#     this script (the browser stamps the correct quarantine itself).
#   - 需要 codesign 与 xattr（macOS 自带）。/ Requires codesign and xattr.
#

set -euo pipefail

APP="${1:-/Applications/GTasks.app}"

if [ ! -d "$APP" ]; then
    echo "错误: $APP 不存在。请先从 DMG 拖到 /Applications。"
    echo "Error: $APP not found. Drag the app to /Applications from the DMG first."
    exit 1
fi

echo "1/4 去掉 bundle 签名 / Stripping bundle signature..."
codesign --remove-signature "$APP" 2>/dev/null || true

echo "2/4 去掉内层二进制签名 / Stripping inner binary signature..."
codesign --remove-signature "$APP/Contents/MacOS/gtasks" 2>/dev/null || true

echo "3/4 清除所有 xattr / Clearing all xattrs..."
xattr -cr "$APP"

echo "4/4 重新打 quarantine / Re-stamping quarantine..."
xattr -w com.apple.quarantine "0083;00000000;GTasks;|gtasks" "$APP"

echo ""
echo "完成！正在启动应用... / Done! Launching app..."
open "$APP"
