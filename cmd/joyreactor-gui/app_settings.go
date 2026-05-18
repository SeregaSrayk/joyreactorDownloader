package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// AppSettings - application-wide preferences persisted to
// %APPDATA%/joyreactorDownloader/settings.json. Things that DON'T fit
// inside a filter preset (per-preset stuff) or window.json (geometry).
type AppSettings struct {
	// ManifestMode controls where the download manifest lives:
	//   "per-folder" (default): <outDir>/.manifest.json, separate per job folder
	//   "global":               %APPDATA%/joyreactorDownloader/manifest.json
	// Switching modes only affects future jobs; existing per-folder manifests
	// are left alone and existing global one is reused.
	ManifestMode string `json:"manifestMode"`

	// AutoPullIntervalHours is the global cadence for the scheduled-pull
	// feature. Presets opt in individually via Preset.AutoPull; whenever
	// now ≥ Preset.LastAutoPullAt + AutoPullIntervalHours, the scheduler
	// enqueues a fresh job for that preset. Default 24 (once a day).
	// Minimum 1 hour — anything faster would hammer the JR API.
	AutoPullIntervalHours int `json:"autoPullIntervalHours"`

	// Autostart toggles whether the app registers itself to launch on user
	// login (Windows: HKCU\...\Run; macOS: LaunchAgent plist; Linux:
	// ~/.config/autostart/joyreactorDownloader.desktop).
	Autostart bool `json:"autostart"`

	// StartMinimized — when true, the window launches hidden (in the system
	// tray) instead of showing on screen. Useful in combination with
	// Autostart so the app boots into the background without interrupting
	// the user.
	StartMinimized bool `json:"startMinimized"`

	// MinimizeToTrayOnClose — when true, clicking the window close button
	// hides the window to tray instead of quitting the app. The tray icon's
	// "Выход" menu item still quits explicitly.
	MinimizeToTrayOnClose bool `json:"minimizeToTrayOnClose"`

	// HideRemovedPosts — when true (default), DMCA-takedown stubs are filtered
	// out of search-preview results. Pointer so an absent field in legacy
	// settings.json reads as nil ≡ "use default true", letting us flip the
	// default in the future without forcing every existing user back to it.
	HideRemovedPosts *bool `json:"hideRemovedPosts,omitempty"`
}

// HideRemoved resolves the optional pointer to a concrete bool using the
// "default true" rule. Call this everywhere instead of dereferencing the
// pointer manually.
func (s AppSettings) HideRemoved() bool {
	if s.HideRemovedPosts == nil {
		return true
	}
	return *s.HideRemovedPosts
}

func defaultAppSettings() AppSettings {
	return AppSettings{
		ManifestMode:          "per-folder",
		AutoPullIntervalHours: 24,
		Autostart:             false,
		StartMinimized:        false,
		MinimizeToTrayOnClose: false,
	}
}

func appSettingsFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "app-settings.json"
	}
	return filepath.Join(dir, "joyreactorDownloader", "settings.json")
}

// globalManifestPath returns the on-disk location of the shared manifest
// used by the "global" ManifestMode. Lives next to settings.json so a
// fresh install only needs one config dir.
func globalManifestPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "manifest.json"
	}
	return filepath.Join(dir, "joyreactorDownloader", "manifest.json")
}

func loadAppSettings() AppSettings {
	s := defaultAppSettings()
	b, err := os.ReadFile(appSettingsFilePath())
	if err != nil {
		return s
	}
	var loaded AppSettings
	if err := json.Unmarshal(b, &loaded); err != nil {
		return s
	}
	// Validate enum-y fields - reject unknown values so a corrupt file
	// doesn't silently break the manifest path resolution.
	switch loaded.ManifestMode {
	case "per-folder", "global":
		s.ManifestMode = loaded.ManifestMode
	}
	if loaded.AutoPullIntervalHours >= 1 {
		s.AutoPullIntervalHours = loaded.AutoPullIntervalHours
	}
	s.Autostart = loaded.Autostart
	s.StartMinimized = loaded.StartMinimized
	s.MinimizeToTrayOnClose = loaded.MinimizeToTrayOnClose
	s.HideRemovedPosts = loaded.HideRemovedPosts
	return s
}

func saveAppSettings(s AppSettings) error {
	path := appSettingsFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// manifestPathFor resolves the manifest location for the given output
// directory according to the current AppSettings.ManifestMode.
func manifestPathFor(outDir string) string {
	if loadAppSettings().ManifestMode == "global" {
		return globalManifestPath()
	}
	return filepath.Join(outDir, ".manifest.json")
}
