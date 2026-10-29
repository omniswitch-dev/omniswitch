package router

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock for breaker tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// halfOpenBreaker returns a breaker with threshold 1 and a one-minute
// cooldown whose circuit for "p" has just become half-open.
func halfOpenBreaker(t *testing.T) (*CircuitBreaker, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	b := NewCircuitBreaker(1, time.Minute)
	b.now = clock.Now
	b.RecordFailure("p")
	if state := b.State("p"); state != CircuitOpen {
		t.Fatalf("state = %s, want open", state)
	}
	if _, ok := b.Acquire("p"); ok {
		t.Fatal("open circuit admitted a call before the cooldown ended")
	}
	clock.Advance(time.Minute)
	return b, clock
}

func TestHalfOpenAdmitsExactlyOneConcurrentProbe(t *testing.T) {
	b, _ := halfOpenBreaker(t)
	const callers = 64
	var admitted atomic.Int32
	var probe atomic.Pointer[Admission]
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if adm, ok := b.Acquire("p"); ok {
				admitted.Add(1)
				probe.Store(&adm)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := admitted.Load(); got != 1 {
		t.Fatalf("admitted %d calls in half-open, want exactly 1", got)
	}
	if !probe.Load().Probe() {
		t.Fatal("the admitted call is not marked as the probe")
	}
	if b.State("p") != CircuitHalfOpen {
		t.Fatalf("state = %s, want half-open", b.State("p"))
	}
	if b.Available("p") {
		t.Fatal("Available() = true while the probe is in flight")
	}
}

func TestHalfOpenProbeSuccessClosesCircuit(t *testing.T) {
	b, _ := halfOpenBreaker(t)
	probe, ok := b.Acquire("p")
	if !ok {
		t.Fatal("first half-open call was not admitted")
	}
	probe.Success()
	if state := b.State("p"); state != CircuitClosed {
		t.Fatalf("state = %s, want closed", state)
	}
	for i := 0; i < 10; i++ {
		adm, ok := b.Acquire("p")
		if !ok || adm.Probe() {
			t.Fatalf("call %d after recovery: admitted=%v probe=%v, want admitted non-probe", i, ok, adm.Probe())
		}
	}
}

func TestHalfOpenProbeFailureReopensForNewCooldown(t *testing.T) {
	b, clock := halfOpenBreaker(t)
	probe, ok := b.Acquire("p")
	if !ok {
		t.Fatal("first half-open call was not admitted")
	}
	clock.Advance(10 * time.Second)
	probe.Failure()
	if state := b.State("p"); state != CircuitOpen {
		t.Fatalf("state = %s, want open after a failed probe", state)
	}
	clock.Advance(time.Minute - time.Second)
	if _, ok := b.Acquire("p"); ok {
		t.Fatal("call admitted before the new cooldown ended")
	}
	clock.Advance(time.Second)
	if adm, ok := b.Acquire("p"); !ok || !adm.Probe() {
		t.Fatalf("after the new cooldown: admitted=%v probe=%v, want a new probe", ok, adm.Probe())
	}
}

func TestHalfOpenSingleProbeFailureReopensWithHigherThreshold(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	b := NewCircuitBreaker(3, time.Minute)
	b.now = clock.Now
	for i := 0; i < 3; i++ {
		b.RecordFailure("p")
	}
	clock.Advance(time.Minute)
	probe, ok := b.Acquire("p")
	if !ok {
		t.Fatal("probe not admitted")
	}
	probe.Failure()
	if state := b.State("p"); state != CircuitOpen {
		t.Fatalf("state = %s, want open after one failed probe (threshold 3)", state)
	}
}

func TestHalfOpenReleasedProbeLetsNextCallProbe(t *testing.T) {
	b, _ := halfOpenBreaker(t)
	probe, ok := b.Acquire("p")
	if !ok {
		t.Fatal("probe not admitted")
	}
	probe.Release()
	if state := b.State("p"); state != CircuitHalfOpen {
		t.Fatalf("state = %s, want half-open after a release", state)
	}
	next, ok := b.Acquire("p")
	if !ok || !next.Probe() {
		t.Fatalf("after release: admitted=%v probe=%v, want a new probe", ok, next.Probe())
	}
	// Releasing the old probe again must not free the new probe's slot.
	probe.Release()
	if _, ok := b.Acquire("p"); ok {
		t.Fatal("a stale release freed the slot of the current probe")
	}
}

func TestHalfOpenLostProbeIsReplacedAfterCooldown(t *testing.T) {
	b, clock := halfOpenBreaker(t)
	lost, ok := b.Acquire("p")
	if !ok {
		t.Fatal("probe not admitted")
	}
	clock.Advance(time.Minute - time.Second)
	if _, ok := b.Acquire("p"); ok {
		t.Fatal("second probe admitted before the first one timed out")
	}
	clock.Advance(time.Second)
	if !b.Available("p") {
		t.Fatal("Available() = false after the probe timed out")
	}
	replacement, ok := b.Acquire("p")
	if !ok || !replacement.Probe() {
		t.Fatalf("after probe timeout: admitted=%v probe=%v, want a replacement probe", ok, replacement.Probe())
	}
	// The lost probe reporting late must not end the replacement probe.
	lost.Failure()
	lost.Release()
	if state := b.State("p"); state != CircuitHalfOpen {
		t.Fatalf("state = %s, want half-open after a late report from a lost probe", state)
	}
	if _, ok := b.Acquire("p"); ok {
		t.Fatal("a late report from a lost probe freed the replacement's slot")
	}
	replacement.Failure()
	if state := b.State("p"); state != CircuitOpen {
		t.Fatalf("state = %s, want open after the replacement probe failed", state)
	}
}

func TestHalfOpenLateReportsFromOlderCalls(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	b := NewCircuitBreaker(1, time.Minute)
	b.now = clock.Now
	// Two calls are admitted while the circuit is closed.
	oldFailure, _ := b.Acquire("p")
	oldSuccess, _ := b.Acquire("p")
	b.RecordFailure("p") // opens the circuit
	clock.Advance(time.Minute)
	probe, ok := b.Acquire("p")
	if !ok {
		t.Fatal("probe not admitted")
	}

	// A late failure from an older call is ignored in half-open: only the
	// probe decides whether the circuit opens again.
	oldFailure.Failure()
	oldFailure.Release()
	if state := b.State("p"); state != CircuitHalfOpen {
		t.Fatalf("state = %s after a late failure, want half-open", state)
	}
	if _, ok := b.Acquire("p"); ok {
		t.Fatal("a late failure freed the probe slot")
	}

	// A late success closes the circuit: the provider answered.
	oldSuccess.Success()
	if state := b.State("p"); state != CircuitClosed {
		t.Fatalf("state = %s after a late success, want closed", state)
	}
	// The probe's own report afterwards is handled as a closed-state call.
	probe.Success()
	if state := b.State("p"); state != CircuitClosed {
		t.Fatalf("state = %s, want closed", state)
	}
	probe.Release()
	if adm, ok := b.Acquire("p"); !ok || adm.Probe() {
		t.Fatalf("closed circuit: admitted=%v probe=%v", ok, adm.Probe())
	}
}

func TestAvailableDoesNotTakeProbeSlot(t *testing.T) {
	b, _ := halfOpenBreaker(t)
	for i := 0; i < 5; i++ {
		if !b.Available("p") {
			t.Fatalf("Available() call %d = false, want true with no probe in flight", i)
		}
	}
	if b.State("p") != CircuitOpen {
		// Available is read-only: it does not move the state either.
		t.Fatalf("state = %s, want open (unchanged by Available)", b.State("p"))
	}
	if _, ok := b.Acquire("p"); !ok {
		t.Fatal("Acquire() after Available() calls was not admitted")
	}
	if !NewCircuitBreaker(1, time.Minute).Available("unknown") {
		t.Fatal("Available() for an unknown provider = false")
	}
}

func TestZeroAdmissionIgnoresReports(t *testing.T) {
	var adm Admission
	adm.Success()
	adm.Failure()
	adm.Release()
	if adm.Probe() {
		t.Fatal("zero Admission reports Probe() = true")
	}
}
