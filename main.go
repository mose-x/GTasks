// Command gtasks is a Google Tasks desktop client built on Wails v3.
//
// The UI is served from the bundled asset server. All Google traffic runs
// inside a second webview that is loaded from tasks.google.com itself, so the
// user authenticates through Google's own pages and the internal batchexecute
// RPCs are issued from that origin with the real session cookies attached.
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"gtasks/internal/api"
	"gtasks/internal/batchexec"
	"gtasks/internal/bridge"
	"gtasks/internal/i18n"
	"gtasks/internal/session"
	"gtasks/internal/tasks"
	"gtasks/internal/theme"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend
var frontendAssets embed.FS

//go:embed build/app-icon.png
var appIcon []byte

const (
	mainWindowName    = "main"
	workbenchName     = "workbench"
	sessionWindowName = "gsession"
	googleTasksURL    = "https://tasks.google.com/"
	captureRingLimit  = 3000
	contextMenuID     = "gtasks-context"
	themeMenuID       = "gtasks-theme"
	langMenuID        = "gtasks-lang"

	// 窗口标题统一用产品名，不再暴露「Google 会话」这类内部叫法。
	windowTitle          = "GTasks"
	workbenchWindowTitle = "GTasks 工作台"
)

// version 由打包脚本用 -ldflags "-X main.version=..." 写入（见 Makefile 与
// scripts/build-*.sh）；go run / go build 时保持 dev。
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println(version)
		return
	}

	dataDir, err := api.DataDir()
	if err != nil {
		log.Printf("无法定位数据目录，抓包日志与操作表将只存在内存中: %v", err)
	}
	log.Printf("GTasks %s（%s/%s）", version, runtime.GOOS, runtime.GOARCH)

	sessionBridge := bridge.New()
	recorder := api.NewRecorder(captureRingLimit)
	sessionBridge.SetSniffHandler(recorder.Add)
	if dataDir != "" {
		captures := api.CapturesPath(dataDir)
		loaded, err := recorder.LoadHistory(captures)
		switch {
		case err != nil:
			log.Printf("读取历史抓包失败: %v", err)
		case loaded > 0:
			log.Printf("已载入 %d 条历史抓包（%s）", loaded, captures)
		}
		if err := recorder.SetPersistence(captures); err != nil {
			log.Printf("抓包日志落盘失败: %v", err)
		}
	}

	themeManager := theme.Load(themePath(dataDir))
	// 词表打包在二进制里（internal/i18n/locales/*.toml），加语言只需加 toml。
	i18nManager := i18n.LoadManager(i18n.Load(), langPath(dataDir))

	registry := loadRegistry(dataDir)
	client := batchexec.NewClient(sessionBridge)
	service := tasks.NewService(registry, client)
	watcher := session.NewWatcher(client, session.DefaultInterval)

	apiHandler := api.NewHandler(api.Deps{
		Bridge:   sessionBridge,
		Client:   client,
		Recorder: recorder,
		Registry: registry,
		Service:  service,
		Watcher:  watcher,
		Theme:    themeManager,
		I18n:     i18nManager,
		DataDir:  dataDir,
	})

	// Loopback-only control plane: the very same handler the in-app asset
	// server mounts, so the discovery workflow can be scripted with curl
	// instead of clicking through the workbench. Binding strictly to loopback
	// keeps it in the same trust domain as the local cookie store.
	controlServer := startControlServer(apiHandler)

	app := application.New(application.Options{
		Name:        "GTasks",
		Description: "Google Tasks 桌面客户端",
		Icon:        appIcon,
		Assets: application.AssetOptions{
			Handler: buildAssetHandler(apiHandler),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		// Every non-reserved message posted by any webview lands here. The
		// bridge filters on its own protocol tag and window name.
		RawMessageHandler: sessionBridge.HandleRaw,
		OnShutdown: func() {
			watcher.Stop()
			recorder.Close()
			if controlServer != nil {
				_ = controlServer.Close()
			}
		},
	})

	startDark := theme.EffectiveDark(themeManager.Mode())

	mainWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      mainWindowName,
		Title:     windowTitle,
		URL:       "/",
		Width:     1180,
		Height:    780,
		MinWidth:  860,
		MinHeight: 540,
		// 首帧之前 webview 涂的就是这个颜色，深色模式下不再闪白屏。
		BackgroundColour: rgbaOf(theme.AppBackground(startDark)),
	})
	applyWebviewBackgroundSoon(mainWindow, func() theme.RGB {
		return theme.AppBackground(theme.EffectiveDark(themeManager.Mode()))
	})

	sessionOptions := application.WebviewWindowOptions{
		Name:  sessionWindowName,
		Title: windowTitle,
		URL:   tasksURLFor(i18nManager.Lang()),
		// Wide enough for tasks.google.com to use its desktop layout. At phone
		// widths Google switches the 所有任务 view to horizontally paged list
		// cards, which is not what the main window reproduces.
		Width:     1200,
		Height:    860,
		MinWidth:  420,
		MinHeight: 620,
		// Re-executed after every navigation, including the redirect chain
		// through accounts.google.com and back.
		JS: bridge.BootstrapJS,
		// 会话页从导航到 Google 首屏渲染完有 1~3 秒空档，底色跟主题走，
		// 页面侧再由 bootstrap 盖一层同色的启动遮罩。
		BackgroundColour: rgbaOf(theme.PageBackground(startDark)),
	}
	if runtime.GOOS == "windows" {
		// The Windows backend never applies WebviewWindowOptions.JS to a window
		// created from an external URL, so start from a stub that redirects and
		// let it register the bootstrap for the whole webview lifetime instead.
		sessionOptions.URL = ""
		sessionOptions.HTML = bridge.RedirectHTML(tasksURLFor(i18nManager.Lang()))
	}
	sessionWindow := app.Window.NewWithOptions(sessionOptions)
	applyWebviewBackgroundSoon(sessionWindow, func() theme.RGB {
		return theme.PageBackground(theme.EffectiveDark(themeManager.Mode()))
	})
	sessionBridge.Bind(sessionWindow)
	apiHandler.BindWindows(mainWindow, sessionWindow)

	// 窗口表：右键菜单靠 ContextMenuData 里的窗口名找回目标窗口，主题广播也按
	// 名字取窗口。工作台被用户关掉后 Wails 会直接销毁它，所以表里的值允许为
	// nil，取窗口一律走 lookupWindow。
	var windowsMu sync.Mutex
	windowsByName := map[string]*application.WebviewWindow{
		mainWindowName:    mainWindow,
		sessionWindowName: sessionWindow,
	}
	lookupWindow := func(name string) *application.WebviewWindow {
		windowsMu.Lock()
		defer windowsMu.Unlock()
		return windowsByName[name]
	}
	storeWindow := func(name string, win *application.WebviewWindow) {
		windowsMu.Lock()
		defer windowsMu.Unlock()
		windowsByName[name] = win
	}

	// 工作台窗口一关就销毁，之后 Show() 全是空操作（用户反馈的「点工作台菜单
	// 没反应」就是这个）。改成按需重建，菜单项任何时候都能拉起一个可用窗口。
	newWorkbenchWindow := func() *application.WebviewWindow {
		win := app.Window.NewWithOptions(application.WebviewWindowOptions{
			Name:      workbenchName,
			Title:     i18nManager.T("app.workbenchTitle", workbenchWindowTitle),
			URL:       "/workbench.html",
			Width:     1240,
			Height:    820,
			MinWidth:  900,
			MinHeight: 560,
			// 工作台固定深色，底色跟 workbench.css 的 --bg 一致。
			BackgroundColour: rgbaOf(theme.WorkbenchBackground()),
			Mac: application.MacWindow{
				// 工作台是逆向工具，固定深色；不跟全局主题走，避免浅色模式下
				// 标题栏和内容区一个白一个黑。
				Appearance: application.NSAppearanceNameDarkAqua,
			},
		})
		storeWindow(workbenchName, win)
		applyWebviewBackgroundSoon(win, func() theme.RGB { return theme.WorkbenchBackground() })
		win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			storeWindow(workbenchName, nil)
		})
		return win
	}
	// 工作台是逆向工具，不跟着启动弹出来：需要时从会话页「?」菜单里开，
	// 关掉之后再点也会按需重建。

	var workbenchMu sync.Mutex
	openWorkbench := func() {
		workbenchMu.Lock()
		defer workbenchMu.Unlock()
		win := lookupWindow(workbenchName)
		if win == nil {
			win = newWorkbenchWindow()
		}
		win.Show()
		win.Focus()
	}

	relabelContext := newContextMenu(lookupWindow, i18nManager)

	apiHandler.SetWindowLookup(lookupWindow)

	langPicker := newLangMenu(i18nManager.Bundle(), func(code string) { i18nManager.Set(code) })
	apiHandler.SetLangMenuOpener(func(source string, x, y int) {
		win, name := menuWindow(lookupWindow, source)
		langPicker.open(win, name, i18nManager.Lang(), x, y)
	})

	// 「会话窗收没收起来」由 sessionOnScreen 自己记账：wails 的 IsVisible 读的
	// 是遮挡状态，被别的窗口盖住也算不可见，拿它当判据会把该载的重载一直欠着。
	// 窗口创建时是显示的（Hidden 没置位），登录后 watcher 才把它收起来。
	var sessionOnScreen atomic.Bool
	sessionOnScreen.Store(true)
	showSessionWindow := func() {
		sessionOnScreen.Store(true)
		sessionWindow.Show()
	}
	hideSessionWindow := func() {
		sessionOnScreen.Store(false)
		sessionWindow.Hide()
	}
	apiHandler.SetSessionShownHook(func(shown bool) { sessionOnScreen.Store(shown) })

	// 内嵌的 Google 页面跟着换界面语言：只改我们自己的文案的话，在会话页里点
	// 语言按钮看起来就像没反应。hl 参数是 Google 自家的界面语言开关，页面依旧是
	// tasks.google.com 原页面。但那是一次整页重载，桥接要跟着停几秒，所以由
	// langNav 决定这次到底要不要载：语言没变不载，窗口藏着先欠着，只有用户正
	// 盯着会话页时才当场载（白屏由 bootstrap 的启动遮罩盖住）。
	langNav := newLangNavigator(i18nManager.Lang())
	// 自己记的账优先，再用遮挡状态兜底：万一窗口是从别的路径露出来的（比如
	// wails 在首次导航结束后补的那次 Show），也不至于把该载的重载一直欠着。
	sessionVisible := func() bool { return sessionOnScreen.Load() || sessionWindow.IsVisible() }
	navigateSession := func(url string) {
		// 先把桥接标成「正在导航」：在飞的调用当场失败，后续调用直接返回
		// ErrNotReady，主窗口那边靠上一次的快照垫着，不会挂住也不会闪空。
		sessionBridge.MarkNavigating()
		sessionWindow.ExecJS(fmt.Sprintf("location.replace(%q);", url))
	}
	reloadSessionWindow := func() {
		sessionBridge.MarkNavigating()
		sessionWindow.Reload()
	}
	// 把欠着的那次重载补上：会话窗露面时由 WindowShow 事件和兜底轮询各调一次。
	settleLangNav := func() {
		if url := langNav.owed(sessionVisible); url != "" {
			navigateSession(url)
		}
	}

	// 切语言后把新词表推给三个窗口：主窗口和工作台走各自的 applyI18n，会话页由
	// bootstrap 改注入的按钮 / 菜单文案；原生右键菜单和工作台标题跟着换。
	i18nManager.OnChange(func(code string) {
		payload := string(i18nManager.Payload())
		script := fmt.Sprintf("window.gtasksApplyI18n&&window.gtasksApplyI18n(%s);", payload)
		mainWindow.ExecJS(script)
		if workbench := lookupWindow(workbenchName); workbench != nil {
			workbench.ExecJS(script)
			workbench.SetTitle(i18nManager.T("app.workbenchTitle", workbenchWindowTitle))
		}
		sessionWindow.ExecJS(fmt.Sprintf("window.__gtask&&window.__gtask.setI18n(%s);", payload))
		if url := langNav.request(code, sessionVisible); url != "" {
			navigateSession(url)
		}
		relabelContext()
	})

	themePicker := newThemeMenu(func(mode theme.Mode) { themeManager.Set(mode) })
	apiHandler.SetThemeMenuOpener(func(source string, x, y int) {
		win, name := menuWindow(lookupWindow, source)
		themePicker.open(win, name, themeManager.Mode(), x, y)
	})

	// 主题切换后把模式推给三个窗口：主窗口和工作台改自己的 data-theme，会话页
	// 刷新注入的主题按钮图标。页面配色全靠 prefers-color-scheme 媒体查询驱动，
	// 原生外观一变就会立刻重算，所以这里不再重载会话页——之前每次切主题都
	// Reload 一次，白白卡上两三秒。媒体查询确实没跟上时，页面会回报
	// reloadForTheme，由下面那个命令兜底重载。
	themeManager.OnChange(func(mode theme.Mode) {
		script := fmt.Sprintf("window.__gtasksTheme&&window.__gtasksTheme(%q);", string(mode))
		mainWindow.ExecJS(script)
		if workbench := lookupWindow(workbenchName); workbench != nil {
			workbench.ExecJS(script)
		}
		dark := theme.EffectiveDark(mode)
		sessionWindow.ExecJS(fmt.Sprintf("window.__gtask&&window.__gtask.setThemeMode(%q,%t);", string(mode), dark))

		// 标题栏跟内容一起变；工作台固定深色，不参与。
		for _, win := range []*application.WebviewWindow{mainWindow, sessionWindow} {
			theme.ApplyWindow(win.NativeWindow(), mode)
		}
		// webview 底色跟着换，之后万一真要重载也不会闪白屏。
		theme.ApplyWebviewBackground(mainWindow.NativeWindow(), theme.AppBackground(dark))
		theme.ApplyWebviewBackground(sessionWindow.NativeWindow(), theme.PageBackground(dark))
	})

	var lastThemeReload time.Time
	sessionBridge.SetCommandHandler(func(name string, args map[string]any) {
		switch name {
		case "openWorkbench":
			openWorkbench()
		case "openThemeMenu":
			x, y := commandPoint(args)
			themePicker.open(sessionWindow, sessionWindowName, themeManager.Mode(), x, y)
		case "openLangMenu":
			x, y := commandPoint(args)
			langPicker.open(sessionWindow, sessionWindowName, i18nManager.Lang(), x, y)
		case "getI18n":
			sessionWindow.ExecJS(fmt.Sprintf("window.__gtask&&window.__gtask.setI18n(%s);",
				string(i18nManager.Payload())))
		case "reloadForTheme":
			// 页面报告 prefers-color-scheme 没跟上原生外观（个别 WebView 版本
			// 不同步窗口外观）才走到这里；限流防止页面反复回报时无限重载。
			if time.Since(lastThemeReload) > 10*time.Second {
				lastThemeReload = time.Now()
				reloadSessionWindow()
			}
		case "contextMenu":
			x, y := commandPoint(args)
			sessionWindow.OpenContextMenu(&application.ContextMenuData{
				Id:   contextMenuID,
				X:    x,
				Y:    y,
				Data: sessionWindowName,
			})
		}
	})

	// 页面每次加载完都会 announce 一次，趁机把当前主题补进注入的按钮。
	sessionBridge.SetReadyHandler(func(json.RawMessage) {
		mode := themeManager.Mode()
		sessionWindow.ExecJS(fmt.Sprintf("window.__gtask&&window.__gtask.setThemeMode(%q,%t);",
			string(mode), theme.EffectiveDark(mode)))
		// 页面每次加载完都补一次词表，保证注入按钮的文案和当前语言同步。
		sessionWindow.ExecJS(fmt.Sprintf("window.__gtask&&window.__gtask.setI18n(%s);",
			string(i18nManager.Payload())))
		// 语言重载已经落地，把留给 watcher 的安静期收短。
		langNav.settled()
	})

	// 会话窗每次露面都检查有没有欠着的一次语言重载。
	sessionWindow.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) {
		settleLangNav()
	})
	// 遮挡事件不一定来（macOS 上窗口被完全盖住时既不算 visible 也不派发），
	// 再挂一个兜底轮询；只在真欠账时才去问窗口可见性，平时什么都不做。
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if langNav.waiting() {
				settleLangNav()
			}
		}
	}()

	// 页面自己发起的导航（Google 内部跳转、掉线后重定向到登录页）同样要把桥接
	// 标成正在导航，否则调用会投进一个正在拆掉的文档里干等超时。Windows 后端
	// 没有导航开始事件，那边靠 navigateSession / reloadSessionWindow 兜住。
	for _, navigating := range []events.WindowEventType{
		events.Mac.WebViewDidStartProvisionalNavigation,
		events.Linux.WindowLoadStarted,
	} {
		sessionWindow.OnWindowEvent(navigating, func(*application.WindowEvent) {
			sessionBridge.MarkNavigating()
		})
	}

	// Show the login window only while it is actually needed.
	var lastRecover time.Time
	watcher.SetChangeHandler(func(current, previous session.Status) {
		// 语言重载期间页面会短暂掉出 ready，别把它当成掉线去 Show/Reload。
		if langNav.isQuiet() {
			return
		}
		if !watcher.AutoManage() {
			return
		}
		switch {
		case current.Expired && !previous.Expired:
			// The cookies or the at token died mid-use while the page still
			// looks signed in. Surface the window and reload it so Google's
			// own login flow takes over; rate-limit the reload so a page that
			// keeps failing cannot loop.
			showSessionWindow()
			if time.Since(lastRecover) > 3*time.Minute {
				lastRecover = time.Now()
				reloadSessionWindow()
			}
		case previous.Expired && !current.Expired && current.LoggedIn:
			hideSessionWindow()
		case current.LoggedIn && !previous.LoggedIn:
			hideSessionWindow()
		case !current.LoggedIn && current.Ready:
			showSessionWindow()
		case !current.Ready && previous.Ready:
			// Navigation in progress or the page died; bring it back so the
			// user can see what Google is showing.
			showSessionWindow()
		}
	})
	if os.Getenv("GTASKS_SESSION_AUTOMANAGE") == "0" {
		// 逆向调试时让会话窗口一直留在屏幕上：页面被判定为 hidden 时 Google 的
		// 前端不渲染，DOM 是空的，没法驱动真实操作来抓写入报文。
		watcher.SetAutoManage(false)
	}
	watcher.Start()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// startControlServer serves the API on 127.0.0.1 (override with GTASKS_API_ADDR,
// use "" to disable). It returns nil when the port cannot be bound, which is
// not fatal: the in-app windows keep working either way.
func startControlServer(handler http.Handler) *http.Server {
	addr := os.Getenv("GTASKS_API_ADDR")
	if addr == "" {
		addr = "127.0.0.1:17321"
	}
	if addr == "-" {
		return nil
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("本地控制端口 %s 不可用，跳过: %v", addr, err)
		return nil
	}
	server := &http.Server{Handler: guardLocal(handler), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("本地控制端口服务结束: %v", err)
		}
	}()
	log.Printf("本地控制端口: http://%s/api/", listener.Addr())
	return server
}

// localClientHeader 是控制端口的跨站防护。浏览器向 127.0.0.1 发的跨源请求一旦
// 带上自定义头就会先走预检，而这里不提供任何 CORS 许可，恶意网页因此调不动
// 本地 API（尤其是能在 Google 会话里执行脚本的 /api/eval）。
const localClientHeader = "X-GTasks-Client"

func guardLocal(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(localClientHeader) != "1" {
			http.Error(w, "本地控制端口需要 "+localClientHeader+": 1 头", http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

// newContextMenu 注册全局右键菜单，返回一个换语言时重贴标签的回调。WebKit 的
// 默认菜单带「前进/后退」，对这个单页应用没意义，页面侧已经 preventDefault 掉
// 并改弹这份：保留返回与重新加载，编辑类操作交给原生 role。目标窗口由
// ContextMenuData 携带的窗口名决定。
func newContextMenu(resolve func(string) *application.WebviewWindow, manager *i18n.Manager) func() {
	menu := application.NewContextMenu(contextMenuID)
	type contextEntry struct {
		item     *application.MenuItem
		key      string
		fallback string
	}
	var entries []contextEntry
	add := func(key, fallback string) *application.MenuItem {
		item := menu.Add(manager.T(key, fallback))
		entries = append(entries, contextEntry{item: item, key: key, fallback: fallback})
		return item
	}
	add("menu.back", "返回").OnClick(func(ctx *application.Context) {
		if win := resolve(ctx.ContextMenuData()); win != nil {
			win.ExecJS("if (history.length > 1) history.back();")
		}
	})
	add("menu.reload", "重新加载").OnClick(func(ctx *application.Context) {
		if win := resolve(ctx.ContextMenuData()); win != nil {
			win.Reload()
		}
	})
	menu.AddSeparator()
	// 编辑类操作走原生 role（作用于 webview 的第一响应者），标签与快捷键
	// 自己给，免得系统没本地化时冒出英文。
	add("menu.cut", "剪切").SetRole(application.Cut).SetAccelerator("CmdOrCtrl+x")
	add("menu.copy", "复制").SetRole(application.Copy).SetAccelerator("CmdOrCtrl+c")
	add("menu.paste", "粘贴").SetRole(application.Paste).SetAccelerator("CmdOrCtrl+v")
	add("menu.selectAll", "全选").SetRole(application.SelectAll).SetAccelerator("CmdOrCtrl+a")
	menu.AddSeparator()
	add("menu.inspect", "检查元素").OnClick(func(ctx *application.Context) {
		if win := resolve(ctx.ContextMenuData()); win != nil {
			win.OpenDevTools()
		}
	})
	return func() {
		for _, entry := range entries {
			entry.item.SetLabel(manager.T(entry.key, entry.fallback))
		}
		menu.Update()
	}
}

// langMenu 是语言单选菜单：菜单项完全由打包进来的词表决定，加一门语言只要
// 加一个 toml 文件，这里不用改。
type langMenu struct {
	menu  *application.ContextMenu
	items map[string]*application.MenuItem
}

func newLangMenu(bundle *i18n.Bundle, apply func(string)) *langMenu {
	result := &langMenu{menu: application.NewContextMenu(langMenuID), items: map[string]*application.MenuItem{}}
	for _, code := range bundle.Codes() {
		target := code
		result.items[code] = result.menu.AddRadio(bundle.Label(code), false).
			OnClick(func(*application.Context) { apply(target) })
	}
	return result
}

// open 先把勾选同步成当前语言，再在 (x, y) 处弹出菜单。
func (l *langMenu) open(win *application.WebviewWindow, windowName string, lang string, x, y int) {
	if win == nil || len(l.items) == 0 {
		return
	}
	for code, item := range l.items {
		item.SetChecked(code == lang)
	}
	win.OpenContextMenu(&application.ContextMenuData{
		Id:   langMenuID,
		X:    x,
		Y:    y,
		Data: windowName,
	})
}

// menuWindow 把菜单请求的来源窗口名映射成窗口对象：工作台里的按钮要把菜单弹在
// 工作台自己上面（坐标是它自己 webview 的 CSS 像素），工作台没开时退回主窗口。
func menuWindow(lookup func(string) *application.WebviewWindow, source string) (*application.WebviewWindow, string) {
	if source == "workbench" {
		if win := lookup(workbenchName); win != nil {
			return win, workbenchName
		}
	}
	return lookup(mainWindowName), mainWindowName
}

// commandPoint 取出页面回调携带的 webview 坐标：CSS 像素、原点左上角，
// 与 NSMenu 的 popUpMenuPositioningItem:atLocation:inView: 用的是同一套。
func commandPoint(args map[string]any) (int, int) {
	x, y := 0, 0
	if value, ok := args["x"].(float64); ok {
		x = int(value)
	}
	if value, ok := args["y"].(float64); ok {
		y = int(value)
	}
	return x, y
}

// themeMenuLabels 是主题菜单里三档的显示名，与会话页注入按钮的文案保持一致。
var themeMenuLabels = map[theme.Mode]string{
	theme.Light:  "明亮",
	theme.Dark:   "暗黑",
	theme.System: "跟随系统",
}

// themeMenu 是「明亮 / 暗黑 / 跟随系统」单选菜单。会话页顶栏的主题按钮与主窗口
// 的主题按钮共用同一份，两处弹出来的东西完全一致。
type themeMenu struct {
	menu  *application.ContextMenu
	items map[theme.Mode]*application.MenuItem
}

func newThemeMenu(apply func(theme.Mode)) *themeMenu {
	menu := application.NewContextMenu(themeMenuID)
	result := &themeMenu{menu: menu, items: make(map[theme.Mode]*application.MenuItem, 3)}
	for _, mode := range []theme.Mode{theme.Light, theme.Dark, theme.System} {
		target := mode
		result.items[mode] = menu.AddRadio(themeMenuLabels[mode], false).
			OnClick(func(*application.Context) { apply(target) })
	}
	return result
}

// open 先把勾选同步成当前模式，再在 (x, y) 处弹出菜单。
func (t *themeMenu) open(win *application.WebviewWindow, windowName string, mode theme.Mode, x, y int) {
	if win == nil {
		return
	}
	// 单选组的联动依赖 Menu.Update 里的 processRadioGroups，而菜单是在应用
	// Run 之前建的，那一步没跑到，所以三项的勾选状态每次都由这里显式刷。
	for itemMode, item := range t.items {
		item.SetChecked(itemMode == mode)
	}
	win.OpenContextMenu(&application.ContextMenuData{
		Id:   themeMenuID,
		X:    x,
		Y:    y,
		Data: windowName,
	})
}

// rgbaOf 把主题底色转成 Wails 的颜色类型。
func rgbaOf(colour theme.RGB) application.RGBA {
	return application.RGBA{Red: colour.R, Green: colour.G, Blue: colour.B, Alpha: 255}
}

// applyWebviewBackgroundSoon 等窗口真正建出来再涂 webview 底色。Wails 要到
// app.Run() 之后才创建原生窗口，之前 NativeWindow() 一直是 nil，而底色必须
// 赶在页面第一帧之前生效，所以这里短轮询等句柄（最多 3 秒）。
func applyWebviewBackgroundSoon(win *application.WebviewWindow, colour func() theme.RGB) {
	go func() {
		for attempt := 0; attempt < 300; attempt++ {
			if handle := win.NativeWindow(); handle != nil {
				theme.ApplyWebviewBackground(handle, colour())
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
}

// themePath 是全局外观模式的持久化位置。
func themePath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "theme.txt")
}

// 会话页重载期间留给 watcher 的安静窗口。sessionQuietWindow 是上限（页面万一
// 起不来，超过它 watcher 就恢复正常工作，该弹登录窗照样弹）；页面一旦 announce
// 就绪，settled 会把它收短到 sessionReadyGrace，刚好盖过 watcher 的下一轮轮询。
const (
	sessionQuietWindow = 20 * time.Second
	sessionReadyGrace  = 6 * time.Second
)

// langNavigator 决定一次语言切换要不要真的重载会话页。
//
// 会话页既是内嵌的 Google 界面，也是唯一的数据通道：整页重载要几秒，期间桥接
// 不可用。而它平时是藏着的，用户根本看不见它是哪门语言。所以语言没变就不重载，
// 窗口藏着就先把这笔账欠着，等它真露出面再补——主窗口这边只换文案，全程无感。
type langNavigator struct {
	mu       sync.Mutex
	page     string // 会话页当前实际加载的语言
	want     string // 用户选定的语言
	quietTil time.Time
}

func newLangNavigator(lang string) *langNavigator {
	return &langNavigator{page: lang, want: lang}
}

// request 记录用户选的语言，返回需要立刻导航的地址；空串表示这次不用重载。
func (n *langNavigator) request(code string, visible func() bool) string {
	n.mu.Lock()
	n.want = code
	n.mu.Unlock()
	return n.owed(visible)
}

// owed 结清欠着的重载：语言已经一致、或窗口还藏着，都返回空串。visible 只在
// 确实欠账时才调用——启动时 OnChange 会先回调一次，那会儿窗口还没建好，问它
// 可见性会把主线程卡住。
func (n *langNavigator) owed(visible func() bool) string {
	if !n.waiting() {
		return ""
	}
	if visible != nil && !visible() {
		return ""
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.want == n.page {
		return ""
	}
	url := tasksURLFor(n.want)
	n.page = n.want
	n.quietTil = time.Now().Add(sessionQuietWindow)
	return url
}

// waiting 报告是否还欠着一次重载，兜底轮询用它跳过无谓的窗口可见性查询。
func (n *langNavigator) waiting() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.want != n.page
}

// settled 在页面 announce 就绪后调用：重载已经落地，把安静期收短。
func (n *langNavigator) settled() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if time.Now().Before(n.quietTil) {
		n.quietTil = time.Now().Add(sessionReadyGrace)
	}
}

// isQuiet 报告 watcher 现在是否该保持安静：重载引起的 ready 抖动不要去
// Show/Hide 会话窗，否则在主窗口里点一下语言，会话窗会突然弹到脸上。
func (n *langNavigator) isQuiet() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return time.Now().Before(n.quietTil)
}

// googleHL 把词表语言码映射成 Google 的界面语言参数值；没收录的语言直接把
// 语言码原样传给 hl（Google 不认时会回落到账号语言，不会报错）。
var googleHL = map[string]string{
	"zh": "zh-CN",
	"jp": "ja",
}

// tasksURLFor 给会话页地址挂上界面语言参数，让内嵌的 Google 页面跟应用语言走。
func tasksURLFor(lang string) string {
	hl, ok := googleHL[lang]
	if !ok {
		hl = lang
	}
	// hl 挂在 /tasks/ 路径上：根路径的重定向会把查询参数丢掉。
	return googleTasksURL + "tasks/?hl=" + hl
}

// langPath 是界面语言的持久化位置，与主题同一个数据目录。
func langPath(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "lang.txt")
}

// loadRegistry reads the persisted rpcid registry, falling back to the bundled
// catalogue of known-but-undiscovered operations.
func loadRegistry(dataDir string) *tasks.Registry {
	if dataDir == "" {
		registry, err := tasks.Default()
		if err != nil {
			log.Fatalf("无法加载内置操作表: %v", err)
		}
		return registry
	}
	registry, err := tasks.LoadFrom(api.OperationsPath(dataDir))
	if err != nil {
		log.Printf("读取操作表失败，改用内置版本: %v", err)
		registry, err = tasks.Seeded(api.OperationsPath(dataDir))
		if err != nil {
			log.Fatalf("无法加载内置操作表: %v", err)
		}
	}
	return registry
}

// buildAssetHandler serves the API, the Wails runtime bundle and the frontend.
func buildAssetHandler(apiHandler http.Handler) http.Handler {
	frontend, err := fs.Sub(frontendAssets, "frontend")
	if err != nil {
		log.Fatalf("无法读取内嵌前端资源: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler)
	// BundledAssetFileServer owns /wails/runtime.js, which the frontend loads so
	// that Wails events and window controls keep working.
	mux.Handle("/wails/", application.BundledAssetFileServer(frontendAssets))
	mux.Handle("/", http.FileServerFS(frontend))
	return mux
}
