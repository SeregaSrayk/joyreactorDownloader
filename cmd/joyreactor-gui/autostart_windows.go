package main

import (
	"golang.org/x/sys/windows/registry"
)

// Windows autostart via the per-user Run key. HKCU avoids needing
// administrator privileges (HKLM\...\Run requires admin) and applies only
// to the current user — which is the right default for a personal
// downloader, not a system service.
const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// syncAutostart writes / removes the registry value that makes Windows
// launch the app at user login. Idempotent.
func syncAutostart(enabled bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if enabled {
		// Quote the exe path so any spaces in "Program Files" don't break
		// the launcher. --autostart hint lets the app's main() detect
		// whether it was kicked by autostart vs a normal double-click,
		// which we use to decide whether to honour StartMinimized.
		val := `"` + resolveExe() + `" --autostart`
		return k.SetStringValue(autostartName, val)
	}
	// Disable: best-effort delete. ErrNotExist means already gone — OK.
	if err := k.DeleteValue(autostartName); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}
