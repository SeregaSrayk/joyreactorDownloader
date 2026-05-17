package main

import (
	"context"
	_ "embed"
	"runtime"
	"sync"

	"fyne.io/systray"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed build/windows/icon.ico
var trayIconWin []byte

//go:embed build/appicon.png
var trayIconPng []byte

// trayState holds the bits the rest of the app needs to query about tray
// behaviour — specifically, "did the user explicitly pick Выход in the
// tray menu?" — so the window-close hook can tell apart "hide to tray"
// from "actually quit".
type trayState struct {
	mu       sync.Mutex
	quitting bool
}

var tray = &trayState{}

// trayIsQuitting reports whether the tray's Выход menu item fired the
// shutdown. The window-close hook reads this to decide hide-vs-quit.
func trayIsQuitting() bool {
	tray.mu.Lock()
	defer tray.mu.Unlock()
	return tray.quitting
}

// pickTrayIcon returns the platform-appropriate icon bytes for the tray
// renderer. fyne.io/systray expects .ico on Windows and .png elsewhere.
func pickTrayIcon() []byte {
	if runtime.GOOS == "windows" {
		return trayIconWin
	}
	return trayIconPng
}

// startSystray runs the tray icon on its own goroutine. Must be invoked
// BEFORE wails.Run() — the working weightCounter app uses this layout,
// and starting the tray after Wails leaves the HWND on the wrong OS
// thread, making WM_LBUTTONUP / WM_RBUTTONUP events vanish.
//
// getCtx returns the Wails runtime context once it's available (nil
// before startup completes); the tray callbacks tolerate a nil ctx by
// no-oping until Wails wires it.
//
// onQuit is invoked when the user picks Выход in the tray menu — it
// should mark quit-intent and tell Wails to shut down.
func startSystray(getCtx func() context.Context, onQuit func()) {
	systray.Run(func() {
		systray.SetIcon(pickTrayIcon())
		systray.SetTitle("Joyreactor Downloader")
		systray.SetTooltip("Joyreactor Downloader")

		showItem := systray.AddMenuItem("Показать окно", "Развернуть основное окно")
		systray.AddSeparator()
		quitItem := systray.AddMenuItem("Выход", "Закрыть приложение")

		// Left-click on the tray icon mirrors the menu's «Показать окно»
		// item — standard Windows tray-app behaviour. Right-click still
		// opens the contextual menu.
		systray.SetOnTapped(func() {
			ctx := getCtx()
			if ctx == nil {
				return
			}
			wailsruntime.WindowShow(ctx)
			wailsruntime.WindowUnminimise(ctx)
			wailsruntime.WindowSetAlwaysOnTop(ctx, true)
			wailsruntime.WindowSetAlwaysOnTop(ctx, false)
		})

		go func() {
			for {
				select {
				case <-showItem.ClickedCh:
					ctx := getCtx()
					if ctx == nil {
						continue
					}
					wailsruntime.WindowShow(ctx)
					wailsruntime.WindowUnminimise(ctx)
					wailsruntime.WindowSetAlwaysOnTop(ctx, true)
					wailsruntime.WindowSetAlwaysOnTop(ctx, false)
				case <-quitItem.ClickedCh:
					tray.mu.Lock()
					tray.quitting = true
					tray.mu.Unlock()
					if onQuit != nil {
						onQuit()
					}
					return
				}
			}
		}()
	}, func() {})
}

// stopSystray tears down the tray icon. Called after wails.Run() returns
// so the icon disappears at process exit. systray.Quit is safe to call
// even if Run hasn't started yet (it does nothing in that case).
func stopSystray() {
	systray.Quit()
}
