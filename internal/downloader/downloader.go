package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"joyreactorDownloader/internal/client"
)

// Job is one file to fetch and save under outDir as Name.
// Key uniquely identifies the source (typically the PostAttributePicture Relay ID)
// for cross-run dedup via the Manifest.
type Job struct {
	URL  string
	Name string
	Key  string
}

type Downloader struct {
	client   *client.Client
	outDir   string
	manifest *Manifest
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

// Fetch downloads job.URL into outDir/job.Name. Returns true if the file was
// freshly downloaded, false if it was already present (on disk or in manifest).
// Writes to a .part file and atomically renames on success; updates the manifest.
func (d *Downloader) Fetch(ctx context.Context, job Job) (downloaded bool, err error) {
	if err := os.MkdirAll(d.outDir, 0o755); err != nil {
		return false, fmt.Errorf("mkdir: %w", err)
	}
	target := filepath.Join(d.outDir, job.Name)
	if _, err := os.Stat(target); err == nil {
		d.recordManifest(job)
		return false, nil
	}
	if d.manifest.Has(job.Key) {
		return false, nil
	}

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
	d.recordManifest(job)
	return true, nil
}

func (d *Downloader) recordManifest(job Job) {
	if job.Key == "" {
		return
	}
	_ = d.manifest.Add(job.Key, ManifestEntry{
		File: job.Name,
		URL:  job.URL,
		At:   time.Now().UTC(),
	})
}
