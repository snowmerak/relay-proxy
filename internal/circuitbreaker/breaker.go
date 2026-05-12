package circuitbreaker

import (
	"time"

	"github.com/sony/gobreaker/v2"
)

// State mirrors gobreaker states for external callers.
type State = gobreaker.State

const (
	StateClosed   = gobreaker.StateClosed
	StateHalfOpen = gobreaker.StateHalfOpen
	StateOpen     = gobreaker.StateOpen
)

// Breaker wraps gobreaker.CircuitBreaker for a single relay.
type Breaker struct {
	cb *gobreaker.CircuitBreaker[struct{}]
}

type Settings struct {
	FailureThreshold uint32
	SuccessThreshold uint32
	OpenTimeout      time.Duration
	OnStateChange    func(name string, from, to State)
}

func New(name string, s Settings) *Breaker {
	st := gobreaker.Settings{
		Name:        name,
		MaxRequests: s.SuccessThreshold,
		Interval:    0,
		Timeout:     s.OpenTimeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= s.FailureThreshold
		},
	}
	if s.OnStateChange != nil {
		st.OnStateChange = func(name string, from, to gobreaker.State) {
			s.OnStateChange(name, from, to)
		}
	}
	return &Breaker{cb: gobreaker.NewCircuitBreaker[struct{}](st)}
}

// Allow returns true when the circuit allows the request through.
func (b *Breaker) Allow() bool {
	_, err := b.cb.Execute(func() (struct{}, error) { return struct{}{}, nil })
	return err == nil
}

// RecordSuccess records a successful call.
func (b *Breaker) RecordSuccess() {
	_, _ = b.cb.Execute(func() (struct{}, error) { return struct{}{}, nil })
}

// RecordFailure records a failed call.
func (b *Breaker) RecordFailure() {
	_, _ = b.cb.Execute(func() (struct{}, error) { return struct{}{}, errFail })
}

// State returns the current circuit state.
func (b *Breaker) State() State {
	return b.cb.State()
}

var errFail = &circuitError{"simulated failure"}

type circuitError struct{ msg string }

func (e *circuitError) Error() string { return e.msg }
