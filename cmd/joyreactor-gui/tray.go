package main

import (
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

// trayController owns the system tray icon + menu and bridges its events
// into Wails window actions on the GUI struct. Created once at startup and
// reused for the app's lifetime; reflects the current "active jobs" count
// in its tooltip so the user knows what's happening when the window is
// hidden.
type trayController struct {
	gui  *GUI
	stop func()

	mu       sync.Mutex
	started  bool
	quitting bool // set true when user picked "Выход" — distinguishes
	// graceful tray-quit from window-X (which we want to hide-to-tray).
}

func newTrayController(g *GUI) *trayController {
	return &trayController{gui: g}
}

// pickIcon returns the platform-appropriate icon bytes for the tray
// renderer. fyne.io/systray expects .ico on Windows and .png elsewhere.
func pickIcon() []byte {
	if runtime.GOOS == "windows" {
		return trayIconWin
	}
	return trayIconPng
}

// Start initializes the tray icon. Uses RunWithExternalLoop so the call
// returns immediately and the Wails main loop can keep running on the
// main goroutine. Safe to call multiple times — subsequent calls are no-op.
func (t *trayController) Start() {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return
	}
	t.started = true
	t.mu.Unlock()

	onReady := func() {
		systray.SetIcon(pickIcon())
		systray.SetTitle("Joyreactor Downloader")
		systray.SetTooltip("Joyreactor Downloader")

		// Left-click on the tray icon shows the window — that's the standard
		// Windows tray-app behaviour ("click to bring app back to front").
		// Right-click still opens the menu (handled by systray defaults).
		systray.SetOnTapped(t.showWindow)

		mShow := systray.AddMenuItem("Показать окно", "Развернуть основное окно")
		systray.AddSeparator()
		mQuit := systray.AddMenuItem("Выход", "Закрыть приложение")

		go func() {
			for {
				select {
				case <-mShow.ClickedCh:
					t.showWindow()
				case <-mQuit.ClickedCh:
					t.mu.Lock()
					t.quitting = true
					t.mu.Unlock()
					if t.gui.ctx != nil {
						wailsruntime.Quit(t.gui.ctx)
					}
					return
				}
			}
		}()
	}
	onExit := func() {}
	start, stop := systray.RunWithExternalLoop(onReady, onExit)
	t.stop = stop
	start()
}

// Stop tears down the tray. Called from the Wails OnShutdown hook so the
// tray icon disappears as soon as the app exits.
func (t *trayController) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started || t.stop == nil {
		return
	}
	t.stop()
	t.stop = nil
	t.started = false
}

// IsQuitting reports whether the user clicked "Выход" in the tray menu.
// Used by the window-close hook to decide between "hide to tray" and
// "actually quit the process".
func (t *trayController) IsQuitting() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.quitting
}

// showWindow brings the main window back from a hidden / minimized state
// and focuses it. Called from the tray "Показать окно" menu item and the
// left-click handler on the tray icon. The AlwaysOnTop flicker is the
// documented Wails trick to force focus on Windows; without it WindowShow
// only restores the window to its previous z-order, which may still be
// behind everything else.
func (t *trayController) showWindow() {
	if t.gui == nil || t.gui.ctx == nil {
		return
	}
	wailsruntime.WindowShow(t.gui.ctx)
	wailsruntime.WindowUnminimise(t.gui.ctx)
	wailsruntime.WindowSetAlwaysOnTop(t.gui.ctx, true)
	wailsruntime.WindowSetAlwaysOnTop(t.gui.ctx, false)
}
