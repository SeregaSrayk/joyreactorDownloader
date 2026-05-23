package client

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Client is a thin HTTP wrapper for fetching binary files (images / gifs / video)
// from the JoyReactor CDN. Accepts absolute URLs.
type Client struct {
	http *http.Client
	ua   string

	// Rate-gate: when minInterval > 0, callers wait until at least that
	// long has elapsed since the previous Get's request was issued. Used
	// by the GUI to apply a configurable "min ms between CDN requests"
	// throttle on top of the worker-count knob (e.g. anti-WAF defense
	// when the CDN starts handing out 403 bursts).
	rlMu        sync.Mutex
	lastReqAt   time.Time
	minInterval atomic.Int64 // nanoseconds; 0 means disabled
}

func New() *Client {
	return &Client{
		http: &http.Client{Timeout: 60 * time.Second},
		ua:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36",
	}
}

// SetMinInterval configures the minimum gap between successive Get calls.
// Zero or negative disables the gate. Safe to call concurrently with Get
// — the new value applies to the next acquisition attempt. The current
// throttle window in flight is not interrupted (intentional: shortening
// the window mid-wait would let a burst slip past the cap).
func (c *Client) SetMinInterval(d time.Duration) {
	if d < 0 {
		d = 0
	}
	c.minInterval.Store(int64(d))
}

// waitRate serializes Get callers and enforces the configured min-interval
// between request starts. A zero minInterval is a fast no-op.
func (c *Client) waitRate(ctx context.Context) error {
	min := time.Duration(c.minInterval.Load())
	if min <= 0 {
		return nil
	}
	c.rlMu.Lock()
	defer c.rlMu.Unlock()
	if !c.lastReqAt.IsZero() {
		if wait := min - time.Since(c.lastReqAt); wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	c.lastReqAt = time.Now()
	return nil
}

// retryableStatus reports whether the HTTP status code is transient
// enough to be worth a retry with backoff.
//
// JR's CDN-WAF uses HTTP 403 (not 429!) to silently block bursts from
// a single IP — there's no Retry-After header, but the block clears
// within minutes once traffic settles. We treat 403 the same as 429
// because both empirically signal "back off, you're going too fast"
// on the JR CDN. 5xx covers transient server hiccups.
//
// 401 / 404 / 400 are NOT retried — they're permanent for a given URL.
func retryableStatus(code int) bool {
	return code == http.StatusForbidden ||
		code == http.StatusTooManyRequests ||
		(code >= 500 && code < 600)
}

// Get issues a GET to an absolute URL, automatically retrying on
// transient failures (CDN-WAF 403 / 429 / 5xx). Caller must close the
// response body on the returned non-error path.
//
// Newer JR CDN content 404s without a joyreactor.cc Referer, so we
// always send one — the request is only ever directed at JR's CDN.
//
// Retry schedule: 1s → 2s → 4s → 8s → 16s, up to 5 attempts total.
// The exponential cap (16s) is intentionally lower than the GraphQL
// backoff because individual picture fetches live inside a parallel
// worker pool — a single slow retry shouldn't stall a 4-worker job
// for half a minute. The final response is returned as-is even if it
// still carries a retryable status, so the downloader can surface a
// real error to the user instead of looping forever.
func (c *Client) Get(ctx context.Context, url string) (*http.Response, error) {
	const maxAttempts = 5
	const maxBackoff = 16 * time.Second
	backoff := 1 * time.Second
	for attempt := 1; ; attempt++ {
		// Rate-gate every attempt — proactively pacing CDN traffic is the
		// whole point. The exponential backoff after a 403/429 is added on
		// top, so a soft-blocked client backs off MORE, not less.
		if err := c.waitRate(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.ua)
		req.Header.Set("Referer", "https://joyreactor.cc/")
		resp, err := c.http.Do(req)
		if err != nil {
			// Network-level error (timeout, connection reset). Treat as
			// retryable too — same kind of transient-failure category.
			if attempt >= maxAttempts {
				return nil, err
			}
			if !sleepWithCtx(ctx, backoff) {
				return nil, ctx.Err()
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}
		if !retryableStatus(resp.StatusCode) || attempt >= maxAttempts {
			return resp, nil
		}
		// Drain and close the failed body so the connection is returned
		// to the pool instead of leaking. ~1MB cap so a misbehaving
		// CDN can't make the drain itself a slow operation.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if !sleepWithCtx(ctx, backoff) {
			return nil, ctx.Err()
		}
		backoff = nextBackoff(backoff, maxBackoff)
	}
}

func sleepWithCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	cur *= 2
	if cur > max {
		cur = max
	}
	return cur
}
