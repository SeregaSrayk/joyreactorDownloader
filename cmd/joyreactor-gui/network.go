package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"joyreactorDownloader/internal/graphql"
)

// NetworkTestResult is the JSON shape returned to the frontend after a
// "test connection" click in the settings modal. Either OK (with latency) or
// Error (with a short reason the user can act on).
type NetworkTestResult struct {
	OK      bool   `json:"ok"`
	LatMs   int64  `json:"latencyMs,omitempty"`
	Error   string `json:"error,omitempty"`
	Address string `json:"address,omitempty"` // what we actually hit, for confirmation
}

// TestNetwork is a Wails-bound method exposed to the frontend. The probe
// shape depends on whether onionBaseURL is set:
//
//   - empty ⇒ run a tiny `{ __typename }` GraphQL query against the clearnet
//     api.joyreactor.cc (optionally routed through the SOCKS5 proxy). Confirms
//     "GraphQL works, optionally through Tor".
//   - non-empty ⇒ GET <onion>/post/4759880 and check that the response looks
//     like the JR mirror (HTTP 200 + HTML body containing /pics/post). Confirms
//     "DMCA recovery will actually find pictures".
//
// Doesn't touch the running app's client — pure dry-run.
func (g *GUI) TestNetwork(socks5Enabled bool, socks5Addr, onionBaseURL string) NetworkTestResult {
	transport, err := socksTransport(AppSettings{
		Socks5Enabled: socks5Enabled,
		Socks5Addr:    socks5Addr,
	})
	if err != nil {
		return NetworkTestResult{Error: err.Error()}
	}

	// Bound the probe — the user is staring at the UI waiting for it. Tor
	// hidden-service rendezvous can be slow (~3–5s) on a cold circuit.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if onionBaseURL != "" {
		return probeOnionMirror(ctx, transport, onionBaseURL)
	}
	return probeClearnetGraphQL(ctx, transport)
}

func probeClearnetGraphQL(ctx context.Context, transport http.RoundTripper) NetworkTestResult {
	res := NetworkTestResult{Address: graphql.DefaultEndpoint}
	client := graphql.NewClientWithTransport("", transport)
	start := time.Now()
	var probe struct {
		Typename string `json:"__typename"`
	}
	if err := client.Do(ctx, "{ __typename }", nil, &probe); err != nil {
		res.Error = humanizeNetErr(err)
		return res
	}
	res.OK = true
	res.LatMs = time.Since(start).Milliseconds()
	return res
}

func probeOnionMirror(ctx context.Context, transport http.RoundTripper, baseURL string) NetworkTestResult {
	// Use a well-known DMCA-removed post that should still exist on the onion
	// mirror. Verifies both connectivity and "mirror has the data we expect".
	const probePath = "/post/4759880"
	url := strings.TrimRight(baseURL, "/") + probePath
	res := NetworkTestResult{Address: url}

	hc := &http.Client{Timeout: 60 * time.Second}
	if transport != nil {
		hc.Transport = transport
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	start := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		res.Error = humanizeNetErr(err)
		return res
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("HTTP %d — это .onion-зеркало?", resp.StatusCode)
		return res
	}
	// Sniff the first few KB looking for a tell that this is actually the JR
	// mirror — otherwise the user might have pointed us at some unrelated
	// onion that happens to return 200.
	body := make([]byte, 32*1024)
	n, _ := resp.Body.Read(body)
	body = body[:n]
	if !strings.Contains(string(body), "/pics/post") && !strings.Contains(string(body), "joyreactor") {
		res.Error = "ответ 200, но не похоже на JR-зеркало (нет ссылок на /pics/post)"
		return res
	}
	res.OK = true
	res.LatMs = time.Since(start).Milliseconds()
	return res
}

// humanizeNetErr rewrites the most common low-level dial/timeout error
// strings into something a non-technical user can act on. Falls through to
// the original message if nothing matches.
func humanizeNetErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "не удалось подключиться к SOCKS5 — Tor не запущен или указан не тот порт?"
	case strings.Contains(msg, "context deadline exceeded"):
		return "превышен таймаут — Tor цепь слишком медленная или хост недоступен"
	case strings.Contains(msg, "no such host"):
		return "хост не резолвится — проверь URL эндпоинта"
	case strings.Contains(msg, "socks connect"):
		return "SOCKS5-сервер отверг соединение: " + msg
	}
	return msg
}

// buildGqlClient turns AppSettings into a configured *graphql.Client. When
// SOCKS5 is enabled with a valid address, GraphQL traffic is routed through
// that proxy. CDN downloads (internal/client) are NOT touched here — they
// always stay direct, because Tor is too slow for binary media transfers.
//
// On any setup error we silently fall back to the direct client rather than
// failing startup — better a working app than a dead one, and the settings
// UI lets the user notice + fix the misconfiguration.
func buildGqlClient(s AppSettings) *graphql.Client {
	transport, _ := socksTransport(s)
	return graphql.NewClientWithTransport("", transport)
}

// socksTransport builds an http.RoundTripper whose connections go through the
// configured SOCKS5 proxy. Returns (nil, nil) when SOCKS5 is disabled — the
// caller treats that as "use the default direct transport".
func socksTransport(s AppSettings) (http.RoundTripper, error) {
	if !s.Socks5Enabled || s.Socks5Addr == "" {
		return nil, nil
	}
	dialer, err := proxy.SOCKS5("tcp", s.Socks5Addr, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("build SOCKS5 dialer: %w", err)
	}
	ctxDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS5 dialer doesn't implement ContextDialer")
	}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return ctxDialer.DialContext(ctx, network, addr)
		},
		// Pooling stays at defaults — Tor doesn't mind connection reuse.
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}, nil
}
