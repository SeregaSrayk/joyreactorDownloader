package downloader

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseAttrID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"id format", "12345_67890.jpg", "67890", true},
		{"tags format", "[art][cat]_12345_67890.jpg", "67890", true},
		{"single tag", "[art]_111_222.png", "222", true},
		{"cyrillic tag", "[арт][котик]_999_1000.gif", "1000", true},
		{"webp", "1_2.webp", "2", true},
		{"uppercase ext", "1_2.JPG", "2", true},
		{"seo format", "easonx-artist-Ayase-Seiko-9047178.jpeg", "9047178", true},
		{"seo with underscore in slug", "Aelion_Draws-Ayase-Seiko-Dandadan-8693046.jpeg", "8693046", true},
		{"seo with unicode tag", "Gōfā-artist-Ayase-Seiko-9332425.jpeg", "9332425", true},
		{"seo plain", "Ayase-Seiko-Dandadan-Anime-9006715.jpeg", "9006715", true},
		{"complex tags with parens and cyrillic", "[Classic (PA)][RupertEverton][Порно-комиксы без слов]_1199849_1104634.jpeg", "1104634", true},
		{"joysave default", "12345_0_000067890__art-cat-dog.jpeg", "67890", true},
		{"joysave no tags", "12345_0_000067890__.jpeg", "67890", true},
		{"joysave comment variant", "12345_1_000067890__art.jpeg", "67890", true},
		{"joysave sanitised char", "12345_0_000067890__art@cat.jpeg", "67890", true},
		{"joysave id ending in zeros not mistaken for padding", "12345_0_000010000__tag.png", "10000", true},
		{"joysave seo-shaped trailing tag still matches joysave", "12345_0_000067890__art-Tag-2025.jpeg", "67890", true},
		{"joysave with cyrillic tag", "12345_0_000067890__арт-котик.gif", "67890", true},
		{"no ext", "12345_67890", "", false},
		{"trailing junk", "12345_67890.jpg.bak", "", false},
		{"non-numeric tail", "foo_bar.jpg", "", false},
		{"only one number", "12345.jpg", "", false},
		{"concatenated digits", "random12345_67890.jpg", "", false},
		{"part file", "12345_67890.jpg.part", "", false},
		{"manifest", ".manifest.json", "", false},
		{"phone camera IMG", "IMG_20260502_131119_047.jpg", "", false},
		{"reddit downloader RDT", "RDT_20260122_1229278534986972765339303.jpg", "", false},
		{"date-named file", "2025-01-15.jpg", "", false},
		{"non-image extension", "12345_67890.txt", "", false},
		{"document with seo-like name", "report-2025.pdf", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAttrID(tc.in)
			if ok != tc.ok || got != tc.want {
				t.Errorf("parseAttrID(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRebuildFromDir(t *testing.T) {
	root := t.TempDir()

	// Files in two formats + noise that must be ignored.
	files := map[string]bool{
		"12345_67890.jpg":              true,  // id format
		"[art][cat]_111_222.png":       true,  // tags format
		"sub/[doge]_333_444.gif":       true,  // nested
		"sub/deep/555_666.webp":        true,  // nested id format
		"readme.txt":                   false, // ignored
		"random12345_67890.jpg":        false, // concatenated digits, no boundary
		"77_88.jpg.part":               false, // in-flight download
		"sub/.hidden":                  false, // dotfile
	}
	for rel, _ := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}

	stats, err := m.RebuildFromDir(root)
	added, removed, scanned := stats.Added, stats.Removed, stats.Scanned
	if err != nil {
		t.Fatal(err)
	}

	wantAdded := 0
	for _, ok := range files {
		if ok {
			wantAdded++
		}
	}
	if added != wantAdded {
		t.Errorf("added = %d, want %d", added, wantAdded)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 on fresh manifest", removed)
	}
	if scanned != wantAdded {
		t.Errorf("scanned = %d, want %d", scanned, wantAdded)
	}

	wantNumeric := []string{"67890", "222", "444", "666"}
	for _, n := range wantNumeric {
		k := encodeAttrRelayID(n)
		if !m.Has(k) {
			t.Errorf("missing key %q (numeric %s)", k, n)
		}
	}

	// Second run is a no-op (idempotent).
	stats2, err := m.RebuildFromDir(root)
	added2, removed2, scanned2 := stats2.Added, stats2.Removed, stats2.Scanned
	if err != nil {
		t.Fatal(err)
	}
	if added2 != 0 || removed2 != 0 {
		t.Errorf("second pass added=%d removed=%d, want 0/0", added2, removed2)
	}
	if scanned2 != wantAdded {
		t.Errorf("second pass scanned %d, want %d", scanned2, wantAdded)
	}

	// Persisted to disk.
	m2, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range wantNumeric {
		k := encodeAttrRelayID(n)
		if !m2.Has(k) {
			t.Errorf("key %q not persisted", k)
		}
	}
}

// Files deleted off disk are dropped from the manifest; surviving entries
// retain their original metadata.
func TestRebuildFromDir_Prune(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "1_100.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "2_200.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	// Pre-seed: one entry matching a file on disk (must survive with original
	// metadata intact) and one orphan whose file no longer exists.
	key100 := encodeAttrRelayID("100")
	key200 := encodeAttrRelayID("200")
	original := ManifestEntry{File: "old_name.jpg", URL: "u", At: time.Unix(1700000000, 0).UTC()}
	if err := m.Add(key100, original); err != nil {
		t.Fatal(err)
	}
	if err := m.Add("orphan", ManifestEntry{File: "999_999.jpg"}); err != nil {
		t.Fatal(err)
	}

	stats, err := m.RebuildFromDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Added != 1 || stats.Removed != 1 {
		t.Errorf("added=%d removed=%d, want 1/1", stats.Added, stats.Removed)
	}
	if m.Has("orphan") {
		t.Error("orphan still present after rebuild")
	}
	if !m.Has(key100) || !m.Has(key200) {
		t.Error("legitimate entries lost during rebuild")
	}
	// Existing entry kept its original fields.
	if got := m.Entries[key100]; got.File != original.File || got.URL != original.URL || !got.At.Equal(original.At) {
		t.Errorf("existing entry overwritten: %+v, want %+v", got, original)
	}
}

// Recursion has to reach files several levels deep, including names with
// brackets and spaces that would not survive a naive shell glob.
func TestRebuildFromDir_DeepRecursion(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "lvl1", "lvl2 with space", "[brackets]", "lvl4")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(root, "1_1.jpg"):                                                "1",
		filepath.Join(root, "lvl1", "[tag]_2_2.png"):                                  "2",
		filepath.Join(root, "lvl1", "lvl2 with space", "3_3.gif"):                     "3",
		filepath.Join(root, "lvl1", "lvl2 with space", "[brackets]", "4_4.webm"):      "4",
		filepath.Join(deep, "[a][b]_5_5.webp"):                                        "5",
		filepath.Join(deep, "ignored.txt"):                                            "x",
	}
	for path := range files {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := LoadManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := m.RebuildFromDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Inspected != len(files) {
		t.Errorf("Inspected = %d, want %d (recursion missed files)", stats.Inspected, len(files))
	}
	if stats.Scanned != 5 {
		t.Errorf("Scanned = %d, want 5 (parseable files only)", stats.Scanned)
	}
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		k := encodeAttrRelayID(n)
		if !m.Has(k) {
			t.Errorf("missing key %q (numeric %s) — recursion did not reach it", k, n)
		}
	}
}

// Sanity check: the Relay encoding used by rebuild must produce the
// exact byte-for-byte form that the GraphQL layer emits, otherwise the
// "downloaded" badge in the GUI won't match.
func TestEncodeAttrRelayID_MatchesGraphQL(t *testing.T) {
	// Same expectations as internal/graphql/id_test.go.
	cases := map[string]string{
		"6414291": "UG9zdEF0dHJpYnV0ZVBpY3R1cmU6NjQxNDI5MQ==",
		"7305572": "UG9zdEF0dHJpYnV0ZVBpY3R1cmU6NzMwNTU3Mg==",
	}
	for num, want := range cases {
		if got := encodeAttrRelayID(num); got != want {
			t.Errorf("encodeAttrRelayID(%q) = %q, want %q", num, got, want)
		}
	}
}
