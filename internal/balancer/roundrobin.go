package balancer

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/snowmerak/relay-proxy/internal/registry"
)

const ewmaAlpha = 0.2 // smoothing factor for latency EWMA

// entry holds the weighted state for a single relay.
type entry struct {
	relay       *registry.Relay
	ewmaLatency float64 // milliseconds
	mu          sync.Mutex
}

func (e *entry) weight() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ewmaLatency <= 0 {
		return 1.0
	}
	// Higher weight for lower latency.
	return 1.0 / e.ewmaLatency
}

func (e *entry) recordLatency(d time.Duration) {
	ms := float64(d.Milliseconds())
	if ms < 1 {
		ms = 1
	}
	e.mu.Lock()
	if e.ewmaLatency == 0 {
		e.ewmaLatency = ms
	} else {
		e.ewmaLatency = ewmaAlpha*ms + (1-ewmaAlpha)*e.ewmaLatency
	}
	e.mu.Unlock()
}

// RoundRobin is a weighted round-robin balancer.
type RoundRobin struct {
	mu      sync.RWMutex
	entries []*entry
	counter atomic.Int64
}

func NewRoundRobin() *RoundRobin {
	return &RoundRobin{}
}

// Pick selects a relay from the candidates using weighted round-robin.
// candidates must be a non-empty subset of relays known to be healthy.
func (rr *RoundRobin) Pick(candidates []*registry.Relay) *registry.Relay {
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) == 1 {
		return candidates[0]
	}

	// Build weight slice for this call.
	type weighted struct {
		relay  *registry.Relay
		weight float64
	}
	items := make([]weighted, 0, len(candidates))
	total := 0.0
	for _, r := range candidates {
		w := rr.getWeight(r.ID)
		items = append(items, weighted{r, w})
		total += w
	}

	// Smooth weighted selection using a counter-based approach.
	idx := int(rr.counter.Add(1)-1) % len(items)
	// Walk forward proportionally by weight.
	threshold := (float64(idx) / float64(len(items))) * total
	cumulative := 0.0
	for _, item := range items {
		cumulative += item.weight
		if cumulative > threshold {
			return item.relay
		}
	}
	return items[len(items)-1].relay
}

// RecordLatency updates the EWMA latency for a relay.
func (rr *RoundRobin) RecordLatency(relayID string, d time.Duration) {
	rr.mu.RLock()
	for _, e := range rr.entries {
		if e.relay.ID == relayID {
			rr.mu.RUnlock()
			e.recordLatency(d)
			return
		}
	}
	rr.mu.RUnlock()
}

// Register adds a relay to the balancer's state table.
func (rr *RoundRobin) Register(relay *registry.Relay) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	for _, e := range rr.entries {
		if e.relay.ID == relay.ID {
			return
		}
	}
	rr.entries = append(rr.entries, &entry{relay: relay, ewmaLatency: 0})
}

// Deregister removes a relay from the state table.
func (rr *RoundRobin) Deregister(relayID string) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	out := rr.entries[:0]
	for _, e := range rr.entries {
		if e.relay.ID != relayID {
			out = append(out, e)
		}
	}
	rr.entries = out
}

func (rr *RoundRobin) getWeight(relayID string) float64 {
	rr.mu.RLock()
	defer rr.mu.RUnlock()
	for _, e := range rr.entries {
		if e.relay.ID == relayID {
			return e.weight()
		}
	}
	return math.SmallestNonzeroFloat64
}
