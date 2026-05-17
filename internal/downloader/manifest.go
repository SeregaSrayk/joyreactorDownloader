package downloader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const manifestFile = ".manifest.json"

// ManifestEntry records that a file was downloaded for a particular key (typically
// the PostAttributePicture Relay ID).
type ManifestEntry struct {
	File string    `json:"file"`
	URL  string    `json:"url"`
	At   time.Time `json:"at"`
}

// Manifest tracks already-downloaded items in an output directory so the
// downloader can skip them on subsequent runs even if files were moved/renamed.
// Persisted as <outDir>/.manifest.json.
type Manifest struct {
	Version int                      `json:"version"`
	Entries map[string]ManifestEntry `json:"entries"`

	mu   sync.Mutex
	path string
}

// LoadManifest reads outDir/.manifest.json if it exists, or returns an empty
// one. Per-folder dedup: each download folder has its own manifest of
// already-saved attribute IDs.
func LoadManifest(outDir string) (*Manifest, error) {
	return LoadManifestFile(filepath.Join(outDir, manifestFile))
}

// LoadManifestFile reads a manifest from an explicit path. Used for the
// "global manifest" mode where all jobs share a single manifest stored
// in the app's config dir (per-folder mode just delegates here with the
// outDir-joined path).
func LoadManifestFile(path string) (*Manifest, error) {
	m := &Manifest{Version: 1, Entries: map[string]ManifestEntry{}, path: path}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return m, err
	}
	if err := json.Unmarshal(b, m); err != nil {
		return m, err
	}
	if m.Entries == nil {
		m.Entries = map[string]ManifestEntry{}
	}
	return m, nil
}

// Has reports whether the manifest already contains an entry for key.
func (m *Manifest) Has(key string) bool {
	if m == nil || key == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.Entries[key]
	return ok
}

// Add records an entry and persists the manifest atomically.
func (m *Manifest) Add(key string, e ManifestEntry) error {
	if m == nil || key == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Entries[key] = e
	return m.saveLocked()
}

func (m *Manifest) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}
