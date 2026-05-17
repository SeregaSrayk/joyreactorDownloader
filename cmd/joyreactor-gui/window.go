package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// WindowSettings is the user-tunable bit of the Wails window — applied at
// startup (via main.go) and on demand (via the Settings modal). Width/Height
// are honoured only when Maximized=false; in maximized mode they're still
// stored so toggling maximized off restores the prior size.
//
// "Maximized" (fills the work area, taskbar stays visible) is what the user
// asked for, not "fullscreen" (which hides the OS chrome).
type WindowSettings struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximized bool `json:"maximized"`
}

// defaultWindowSettings — what we ship with for a fresh install.
func defaultWindowSettings() WindowSettings {
	return WindowSettings{Width: 1180, Height: 820, Maximized: false}
}

func windowSettingsFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "window.json"
	}
	return filepath.Join(dir, "joyreactorDownloader", "window.json")
}

// loadWindowSettings reads %APPDATA%/joyreactorDownloader/window.json or returns
// defaults if the file is missing/corrupt. Width/Height below sensible
// minimums get clamped — a 50x50 window after an accidental save would
// otherwise be near-impossible to drag.
//
// Honours a legacy `fullscreen` key from earlier builds: that toggle was
// originally meant as "maximize", so we read it as Maximized when no explicit
// maximized field is present.
func loadWindowSettings() WindowSettings {
	ws := defaultWindowSettings()
	b, err := os.ReadFile(windowSettingsFilePath())
	if err != nil {
		return ws
	}
	var loaded struct {
		Width      int   `json:"width"`
		Height     int   `json:"height"`
		Maximized  *bool `json:"maximized,omitempty"`
		Fullscreen *bool `json:"fullscreen,omitempty"`
	}
	if err := json.Unmarshal(b, &loaded); err != nil {
		return ws
	}
	out := WindowSettings{Width: loaded.Width, Height: loaded.Height}
	switch {
	case loaded.Maximized != nil:
		out.Maximized = *loaded.Maximized
	case loaded.Fullscreen != nil:
		out.Maximized = *loaded.Fullscreen
	}
	if out.Width < 600 {
		out.Width = ws.Width
	}
	if out.Height < 400 {
		out.Height = ws.Height
	}
	return out
}

func saveWindowSettings(ws WindowSettings) error {
	path := windowSettingsFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
