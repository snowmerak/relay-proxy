package discovery

import (
	"context"
	"sync"
	"time"

	"github.com/snowmerak/relay-proxy/internal/registry"
)

type appEntry struct {
	relays   []*registry.Relay
	probedAt time.Time
	mu       sync.RWMutex
	// probing is used to coalesce concurrent probe requests for the same app.
	probing bool
	done    chan struct{} // closed when probe completes
}

// Manager is the in-memory AppRegistry: appName → []Relay.
type Manager struct {
	probeTTL  time.Duration
	prober    *Prober
	isHealthy func(relayID string) bool
	relaysFn  func() []*registry.Relay // returns current relay list

	entries sync.Map // string → *appEntry
}

func NewManager(probeTTL time.Duration, prober *Prober, relaysFn func() []*registry.Relay, isHealthy func(relayID string) bool) *Manager {
	return &Manager{
		probeTTL:  probeTTL,
		prober:    prober,
		relaysFn:  relaysFn,
		isHealthy: isHealthy,
	}
}

// Resolve returns the list of relays that currently serve appName.
// It blocks until a probe completes if no valid cache entry exists.
func (m *Manager) Resolve(ctx context.Context, appName string) []*registry.Relay {
	for {
		val, _ := m.entries.LoadOrStore(appName, &appEntry{done: make(chan struct{})})
		entry := val.(*appEntry)

		entry.mu.RLock()
		valid := !entry.probedAt.IsZero() && time.Since(entry.probedAt) < m.probeTTL
		relays := entry.relays
		probing := entry.probing
		entry.mu.RUnlock()

		if valid {
			return relays
		}

		// Try to become the prober.
		entry.mu.Lock()
		// Re-check after acquiring write lock.
		if !entry.probedAt.IsZero() && time.Since(entry.probedAt) < m.probeTTL {
			entry.mu.Unlock()
			return entry.relays
		}
		if entry.probing {
			doneCh := entry.done
			entry.mu.Unlock()
			// Another goroutine is probing — wait for it.
			select {
			case <-doneCh:
			case <-ctx.Done():
				return nil
			}
			continue
		}
		// Reset done channel and mark as probing.
		entry.probing = true
		entry.done = make(chan struct{})
		doneCh := entry.done
		entry.mu.Unlock()

		_ = probing // suppress unused warning

		// Run probe outside the lock.
		relays = m.prober.Probe(ctx, appName, m.relaysFn(), m.isHealthy)

		entry.mu.Lock()
		entry.relays = relays
		entry.probedAt = time.Now()
		entry.probing = false
		entry.mu.Unlock()

		close(doneCh)
		return relays
	}
}

// InvalidateRelay removes a relay from every cached AppEntry.
// Called when a circuit opens for that relay.
func (m *Manager) InvalidateRelay(relayID string) {
	m.entries.Range(func(key, val any) bool {
		entry := val.(*appEntry)
		entry.mu.Lock()
		filtered := entry.relays[:0]
		for _, r := range entry.relays {
			if r.ID != relayID {
				filtered = append(filtered, r)
			}
		}
		entry.relays = filtered
		entry.mu.Unlock()
		return true
	})
}

// InvalidateApp forces a re-probe for a specific app on the next request.
func (m *Manager) InvalidateApp(appName string) {
	m.entries.Delete(appName)
}
