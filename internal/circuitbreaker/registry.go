package circuitbreaker

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/snowmerak/relay-proxy/internal/registry"
)

// Registry manages one Breaker per relay and runs health checks.
type Registry struct {
	settings   Settings
	hcInterval time.Duration
	hcTimeout  time.Duration
	transport  http.RoundTripper

	mu       sync.RWMutex
	breakers map[string]*Breaker // keyed by relay ID

	// onOpen is called when a relay's circuit opens.
	onOpen func(relayID string)
}

func NewRegistry(settings Settings, hcInterval, hcTimeout time.Duration, onOpen func(relayID string), transport http.RoundTripper) *Registry {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Registry{
		settings:   settings,
		hcInterval: hcInterval,
		hcTimeout:  hcTimeout,
		transport:  transport,
		breakers:   make(map[string]*Breaker),
		onOpen:     onOpen,
	}
}

// Add registers a new relay's breaker and starts its health check goroutine.
func (r *Registry) Add(ctx context.Context, relay *registry.Relay) {
	r.mu.Lock()
	if _, exists := r.breakers[relay.ID]; exists {
		r.mu.Unlock()
		return
	}
	onOpen := r.onOpen
	relayID := relay.ID
	s := r.settings
	s.OnStateChange = func(name string, from, to State) {
		slog.Info("circuit state change", "relay", name, "from", from, "to", to)
		if to == StateOpen && onOpen != nil {
			onOpen(relayID)
		}
	}
	b := New(relay.ID, s)
	r.breakers[relay.ID] = b
	r.mu.Unlock()

	go r.healthCheckLoop(ctx, relay, b)
}

// Remove deregisters a relay's breaker.
func (r *Registry) Remove(relayID string) {
	r.mu.Lock()
	delete(r.breakers, relayID)
	r.mu.Unlock()
}

// IsHealthy returns true when the relay's circuit is Closed or HalfOpen.
func (r *Registry) IsHealthy(relayID string) bool {
	r.mu.RLock()
	b, ok := r.breakers[relayID]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	return b.State() != StateOpen
}

// RecordSuccess records a successful upstream call for the relay.
func (r *Registry) RecordSuccess(relayID string) {
	r.mu.RLock()
	b, ok := r.breakers[relayID]
	r.mu.RUnlock()
	if ok {
		b.RecordSuccess()
	}
}

// RecordFailure records a failed upstream call for the relay.
func (r *Registry) RecordFailure(relayID string) {
	r.mu.RLock()
	b, ok := r.breakers[relayID]
	r.mu.RUnlock()
	if ok {
		b.RecordFailure()
	}
}

func (r *Registry) healthCheckLoop(ctx context.Context, relay *registry.Relay, b *Breaker) {
	ticker := time.NewTicker(r.hcInterval)
	defer ticker.Stop()

	client := &http.Client{Timeout: r.hcTimeout, Transport: r.transport}
	healthURL := relay.BaseURL.String()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Only probe when circuit is not already open to avoid noise.
			if b.State() == StateOpen {
				continue
			}
			reqCtx, cancel := context.WithTimeout(ctx, r.hcTimeout)
			req, err := http.NewRequestWithContext(reqCtx, http.MethodHead, healthURL, nil)
			if err != nil {
				cancel()
				b.RecordFailure()
				continue
			}
			resp, err := client.Do(req)
			cancel()
			if err != nil {
				slog.Warn("health check failed", "relay", relay.ID, "err", err)
				b.RecordFailure()
			} else {
				resp.Body.Close()
				if resp.StatusCode >= 500 {
					b.RecordFailure()
				} else {
					b.RecordSuccess()
				}
			}
		}
	}
}
