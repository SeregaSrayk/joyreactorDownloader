package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const maxAuthors = 1000

// authorStore holds locally-collected author usernames (with frequency counts)
// gathered from the user's past searches. The Joyreactor API exposes no user
// autocomplete endpoint, so we accumulate one organically.
type authorStore struct {
	Version int            `json:"version"`
	Counts  map[string]int `json:"counts"`

	mu   sync.Mutex
	path string
}

func authorsFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "authors.json"
	}
	return filepath.Join(dir, "joyreactorDownloader", "authors.json")
}

func loadAuthors() *authorStore {
	s := &authorStore{Version: 1, Counts: map[string]int{}, path: authorsFilePath()}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, s)
	if s.Counts == nil {
		s.Counts = map[string]int{}
	}
	return s
}

// RecordBatch increments counters for the given names and persists once.
// Empty strings are ignored. Prunes least-frequent entries when over maxAuthors.
func (s *authorStore) RecordBatch(names []string) {
	if len(names) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range names {
		if n == "" {
			continue
		}
		s.Counts[n]++
	}
	if len(s.Counts) > maxAuthors {
		s.pruneLocked()
	}
	_ = s.saveLocked()
}

// Suggest returns up to limit usernames that case-insensitively contain mask,
// ordered by recorded frequency (descending). Empty mask returns nil.
func (s *authorStore) Suggest(mask string, limit int) []string {
	if mask == "" {
		return nil
	}
	maskL := strings.ToLower(mask)
	s.mu.Lock()
	type kv struct {
		name  string
		count int
	}
	var matches []kv
	for name, c := range s.Counts {
		if strings.Contains(strings.ToLower(name), maskL) {
			matches = append(matches, kv{name, c})
		}
	}
	s.mu.Unlock()
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].count != matches[j].count {
			return matches[i].count > matches[j].count
		}
		return matches[i].name < matches[j].name
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.name
	}
	return out
}

// pruneLocked drops the bottom 20% of entries by count. Caller must hold mu.
func (s *authorStore) pruneLocked() {
	type kv struct {
		name  string
		count int
	}
	all := make([]kv, 0, len(s.Counts))
	for n, c := range s.Counts {
		all = append(all, kv{n, c})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].count > all[j].count })
	keep := (maxAuthors * 4) / 5 // 800
	if keep > len(all) {
		keep = len(all)
	}
	next := make(map[string]int, keep)
	for _, kv := range all[:keep] {
		next[kv.name] = kv.count
	}
	s.Counts = next
}

func (s *authorStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := s.path + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
