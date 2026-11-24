package main

import (
	"embed"
	"log"
	"runtime"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/trayTemplate.png
var trayTemplate []byte

//go:embed build/appicon.png
var appIcon []byte

func main() {
	ensureUserPath()

	devices := &DeviceService{}
	var cleaned sync.Once
	cleanup := func() {
		cleaned.Do(func() {
			devices.Cleanup()
		})
	}
	defer cleanup()

	app := application.New(application.Options{
		Name:        "serial-tool",
		Description: "serial-tool",
		Services: []application.Service{
			application.NewService(devices),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyRegular,
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		OnShutdown: cleanup,
	})

	go func() {
		<-notifyQuitSignals()
		cleanup()
		app.Quit()
	}()

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "serial-tool",
		Width:  380,
		Height: 520,
		Mac: application.MacWindow{
			Backdrop: application.MacBackdropNormal,
			TitleBar: application.MacTitleBarHidden,
		},
		Windows: application.WindowsWindow{
			HiddenOnTaskbar: false,
		},
		BackgroundColour: application.NewRGB(255, 255, 255),
		URL:              "/",
	})

	showWin := func() {
		application.InvokeSync(func() {
			setDockVisible(true)
			win.Show()
			win.Focus()
		})
	}
	hideWin := func() {
		application.InvokeSync(func() {
			win.Hide()
			setDockVisible(false)
		})
	}

	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		e.Cancel()
		hideWin()
	})
	win.OnWindowEvent(events.Common.WindowFocus, func(*application.WindowEvent) {
		syncTrafficLights(win, true)
	})
	win.OnWindowEvent(events.Common.WindowLostFocus, func(*application.WindowEvent) {
		syncTrafficLights(win, false)
	})

	tray := app.SystemTray.New()
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(trayTemplate)
	} else {
		tray.SetIcon(appIcon)
	}
	tray.SetTooltip("serial-tool")
	tray.OnClick(func() {
		showWin()
	})

	menu := app.NewMenu()
	menu.Add("显示窗口").OnClick(func(*application.Context) {
		showWin()
	})
	menu.Add("停止全部设备").OnClick(func(*application.Context) {
		devices.StopAll()
	})
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) {
		cleanup()
		app.Quit()
	})
	tray.SetMenu(menu)

	if err := app.Run(); err != nil {
		cleanup()
		log.Fatal(err)
	}
}
