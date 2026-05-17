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
}

func defaultAppSettings() AppSettings {
	return AppSettings{ManifestMode: "per-folder"}
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
