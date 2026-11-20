#!/bin/zsh
# 由 build/icon.svg 生成 macOS 应用图标（GTasks.icns）与内嵌用的 app-icon.png。
# 依赖：Google Chrome（无头截图，保证 SVG 的滤镜/渐变被真实渲染）+ sips + iconutil。
set -euo pipefail
cd "$(dirname "$0")"

CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
MASTER=/tmp/gtasks-icon-1024.png
ICONSET=macos/GTasks.iconset
OUT=macos/GTasks.icns

[[ -x "$CHROME" ]] || { echo "找不到 Chrome: $CHROME" >&2; exit 1; }

"$CHROME" --headless --disable-gpu --no-sandbox --hide-scrollbars \
  --force-device-scale-factor=1 --default-background-color=00000000 \
  --window-size=1024,1024 --screenshot="$MASTER" "file://$PWD/icon.svg" >/dev/null 2>&1

python3 - "$MASTER" <<'PY'
import struct, sys, zlib
path = sys.argv[1]
data = open(path, 'rb').read()
pos, idat = 8, b''
while pos < len(data):
    ln = struct.unpack('>I', data[pos:pos+4])[0]
    typ = data[pos+4:pos+8]
    if typ == b'IDAT':
        idat += data[pos+8:pos+8+ln]
    pos += 12 + ln
w = h = ct = None
pos = 8
while pos < len(data):
    ln = struct.unpack('>I', data[pos:pos+4])[0]
    typ = data[pos+4:pos+8]
    if typ == b'IHDR':
        w, h, depth, ct = struct.unpack('>IIBB', data[pos+8:pos+18])
    pos += 12 + ln
raw = zlib.decompress(idat)
if (w, h, depth, ct) != (1024, 1024, 8, 6) or len(raw) != 1024 * (1024 * 4 + 1):
    raise SystemExit('截图不是预期的 1024x1024 RGBA 图: %s %s' % ((w, h, depth, ct), len(raw)))
print('master ok')
PY

rm -rf "$ICONSET"; mkdir -p "$ICONSET"
for spec in 16 32 64 128 256 512 1024; do
  case $spec in
    16)  name=icon_16x16.png ;;
    32)  name=icon_16x16@2x.png ;;
    64)  name=icon_32x32@2x.png ;;
    128) name=icon_128x128.png ;;
    256) name=icon_128x128@2x.png ;;
    512) name=icon_256x256@2x.png ;;
    1024) name=icon_512x512@2x.png ;;
  esac
  cp "$MASTER" "$ICONSET/$name"
  sips -z $spec $spec "$ICONSET/$name" >/dev/null
done
# 32/256/512 的单倍图直接复用对应像素尺寸的副本。
cp "$ICONSET/icon_16x16@2x.png" "$ICONSET/icon_32x32.png"
cp "$ICONSET/icon_128x128@2x.png" "$ICONSET/icon_256x256.png"
cp "$ICONSET/icon_256x256@2x.png" "$ICONSET/icon_512x512.png"

iconutil -c icns "$ICONSET" -o "$OUT"
cp "$ICONSET/icon_512x512.png" app-icon.png
rm -rf "$ICONSET"
echo "已生成 $OUT 与 app-icon.png"
