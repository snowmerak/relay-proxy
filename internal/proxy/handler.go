package proxy

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/snowmerak/relay-proxy/internal/circuitbreaker"
	"github.com/snowmerak/relay-proxy/internal/registry"
)

// Handler is the main HTTP handler.
//
// The relay is already encoded in the Host subdomain:
//
//	{appName}.{relayDomain}  →  forward to that relay with Host header intact
//	{relayDomain}            →  forward relay root directly
//
// No discovery/probing needed — the target relay is known from the request.
type Handler struct {
	fetcher relayLister
	cbReg   *circuitbreaker.Registry
	rp      *ReverseProxy
}

type relayLister interface {
	Relays() []*registry.Relay
}

func NewHandler(fetcher relayLister, cbReg *circuitbreaker.Registry, rp *ReverseProxy) *Handler {
	return &Handler{fetcher: fetcher, cbReg: cbReg, rp: rp}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if host == "" {
		http.Error(w, "missing Host header", http.StatusBadRequest)
		return
	}
	// Strip port if present.
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	appName, relay := h.parseHost(host)
	if relay == nil {
		slog.Warn("proxy: unknown host", "host", host)
		http.Error(w, "unknown host", http.StatusNotFound)
		return
	}

	// Relay root request (e.g. portal.thumbgo.kr — no appName).
	if appName == "" {
		slog.Info("proxy: relay root", "relay", relay.ID)
		h.rp.ForwardRoot(w, r, relay)
		return
	}

	// Check circuit breaker.
	if !h.cbReg.IsHealthy(relay.ID) {
		slog.Warn("proxy: relay circuit open", "relay", relay.ID, "app", appName)
		http.Error(w, "relay unavailable", http.StatusServiceUnavailable)
		return
	}

	status := h.rp.Forward(w, r, relay, appName)
	if status >= 500 {
		h.cbReg.RecordFailure(relay.ID)
		slog.Warn("proxy: upstream error", "relay", relay.ID, "app", appName, "status", status)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	h.cbReg.RecordSuccess(relay.ID)
	slog.Info("proxied", "app", appName, "relay", relay.ID, "status", status)
}

// parseHost extracts (appName, relay) from a host string.
//
//	"gopher.portal.thumbgo.kr" → ("gopher", relay{portal.thumbgo.kr})
//	"portal.thumbgo.kr"        → ("",       relay{portal.thumbgo.kr})
func (h *Handler) parseHost(host string) (appName string, relay *registry.Relay) {
	for _, r := range h.fetcher.Relays() {
		if strings.HasSuffix(host, "."+r.ID) {
			return strings.TrimSuffix(host, "."+r.ID), r
		}
		if host == r.ID {
			return "", r
		}
	}
	return "", nil
}
