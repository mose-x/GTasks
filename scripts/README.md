# 打包脚本 / Packaging scripts

| 脚本                               | 平台                     | 产物（`build/bin/`）                                   |
| ---------------------------------- | ------------------------ | ------------------------------------------------------ |
| `build-macos.sh`                   | macOS（amd64 / arm64）   | `.dmg`                                                 |
| `build-windows.sh`                 | Windows（amd64 / arm64） | `.exe`                                                 |
| `build-linux.sh`                   | Linux（amd64 / arm64）   | 裸二进制 + `.tar.gz`（+ `.deb` / `.rpm`，装了 fpm 时） |
| `run-first-installed-local-mac.sh` | macOS（安装后修复）      | 剥 ad-hoc 签名 + 重打 quarantine                       |
| `verify-lang-switch.py`            | 跨平台                   | 语言 / 主题切换的端到端回归（`make verify`）           |

三个 `build-*.sh` 都是 `./脚本 <版本号> [架构]`，架构默认取本机 `GOARCH`。
它们是各平台打包步骤的唯一事实来源：`.github/workflows/build.yml` 直接调它们，
所以 CI 与本地产出的产物完全一致。日常开发用仓库根的 `Makefile` 即可：

```bash
make package              # 当前平台的发布物
make dist                 # 三个平台 x amd64/arm64 全量（DMG 需要 macOS）
make package-linux VERSION=1.0.0 ARCH=arm64   # 等价于 scripts/build-linux.sh 1.0.0 arm64
make checksums            # 为 build/bin/ 里的产物生成 sha256sums.txt
```

---

## 前置条件 / Prerequisites

### 所有平台 / All platforms

- **Go 1.26+**（Wails v3.1.0 的要求；本机是 1.25 时 toolchain 会自动下载 1.26）
- 国内网络可加 `GOPROXY=https://goproxy.cn,direct`（`Makefile` 已默认带上）

### macOS

- Xcode Command Line Tools（Wails v3 的 darwin 后端走 cgo）
- `hdiutil`（系统自带）——生成 DMG
- 另一个架构可以交叉编译：`GOARCH=arm64` 由 Xcode 的 `clang -arch` 接管
- 应用图标已提交在 `build/macos/GTasks.icns`；只有改了 `build/icon.svg` 才需要
  `make icon`（依赖本机 Chrome）

### Windows

- 无构建依赖：Wails v3 通过 purego 加载 WebView2，`CGO_ENABLED=0` 即可，
  能从 macOS / Linux 直接交叉编译出 amd64 与 arm64
- 运行时需要 **WebView2 Runtime**（Windows 10/11 一般已内置）

### Linux

- 无构建依赖：走 purego 通道（`CGO_ENABLED=0 -tags purego`），
  amd64 / arm64 都能交叉编译，构建机不需要 gtk / webkit 的 dev 包
- 运行时库：`libgtk-3-0`、`libwebkit2gtk-4.1-0`（Arch：`gtk3 webkit2gtk-4.1`）
- `.deb` / `.rpm` 需要 `fpm`（`sudo gem install fpm`）；没有就只产出裸二进制与 `.tar.gz`

---

## 产物命名 / Artifact naming

```
gtasks-<版本>-<平台>-<架构>.<后缀>
```

- 平台：`macos` / `windows` / `linux`
- 架构：Go 的 `amd64` → 资产名里写 `x64`，`arm64` 原样保留
  （deb 元数据用 `amd64`/`arm64`，rpm 用 `x86_64`/`aarch64`）
- 版本号里带 `-rc1` / `-beta` 这类后缀时，deb/rpm 的元数据版本会把 `-` 换成 `~`，
  这样 `1.0.0~rc1` 在 dpkg 与 rpmvercmp 里都排在 `1.0.0` 之前，rc 用户有升级路径；
  文件名仍保留完整版本号

例：`gtasks-1.0.0-macos-arm64.dmg`、`gtasks-1.0.0-windows-x64.exe`、
`gtasks-1.0.0-linux-x64.tar.gz`。

版本号还会写进产物自身：二进制的 `main.version`（`gtasks --version` 可查）
与 macOS 的 `Info.plist`（`CFBundleShortVersionString` / `CFBundleVersion`）。

---

## 发布流程 / Releasing

1. `make check`（gofmt + vet + test）
2. 需要时 `make verify`（应用要在运行且已登录 Google）
3. 打标签并推送，CI 会构建三平台并自动建 Release：

```bash
git tag -a v1.0.0 -m "GTasks 1.0.0"
git push origin v1.0.0
```

`.github/workflows/build.yml` 在 `v*` 标签上跑 windows / macos / linux 三个 job
（各 amd64 + arm64），产物汇总后生成 `sha256sums.txt` 一起发布。
带 `-rc` / `-beta` / `-alpha` / `-pre` / `-dev` 后缀的标签会标成 pre-release，
不进 `releases/latest`。

---

## MDM / Gatekeeper（macOS 未签名应用）

本项目不做代码签名，也没有 Apple 开发者证书。要点：

- **不 ad-hoc 签名**。严格 MDM 管控的 Mac 上，ad-hoc（`codesign --sign -`）会被
  amfid 以 `-423` 拒绝，比完全不签名更糟。
- **剥掉 Go 链接器自动打的 ad-hoc 签名**，bundle 与内层二进制都要剥：只剥 bundle
  会留下签名不一致，Gatekeeper 照样拦。arm64 内核会在 exec 时补一个 ad-hoc 签名，
  所以剥掉是安全的。
- **保留 `com.apple.quarantine`**。带隔离属性的 .app 走 Gatekeeper，用户
  「右键 → 打开」就能过；`xattr -cr` 清掉隔离属性反而会走 provenance 校验路径，
  未签名程序被直接 SIGKILL（退出码 137）。DMG 也由 `build-macos.sh` 主动打上
  这个属性，覆盖本机构建 / CI 上传后属性丢失的情况。
- DMG 里放了一份 `READ ME FIRST.txt`，中英双语写明「右键 → 打开」与「不要 `xattr -cr`」。

如果装完还是打不开，跑一次：

```bash
./scripts/run-first-installed-local-mac.sh
```

它会剥签名、清 xattr、重新打一个干净的 quarantine，然后启动应用。
