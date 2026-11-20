#!/bin/zsh
# 组装 macOS 的 GTasks.app：编译主程序，套上 Info.plist 与应用图标。
#
# 用法: build/macos-bundle.sh [输出目录]      默认 /tmp/GTasks.app
#
# 注意: CFBundleIdentifier 固定为 gtasks —— WebKit 的 cookie / localStorage
# 按它隔离，改了就要重新登录 Google。
set -euo pipefail
cd "$(dirname "$0")/.."

OUT="${1:-/tmp/GTasks.app}"

GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" go build -o gtasks .

mkdir -p "$OUT/Contents/MacOS" "$OUT/Contents/Resources"
cp gtasks "$OUT/Contents/MacOS/gtasks"
cp build/macos/GTasks.icns "$OUT/Contents/Resources/GTasks.icns"

cat > "$OUT/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key><string>gtasks</string>
	<key>CFBundleIdentifier</key><string>gtasks</string>
	<key>CFBundleName</key><string>GTasks</string>
	<key>CFBundleDisplayName</key><string>GTasks</string>
	<key>CFBundleIconFile</key><string>GTasks</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>1.0</string>
	<key>CFBundleVersion</key><string>1</string>
	<key>LSMinimumSystemVersion</key><string>11.0</string>
	<key>NSHighResolutionCapable</key><true/>
	<key>NSPrincipalClass</key><string>NSApplication</string>
</dict>
</plist>
PLIST

echo "已生成 $OUT"
