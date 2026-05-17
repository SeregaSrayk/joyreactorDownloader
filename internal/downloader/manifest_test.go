package downloader

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Has("anything") {
		t.Error("fresh manifest should be empty")
	}

	if err := m.Add("attr-1", ManifestEntry{File: "a.jpg", URL: "u1", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := m.Add("attr-2", ManifestEntry{File: "b.png", URL: "u2", At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if !m.Has("attr-1") || !m.Has("attr-2") {
		t.Error("entries not retained")
	}

	// File on disk.
	if _, err := os.Stat(filepath.Join(dir, manifestFile)); err != nil {
		t.Fatalf("manifest file not written: %v", err)
	}

	// Reload.
	m2, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !m2.Has("attr-1") || !m2.Has("attr-2") {
		t.Error("entries not persisted")
	}
	if got := m2.Entries["attr-1"].File; got != "a.jpg" {
		t.Errorf("entry data lost: file=%q", got)
	}
}

func TestManifest_NilSafe(t *testing.T) {
	var m *Manifest
	if m.Has("x") {
		t.Error("nil.Has should be false")
	}
	if err := m.Add("x", ManifestEntry{}); err != nil {
		t.Errorf("nil.Add should be no-op, got %v", err)
	}
}
