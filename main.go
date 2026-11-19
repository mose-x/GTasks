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
	"net/http"
	"runtime"

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
		if err := recorder.SetPersistence(api.CapturesPath(dataDir)); err != nil {
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
		Name:   sessionWindowName,
		Title:  "Google 会话（登录 / RPC 通道）",
		URL:    googleTasksURL,
		Width:  460,
		Height: 760,
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
	watcher.SetChangeHandler(func(current, previous session.Status) {
		if !watcher.AutoManage() {
			return
		}
		switch {
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
	watcher.Start()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
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
