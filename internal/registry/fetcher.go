package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type registryJSON struct {
	Relays []string `json:"relays"`
}

// Fetcher periodically fetches the relay registry and notifies subscribers.
type Fetcher struct {
	registryURL     string
	refreshInterval time.Duration
	httpTimeout     time.Duration
	transport       http.RoundTripper

	mu     sync.RWMutex
	relays []*Relay

	subscribers []func(added, removed []*Relay)
	subMu       sync.Mutex
}

func NewFetcher(registryURL string, refreshInterval, httpTimeout time.Duration, transport http.RoundTripper) *Fetcher {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Fetcher{
		registryURL:     registryURL,
		refreshInterval: refreshInterval,
		httpTimeout:     httpTimeout,
		transport:       transport,
	}
}

// Subscribe registers a callback invoked on each successful refresh with diffs.
func (f *Fetcher) Subscribe(fn func(added, removed []*Relay)) {
	f.subMu.Lock()
	defer f.subMu.Unlock()
	f.subscribers = append(f.subscribers, fn)
}

// Relays returns the current relay list (snapshot).
func (f *Fetcher) Relays() []*Relay {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]*Relay, len(f.relays))
	copy(out, f.relays)
	return out
}

// Run starts the fetch loop and blocks until ctx is cancelled.
func (f *Fetcher) Run(ctx context.Context) {
	if err := f.fetch(ctx); err != nil {
		slog.Error("initial registry fetch failed", "err", err)
	}

	ticker := time.NewTicker(f.refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := f.fetch(ctx); err != nil {
				slog.Error("registry refresh failed", "err", err)
			}
		}
	}
}

func (f *Fetcher) fetch(ctx context.Context) error {
	client := &http.Client{Timeout: f.httpTimeout, Transport: f.transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.registryURL, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry fetch: unexpected status %d", resp.StatusCode)
	}

	var reg registryJSON
	if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
		return err
	}

	fresh := make([]*Relay, 0, len(reg.Relays))
	for _, rawURL := range reg.Relays {
		u, err := url.Parse(rawURL)
		if err != nil {
			slog.Warn("invalid relay URL", "url", rawURL, "err", err)
			continue
		}
		fresh = append(fresh, &Relay{ID: u.Host, BaseURL: u})
	}

	f.mu.Lock()
	old := f.relays
	f.relays = fresh
	f.mu.Unlock()

	added, removed := diff(old, fresh)
	if len(added) > 0 || len(removed) > 0 {
		f.subMu.Lock()
		subs := make([]func([]*Relay, []*Relay), len(f.subscribers))
		copy(subs, f.subscribers)
		f.subMu.Unlock()
		for _, fn := range subs {
			fn(added, removed)
		}
	}

	return nil
}

func diff(old, fresh []*Relay) (added, removed []*Relay) {
	oldSet := make(map[string]*Relay, len(old))
	for _, r := range old {
		oldSet[r.ID] = r
	}
	freshSet := make(map[string]*Relay, len(fresh))
	for _, r := range fresh {
		freshSet[r.ID] = r
	}
	for id, r := range freshSet {
		if _, ok := oldSet[id]; !ok {
			added = append(added, r)
		}
	}
	for id, r := range oldSet {
		if _, ok := freshSet[id]; !ok {
			removed = append(removed, r)
		}
	}
	return
}
