// Command gtasks is a Google Tasks desktop client built on Wails v3.
//
// The UI is served from the bundled asset server. All Google traffic runs
// inside a second webview that is loaded from tasks.google.com itself, so the
// user authenticates through Google's own pages and the internal batchexecute
// RPCs are issued from that origin with the real session cookies attached.
package main

import (
	"embed"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"gtasks/internal/api"
	"gtasks/internal/batchexec"
	"gtasks/internal/bridge"
	"gtasks/internal/session"
	"gtasks/internal/tasks"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend
var frontendAssets embed.FS

const (
	mainWindowName    = "main"
	workbenchName     = "workbench"
	sessionWindowName = "gsession"
	googleTasksURL    = "https://tasks.google.com/"
	captureRingLimit  = 3000
)

func main() {
	dataDir, err := api.DataDir()
	if err != nil {
		log.Printf("无法定位数据目录，抓包日志与操作表将只存在内存中: %v", err)
	}

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

	mainWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      mainWindowName,
		Title:     "Google Tasks",
		URL:       "/",
		Width:     1180,
		Height:    780,
		MinWidth:  860,
		MinHeight: 540,
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      workbenchName,
		Title:     "GTasks 逆向工作台",
		URL:       "/workbench.html",
		Width:     1240,
		Height:    820,
		MinWidth:  900,
		MinHeight: 560,
	})

	sessionOptions := application.WebviewWindowOptions{
		Name:  sessionWindowName,
		Title: "Google 会话（登录 / RPC 通道）",
		URL:   googleTasksURL,
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
	}
	if runtime.GOOS == "windows" {
		// The Windows backend never applies WebviewWindowOptions.JS to a window
		// created from an external URL, so start from a stub that redirects and
		// let it register the bootstrap for the whole webview lifetime instead.
		sessionOptions.URL = ""
		sessionOptions.HTML = bridge.RedirectHTML(googleTasksURL)
	}
	sessionWindow := app.Window.NewWithOptions(sessionOptions)
	sessionBridge.Bind(sessionWindow)
	apiHandler.BindWindows(mainWindow, sessionWindow)

	// Show the login window only while it is actually needed.
	var lastRecover time.Time
	watcher.SetChangeHandler(func(current, previous session.Status) {
		if !watcher.AutoManage() {
			return
		}
		switch {
		case current.Expired && !previous.Expired:
			// The cookies or the at token died mid-use while the page still
			// looks signed in. Surface the window and reload it so Google's
			// own login flow takes over; rate-limit the reload so a page that
			// keeps failing cannot loop.
			sessionWindow.Show()
			if time.Since(lastRecover) > 3*time.Minute {
				lastRecover = time.Now()
				sessionWindow.Reload()
			}
		case previous.Expired && !current.Expired && current.LoggedIn:
			sessionWindow.Hide()
		case current.LoggedIn && !previous.LoggedIn:
			sessionWindow.Hide()
		case !current.LoggedIn && current.Ready:
			sessionWindow.Show()
		case !current.Ready && previous.Ready:
			// Navigation in progress or the page died; bring it back so the
			// user can see what Google is showing.
			sessionWindow.Show()
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
