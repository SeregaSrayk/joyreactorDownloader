package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"
)

const DefaultEndpoint = "https://api.joyreactor.cc/graphql"

type Client struct {
	http     *http.Client
	endpoint string
	ua       string
	jar      http.CookieJar

	mu       sync.Mutex
	minDelay time.Duration
	lastCall time.Time

	// txMu guards http.Transport swap-in for hot-reload of SOCKS5/onion
	// settings without recreating the Client (and losing the cookie jar /
	// session). SetTransport takes it write-side; doOnce takes it read-side
	// before issuing the request.
	txMu sync.RWMutex
}

func NewClient(endpoint string) *Client {
	return NewClientWithTransport(endpoint, nil)
}

// NewClientWithTransport is the SOCKS5/Tor-friendly constructor: pass a custom
// http.RoundTripper (e.g. one whose DialContext goes through a SOCKS5 dialer)
// and the cookie jar / throttling / retry behaviour stays identical to a
// regular Client. Pass nil transport for the default behaviour.
//
// The timeout is larger here (60s instead of 30s) because Tor circuits add a
// few hundred ms baseline latency and rendezvous to a hidden service can take
// noticeably longer than a clearnet TCP+TLS handshake.
func NewClientWithTransport(endpoint string, transport http.RoundTripper) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	jar, _ := cookiejar.New(nil) // cookiejar.New never errors when opts is nil
	hc := &http.Client{Timeout: 30 * time.Second, Jar: jar}
	if transport != nil {
		hc.Transport = transport
		hc.Timeout = 60 * time.Second
	}
	return &Client{
		http:     hc,
		endpoint: endpoint,
		ua:       "joyreactor-dl/0.1",
		minDelay: 500 * time.Millisecond,
		jar:      jar,
	}
}

// Endpoint returns the configured GraphQL endpoint URL.
func (c *Client) Endpoint() string { return c.endpoint }

// Jar returns the underlying cookie jar so the GUI can persist/restore sessions.
func (c *Client) Jar() http.CookieJar { return c.jar }

// SetTransport hot-swaps the underlying http.Transport so SOCKS5 / onion
// settings can be toggled at runtime without recreating the Client (which
// would drop the cookie jar and force the user to log in again).
//
// Pass nil to revert to the default direct transport. The HTTP timeout is
// stretched to 60s when a custom transport is in play because Tor hidden
// services add hundreds of ms baseline latency.
func (c *Client) SetTransport(t http.RoundTripper) {
	c.txMu.Lock()
	defer c.txMu.Unlock()
	if t == nil {
		c.http.Transport = nil
		c.http.Timeout = 30 * time.Second
		return
	}
	c.http.Transport = t
	c.http.Timeout = 60 * time.Second
}

type gqlError struct {
	Message string `json:"message"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors,omitempty"`
}

// ErrRateLimited is returned when the server replies with HTTP 429.
var ErrRateLimited = errors.New("graphql: rate limited (HTTP 429)")

// Do executes a GraphQL query and decodes data into out (if non-nil).
// On HTTP 429 retries with exponential backoff up to maxRetryAttempts times.
func (c *Client) Do(ctx context.Context, query string, vars map[string]any, out any) error {
	const maxRetryAttempts = 6
	const maxBackoff = 30 * time.Second
	backoff := 1 * time.Second
	for attempt := 1; ; attempt++ {
		err := c.doOnce(ctx, query, vars, out)
		if !errors.Is(err, ErrRateLimited) || attempt >= maxRetryAttempts {
			return err
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// doOnce executes a single GraphQL request. Requests are serialized and throttled.
func (c *Client) doOnce(ctx context.Context, query string, vars map[string]any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if d := time.Until(c.lastCall.Add(c.minDelay)); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.lastCall = time.Now()

	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.ua)

	// RLock pairs with SetTransport: prevents a SetTransport call from
	// mutating http.Client.Transport while http.Client.Do is reading it.
	c.txMu.RLock()
	resp, err := c.http.Do(req)
	c.txMu.RUnlock()
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("graphql HTTP %d: %s", resp.StatusCode, string(b))
	}

	var r gqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(r.Errors) > 0 {
		msg := r.Errors[0].Message
		// JR's GraphQL rate-limiter signals the cap by returning HTTP 200
		// with `{"errors":[{"message":"Rate ..."}]}` in the body — NOT
		// HTTP 429. Discovered by looking at corax4/JoySave, which
		// detects the same prefix to know when to wait. Route through
		// ErrRateLimited so the surrounding Do() exponential-backoff
		// retry path catches it, instead of bubbling up to the caller
		// as a fatal "graphql error: Rate ...".
		if strings.HasPrefix(msg, "Rate") {
			return ErrRateLimited
		}
		return fmt.Errorf("graphql error: %s", msg)
	}
	if out != nil {
		if err := json.Unmarshal(r.Data, out); err != nil {
			return fmt.Errorf("decode data: %w", err)
		}
	}
	return nil
}
