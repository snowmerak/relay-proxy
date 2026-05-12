package discovery

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/snowmerak/relay-proxy/internal/registry"
)

// Prober checks whether a given appName is reachable on each relay.
type Prober struct {
	timeout     time.Duration
	concurrency int
}

func NewProber(timeout time.Duration, concurrency int) *Prober {
	return &Prober{timeout: timeout, concurrency: concurrency}
}

// Probe checks all provided relays in parallel and returns those that serve appName.
// isHealthy filters relays before attempting the connection.
func (p *Prober) Probe(ctx context.Context, appName string, relays []*registry.Relay, isHealthy func(relayID string) bool) []*registry.Relay {
	type result struct {
		relay *registry.Relay
		ok    bool
	}

	sem := make(chan struct{}, p.concurrency)
	results := make(chan result, len(relays))
	var wg sync.WaitGroup

	client := &http.Client{
		Timeout: p.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // treat redirect as "exists"
		},
	}

	for _, relay := range relays {
		if !isHealthy(relay.ID) {
			continue
		}
		wg.Add(1)
		go func(r *registry.Relay) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			target := fmt.Sprintf("%s/%s/", r.BaseURL.String(), appName)
			reqCtx, cancel := context.WithTimeout(ctx, p.timeout)
			defer cancel()

			req, err := http.NewRequestWithContext(reqCtx, http.MethodHead, target, nil)
			if err != nil {
				results <- result{r, false}
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				results <- result{r, false}
				return
			}
			resp.Body.Close()
			ok := resp.StatusCode != http.StatusNotFound && resp.StatusCode < 500
			results <- result{r, ok}
		}(relay)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var found []*registry.Relay
	for res := range results {
		if res.ok {
			found = append(found, res.relay)
		}
	}
	return found
}
