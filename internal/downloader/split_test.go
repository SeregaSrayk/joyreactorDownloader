package downloader

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReserveSlot_RotatesEveryN verifies that with EnableSplit(N, SplitFiles)
// the downloader returns the same subdir for N successive reservations
// then rolls over to the next one.
func TestReserveSlot_RotatesEveryN(t *testing.T) {
	dir := t.TempDir()
	d := &Downloader{outDir: dir}
	d.EnableSplit(3, SplitFiles)

	got := make([]string, 0, 7)
	for i := 0; i < 7; i++ {
		got = append(got, d.reserveSlot())
	}
	want := []string{
		"part-001", "part-001", "part-001",
		"part-002", "part-002", "part-002",
		"part-003",
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("slot[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestReserveSlot_Disabled returns "" when splitting is off.
func TestReserveSlot_Disabled(t *testing.T) {
	d := &Downloader{outDir: t.TempDir()}
	if got := d.reserveSlot(); got != "" {
		t.Errorf("disabled reserveSlot returned %q, want \"\"", got)
	}
}

// TestReserveSlot_InapplicableUnit returns "" when split is on but the
// unit is posts/pages — reserveSlot is the SplitFiles fallback only.
func TestReserveSlot_InapplicableUnit(t *testing.T) {
	for _, u := range []SplitUnit{SplitPosts, SplitPages} {
		d := &Downloader{outDir: t.TempDir()}
		d.EnableSplit(5, u)
		if got := d.reserveSlot(); got != "" {
			t.Errorf("unit=%q reserveSlot returned %q, want \"\"", u, got)
		}
	}
}

// TestEnableSplit_ResumesHighestExisting opens the last partially filled
// part-XXX from a prior run instead of starting over at part-001.
// Resume by file count only applies to SplitFiles mode.
func TestEnableSplit_ResumesHighestExisting(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"part-001", "part-002", "part-005"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// part-005 has 2 files (under the cap of 5) — should resume into it.
	for _, fn := range []string{"a.jpg", "b.jpg"} {
		if err := os.WriteFile(filepath.Join(dir, "part-005", fn), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Drop a stray .part file and a dotfile — these must not affect the count.
	_ = os.WriteFile(filepath.Join(dir, "part-005", "ghost.part"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "part-005", ".keep"), []byte("x"), 0o644)

	d := &Downloader{outDir: dir}
	d.EnableSplit(5, SplitFiles)
	if d.splitPart != 5 {
		t.Errorf("splitPart = %d, want 5", d.splitPart)
	}
	if d.splitCount != 2 {
		t.Errorf("splitCount = %d, want 2", d.splitCount)
	}
	// Three more reservations should fit into part-005; the next opens part-006.
	for i := 0; i < 3; i++ {
		if got := d.reserveSlot(); got != "part-005" {
			t.Errorf("reservation %d = %q, want part-005", i, got)
		}
	}
	if got := d.reserveSlot(); got != "part-006" {
		t.Errorf("post-fill reservation = %q, want part-006", got)
	}
}

// TestEnableSplit_FullExistingRollsOver opens the next part when the
// highest existing one is already at the cap.
func TestEnableSplit_FullExistingRollsOver(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "part-002")
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		if err := os.WriteFile(filepath.Join(full, fn), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := &Downloader{outDir: dir}
	d.EnableSplit(3, SplitFiles)
	if d.splitPart != 3 || d.splitCount != 0 {
		t.Errorf("splitPart=%d splitCount=%d, want 3/0", d.splitPart, d.splitCount)
	}
	if got := d.reserveSlot(); got != "part-003" {
		t.Errorf("first reservation = %q, want part-003", got)
	}
}

// TestEnableSplit_FreshFolderStartsAtOne verifies an empty outDir begins
// at part-001 instead of inheriting some weird value.
func TestEnableSplit_FreshFolderStartsAtOne(t *testing.T) {
	d := &Downloader{outDir: t.TempDir()}
	d.EnableSplit(10, SplitFiles)
	if d.splitPart != 1 || d.splitCount != 0 {
		t.Errorf("fresh folder: splitPart=%d splitCount=%d, want 1/0", d.splitPart, d.splitCount)
	}
	if got := d.reserveSlot(); got != "part-001" {
		t.Errorf("first reservation = %q, want part-001", got)
	}
}

// TestNextPostSubdir_RotatesEveryNPosts: every call to NextPostSubdir
// represents one post, and the same subdir is returned for N successive
// calls before rolling over.
func TestNextPostSubdir_RotatesEveryNPosts(t *testing.T) {
	d := &Downloader{outDir: t.TempDir()}
	d.EnableSplit(2, SplitPosts)
	got := []string{
		d.NextPostSubdir(), d.NextPostSubdir(),
		d.NextPostSubdir(), d.NextPostSubdir(),
		d.NextPostSubdir(),
	}
	want := []string{"part-001", "part-001", "part-002", "part-002", "part-003"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("post[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestNextPostSubdir_WrongUnitReturnsEmpty: in SplitPages or SplitFiles
// mode, NextPostSubdir returns "" so producer logic gracefully no-ops.
func TestNextPostSubdir_WrongUnitReturnsEmpty(t *testing.T) {
	for _, u := range []SplitUnit{SplitPages, SplitFiles} {
		d := &Downloader{outDir: t.TempDir()}
		d.EnableSplit(5, u)
		if got := d.NextPostSubdir(); got != "" {
			t.Errorf("unit=%q NextPostSubdir returned %q, want \"\"", u, got)
		}
	}
}

// TestNextPageSubdir_RotatesEveryNPages mirrors the post test but for pages.
func TestNextPageSubdir_RotatesEveryNPages(t *testing.T) {
	d := &Downloader{outDir: t.TempDir()}
	d.EnableSplit(3, SplitPages)
	got := []string{
		d.NextPageSubdir(), d.NextPageSubdir(), d.NextPageSubdir(),
		d.NextPageSubdir(),
	}
	want := []string{"part-001", "part-001", "part-001", "part-002"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("page[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestEnableSplit_PostsModeAlwaysOpensFreshPart verifies the resume rule
// for SplitPosts: never re-enter an existing partially-filled part,
// because we can't tell from disk how many *posts* it holds.
func TestEnableSplit_PostsModeAlwaysOpensFreshPart(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"part-001", "part-007"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// part-007 is barely populated — would be reused under SplitFiles,
	// but SplitPosts must skip ahead to part-008.
	_ = os.WriteFile(filepath.Join(dir, "part-007", "a.jpg"), []byte("x"), 0o644)

	d := &Downloader{outDir: dir}
	d.EnableSplit(5, SplitPosts)
	if d.splitPart != 8 || d.splitCount != 0 {
		t.Errorf("splitPart=%d splitCount=%d, want 8/0", d.splitPart, d.splitCount)
	}
	if got := d.NextPostSubdir(); got != "part-008" {
		t.Errorf("first reservation = %q, want part-008", got)
	}
}

// TestSplitUnitGetter reports the active unit (or "" when off).
func TestSplitUnitGetter(t *testing.T) {
	d := &Downloader{outDir: t.TempDir()}
	if got := d.SplitUnit(); got != "" {
		t.Errorf("disabled SplitUnit = %q, want \"\"", got)
	}
	d.EnableSplit(10, SplitPosts)
	if got := d.SplitUnit(); got != SplitPosts {
		t.Errorf("after enable: SplitUnit = %q, want %q", got, SplitPosts)
	}
}
