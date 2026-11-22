#!/usr/bin/env bash
# 打 macOS 发布物：GTasks.app + DMG。
#
# 用法:   ./scripts/build-macos.sh <版本号> [架构]
#   版本号: 例如 1.0.0
#   架构:   amd64 | arm64（默认取本机 GOARCH）
#
# 产物（build/bin/）:
#   gtasks-<ver>-macos-<x64|arm64>.dmg   首装安装包
#   gtasks-<ver>-macos-<x64|arm64>.bin   自升级裸二进制（internal/update 原地替换用）
#
# 依赖: Go 1.26+、Xcode Command Line Tools（cgo）、hdiutil（macOS 自带）。
# 只能在 macOS 上跑；另一个架构可以交叉编译（GOARCH 交给 Xcode 的 clang -arch）。
#
# 这个脚本是 macOS 打包步骤的唯一事实来源，.github/workflows/build.yml 直接调它，
# 保证 CI 与本机产出一致。bundle 的组装（Info.plist / icns / 版本号）复用
# build/macos-bundle.sh。
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?需要版本号，例如 1.0.0}"
ARCH="${2:-$(go env GOARCH)}"
case "$ARCH" in
  amd64) ASSET_ARCH="x64" ;;
  arm64) ASSET_ARCH="arm64" ;;
  *) echo "不支持的架构: $ARCH（只支持 amd64|arm64）" >&2; exit 1 ;;
esac

if [ "$(go env GOOS)" != "darwin" ]; then
  echo "本脚本只能在 macOS 上运行（需要 hdiutil / iconutil）。" >&2
  exit 1
fi

mkdir -p build/bin
DMG_NAME="gtasks-${VERSION}-macos-${ASSET_ARCH}.dmg"
STAGING="$(mktemp -d)"
trap 'rm -rf "$STAGING"' EXIT

# --- 组装 .app（版本号同时写进二进制与 Info.plist）
# ld 的 "newer macOS version" 告警是 Wails cgo 的既有噪声，不影响产物。
GOARCH="$ARCH" CGO_ENABLED=1 bash build/macos-bundle.sh "$STAGING/GTasks.app" "$VERSION"

# --- MDM 关键处理：剥掉 Go 链接器打的 ad-hoc 签名，且不再重签。
# 严格 MDM 管控的 Mac 上，ad-hoc 签名会被 amfid 以 -423 拒绝，比「完全不签名」更糟；
# 带浏览器 quarantine 的 ad-hoc .app 在 Gatekeeper 下会显示「已损坏」，连右键 → 打开
# 的旁路都没有。bundle 与内层二进制都要剥：只剥 bundle 会留下签名不一致，Gatekeeper
# 照样拦（实机验证过）。arm64 内核会在 exec 时给内层二进制补一个 ad-hoc 签名，
# 所以剥掉是安全的。
codesign --remove-signature "$STAGING/GTasks.app" 2>/dev/null || true
codesign --remove-signature "$STAGING/GTasks.app/Contents/MacOS/gtasks" 2>/dev/null || true

# --- DMG 里放一份首启说明：未签名应用必须走「右键 → 打开」这条 Gatekeeper 旁路。
cat > "$STAGING/READ ME FIRST.txt" <<'TXT'
GTasks is not code-signed (see README: MDM / Gatekeeper).

If macOS says the app "is damaged" or "cannot be opened":
    Right-click GTasks.app  ->  Open  ->  click "Open"

Do NOT run `xattr -cr` on it: stripping the quarantine attribute routes the
launch through the provenance path, which SIGKILLs unsigned binaries
(exit 137) on MDM-managed Macs.

----------------------------------------------------------------------

GTasks 没有做代码签名（见 README 的 MDM / Gatekeeper 一节）。

如果 macOS 提示「已损坏」或「无法打开」：
    右键 GTasks.app  ->  打开  ->  再点「打开」

不要对它执行 `xattr -cr`：清掉隔离属性会走 provenance 校验路径，
在 MDM 管控的 Mac 上未签名程序会被直接 SIGKILL（退出码 137）。
TXT
ln -s /Applications "$STAGING/Applications"

# --- 自升级资产：裸二进制。internal/update 的 matchPlatformAsset 在 darwin 上
# 只认 .bin（.dmg 是安装包，原地替换不了），命名必须保持 gtasks-<ver>-macos-<arch>.bin。
BIN_NAME="gtasks-${VERSION}-macos-${ASSET_ARCH}.bin"
cp "$STAGING/GTasks.app/Contents/MacOS/gtasks" "build/bin/${BIN_NAME}"

hdiutil create -volname "GTasks" -srcfolder "$STAGING" -ov -format UDZO \
  -imagekey zlib-level=9 "build/bin/${DMG_NAME}" >/dev/null

# --- 给 DMG 打上 com.apple.quarantine，让拖进 /Applications 的 .app 继承它，
# 从而走 Gatekeeper（右键 → 打开）而不是把未签名程序直接 SIGKILL 的 provenance 路径。
# 浏览器下载的 DMG 本来就带这个属性，这里覆盖本机构建 / CI 上传后属性丢失的情况。
xattr -w com.apple.quarantine "0083;00000000;GTasks;|gtasks" "build/bin/${DMG_NAME}" 2>/dev/null || true

echo
echo "已产出:"
echo "  build/bin/${DMG_NAME}"
echo "  build/bin/${BIN_NAME}"
