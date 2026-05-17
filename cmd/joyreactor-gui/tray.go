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
		systray.SetOnTapped(func() { restoreFromTray(getCtx()) })

		go func() {
			for {
				select {
				case <-showItem.ClickedCh:
					restoreFromTray(getCtx())
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

// restoreFromTray brings the window back from a hidden state and forces
// it to the size/maximize state the user configured. Without re-applying
// these settings here, Wails's WindowShow restores the window with the
// initial size from wails.Run() options — which means a user who had
// maximized or resized the window before sending it to tray gets a tiny
// default window back instead of what they expect.
//
// AlwaysOnTop flicker is the documented Wails trick to force focus on
// Windows — true→false brings the window to the front of the z-order
// without leaving it pinned on top.
func restoreFromTray(ctx context.Context) {
	if ctx == nil {
		return
	}
	wailsruntime.WindowShow(ctx)
	wailsruntime.WindowUnminimise(ctx)
	ws := loadWindowSettings()
	if ws.Maximized {
		wailsruntime.WindowMaximise(ctx)
	} else {
		wailsruntime.WindowUnmaximise(ctx)
		if ws.Width > 0 && ws.Height > 0 {
			wailsruntime.WindowSetSize(ctx, ws.Width, ws.Height)
		}
	}
	wailsruntime.WindowSetAlwaysOnTop(ctx, true)
	wailsruntime.WindowSetAlwaysOnTop(ctx, false)
}
