package client

import (
	"context"
	"net/http"
	"time"
)

// Client is a thin HTTP wrapper for fetching binary files (images / gifs / video)
// from the JoyReactor CDN. Accepts absolute URLs.
type Client struct {
	http *http.Client
	ua   string
}

func New() *Client {
	return &Client{
		http: &http.Client{Timeout: 60 * time.Second},
		ua:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36",
	}
}

// Get issues a GET to an absolute URL. Caller must close the response body.
// Newer JR CDN content 404s without a joyreactor.cc Referer, so we always
// send one — the request is only ever directed at JR's CDN.
func (c *Client) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Referer", "https://joyreactor.cc/")
	return c.http.Do(req)
}
