package main

// Cross-platform autostart toggle without cgo. Each platform has its own
// file (autostart_*.go); this file holds the platform-agnostic façade.

import "os"

// autostartName is the identifier used by the OS to track our autostart
// entry. Stable across versions so toggling off via Settings reliably
// finds the same key/file the previous install created.
const autostartName = "joyreactorDownloader"

// resolveExe returns the absolute path to the running executable.
// Falls back to os.Args[0] if Executable() fails (rare; mostly when the
// binary was deleted while running).
func resolveExe() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return os.Args[0]
	}
	return exe
}
