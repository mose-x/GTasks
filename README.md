# GTasks — Google Tasks 桌面客户端（Wails v3 + Go）

Google Tasks 的第三方桌面客户端。目标：**界面与网页版一致**，登录走 **Google 原生页面**，
数据面覆盖官方 API 拿不到的能力（星标 / 截止时分 / 重复）。

当前阶段包含两部分：

1. **主界面** — 按 `tasks.google.com` 还原的浅色主题界面（侧栏 / 任务列表 / 详情面板），
   带演示数据模式，可以在 rpcid 尚未发现时先校对还原度。
2. **逆向工作台** — 独立窗口，用来登录、抓包、把抓到的 rpcid 固化成可复用的「操作」。

> ⚠️ 本项目通过非公开接口访问 Google 服务，违反 [Google 服务条款](https://policies.google.com/terms)。
> 仅供个人学习使用，**账号风险由使用者自行承担**。

## 为什么是这个架构

官方 Tasks API v1 覆盖不了网页版的全部能力。以 `google.golang.org/api@v0.316.0` 的
`tasks/v1/tasks-gen.go` 为准，`Task` 资源里**完全没有** `starred` / `recurrence` /
`reminder` / `dueTime` 字段，方法也只有 `clear` `delete` `get` `insert` `list` `move`
`patch` `update` 八个。缺的是：

- ⭐ 星标 — 官方 API 无法读写
- 🕐 截止时间的**时分** — `due` 字段只保留日期
- 🔁 重复规则、🔔 提醒 — 官方 API 不存在

而内部 `batchexecute` 接口有两个硬约束：OAuth Bearer token **不认**，只认网页会话
cookie（`__Secure-1PSID` 等，且是 httpOnly）；请求还需要 `at`（`SNlM0e`）和
`bl`（build label）。

常规做法是"把 cookie 抓到 Go 里自己发请求"，跨平台且脆弱。本项目换成：

```
┌────────────────┐  ┌────────────────┐   ┌──────────────────────────────┐
│ 主窗口          │  │ 工作台窗口      │   │ 会话窗口（可自动隐藏）        │
│ /              │  │ /workbench.html│   │ https://tasks.google.com/    │
│ Tasks 界面      │  │ 抓包 / 固化     │   │ Google 原生登录页 + 网页版    │
│                │  │                │   │ ← bootstrap.js 注入          │
└───────┬────────┘  └───────┬────────┘   └──────────┬────────▲──────────┘
        │ HTTP (asset server)                       │ ExecJS │ external
        ▼                                           ▼        │ postMessage
┌────────────────────────────────────────────────────────────┴──────────────┐
│ api.Handler → tasks.Service → tasks.Registry → batchexec.Client           │
│                                ↘ session.Watcher（登录状态轮询）           │
└───────────────────────────────────────────────────────────────────────────┘
```

**关键点：所有 Google 请求都由会话窗口页面内部的 `fetch` 发出。**

- cookie 由浏览器自动附加，Go 端永远接触不到任何凭据
- `at` / `bl` 直接读页面里的 `window.WIZ_global_data`，不用抓 HTML
- UA、`X-Same-Domain`、同源策略全部是真实浏览器的行为
- 登录就是 Google 自己的登录页，**不需要 OAuth client_id、不需要 Google 审核**

### 解锁 `ExecJS` 的技巧

Wails v3 的 `WebviewWindow.ExecJS()` 有 `runtimeLoaded` 门控
（`pkg/application/webview_window.go:701`），只有收到页面发来的 `wails:runtime:ready`
才会置位（同文件 `:884`）。外部域名的页面永远不会加载 Wails runtime bundle，
所以 `ExecJS` 会把脚本静默塞进队列永不执行。

`internal/bridge/bootstrap.js` 通过 `WebviewWindowOptions.JS` 注入，它做的第一件事就是
自己回发这条保留消息：

```js
send("wails:runtime:ready");   // → WebviewWindow.HandleMessage → runtimeLoaded = true
```

于是 `ExecJS` 恢复正常，**不需要 cgo、不需要读原生 cookie store、不需要 fork Wails**，
三个平台通用。回传方向走 Wails 早就注册好的消息通道
（macOS `webkit.messageHandlers.external`，`webview_window_darwin.go:193`；Linux 同名
handler，`register_script_message_handler(manager, "external")`；Windows
`chrome.webview.postMessage` → `TryGetWebMessageAsString`），由
`application.Options.RawMessageHandler` 接收 —— 这个 handler 与页面来源无关。

### 三个平台的注入时机不一样

| 平台 | 注入点 | 时机 |
| --- | --- | --- |
| macOS | `webview_window_darwin.go:1755` | `WebViewDidFinishNavigation` 之后 `execJS` |
| Linux | `webview_window_linux.go:527` | `WindowLoadFinished` 之后 `execJS` |
| Windows | `webview_window_windows.go:2835` | **仅 HTML 窗口**：`chromium.Init` → `AddScriptToExecuteOnDocumentCreated` |

Windows 那一行是关键坑：`navigateInitialPage` 只在 `options.HTML != ""` 分支里处理
`options.JS`，外部 URL 窗口的 `default:` 分支只调 `setURL`，**bootstrap 永远不会注入**。
所以 `main.go` 在 Windows 上改用 `bridge.RedirectHTML(googleTasksURL)`：先加载一个只含
`location.replace(...)` 的桩文档，让 Wails 走 HTML 分支把 bootstrap 注册进
`AddScriptToExecuteOnDocumentCreated`。该注册对整个 webview 生命周期有效，之后每个文档
（含 `accounts.google.com` 往返）都在 **document start** 阶段注入 —— 比 macOS/Linux 更早，
首屏 RPC 也抓得到。

代价是脚本会在该 webview 的**每个**文档里运行，所以 `bootstrap.js` 开头有 Google 域名
守卫（`isGoogleHost()`）：桩文档和非 Google 页面直接 return，既不会误报登录状态，也不会
提前解锁 `ExecJS`。

另一个平台差异：`OriginInfo.IsMainFrame` **只有 darwin 后端会填**
（`application_darwin.go:409`），Windows（`webview_window_windows.go:2475`）和 WebKitGTK
（`linux_cgo.go:1711`）恒为 false。`bridge.HandleRaw` 因此只在该字段可信的平台
（`reportsMainFrame = runtime.GOOS == "darwin"`）上过滤子框架，其余平台靠协议前缀 +
窗口名过滤 —— 否则整条桥接会被静默丢弃。

### 会话韧性：识别「看着登录、实际掉线」

cookie 失效之后 SPA 仍留着旧的 `WIZ_global_data`，页面上 `SNlM0e` 还在、probe 仍报「已登录」，
但每个 RPC 都会 401 或被 302 去登录页。所以 watcher 每 8 轮轮询（约半分钟）额外做一次
**健康检查**（`bootstrap.js` 的 `health` 方法）：用一个必然不存在的 rpcid `AAAAA` 发最小请求 ——
会话活着回 200 + 一条错误帧；cookie 死了回 401，或 fetch 跟随重定向落到 `accounts.google.com`
（`response.redirected`）。该请求带 `__gtaskNoSniff` 标记，不会混进抓包记录；上一轮结果异常时
改为每轮都复查。

判定为 `unauthorized` / `redirected` 之后：

- 主界面横幅提示「会话已失效，请重新登录」，附「打开登录窗口」按钮
- 工作台 `登录` 徽标变成 `是（会话失效）`
- `main.go` 弹出会话窗口并重载一次（限速 3 分钟一次），把后续交给 Google 自己的登录流程
- 登录完成或会话恢复后窗口自动隐藏

`__Secure-1PSIDTS` 这类 cookie 轮换由网页自己的 JS 完成（会话窗口加载的是真实
tasks.google.com），本项目不做轮换；轮换失败最终表现为 401 / 重定向，同样被健康检查兜住。

## 读取层：一次 RPC 拿回整份数据

网页版切换清单、点「已加星标」、展开「已完成」时**都不会再发请求**——所有视图都由首屏
那一次数据在本地渲染。所以读取层只需要一个 rpcid：

| | |
| --- | --- |
| rpcid | `Yz8iP`（页面 `window.AF_dataServiceRequests["ds:0"].id`） |
| 端点 | `/_/TasksWebEmbedUi/data/batchexecute`（页面在 `WIZ_global_data.Im6cmf` 里公布） |
| 参数 | 空数组 |
| 返回 | 全部清单 + 未完成任务（含服务端声明的显示顺序）+ 每个清单前 10 条已完成任务 + 星标位 + 清单顺序 |

响应是**位置数组**而不是带键的对象，Google 也不公布任何 schema，所以
`internal/tasks/model.go` 里的槽位映射是靠「活响应 ↔ 渲染后的 DOM」逐位比对定下来的：

- 任务 `inner[1][13]` 是星标布尔、`inner[1][14]` 是加星时间 —— 官方 API 完全没有的字段
- 清单 `d[2][0]` 是服务端声明的未完成任务显示顺序，**它是唯一可信的排序依据**
  （任务自带的 `inner[7]` 排序键并不单调）
- 清单 `d[9]` 是真实已完成总数，比快照实际带回的多（每清单只回前 10 条），
  正好是网页「已完成 (44)」那个数字
- 已完成任务按 `completedAt` 倒序渲染，与网页一致

还没定下含义的槽位不会被丢掉，而是原样收进 `SlotBag`（`Extra` / `DetailExtra` /
`MetaExtra`）随 `/api/tasks` 一起返回：Google 改结构时会先在这里冒出来，
而不是表现为「某个字段悄悄没了」。`internal/tasks/model_test.go` 用一份合成快照
把每个槽位钉死（不含任何真实任务内容）。

主界面因此只有一个数据接口：`GET /api/tasks`。

## 写入层：一条双向同步通道 `SOV0tb`

原以为每个动作（加星、改标题、删除……）各有自己的 rpcid，抓包后发现**不是**：网页端所有
写入都走同一条同步流 `SOV0tb`。请求里带的是**字段级 patch 事件**，响应里回带服务端自己的
变更和一个新的同步时间戳。所以写入层不是「找到 N 个 rpcid」，而是「学会合成这一种报文」——
`internal/tasks/sync.go` 就是这门协议的实现。

### 报文形状

请求的 args（单元素数组）：

```json
[[null, 同步状态], null, [null, ["账户 id"]], null,
 ["随机 16 位", [null, 4, "boq_...", "prod-00-us", 3, 1, 2, null, 1]], []]
```

- **轮询**：`同步状态 = [[秒, 纳秒]]`，意思是「给我这个时间点之后的变更」
- **写入**：`同步状态 = [[秒, 纳秒], null, 事件组]`，`事件组 = [[[事件, ...], []]]`
- **响应**：`[实体变更, null, [[新的秒, 新的纳秒]]]`；args 发空数组会被拒（错误码 13）

**时间戳必须链式沿用**：下一次请求要带的是上一次响应尾部那个新值。带陈旧时间戳会偶发
错误码 3；而页面自己也在按定时器轮询同一条通道，随时会把服务端时间推走。所以写入层每次
提交前先空轮询一次拿最新值，真收到错误码 3 就再轮询一次并重试（`SyncWriter.commit`）。

### 事件结构

一个事件是 28 个槽位的数组，有意义的只有这些：

| 槽 | 含义 |
| --- | --- |
| 0 | 客户端生成的事件 id（16 位 base64url） |
| 1 | 实体类型：`2` 账户级清单顺序（单例，无 id）、`3` 任务、`4` 清单 |
| 2 | 实体 id |
| 3 | 路径，单事件写入是 `[1]`，新建任务用 `[1]`/`[1,1]`/`[1,2]` |
| 5 / 6 / 7 | patch，分别对应实体类型 2 / 3 / 4 |
| 8 | 清单内插入位置：`[任务 id, null, null, null, null, null, 1]` |
| 9 | 客户端时间戳 `[秒, 纳秒]` |
| 10 | `[null, 清单 id]`，任务归属 |
| 11 | `[[], []]`，顺序占位 |
| 25 | 固定 `1` |
| 26 | 组内序号（多事件时 1、2、3……） |
| 27 | 尾部，服务端不校验 |

### patch 与字段号

```json
[[null, [字段号...]], 值数组]
```

值数组是**稠密**的，下标 = 字段号 − 1，尾部空槽被裁掉。所以「改标题」是
`[[null,[2]],[null,"新标题"]]`；而**清除一个字段就是发空值数组**——取消星标、取消完成、
清除截止日期都是这么写的。

任务 detail 的字段号（读取侧同一个编号：detail 数组下标 = 字段号 − 1）：

| 字段 | 含义 | 写法 |
| --- | --- | --- |
| 1 | 完成 | `1`，与字段 5 一起写 |
| 2 | 标题 | 字符串 |
| 3 | 备注 | 字符串，可含换行 |
| 5 | 完成时间 | `[秒, 纳秒]` |
| 6 | 已删除 | `1` 删除、空数组恢复。服务端自动把任务从清单顺序里摘掉并写入删除时间（detail 下标 12） |
| 14 | 星标 | `1`，与字段 15 一起写 |
| 15 | 星标时间 | `[秒, 纳秒]` |
| 18 | 截止日期 | 消息体 `[[秒, 纳秒]]`，比普通时间戳多包一层 |

清单：字段 `1` 标题、`3` kind（服务端自己填 `user`）、`5` 排序。账户级顺序单例：字段 `3`
是逗号分隔的清单 csv，**服务端校验它必须恰好包含全部未删除清单**，所以新建清单前要重新读
一次快照拿当前顺序，不能用缓存。

截止时间的时区约定：只给日期时存 **UTC 零点**（Google 的整天约定，任何时区看到的都是同一
天）；给了时分就按本机时区换算成真实时刻，Google 网页 / 手机端显示的才是用户选的钟点。
读取侧按同一规则还原：UTC 零点 → 只有日期，其它 → 本机日期 + 时分。

### 组合出来的操作

| 操作 | 事件 |
| --- | --- |
| 新建任务 | 4 个：建实体（带标题）→ 顺序占位（槽 11）→ 插入清单（类型 4 + 槽 8）→ 绑定清单（槽 10）。任务 id 由客户端生成，服务端照用 |
| 改标题 / 改备注 / 星标 / 完成 / 截止 / 删除 / 恢复 | 1 个任务 patch 事件 |
| 新建清单 | 2 个：建清单（类型 4，字段 1）+ 重写顺序 csv（类型 2，字段 3） |
| 重命名清单 | 1 个清单 patch 事件 |

这些报文由 Go 现算，**不走 `operations.json` 的模板**——模板表达不了链式时间戳和客户端
生成的 id。`tasks.Service.Execute` 把它们分派给 `SyncWriter`，工作台里显示成
`SOV0tb（同步通道）`；前端契约没变，还是 `POST /api/ops/execute {op, params}`。
`internal/tasks/sync_test.go` 用假客户端把每种报文的确切 JSON 钉死，包括时间戳链式传递与
错误码 3 的重试。

**还没破解**：删除清单（字段 2 置 true → 错误码 13 INTERNAL，估计不是字段级 patch）、任务
排序与跨清单移动（只写槽 10 不够，槽 8 的操作码 0/2/3/4 都无效）、子任务、重复规则（字段 8
能写进去但不生效）。

## 操作映射：把抓包变成能力

rpcid 和参数结构没有文档，只能从活的网页上抓。抓到之后要能复用，所以有一层
**操作注册表**（`internal/tasks/operations.json`）：23 个逻辑操作，每个对应一到多个
batchexecute 调用，参数是 `text/template` 模板。目前 `snapshot` 走模板，12 个写入操作走
上一节的同步通道（注册表里只留说明，不带 steps），其余的还在等抓包：

```json
{ "op": "setStarred",
  "steps": [{ "rpcid": "AbCdE", "args": "[null,{{json .TaskID}},[{{json .Starred}}]]" }] }
```

`tasks.Service.Execute(ctx, "snapshot", params)` 渲染模板 → 组批 → 走页面发出 → 解码响应。
主界面的每个按钮就是调一个 op，所以**打通一条通道，界面上就多亮一批功能**。

注册表落盘在用户配置目录，抓包记录也持久化成 JSONL：

| 平台 | 目录 |
| --- | --- |
| macOS | `~/Library/Application Support/gtasks/` |
| Linux | `~/.config/gtasks/` |
| Windows | `%AppData%\gtasks\` |

里面是 `operations.json`（操作映射）和 `captures.jsonl`（抓包历史，只追加）。

## 桌面化改造：会话页里的注入层

会话窗口就是每天真正在用的 tasks.google.com，`internal/bridge/bootstrap.js` 的 UI 层
在它上面做了几处贴合桌面端的改动（只动 DOM 与回调 Go，不替换 Google 自己的逻辑）：

- **顶栏去掉「Google 应用」九宫格**（纯 CSS 隐藏）。
- **顶栏主题按钮**：贴在「支持」按钮右边（绝对定位，不占 Google 的行内排版），
  点击弹 Go 的原生单选菜单：明亮 / 暗黑 / 跟随系统。模式由 Go 持久化
  （`<数据目录>/theme.txt`），macOS 上同时切 `NSApp.appearance` 和每个窗口的
  `appearance`，标题栏与 Google 页面的 `prefers-color-scheme` 一起变。Google 的配色
  全靠媒体查询驱动，外观一变立刻重算，所以切主题**不再重载会话页**；只有页面回报
  媒体查询没跟上时（个别 WebView 版本）才兜底重载一次。
- **「支持」菜单里的「帮助」改造成「GTasks 工作台」**：沿用 Google 自己的菜单项
  （行高、悬停高亮、键盘导航都一致），挪到菜单末尾；桌面客户端里帮助页没有意义，
  正好腾出这一项。点击打开逆向工作台窗口。
- **启动遮罩**：会话页从导航到 Google 首屏渲染完之间有 1~3 秒空档，bootstrap 在
  document-start 就盖一层与当前主题同色的遮罩 + 转圈（`#gtasks-splash`），外壳渲染
  出来即淡出；遮罩不吃鼠标事件、带 8 秒硬超时。三个窗口的 `BackgroundColour` 与
  webview 的 `underPageBackgroundColor` 也按主题预设，深色模式下不再闪白屏。
- **侧栏每个任务列表行加「⋮」**：悬停时出现，点的还是 Google 自己的「列表选项」菜单
  （排序 / 重命名 / 删除 / 移动……）；该清单没在内容区渲染时会先切过去再点。
- **右键换成原生自定义菜单**：返回 / 重新加载 / 剪切 / 复制 / 粘贴 / 全选 / 检查元素，
  去掉了 WebKit 默认菜单里的「前进」。主界面与工作台走 Wails 运行时的
  `--custom-contextmenu` CSS 变量，会话页由 bootstrap 拦截 `contextmenu` 后回调 Go。

应用图标取自 Google Tasks 官方产品标识（gstatic 的 `tasks_2026` SVG，仅自用）：
`build/macos-icon.sh` 用无头 Chrome 栅格化成 iconset 再 `iconutil` 打包，
`build/macos-bundle.sh` 组装 `GTasks.app`（Info.plist + icns + 主程序）。
**`CFBundleIdentifier` 固定为 `gtasks`**：WebKit 的 cookie / localStorage 按它隔离，改了要重新登录。

## 构建与运行

仓库：<https://github.com/mose-x/googleTasks.git>（只做 git 分发，不提供安装包）

```bash
git clone https://github.com/mose-x/googleTasks.git
cd googleTasks
go mod tidy          # 国内网络可加 GOPROXY=https://goproxy.cn,direct
go run .             # 或 go build -o gtasks . && ./gtasks

# macOS 想要带图标的 .app：
./build/macos-bundle.sh /tmp/GTasks.app
./build/macos-icon.sh          # 仅在改 build/icon.svg 后需要，依赖本机 Chrome
```

需要 **Go 1.26+**（Wails v3.1.0 的要求；装的是 1.25 时 toolchain 会自动下载 1.26）。

| 目标 | 命令 | 依赖 |
| --- | --- | --- |
| macOS | `go build -o gtasks .` | Xcode Command Line Tools（cgo） |
| Windows | `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o gtasks.exe .` | 可从 macOS/Linux 交叉编译；运行时需要 WebView2 Runtime（Win10/11 一般已内置） |
| Linux | `go build -o gtasks .` | `sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev`（Arch：`gtk3 webkit2gtk-4.1`） |
| Linux 无 cgo | `CGO_ENABLED=0 go build -tags purego -o gtasks .` | 只需运行时库 `libgtk-3-0 libwebkit2gtk-4.1-0` |

三个目标都已通过 `go build` 验证（Windows amd64/arm64、Linux amd64 purego、macOS arm64）；
Windows 与 Linux 的**实机冒烟测试还没做**。

启动后有两个窗口：**Google Tasks**（主界面）与
**Google 会话**（加载 `tasks.google.com`，未登录会自动跳到 Google 登录页）。
**逆向工作台**默认不弹，需要时从会话页顶栏「?」菜单里的「GTasks 工作台」打开
（关掉后再点会按需重建）。

主题三档（明亮 / 暗黑 / 跟随系统）由 Go 统一持有，主界面侧栏和会话页顶栏各有一个按钮，
任一处切换全端同步；深色沿用 Google 网页版暗色规范（底 `#202124`、正文 `#e8eaed`、
强调 `#8ab4f8`），并设置 `color-scheme: dark` 让原生控件（日期选择、下拉、滚动条）一起变暗。
切换是即时的：原生外观一变，各窗口的 CSS 变量与 Google 页面的媒体查询同时重算，不重载页面。
主窗口首帧之前由 `index.html` 里的内联脚本按 localStorage 缓存先上色，避免深色模式闪白。
工作台是逆向工具，固定深色不跟主题走。
抓包历史在重启后会自动读回工作台（只保留最近 3000 条，界面只显示最新 400 行）。

`session.Watcher` 每 4 秒探测一次会话页面：登录后自动隐藏会话窗口，掉线后自动弹回来；
桥接一就绪就自动打开抓包。两个行为都能在工作台顶栏关掉。

测试：

```bash
go test ./...
```

- `internal/bridge` 用 Node 把 `bootstrap.js` 跑在伪造的 tasks.google.com 页面里，
  实测验证：`wails:runtime:ready` 确实发出、`at`/`bl`/`loggedIn` 正确读到、发出的请求
  URL 含 `rpcids`/`bl`/`rt=c`/`source-path`/`_reqid`、头里有 `X-Same-Domain: 1`、
  `credentials: include`、`f.req` 双重编码结构正确。没有 `node` 时自动跳过。
- `internal/batchexec` 覆盖 `f.req` 编码与 `)]}'` / 分块响应解码。
- `internal/tasks` 覆盖注册表落盘重载、模板参数替换、未配置操作的报错。
- `internal/api` 覆盖抓包 → Go 代码生成（含 `go/format` 校验过的片段）。

## rpcid 发现流程

1. 在**会话窗口**里正常登录 Google；工作台顶栏应显示 `登录: 是`、`at`/`bl` 有值
2. 抓包默认已自动开启（也可手动点 **开始**）
3. 在会话窗口里做一个操作，例如给某个任务加星
4. 工作台「RPC 抓包」出现一条记录，展开可看 `f.req` 与响应，点 **解码响应** 看结构
5. 点 **生成代码 / 固化为操作**：右侧给出可直接编译的 Go 片段和 steps JSON
6. 在「固化为操作」下拉里选对应的 op（如 `setStarred`），必要时把字面量参数改成
   `{{json .TaskID}}` 这样的模板占位符，点 **保存为操作**
7. 切到「操作映射」页，用 **执行** 面板带上参数重放，确认语义正确
8. 回到主界面，对应按钮就活了

逐个覆盖：增 / 删 / 改标题 / 改备注 / 改日期含时分 / 加星 / 设重复 / 拖拽排序
（同列表与跨列表）/ 加子任务 / 恢复已删除 / 清空已完成 / 列表增删改与排序 / 搜索。

**已知限制**：macOS / Linux 上 `WebviewWindowOptions.JS` 在导航结束**之后**注入，页面首屏
那几个 RPC 可能抓不到（Windows 是 document start 注入，首屏也能抓到）。任何**用户交互
触发**的调用三平台都能抓到，这已覆盖发现 rpcid 的全部需求。

另外，Wails 每次导航后都会重跑 bootstrap，新副本的抓包开关默认是关的。所以开关状态放在
`window.__gtaskSniffing` 上而不是闭包里，`session.Watcher` 也会在每次轮询时重新
`sniffStart`（幂等），避免登录跳转之后静默停止记录。

## 待办

- [x] 读取层：`snapshot`（`Yz8iP`）解析 + `GET /api/tasks` + 主界面接真实数据
      （侧栏「所有任务 / 已加星标 / 任务列表（带数量）/ 新建任务列表」、
      每清单「已完成 (N)」折叠段、已完成行的「完成日期：M月D日周X」）
- [x] 写入层：`SOV0tb` 同步通道打通，新建 / 改标题 / 改备注 / 加星 / 完成 / 取消完成 /
      截止日期含时分 / 删除 / 恢复 / 新建清单 / 重命名清单全部可用，并逐项读回验证过
- [x] 截止日期的槽位：任务 detail 字段 18（下标 17），消息体 `[[秒, 纳秒]]`，
      读取侧一并解析成 `due.date` / `due.time`
- [ ] 删除清单：字段级 patch 走不通（错误码 13），需要抓网页端「删除清单」的真实事件组
- [ ] 任务排序与跨清单移动：槽 8 的操作码还没试对，需要抓一次真实拖拽
- [ ] 子任务：预计在 `createTask` 的四事件之外再加一个父子关系事件
- [ ] 重复规则：detail 字段 8 能写进去但不生效，内部结构未摸清
- [ ] 已完成翻页：快照每清单只回前 10 条，需要抓「加载更多」那个 rpcid
- [ ] 「所有任务」视图：网页在宽屏下把每个清单渲染成横向并排的列，
      客户端目前按纵向分段渲染（内容一致、更适合桌面窗口），待确认是否改成横向
- [ ] 主界面与真实网页并排比对，收敛像素级差异（把会话窗口拖到旁边即可）
- [x] 三态主题全局化：Go 持久化 + macOS 原生外观（标题栏、会话页一起变），会话页顶栏也有按钮
- [x] 会话页桌面化：去「Google 应用」九宫格、「支持」菜单加工作台入口、侧栏列表行「⋮」、
      自定义右键菜单（去掉前进）
- [x] 启动体验：主题色首屏底色 + 会话页启动遮罩，切主题不再整页重载
- [x] 应用图标与 macOS 打包脚本（`build/macos-bundle.sh` / `build/macos-icon.sh`）
- [x] Windows / Linux 桥接对齐（bootstrap 注入路径、`IsMainFrame` 过滤）+ 交叉编译验证
- [ ] Windows / Linux 实机冒烟测试（需要真实 Windows / Linux 环境与 Google 账号）
- [x] 掉线检测与自动重登（健康检查识别 401 / 登录重定向 → 横幅 + 弹窗重载）
- [ ] 本地缓存与离线视图

## 风险

- **不分发、不上架也不豁免 ToS**。私有接口访问本身就违反条款。
- 真正的风险落在**使用者账号**上：限流、强制重新登录，极端情况封号，且不可逆。
  个人低频使用被识别的概率很低，但不是零。
- 内部接口没有文档、没有兼容性承诺。Google 每次发版都可能改动 `bl`、rpcid 或参数
  数组结构，坏了只能重新抓包。请把这当成长期维护成本。
