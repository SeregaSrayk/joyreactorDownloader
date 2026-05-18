package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

	// Socks5Enabled routes GraphQL traffic through a user-provided SOCKS5
	// proxy (typically Tor Browser or a tor daemon running on localhost).
	// CDN downloads stay on clearnet — only metadata requests get tunneled.
	// Off by default; we don't ship Tor, the user installs it themselves.
	Socks5Enabled bool `json:"socks5Enabled,omitempty"`

	// Socks5Addr is the host:port of the local SOCKS5 listener. Tor Browser
	// uses 127.0.0.1:9150, the standalone tor daemon uses 9050.
	Socks5Addr string `json:"socks5Addr,omitempty"`

	// OnionBaseURL is the base URL of a JR .onion mirror (without trailing
	// slash, e.g. http://reactorccdnf...onion). It's used for two things:
	// (a) the TestNetwork probe, (b) DMCA-recovery HTML scraping at
	// "<base>/post/<id>". Empty ⇒ no recovery; clearnet GraphQL stays the
	// only metadata source.
	OnionBaseURL string `json:"onionBaseURL,omitempty"`

	// RecoverDmcaViaOnion, when true AND Socks5Enabled AND OnionBaseURL set,
	// makes Search() try to recover DMCA-stubbed posts by scraping the onion
	// mirror's HTML and downloading the orphaned files from clearnet CDN.
	// Default false — opt-in because it costs an extra Tor round-trip per
	// removed post in the search result.
	RecoverDmcaViaOnion bool `json:"recoverDmcaViaOnion,omitempty"`
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

// DefaultSocks5Addr is the address pre-filled in the settings UI. Tor Browser's
// embedded tor listens here; the standalone tor daemon uses :9050.
const DefaultSocks5Addr = "127.0.0.1:9150"

// DefaultOnionBaseURL is the JR .onion mirror root (no trailing slash). HTML
// scraping appends "/post/<id>". Pre-filled when the user clicks
// "Подставить .onion" in the network settings.
const DefaultOnionBaseURL = "http://reactorccdnf36aqvq34zbfzqyrcrpg3eyhilauovitrvmcjovsujmid.onion"

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
	s.Socks5Enabled = loaded.Socks5Enabled
	s.Socks5Addr = loaded.Socks5Addr
	s.OnionBaseURL = loaded.OnionBaseURL
	s.RecoverDmcaViaOnion = loaded.RecoverDmcaViaOnion
	// Legacy migration: earlier builds called this field GraphQLEndpoint and
	// pre-filled it with "...onion/graphql". The new semantics is "base URL"
	// (no path), so trim the obsolete suffix on first load. Cheap to do
	// unconditionally — clearnet GraphQL URLs also end with /graphql and
	// stripping them just yields the bare api host, which is still wrong
	// to use as a mirror base, but the user can just clear the field.
	s.OnionBaseURL = strings.TrimSuffix(s.OnionBaseURL, "/graphql")
	s.OnionBaseURL = strings.TrimRight(s.OnionBaseURL, "/")
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
