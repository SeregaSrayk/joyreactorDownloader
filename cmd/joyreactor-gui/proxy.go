package main

import (
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

const proxyPath = "/proxy"

// proxyHandler forwards <img>/<video> requests from the WebView to the
// joyreactor.cc CDN, adding the Referer header the CDN's hotlink protection
// requires for newer content (it 404s anything else, including the wails://
// origin WebView2 sends by default).
//
// Range requests are forwarded so <video> seek works correctly.
func proxyHandler() http.Handler {
	client := &http.Client{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != proxyPath {
			http.NotFound(w, r)
			return
		}
		raw := r.URL.Query().Get("url")
		if raw == "" {
			http.Error(w, "missing url param", http.StatusBadRequest)
			return
		}
		target, err := url.Parse(raw)
		if err != nil || target.Scheme == "" || target.Host == "" {
			http.Error(w, "bad url", http.StatusBadRequest)
			return
		}
		if !isJoyreactorHost(target.Host) {
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Referer", "https://joyreactor.cc/")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
		if ra := r.Header.Get("Range"); ra != "" {
			req.Header.Set("Range", ra)
		}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for _, h := range []string{
			"Content-Type", "Content-Length", "Content-Range",
			"Accept-Ranges", "Last-Modified", "ETag", "Cache-Control",
		} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		// Hint a sensible filename so WebView2's "Save image as" lines up
		// with what the downloader would save. Frontend stuffs the picture's
		// metadata into the proxy URL (pid/aid/type/tag/fmt) so we can run
		// the exact same buildFilenamePrim that download jobs use.
		fn := filenameFromQuery(r.URL.Query())
		if fn == "" {
			fn = filenameFromURL(target)
		}
		if fn != "" {
			// filename*=UTF-8'' lets Chromium accept non-ASCII (Cyrillic
			// tags) and percent-encoded specials (brackets) without
			// breaking the header parse.
			w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(fn))
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}

// filenameFromQuery builds the same filename the downloader would, when the
// frontend has passed enough metadata in the proxy URL. Returns "" if the
// caller didn't provide pid/aid/type — we then fall back to a URL-derived
// name (filenameFromURL below).
func filenameFromQuery(q url.Values) string {
	pid := q.Get("pid")
	aid := q.Get("aid")
	typ := q.Get("type")
	if pid == "" || aid == "" || typ == "" {
		return ""
	}
	return buildFilenamePrim(pid, aid, typ, q["tag"], q.Get("fmt"))
}

// filenameFromURL derives a save-dialog friendly filename from a JR CDN URL.
// JR paths look like `/pics/post/full/-5153532.jpg`; the leading dash trips
// CLIs and looks odd, so we trim it. Returns "" if no sane segment exists.
func filenameFromURL(u *url.URL) string {
	seg := path.Base(u.Path)
	if seg == "" || seg == "/" || seg == "." {
		return ""
	}
	seg = strings.TrimLeft(seg, "-")
	return seg
}

func isJoyreactorHost(hostPort string) bool {
	host := hostPort
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	if host == "joyreactor.cc" {
		return true
	}
	return strings.HasSuffix(host, ".joyreactor.cc")
}
