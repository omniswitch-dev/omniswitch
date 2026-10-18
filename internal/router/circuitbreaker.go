package router

import (
	"sync"
	"time"
)

type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitOpen     CircuitState = "open"
	CircuitHalfOpen CircuitState = "half-open"
)

// CircuitBreaker tracks provider health. A provider's circuit opens after
// threshold consecutive counted failures and stays open for cooldown. When
// the cooldown ends the circuit is half-open: exactly one request (the
// probe) may reach the provider at a time. The probe's success closes the
// circuit; its failure opens it again for a new cooldown; a probe that ends
// without a verdict (for example the caller cancelled) is released so the
// next request can probe. A probe that reports nothing within cooldown is
// treated as lost and the next request may take its place.
//
// Reports from requests that were admitted before the circuit opened can
// arrive while it is half-open. A late success closes the circuit (the
// provider answered). A late failure is ignored in the half-open state:
// only the probe decides whether the circuit opens again.
type CircuitBreaker struct {
	mu        sync.Mutex
	entries   map[string]*circuitEntry
	threshold int
	cooldown  time.Duration
	now       func() time.Time
	lastProbe uint64
}

type circuitEntry struct {
	state       CircuitState
	failures    int
	openUntil   time.Time
	lastFailure time.Time
	// probe is the ID of the in-flight half-open probe, or 0 when none.
	probe uint64
	// probeExpires is when an unreported probe counts as lost.
	probeExpires time.Time
}

// Admission is the breaker's permission for one provider call. Report the
// outcome of the call with exactly one of Success, Failure or Release.
// The zero Admission (no breaker) ignores every report.
type Admission struct {
	breaker  *CircuitBreaker
	provider string
	probe    uint64
}

// Probe reports whether the admission is the half-open probe.
func (a Admission) Probe() bool { return a.probe != 0 }

// Success records that the provider answered; it closes the circuit.
func (a Admission) Success() {
	if a.breaker != nil {
		a.breaker.recordSuccess(a.provider)
	}
}

// Failure records a provider failure. For the probe it opens the circuit
// for a new cooldown.
func (a Admission) Failure() {
	if a.breaker != nil {
		a.breaker.recordFailure(a.provider, a.probe)
	}
}

// Release ends the call without a verdict on the provider's health (for
// example the caller cancelled). If the admission is the probe, the next
// request may probe; the circuit state does not change.
func (a Admission) Release() {
	if a.breaker != nil && a.probe != 0 {
		a.breaker.release(a.provider, a.probe)
	}
}

func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 5
	}
	if cooldown <= 0 {
		cooldown = 60 * time.Second
	}
	return &CircuitBreaker{
		entries:   map[string]*circuitEntry{},
		threshold: threshold,
		cooldown:  cooldown,
		now:       time.Now,
	}
}

// Settings returns the failure threshold and the cooldown of the breaker.
// They do not change after NewCircuitBreaker.
func (b *CircuitBreaker) Settings() (threshold int, cooldown time.Duration) {
	return b.threshold, b.cooldown
}

// Acquire asks whether a call to providerName may go ahead. In the closed
// state every call is admitted. In the open state no call is admitted until
// the cooldown ends. In the half-open state only one call (the probe) is
// admitted at a time. The caller must report the outcome on the returned
// Admission.
func (b *CircuitBreaker) Acquire(providerName string) (Admission, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entry(providerName)
	now := b.now()
	switch entry.state {
	case CircuitClosed:
		return Admission{breaker: b, provider: providerName}, true
	case CircuitOpen:
		if now.Before(entry.openUntil) {
			return Admission{}, false
		}
		entry.state = CircuitHalfOpen
		entry.probe = 0
	}
	if entry.probe != 0 && now.Before(entry.probeExpires) {
		return Admission{}, false
	}
	b.lastProbe++
	entry.probe = b.lastProbe
	entry.probeExpires = now.Add(b.cooldown)
	return Admission{breaker: b, provider: providerName, probe: entry.probe}, true
}

// Available reports, without taking the half-open probe slot, whether
// Acquire would admit a call to providerName now.
func (b *CircuitBreaker) Available(providerName string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.entries[providerName]
	if !ok {
		return true
	}
	now := b.now()
	switch entry.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		return !now.Before(entry.openUntil)
	default:
		return entry.probe == 0 || !now.Before(entry.probeExpires)
	}
}

// RecordSuccess records a success that is not tied to an Admission. It
// closes the circuit.
func (b *CircuitBreaker) RecordSuccess(providerName string) {
	b.recordSuccess(providerName)
}

// RecordFailure records a failure that is not tied to an Admission. It is
// counted like a failure of a non-probe call: in the half-open state it is
// ignored.
func (b *CircuitBreaker) RecordFailure(providerName string) {
	b.recordFailure(providerName, 0)
}

func (b *CircuitBreaker) recordSuccess(providerName string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entry(providerName)
	entry.state = CircuitClosed
	entry.failures = 0
	entry.openUntil = time.Time{}
	entry.probe = 0
	entry.probeExpires = time.Time{}
}

func (b *CircuitBreaker) recordFailure(providerName string, probe uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entry(providerName)
	now := b.now()
	entry.lastFailure = now
	if entry.state == CircuitHalfOpen {
		if probe == 0 || probe != entry.probe {
			// A late failure from a call admitted before the circuit
			// opened, or from a probe already counted as lost.
			return
		}
		entry.state = CircuitOpen
		entry.openUntil = now.Add(b.cooldown)
		entry.probe = 0
		entry.probeExpires = time.Time{}
		return
	}
	entry.failures++
	if entry.failures >= b.threshold {
		entry.state = CircuitOpen
		entry.openUntil = now.Add(b.cooldown)
	}
}

func (b *CircuitBreaker) release(providerName string, probe uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entry(providerName)
	if entry.state == CircuitHalfOpen && entry.probe == probe {
		entry.probe = 0
		entry.probeExpires = time.Time{}
	}
}

func (b *CircuitBreaker) State(providerName string) CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.entry(providerName).state
}

func (b *CircuitBreaker) entry(providerName string) *circuitEntry {
	entry, ok := b.entries[providerName]
	if !ok {
		entry = &circuitEntry{state: CircuitClosed}
		b.entries[providerName] = entry
	}
	return entry
}
