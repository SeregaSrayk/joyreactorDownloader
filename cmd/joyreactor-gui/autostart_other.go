//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// syncAutostart implements OS-level autostart toggling for non-Windows
// platforms. macOS uses a LaunchAgent plist under ~/Library/LaunchAgents,
// Linux uses an XDG .desktop file under ~/.config/autostart.
//
// Both formats are plain text and can be written / removed with stdlib;
// no cgo or external library required.
func syncAutostart(enabled bool) error {
	path, body, err := autostartFile()
	if err != nil {
		return err
	}
	if !enabled {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

func autostartFile() (path, body string, err error) {
	exe := resolveExe()
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	switch runtime.GOOS {
	case "darwin":
		path = filepath.Join(home, "Library", "LaunchAgents", "com.joyreactor.downloader.plist")
		body = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>com.joyreactor.downloader</string>
    <key>ProgramArguments</key>
    <array>
        <string>` + exe + `</string>
        <string>--autostart</string>
    </array>
    <key>RunAtLoad</key><true/>
</dict>
</plist>
`
	case "linux":
		path = filepath.Join(home, ".config", "autostart", autostartName+".desktop")
		body = `[Desktop Entry]
Type=Application
Name=Joyreactor Downloader
Exec=` + exe + ` --autostart
X-GNOME-Autostart-enabled=true
`
	default:
		return "", "", errors.New("autostart not supported on " + runtime.GOOS)
	}
	return path, body, nil
}
