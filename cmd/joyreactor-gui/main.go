package main

import (
	"context"
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	gui := NewGUI()
	ws := loadWindowSettings()

	startState := options.Normal
	if ws.Maximized {
		startState = options.Maximised
	}

	// Tray icon must be started on its own goroutine BEFORE wails.Run()
	// — fyne.io/systray creates a hidden window for its message pump that
	// must live on the same OS thread as the goroutine reading messages.
	// Starting the tray inside the Wails OnStartup callback puts the HWND
	// on a different thread and the click events disappear silently
	// (verified empirically; the weightCounter app uses this same layout).
	go startSystray(
		func() context.Context { return gui.ctx },
		func() {
			if gui.ctx != nil {
				wailsruntime.Quit(gui.ctx)
			}
		},
	)

	err := wails.Run(&options.App{
		Title:            "Joyreactor Downloader",
		Width:            ws.Width,
		Height:           ws.Height,
		WindowStartState: startState,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: proxyHandler(),
		},
		BackgroundColour: &options.RGBA{R: 24, G: 24, B: 28, A: 1},
		// Right-click anywhere uses WebView2's native context menu
		// (Copy / Save image as / Open link in browser / …). Off by default
		// in Wails production builds; we want it on so the user can grab
		// individual images from the preview overlay or comments.
		EnableDefaultContextMenu: true,
		OnStartup:                gui.startup,
		OnShutdown:               gui.shutdown,
		OnBeforeClose:            gui.onBeforeClose,
		Bind: []any{
			gui,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
		},
		// Block double-launches: the second exe sends a signal here, then
		// exits. We use that signal to surface the existing window so the
		// user notices their app is already running.
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "joyreactor-downloader-3a8b21f6",
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				if gui.ctx == nil {
					return
				}
				wailsruntime.WindowUnminimise(gui.ctx)
				wailsruntime.WindowShow(gui.ctx)
				// AlwaysOnTop flicker is the documented Wails trick to force
				// focus on Windows — true→false leaves the window normal but
				// brings it to the front of the z-order.
				wailsruntime.WindowSetAlwaysOnTop(gui.ctx, true)
				wailsruntime.WindowSetAlwaysOnTop(gui.ctx, false)
			},
		},
	})
	if err != nil {
		println("wails error:", err.Error())
	}
	stopSystray()
}
