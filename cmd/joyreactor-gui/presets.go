package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Preset stores a named snapshot of filter + download settings, including
// the output directory: each preset has its own download folder.
type Preset struct {
	Query        string   `json:"query"`
	Tags         []string `json:"tags"`
	ExcludeTags  []string `json:"excludeTags"`
	Username     string   `json:"username"`
	MinRating    *int     `json:"minRating,omitempty"`
	MaxRating    *int     `json:"maxRating,omitempty"`
	Sort         string   `json:"sort"`
	ShowNsfw     bool     `json:"showNsfw"`
	OnlyNsfw     bool     `json:"onlyNsfw"`
	ShowUnsafe   bool     `json:"showUnsafe"`
	OnlyFavorite bool     `json:"onlyFavorite"`
	MediaKinds   []string `json:"mediaKinds"`
	// Legacy single-kind field — migrated to MediaKinds on load.
	MediaKindLegacy string `json:"mediaKind,omitempty"`
	MinWidth        int    `json:"minWidth"`
	MinHeight       int    `json:"minHeight"`
	DateFrom        string `json:"dateFrom"`
	DateTo          string `json:"dateTo"`
	Limit           int    `json:"limit"`
	Workers         int    `json:"workers"`
	PageFrom        int    `json:"pageFrom,omitempty"`
	PageTo          int    `json:"pageTo,omitempty"`
	OutDir          string `json:"outDir,omitempty"`

	// AutoPull opts this preset into the background scheduler. When true,
	// the scheduler enqueues a job for this preset whenever the global
	// interval (AppSettings.AutoPullIntervalHours) has elapsed since
	// LastAutoPullAt. Default false (no schedule).
	AutoPull bool `json:"autoPull,omitempty"`

	// LastAutoPullAt is the timestamp at which the scheduler last STARTED
	// (not finished) an auto-pull job for this preset. Updated when the
	// job is enqueued — this way a crash mid-download doesn't cause an
	// immediate retry on next launch; we wait the full interval again.
	LastAutoPullAt time.Time `json:"lastAutoPullAt,omitempty"`
}

type presetStore struct {
	Version int               `json:"version"`
	Presets map[string]Preset `json:"presets"`

	mu   sync.Mutex
	path string
}

func presetsFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "presets.json"
	}
	return filepath.Join(dir, "joyreactorDownloader", "presets.json")
}

func loadPresets() *presetStore {
	s := &presetStore{Version: 1, Presets: map[string]Preset{}, path: presetsFilePath()}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, s)
	if s.Presets == nil {
		s.Presets = map[string]Preset{}
	}
	for name, p := range s.Presets {
		if len(p.MediaKinds) == 0 && p.MediaKindLegacy != "" && p.MediaKindLegacy != "any" {
			p.MediaKinds = []string{p.MediaKindLegacy}
		}
		p.MediaKindLegacy = ""
		s.Presets[name] = p
	}
	return s
}

func (s *presetStore) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.Presets))
	for k := range s.Presets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *presetStore) Get(name string) (Preset, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.Presets[name]
	return p, ok
}

func (s *presetStore) Save(name string, p Preset) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("пустое имя пресета")
	}
	s.mu.Lock()
	s.Presets[name] = p
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

func (s *presetStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Presets[name]; !ok {
		return errors.New("пресет не найден")
	}
	delete(s.Presets, name)
	return s.saveLocked()
}

// All returns a snapshot of all presets keyed by name. Used by the
// scheduler to walk through opted-in presets without holding the lock
// for the duration of a job spawn.
func (s *presetStore) All() map[string]Preset {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Preset, len(s.Presets))
	for k, v := range s.Presets {
		out[k] = v
	}
	return out
}

// SetAutoPull toggles the AutoPull flag for a preset without rewriting
// the other fields. Used by the GUI checkbox in the preset row.
func (s *presetStore) SetAutoPull(name string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.Presets[name]
	if !ok {
		return errors.New("пресет не найден")
	}
	p.AutoPull = on
	s.Presets[name] = p
	return s.saveLocked()
}

// MarkAutoPullStarted records the scheduler's intent to fire a job for
// this preset right now. Updates LastAutoPullAt, so the next eligibility
// check waits the full interval again — even if the job ends up failing
// or being cancelled.
func (s *presetStore) MarkAutoPullStarted(name string, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.Presets[name]
	if !ok {
		return errors.New("пресет не найден")
	}
	p.LastAutoPullAt = t
	s.Presets[name] = p
	return s.saveLocked()
}

func (s *presetStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
