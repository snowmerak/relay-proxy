// Package httpclient provides an HTTP transport that bypasses the OS DNS
// resolver and queries configured upstream servers directly.
//
// This is necessary because relay-proxy sets the system DNS to itself
// (127.0.0.1). Without a bypass, relay-proxy's own outbound requests
// (registry fetch, relay probe, health check) would loop back through its
// own DNS server — which may not be running yet at startup.
package httpclient

import (
	"context"
	"net"
	"net/http"
	"time"
)

// NewBypassTransport returns an *http.Transport that resolves DNS using the
// provided upstream addresses (e.g. "8.8.8.8:53") directly, skipping the OS
// resolver. Falls back to http.DefaultTransport behaviour when upstreams is
// empty.
func NewBypassTransport(upstreams []string) *http.Transport {
	d := NewBypassDialer(upstreams)
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = d.DialContext
	return t
}

// NewBypassDialer returns a *net.Dialer that resolves DNS using the provided
// upstream addresses directly, bypassing the OS resolver.
func NewBypassDialer(upstreams []string) *net.Dialer {
	if len(upstreams) == 0 {
		return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var lastErr error
			for _, up := range upstreams {
				conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "udp", up)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}

	return &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver:  resolver,
	}
}
