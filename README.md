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

## 操作映射：把抓包变成能力

rpcid 和参数结构没有文档，只能从活的网页上抓。抓到之后要能复用，所以有一层
**操作注册表**（`internal/tasks/operations.json`）：22 个逻辑操作（`setStarred`、
`setDueDateTime`、`setRecurrence`、`moveTask`……）每个对应一到多个 batchexecute 调用，
参数是 `text/template` 模板：

```json
{ "op": "setStarred",
  "steps": [{ "rpcid": "AbCdE", "args": "[null,{{json .TaskID}},[{{json .Starred}}]]" }] }
```

`tasks.Service.Execute(ctx, "setStarred", params)` 渲染模板 → 组批 → 走页面发出 →
解码响应。主界面的每个按钮就是调一个 op，所以**发现一个 rpcid，界面上就多亮一个功能**。

注册表落盘在用户配置目录，抓包记录也持久化成 JSONL：

| 平台 | 目录 |
| --- | --- |
| macOS | `~/Library/Application Support/gtasks/` |
| Linux | `~/.config/gtasks/` |
| Windows | `%AppData%\gtasks\` |

里面是 `operations.json`（操作映射）和 `captures.jsonl`（抓包历史，只追加）。

## 构建与运行

仓库：<https://github.com/mose-x/googleTasks.git>（只做 git 分发，不提供安装包）

```bash
git clone https://github.com/mose-x/googleTasks.git
cd googleTasks
go mod tidy          # 国内网络可加 GOPROXY=https://goproxy.cn,direct
go run .             # 或 go build -o gtasks . && ./gtasks
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

启动后有三个窗口：**Google Tasks**（主界面）、**逆向工作台**、
**Google 会话**（加载 `tasks.google.com`，未登录会自动跳到 Google 登录页）。

主界面右上角侧栏里有主题按钮（太阳 / 月亮 / 自动），在浅色、深色、跟随系统之间循环；
深色沿用 Google 网页版暗色规范（底 `#202124`、正文 `#e8eaed`、强调 `#8ab4f8`），
并设置 `color-scheme: dark` 让原生控件（日期选择、下拉、滚动条）一起变暗。
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

- [ ] 按抓包结果实现各 op 的**响应解析**（`app.js` 里 `loadData()` 已留好位置）
- [ ] 主界面与真实网页并排比对，收敛像素级差异（把会话窗口拖到旁边即可）
- [x] 深色主题（侧栏三态按钮：浅色 → 深色 → 跟随系统，选择存 `localStorage`）
- [x] Windows / Linux 桥接对齐（bootstrap 注入路径、`IsMainFrame` 过滤）+ 交叉编译验证
- [ ] Windows / Linux 实机冒烟测试（需要真实 Windows / Linux 环境与 Google 账号）
- [ ] 掉线检测与自动重登（`at` 过期、`__Secure-1PSIDTS` 轮换）
- [ ] 本地缓存与离线视图

## 风险

- **不分发、不上架也不豁免 ToS**。私有接口访问本身就违反条款。
- 真正的风险落在**使用者账号**上：限流、强制重新登录，极端情况封号，且不可逆。
  个人低频使用被识别的概率很低，但不是零。
- 内部接口没有文档、没有兼容性承诺。Google 每次发版都可能改动 `bl`、rpcid 或参数
  数组结构，坏了只能重新抓包。请把这当成长期维护成本。
