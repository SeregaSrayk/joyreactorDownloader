package downloader

import (
	"encoding/base64"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// relayAttrTypeName matches the GraphQL type used by JR for picture
// attributes. The manifest is keyed by Relay global IDs
// (base64("<typeName>:<numericId>")) because that is what
// downloader.Job.Key carries during real downloads and what the GUI's
// "downloaded" badge matches against. We reproduce the encoding here
// instead of importing internal/graphql so the downloader package stays
// free of graphql-layer dependencies.
const relayAttrTypeName = "PostAttributePicture"

// encodeAttrRelayID returns base64("PostAttributePicture:<numericId>"),
// the exact form that PostAttributePicture.id has in GraphQL responses.
func encodeAttrRelayID(numericID string) string {
	return base64.StdEncoding.EncodeToString([]byte(relayAttrTypeName + ":" + numericID))
}

// nameRes is the ordered list of full-match regexes used to recognise a
// JR picture filename. Each captures the numeric attribute id in group
// 1; the first matching pattern wins.
//
// Recognised forms:
//   - "id":      <postNum>_<attrNum>.<ext>
//                   produced by buildFilenamePrim with format == "id".
//   - "tags":    [tag1][tag2]..._<postNum>_<attrNum>.<ext>
//                   produced by buildFilenamePrim with format == "tags".
//   - "joysave": <postNum>_<0|1>_<attrNum padded to 9>__<tag1>-<tag2>...<ext>
//                   produced by buildFilenamePrim with format == "joysave"
//                   AND by corax4/JoySave for the same posts. Padding zeros
//                   are stripped from the captured id before encoding, so the
//                   relay key matches what GraphQL emits.
//   - "seo":     <slug-with-dashes>-<attrNum>.<ext>
//                   JR's canonical CDN URL form. Filenames in this shape
//                   appear when the user saved a picture via right-click
//                   in the browser instead of through this tool, since the
//                   browser uses the URL's last path segment as filename.
//
// Each regex full-matches (^…$) and accepts only JR-supported media
// extensions so unrelated files with trailing numbers — phone-camera
// IMG_YYYYMMDD_HHMMSS_NNN.jpg, Reddit-downloader RDT_*.jpg, dated
// 2025-01-15.jpg — cannot be misread as picture filenames.
//
// Order in nameRes matters for one specific overlap: joysave names can
// contain trailing tag segments that look seo-shaped (e.g. a tag like
// "Tag-2025" at the end), so joysaveFormatRe MUST run before seoFormatRe
// to claim joysave files before seo's looser pattern grabs the last hyphen
// group.
var (
	extAlt = `(?i:jpe?g|png|gif|bmp|tiff?|mp4|webm|webp)`

	idFormatRe      = regexp.MustCompile(`^\d+_(\d+)\.` + extAlt + `$`)
	tagsFormatRe    = regexp.MustCompile(`^\[.+\]_\d+_(\d+)\.` + extAlt + `$`)
	joysaveFormatRe = regexp.MustCompile(`^\d+_[01]_0*(\d+)__.*\.` + extAlt + `$`)
	seoFormatRe     = regexp.MustCompile(`^.*[A-Za-z].*-(\d+)\.` + extAlt + `$`)

	nameRes = []*regexp.Regexp{idFormatRe, tagsFormatRe, joysaveFormatRe, seoFormatRe}
)

// parseAttrID extracts the attribute numeric ID from a filename in any
// of the three recognised formats. Returns ("", false) if the name
// matches none of them. Operates on the basename only (no path).
func parseAttrID(basename string) (string, bool) {
	for _, re := range nameRes {
		if m := re.FindStringSubmatch(basename); m != nil {
			return m[1], true
		}
	}
	return "", false
}

// RebuildStats summarises what RebuildFromDir did:
//   - Inspected: every file (not dir) the walk visited, including
//     ones whose names did not match either filename format. Useful
//     to verify recursion actually reached deep folders.
//   - Scanned: files that did match a format (Inspected - "unrecognised").
//   - Added/Removed: net changes to the manifest.
type RebuildStats struct {
	Added     int
	Removed   int
	Scanned   int
	Inspected int
}

// RebuildFromDir walks root recursively, parses filenames in either of
// the two supported naming formats, and rewrites the manifest so its
// entries map 1:1 to what is on disk:
//   - files whose attribute ID is not yet in the manifest are added,
//   - existing entries whose ID is NOT present on disk are removed,
//   - existing entries whose ID IS present keep their original
//     File/URL/At fields (so the original download timestamp survives).
//
// The destructiveness is intentional and the caller's responsibility
// to confirm — running this on one folder of a multi-folder shared
// manifest erases everything that lives elsewhere.
//
// Per-entry walk errors (permission denied on a single file or sub-
// directory, etc.) are swallowed so a single bad entry doesn't abort
// the whole rebuild; only an error visiting the root itself is fatal.
//
// The manifest is saved once at the end rather than per-file.
func (m *Manifest) RebuildFromDir(root string) (stats RebuildStats, err error) {
	if m == nil {
		return RebuildStats{}, nil
	}
	onDisk := make(map[string]struct{})
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			// Fail loudly only if the root itself is unreadable; otherwise
			// skip the offending entry and keep walking.
			if path == root {
				return werr
			}
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		stats.Inspected++
		name := d.Name()
		if name == manifestFile || strings.HasSuffix(name, ".part") {
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		numericID, ok := parseAttrID(name)
		if !ok {
			return nil
		}
		key := encodeAttrRelayID(numericID)
		stats.Scanned++
		onDisk[key] = struct{}{}
		m.mu.Lock()
		if _, exists := m.Entries[key]; !exists {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				rel = name
			}
			m.Entries[key] = ManifestEntry{
				File: filepath.ToSlash(rel),
				At:   time.Now().UTC(),
			}
			stats.Added++
		}
		m.mu.Unlock()
		return nil
	})
	if walkErr != nil {
		return stats, walkErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.Entries {
		if _, ok := onDisk[id]; !ok {
			delete(m.Entries, id)
			stats.Removed++
		}
	}
	if stats.Added == 0 && stats.Removed == 0 {
		return stats, nil
	}
	return stats, m.saveLocked()
}
