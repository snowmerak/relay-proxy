package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/snowmerak/relay-proxy/internal/balancer"
	"github.com/snowmerak/relay-proxy/internal/circuitbreaker"
	"github.com/snowmerak/relay-proxy/internal/discovery"
	"github.com/snowmerak/relay-proxy/internal/registry"
)

// Handler is the main HTTP handler that parses the Host, resolves the app,
// selects a relay via the circuit breaker + balancer, and forwards the request.
type Handler struct {
	fetcher  relayLister
	cbReg    *circuitbreaker.Registry
	manager  *discovery.Manager
	balancer *balancer.RoundRobin
	rp       *ReverseProxy
	maxRetry int
}

type relayLister interface {
	Relays() []*registry.Relay
}

func NewHandler(
	fetcher relayLister,
	cbReg *circuitbreaker.Registry,
	manager *discovery.Manager,
	bal *balancer.RoundRobin,
) *Handler {
	rp := NewReverseProxy(bal)
	return &Handler{
		fetcher:  fetcher,
		cbReg:    cbReg,
		manager:  manager,
		balancer: bal,
		rp:       rp,
		maxRetry: 2,
	}
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
		// Host does not match any known relay domain.
		slog.Warn("proxy: unknown host", "host", host)
		http.Error(w, "unknown host", http.StatusNotFound)
		return
	}

	// No appName: the relay root itself was requested (e.g. portal.thumbgo.kr).
	// Forward directly to that relay without going through discovery.
	if appName == "" {
		slog.Info("proxy: relay root request", "relay", relay.ID)
		h.rp.ForwardRoot(w, r, relay)
		return
	}

	// Add a request-scoped timeout for discovery.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	candidates := h.manager.Resolve(ctx, appName)
	if len(candidates) == 0 {
		http.Error(w, "no upstream available", http.StatusServiceUnavailable)
		return
	}

	// Filter by circuit breaker state.
	healthy := make([]*registry.Relay, 0, len(candidates))
	for _, rel := range candidates {
		if h.cbReg.IsHealthy(rel.ID) {
			healthy = append(healthy, rel)
		}
	}
	if len(healthy) == 0 {
		http.Error(w, "all upstreams unavailable", http.StatusServiceUnavailable)
		return
	}

	// Prefer the hint relay if it is healthy.
	if relay != nil && h.cbReg.IsHealthy(relay.ID) {
		if containsRelay(healthy, relay.ID) {
			healthy = moveToFront(healthy, relay.ID)
		}
	}

	// Attempt with retry/fallback.
	// Forward() buffers the response; only flushes to w on success (<500).
	// On 5xx we discard the buffer and try the next relay.
	tried := make(map[string]bool)
	for attempt := 0; attempt <= h.maxRetry; attempt++ {
		remaining := filterNot(healthy, tried)
		if len(remaining) == 0 {
			break
		}
		chosen := h.balancer.Pick(remaining)
		if chosen == nil {
			break
		}
		tried[chosen.ID] = true

		status := h.rp.Forward(w, r, chosen, appName)

		if status < 500 {
			h.cbReg.RecordSuccess(chosen.ID)
			slog.Info("proxied", "app", appName, "relay", chosen.ID, "status", status)
			return
		}
		h.cbReg.RecordFailure(chosen.ID)
		slog.Warn("upstream error, retrying", "app", appName, "relay", chosen.ID, "status", status, "attempt", attempt+1)
	}

	http.Error(w, "upstream error", http.StatusBadGateway)
}

// parseHost extracts (appName, hintRelay) from a host.
//
// "gopher.portal.thumbgo.kr" → ("gopher", relay)
// "portal.thumbgo.kr"        → ("", relay)  — relay root, no appName
func (h *Handler) parseHost(host string) (appName string, hintRelay *registry.Relay) {
	for _, relay := range h.fetcher.Relays() {
		suffix := relay.ID
		if strings.HasSuffix(host, "."+suffix) {
			appName = strings.TrimSuffix(host, "."+suffix)
			hintRelay = relay
			return
		}
		// Exact match: the relay domain itself with no subdomain.
		if host == suffix {
			return "", relay
		}
	}
	return "", nil
}

func containsRelay(relays []*registry.Relay, id string) bool {
	for _, r := range relays {
		if r.ID == id {
			return true
		}
	}
	return false
}

func moveToFront(relays []*registry.Relay, id string) []*registry.Relay {
	out := make([]*registry.Relay, 0, len(relays))
	for _, r := range relays {
		if r.ID == id {
			out = append([]*registry.Relay{r}, out...)
		} else {
			out = append(out, r)
		}
	}
	return out
}

func filterNot(relays []*registry.Relay, exclude map[string]bool) []*registry.Relay {
	out := make([]*registry.Relay, 0, len(relays))
	for _, r := range relays {
		if !exclude[r.ID] {
			out = append(out, r)
		}
	}
	return out
}
