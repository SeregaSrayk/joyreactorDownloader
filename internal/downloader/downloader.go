package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"joyreactorDownloader/internal/client"
)

// SplitUnit picks what EnableSplit's "every N" counts. See AppSettings
// docs for the user-facing description. SplitFiles is the simplest but
// can split a multi-image post in half; SplitPosts is the recommended
// default because photosets / comics stay together.
type SplitUnit string

const (
	SplitFiles SplitUnit = "files"
	SplitPosts SplitUnit = "posts"
	SplitPages SplitUnit = "pages"
)

// Job is one file to fetch and save under outDir as Name.
// Key uniquely identifies the source (typically the PostAttributePicture Relay ID)
// for cross-run dedup via the Manifest.
//
// SubdirHint, when non-empty, is the rotating sub-folder name the
// producer reserved for this file's post/page (e.g. "part-007"). The
// downloader uses it verbatim instead of consulting its own split
// counter — this is how SplitPosts / SplitPages keep every picture of
// the same post in the same part-XXX even when N parallel workers race.
type Job struct {
	URL        string
	Name       string
	Key        string
	SubdirHint string
}

type Downloader struct {
	client   *client.Client
	outDir   string
	manifest *Manifest

	// Sub-folder rotation. When splitEvery > 0 each save goes into
	// part-001 / part-002 / … and a new sub-dir is opened once the
	// current unit count reaches splitEvery. Counting unit is
	// splitUnit — see SplitUnit docs.
	splitMu    sync.Mutex
	splitEvery int
	splitUnit  SplitUnit
	splitPart  int
	splitCount int
}

// New constructs a Downloader with a per-folder manifest at
// `outDir/.manifest.json`. Equivalent to NewWithManifest with the default
// path. A manifest read error is non-fatal: the downloader still works,
// only loses cross-run dedup until the next successful save.
func New(c *client.Client, outDir string) *Downloader {
	m, _ := LoadManifest(outDir)
	return &Downloader{client: c, outDir: outDir, manifest: m}
}

// NewWithManifest is like New but lets the caller pick where the manifest
// lives. The "global manifest" GUI mode points every job at a shared file
// in the app's config dir so downloads dedupe across all output folders,
// not just within a single folder.
func NewWithManifest(c *client.Client, outDir, manifestPath string) *Downloader {
	m, _ := LoadManifestFile(manifestPath)
	return &Downloader{client: c, outDir: outDir, manifest: m}
}

// Manifest returns the loaded manifest (never nil).
func (d *Downloader) Manifest() *Manifest { return d.manifest }

// HasKey reports whether the manifest already records the given key.
func (d *Downloader) HasKey(key string) bool { return d.manifest.Has(key) }

// EnableSplit turns on subfolder rotation. Saves go into outDir/part-001,
// part-002 etc.; a new sub-dir opens after every `every` units of `unit`
// (files / posts / pages). Zero/negative `every` disables splitting.
//
// Resume semantics depend on unit:
//   - SplitFiles: scan outDir, find the highest part-XXX, count files
//     inside, continue filling that one until it hits `every`. The
//     last part can grow slightly past `every` if the previous run
//     died mid-fetch.
//   - SplitPosts / SplitPages: never re-enter an existing part-XXX —
//     we can't tell from disk how many *posts* it already holds, and
//     we'd risk splitting a post if we guessed wrong. Start a fresh
//     part-(max+1) instead. Costs an extra mostly-empty dir at worst.
//
// Manifest dedup is the only cross-run guarantee in split mode: the
// per-file os.Stat fast path that picks up an already-present file
// with an empty manifest only kicks in when splitting is off. Users
// who want to recover an existing split folder should run «Полностью
// пересобрать манифест» first.
func (d *Downloader) EnableSplit(every int, unit SplitUnit) {
	if every <= 0 {
		return
	}
	if unit == "" {
		unit = SplitPosts
	}
	d.splitMu.Lock()
	defer d.splitMu.Unlock()
	d.splitEvery = every
	d.splitUnit = unit
	if unit == SplitFiles {
		d.splitPart, d.splitCount = scanInitialPart(d.outDir, every)
	} else {
		d.splitPart, d.splitCount = scanNextPart(d.outDir), 0
	}
}

// SplitUnit returns the active splitting unit, or "" when splitting is off.
func (d *Downloader) SplitUnit() SplitUnit {
	d.splitMu.Lock()
	defer d.splitMu.Unlock()
	if d.splitEvery <= 0 {
		return ""
	}
	return d.splitUnit
}

// NextPostSubdir advances the post-mode counter by one post and returns
// the part-XXX subdir name that every picture of this post must go
// into. Producer calls this once per post (sequentially); workers read
// the returned hint via job.SubdirHint and never split a post across
// parts even when racing. Returns "" when splitting is off or the unit
// isn't SplitPosts.
func (d *Downloader) NextPostSubdir() string {
	return d.nextUnitSubdir(SplitPosts)
}

// NextPageSubdir is the SplitPages equivalent — call once per feed page
// before queueing that page's posts. Returns "" when splitting is off
// or the unit isn't SplitPages.
func (d *Downloader) NextPageSubdir() string {
	return d.nextUnitSubdir(SplitPages)
}

func (d *Downloader) nextUnitSubdir(want SplitUnit) string {
	d.splitMu.Lock()
	defer d.splitMu.Unlock()
	if d.splitEvery <= 0 || d.splitUnit != want {
		return ""
	}
	if d.splitCount >= d.splitEvery {
		d.splitPart++
		d.splitCount = 0
	}
	d.splitCount++
	return formatPartDir(d.splitPart)
}

// reserveSlot is the SplitFiles fallback: bumps the counter once per
// downloaded file. Used only when no SubdirHint was set on the job —
// i.e. SplitPosts / SplitPages route through SubdirHint and never hit
// this path.
func (d *Downloader) reserveSlot() string {
	d.splitMu.Lock()
	defer d.splitMu.Unlock()
	if d.splitEvery <= 0 || d.splitUnit != SplitFiles {
		return ""
	}
	if d.splitCount >= d.splitEvery {
		d.splitPart++
		d.splitCount = 0
	}
	d.splitCount++
	return formatPartDir(d.splitPart)
}

// Fetch downloads job.URL into outDir/job.Name. Returns true if the file was
// freshly downloaded, false if it was already present (on disk or in manifest).
// Writes to a .part file and atomically renames on success; updates the manifest.
//
// Subdir selection:
//   - job.SubdirHint set ⇒ goes into outDir/<hint>/ verbatim (this is
//     how SplitPosts / SplitPages pin all of a post's pictures to the
//     same part-XXX even across N parallel workers).
//   - hint empty AND SplitFiles enabled ⇒ reserveSlot() picks the
//     current part-XXX and bumps the file counter.
//   - hint empty AND splitting disabled ⇒ outDir flat.
//
// The manifest entry's File records the relative path including any
// part-XXX prefix so a future scan can find the file.
func (d *Downloader) Fetch(ctx context.Context, job Job) (downloaded bool, err error) {
	if d.manifest.Has(job.Key) {
		return false, nil
	}
	splitOff := d.splitEvery == 0 && job.SubdirHint == ""
	if splitOff {
		// Disk-side fast path: file already exists (user copied it in,
		// or the manifest was wiped). Only useful without splitting,
		// because we don't know which part-XXX to look in otherwise.
		direct := filepath.Join(d.outDir, job.Name)
		if _, err := os.Stat(direct); err == nil {
			d.recordManifest(job, job.Name)
			return false, nil
		}
	}

	subdir := job.SubdirHint
	if subdir == "" {
		subdir = d.reserveSlot()
	}
	targetDir := d.outDir
	relName := job.Name
	if subdir != "" {
		targetDir = filepath.Join(d.outDir, subdir)
		relName = filepath.ToSlash(filepath.Join(subdir, job.Name))
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return false, fmt.Errorf("mkdir: %w", err)
	}
	target := filepath.Join(targetDir, job.Name)

	resp, err := d.client.Get(ctx, job.URL)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GET %s: HTTP %d", job.URL, resp.StatusCode)
	}

	tmp := target + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return false, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, target); err != nil {
		return false, err
	}
	d.recordManifest(job, relName)
	return true, nil
}

func (d *Downloader) recordManifest(job Job, relPath string) {
	if job.Key == "" {
		return
	}
	_ = d.manifest.Add(job.Key, ManifestEntry{
		File: relPath,
		URL:  job.URL,
		At:   time.Now().UTC(),
	})
}

// partDirRe matches the subfolder names that EnableSplit produces.
var partDirRe = regexp.MustCompile(`^part-(\d{3,})$`)

func formatPartDir(idx int) string {
	return fmt.Sprintf("part-%03d", idx)
}

// scanNextPart returns max(existing-part-XXX) + 1 in outDir, or 1 if
// none exist. Used by SplitPosts/SplitPages resume: we can't tell how
// many *posts* a partially-filled part holds just from file count, so
// we always open a fresh dir to keep the "no post is ever split" rule.
func scanNextPart(outDir string) int {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return 1
	}
	maxPart := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := partDirRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, perr := strconv.Atoi(m[1])
		if perr != nil {
			continue
		}
		if n > maxPart {
			maxPart = n
		}
	}
	return maxPart + 1
}

// scanInitialPart finds the highest existing part-XXX in outDir and
// counts the visible media files inside, so a re-launched job continues
// filling that part rather than restarting at 001. If none exist
// returns (1, 0).
func scanInitialPart(outDir string, every int) (part, count int) {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return 1, 0
	}
	maxPart := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := partDirRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, perr := strconv.Atoi(m[1])
		if perr != nil {
			continue
		}
		if n > maxPart {
			maxPart = n
		}
	}
	if maxPart == 0 {
		return 1, 0
	}
	c := countMediaFiles(filepath.Join(outDir, formatPartDir(maxPart)))
	if c >= every {
		return maxPart + 1, 0
	}
	return maxPart, c
}

// countMediaFiles counts visible files in dir, ignoring .part artefacts
// and dotfiles (incl. .manifest.json). Used to figure out how full the
// last part-XXX is on resume.
func countMediaFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".part") {
			continue
		}
		if strings.HasPrefix(name, ".") {
			continue
		}
		n++
	}
	return n
}
