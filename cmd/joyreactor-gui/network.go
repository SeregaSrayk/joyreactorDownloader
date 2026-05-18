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

// TestNetwork is a Wails-bound method exposed to the frontend. It builds a
// one-shot client with the given SOCKS5 + endpoint settings and runs a tiny
// `{__typename}` query against it, returning latency or a human-readable
// error. Doesn't touch the running app's client — pure dry-run.
func (g *GUI) TestNetwork(socks5Enabled bool, socks5Addr, endpoint string) NetworkTestResult {
	res := NetworkTestResult{Address: endpoint}
	if endpoint == "" {
		res.Address = graphql.DefaultEndpoint
	}
	transport, err := socksTransport(AppSettings{
		Socks5Enabled: socks5Enabled,
		Socks5Addr:    socks5Addr,
	})
	if err != nil {
		res.Error = err.Error()
		return res
	}
	client := graphql.NewClientWithTransport(endpoint, transport)

	// Bound the probe — the user is staring at the UI waiting for it.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

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
// that proxy (typically Tor). CDN downloads (internal/client) are NOT touched
// here — they always stay on clearnet, both because Tor is too slow for media
// and because hotlink-protected JR CDN URLs are not geo-filtered anyway.
//
// On any setup error we silently fall back to the clearnet client rather than
// failing startup — better a working clearnet app than a dead one, and the
// settings UI lets the user notice + fix the misconfiguration.
func buildGqlClient(s AppSettings) *graphql.Client {
	endpoint := s.GraphQLEndpoint // empty ⇒ NewClient picks the clearnet default
	transport, _ := socksTransport(s)
	return graphql.NewClientWithTransport(endpoint, transport)
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
