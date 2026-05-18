package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"joyreactorDownloader/internal/graphql"
)

// recoveredPicture is what a successful onion-recovery yields per attribute:
// the synthetic graphql.Attribute we'll splice into the Preview, plus the
// optional image dimensions parsed out of the HTML for the same <img> tag.
// We can't always get width/height from old JR markup, so they're best-effort.
type recoveredPicture struct {
	Attr   graphql.Attribute
	Width  int
	Height int
}

// onionPicRe captures attribute id + extension from a "/pics/post[/full]/<slug>-<id>.<ext>"
// URL. Anchors the literal `/pics/post` so we don't match avatar / icon URLs
// that share the host but a different path.
var onionPicRe = regexp.MustCompile(`/pics/post/(?:full/)?[^"'\s]*?-(\d+)\.(jpe?g|png|gif|webm|webp)`)

// imgTagRe finds full <img …> tags so we can pair an extracted attr id with
// the dimensions declared on the same tag. Two pass parse: by-tag for dims,
// then by-URL for the canonical attribute id list. (We don't pull in
// x/net/html — the markup is regular enough and one regex is cheaper than a
// whole HTML parser for this single endpoint.)
var imgTagRe = regexp.MustCompile(`(?is)<img\s+[^>]*?>`)
var widthRe = regexp.MustCompile(`width\s*=\s*["']?(\d+)`)
var heightRe = regexp.MustCompile(`height\s*=\s*["']?(\d+)`)
var srcRe = regexp.MustCompile(`src\s*=\s*["']([^"']+)["']`)

// recoverPostViaOnion fetches <baseURL>/post/<numericPostID> through the
// provided transport (typically SOCKS5 → Tor), parses out picture attribute
// IDs from the rendered HTML, and returns synthetic graphql.Attribute records
// with predictable image types. The Attribute.ID is encoded in JR's normal
// Relay base64 form so callers can route through the existing
// graphql.Attribute.FileURL() to build the CDN URL.
//
// Returns (nil, nil) if the HTML had no picture URLs at all — caller decides
// whether that's "post genuinely has no pictures" or "scrape failed".
func recoverPostViaOnion(ctx context.Context, transport http.RoundTripper, baseURL string, numericPostID int64) ([]recoveredPicture, error) {
	if baseURL == "" {
		return nil, errors.New("empty onion base URL")
	}
	url := strings.TrimRight(baseURL, "/") + "/post/" + strconv.FormatInt(numericPostID, 10)

	hc := &http.Client{Timeout: 60 * time.Second}
	if transport != nil {
		hc.Transport = transport
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	// Mimic a regular browser so static caches don't serve us a stale stub.
	req.Header.Set("User-Agent", "Mozilla/5.0 joyreactorDownloader/1.0")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("onion HTTP %d", resp.StatusCode)
	}
	// Bound the read — JR post pages are typically 50–150 KB; 1 MB is a
	// generous ceiling that protects us from a misbehaving mirror.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseOnionPicturesHTML(body), nil
}

// parseOnionPicturesHTML extracts the unique attribute ids out of the post
// HTML and pairs each one with its declared width/height from the matching
// <img> tag (when possible). Same attribute id can appear twice in the page
// (preview + full) — we dedup on the numeric id and prefer the entry where
// we got dimensions.
func parseOnionPicturesHTML(html []byte) []recoveredPicture {
	type acc struct {
		ext       string
		w, h      int
		hasDims   bool
		insertIdx int
	}
	seen := make(map[int64]*acc)
	order := []int64{}

	for _, tag := range imgTagRe.FindAll(html, -1) {
		srcM := srcRe.FindSubmatch(tag)
		if srcM == nil {
			continue
		}
		picM := onionPicRe.FindStringSubmatch(string(srcM[1]))
		if picM == nil {
			continue
		}
		id, err := strconv.ParseInt(picM[1], 10, 64)
		if err != nil {
			continue
		}
		ext := strings.ToLower(picM[2])
		// Normalise jpg → jpeg so it matches graphql.ImageJPEG ("JPEG").
		if ext == "jpg" {
			ext = "jpeg"
		}
		entry := seen[id]
		if entry == nil {
			entry = &acc{ext: ext, insertIdx: len(order)}
			seen[id] = entry
			order = append(order, id)
		}
		if entry.hasDims {
			continue
		}
		if w := widthRe.FindSubmatch(tag); w != nil {
			if v, err := strconv.Atoi(string(w[1])); err == nil {
				entry.w = v
			}
		}
		if h := heightRe.FindSubmatch(tag); h != nil {
			if v, err := strconv.Atoi(string(h[1])); err == nil {
				entry.h = v
			}
		}
		entry.hasDims = entry.w > 0 || entry.h > 0
	}

	out := make([]recoveredPicture, 0, len(order))
	for _, id := range order {
		entry := seen[id]
		out = append(out, recoveredPicture{
			Attr: graphql.Attribute{
				ID:       relayEncodeAttrID(id),
				Type:     graphql.AttrPicture,
				Typename: "PostAttributePicture",
				Image: &graphql.Image{
					Type:   graphql.ImageType(strings.ToUpper(entry.ext)),
					Width:  entry.w,
					Height: entry.h,
				},
			},
			Width:  entry.w,
			Height: entry.h,
		})
	}
	return out
}

// relayEncodeAttrID re-creates the Relay-style base64("PostAttributePicture:<id>")
// that the rest of the pipeline expects. graphql.DecodeID would happily reverse
// this back to the numeric id when buildPicture / FileURL ask for it.
func relayEncodeAttrID(id int64) string {
	return base64.StdEncoding.EncodeToString([]byte("PostAttributePicture:" + strconv.FormatInt(id, 10)))
}

// recoverViaOnion fans out onion HTML scrapes for the previews at removedIdx
// and patches each successful recovery back into the slice in place. Order is
// preserved; failures leave prev.Removed=true so the UI still shows the DMCA
// state for the unrecoverable ones.
//
// Bounded parallelism (4 workers) — Tor circuits are limited, hammering the
// hidden service with one request per removed post would both be slow and
// risk getting throttled by the mirror.
func (g *GUI) recoverViaOnion(previews []Preview, removedIdx []int, posts []graphql.Post, s AppSettings, _ bool) {
	if len(removedIdx) == 0 {
		return
	}
	transport, err := socksTransport(s)
	if err != nil {
		return
	}
	// The recovery is bounded as a whole — the whole search must remain
	// responsive. 90s caps a worst-case of "Tor circuit is slow + 4 posts to
	// scrape" without blocking the user for minutes.
	ctx, cancel := context.WithTimeout(g.ctx, 90*time.Second)
	defer cancel()

	const workers = 4
	type job struct {
		previewSlot int
		post        graphql.Post
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				_, postNum, decErr := graphql.DecodeID(j.post.ID)
				if decErr != nil {
					continue
				}
				recovered, err := recoverPostViaOnion(ctx, transport, s.OnionBaseURL, postNum)
				if err != nil || len(recovered) == 0 {
					continue
				}
				webmSlug := firstTagSlug(j.post)
				pics := make([]Picture, 0, len(recovered))
				for _, r := range recovered {
					if pic, ok := buildPicture(r.Attr, webmSlug); ok {
						pics = append(pics, pic)
					}
				}
				if len(pics) == 0 {
					continue
				}
				// Patching the preview slot is safe: each removedIdx is
				// unique and only this worker writes to that slot.
				previews[j.previewSlot].Pictures = pics
				previews[j.previewSlot].Removed = false
			}
		}()
	}
	for i, slot := range removedIdx {
		jobs <- job{previewSlot: slot, post: posts[i]}
	}
	close(jobs)
	wg.Wait()
}
