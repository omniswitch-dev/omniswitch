package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

var (
	rolloverJan = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rolloverFeb = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
)

// rolloverHarness drives a ledger across the end of January with a store
// load that can be held open. It wraps fakeBudgetStore with a signal when a
// load reaches the store, a per-month load count, an optional failure for
// February loads, and a clock hook that reports admit calls.
type rolloverHarness struct {
	f       *fakeBudgetStore
	clock   *fakeClock
	entered chan struct{}

	mu        sync.Mutex
	monthLoad map[time.Time]int
	failFeb   error
	// armed counts down the clock reads to report on nowCalls.
	armed    int
	nowCalls chan struct{}
}

func newRolloverHarness(janUsage float64) *rolloverHarness {
	r := &rolloverHarness{
		f:         newFakeBudgetStore(budgetLimits{MonthlyCost: 1}),
		clock:     &fakeClock{now: time.Date(2026, 1, 31, 23, 59, 50, 0, time.UTC)},
		entered:   make(chan struct{}, 16),
		monthLoad: map[time.Time]int{},
		nowCalls:  make(chan struct{}, 64),
	}
	r.f.monthly[rolloverJan] = budgetAmount{Cost: janUsage}
	return r
}

func (r *rolloverHarness) load(ctx context.Context, scope budgetScope, month time.Time, withMonthly bool) (budgetSnapshot, error) {
	r.entered <- struct{}{}
	snap, err := r.f.load(ctx, scope, month, withMonthly)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.monthLoad[month]++
	if err == nil && month.Equal(rolloverFeb) && r.failFeb != nil {
		return budgetSnapshot{}, r.failFeb
	}
	return snap, err
}

func (r *rolloverHarness) now() time.Time {
	t := r.clock.Now()
	r.mu.Lock()
	if r.armed > 0 {
		r.armed--
		r.nowCalls <- struct{}{}
	}
	r.mu.Unlock()
	return t
}

// arm reports the next n clock reads on nowCalls.
func (r *rolloverHarness) arm(n int) {
	r.mu.Lock()
	r.armed = n
	r.mu.Unlock()
}

func (r *rolloverHarness) loads(month time.Time) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.monthLoad[month]
}

func (r *rolloverHarness) attach(l *budgetLedger) {
	l.load = r.load
	l.now = r.now
}

// startStaleRefresh loads January, then starts a background refresh at
// 23:59:55 that stays in the store until release is called, and moves the
// clock past midnight. admitOnce performs the admissions that start it.
func (r *rolloverHarness) startStaleRefresh(t *testing.T, admitOnce func()) (release func()) {
	t.Helper()
	admitOnce() // loads January synchronously
	<-r.entered
	r.clock.Advance(defaultBudgetRefreshInterval)
	block := make(chan struct{})
	r.f.mu.Lock()
	r.f.block = block
	r.f.mu.Unlock()
	admitOnce() // usable, starts the background refresh for January
	<-r.entered // the refresh read its start time and is in the store
	r.clock.Advance(10 * time.Second)
	if budgetMonthStart(r.clock.Now()) != rolloverFeb {
		t.Fatalf("clock = %v, want February", r.clock.Now())
	}
	return func() {
		r.f.mu.Lock()
		r.f.block = nil
		r.f.mu.Unlock()
		close(block)
	}
}

type admitResult struct {
	res     *budgetReservation
	verdict budgetVerdict
	err     error
}

// waitingAdmits starts n admits after midnight and returns once each has
// read the clock under the ledger lock, so each has seen the running stale
// refresh and will wait for it.
func (r *rolloverHarness) waitingAdmits(l *budgetLedger, n int, hint *budgetLimits, estimate budgetAmount) chan admitResult {
	results := make(chan admitResult, n)
	r.arm(n)
	for i := 0; i < n; i++ {
		go func() {
			res, verdict, err := l.admit(context.Background(), testKeyScope, hint, estimate)
			results <- admitResult{res, verdict, err}
		}()
	}
	for i := 0; i < n; i++ {
		<-r.nowCalls
	}
	return results
}

// B1: the reviewer's repro. A request right after midnight waits on a
// refresh that started in January; it now loads February instead of
// returning unavailable, and is decided on February's usage.
func TestBudgetLedgerWaiterOnStaleRefreshLoadsNewMonth(t *testing.T) {
	r := newRolloverHarness(0.9)
	l := newBudgetLedger(r.load)
	r.attach(l)
	limits := budgetLimits{MonthlyCost: 1}
	release := r.startStaleRefresh(t, func() { mustAdmit(t, l, &limits, budgetAmount{}).release() })

	results := r.waitingAdmits(l, 1, &limits, budgetAmount{Cost: 0.5})
	release()
	got := <-results
	if got.err != nil || got.verdict.rejected() {
		t.Fatalf("admit after midnight = %+v, %v; want admitted on February's usage", got.verdict, got.err)
	}
	got.res.release()
	l.loads.Wait()
	if n := r.loads(rolloverFeb); n != 1 {
		t.Fatalf("February loads = %d, want 1", n)
	}
	if _, monthly := committedNow(l); monthly.Cost != 0 {
		t.Fatalf("February usage = %+v, want 0", monthly)
	}
}

// B1: the extra load is a real load. When it fails, the waiter still gets
// unavailable rather than a decision on January's numbers.
func TestBudgetLedgerWaiterExtraLoadErrorIsUnavailable(t *testing.T) {
	r := newRolloverHarness(0.9)
	r.failFeb = errors.New("connection refused")
	l := newBudgetLedger(r.load)
	r.attach(l)
	limits := budgetLimits{MonthlyCost: 1}
	release := r.startStaleRefresh(t, func() { mustAdmit(t, l, &limits, budgetAmount{}).release() })

	results := r.waitingAdmits(l, 1, &limits, budgetAmount{Cost: 0.5})
	release()
	got := <-results
	if !got.verdict.Unavailable || got.err == nil || got.res != nil {
		t.Fatalf("admit with a failing February load = %+v, %v; want unavailable", got.verdict, got.err)
	}
	if n := r.loads(rolloverFeb); n != 1 {
		t.Fatalf("February loads = %d, want exactly one extra load", n)
	}
}

// B1: a waiter whose shared load produced a usable snapshot does not read
// the store again.
func TestBudgetLedgerWaiterOnUsableLoadDoesNotReload(t *testing.T) {
	r := newRolloverHarness(0)
	l := newBudgetLedger(r.load)
	r.attach(l)
	limits := budgetLimits{MonthlyCost: 1}
	block := make(chan struct{})
	r.f.block = block

	first := make(chan admitResult, 1)
	go func() {
		res, verdict, err := l.admit(context.Background(), testKeyScope, &limits, budgetAmount{})
		first <- admitResult{res, verdict, err}
	}()
	<-r.entered // the first admit's load is in the store
	second := r.waitingAdmits(l, 1, &limits, budgetAmount{})
	r.f.mu.Lock()
	r.f.block = nil
	r.f.mu.Unlock()
	close(block)
	for _, ch := range []chan admitResult{first, second} {
		got := <-ch
		if got.err != nil || got.verdict.rejected() {
			t.Fatalf("admit = %+v, %v; want admitted", got.verdict, got.err)
		}
		got.res.release()
	}
	if n := r.f.loadCount(); n != 1 {
		t.Fatalf("store loads = %d, want 1 shared load", n)
	}
}

// B1: several requests waiting on the stale refresh across the boundary
// trigger one February load between them, and all are admitted.
func TestBudgetLedgerWaitersAcrossMonthLoadOnce(t *testing.T) {
	r := newRolloverHarness(0.9)
	l := newBudgetLedger(r.load)
	r.attach(l)
	limits := budgetLimits{MonthlyCost: 1}
	release := r.startStaleRefresh(t, func() { mustAdmit(t, l, &limits, budgetAmount{}).release() })

	const waiters = 8
	results := r.waitingAdmits(l, waiters, &limits, budgetAmount{Cost: 0.1})
	release()
	for i := 0; i < waiters; i++ {
		got := <-results
		if got.err != nil || got.verdict.rejected() {
			t.Fatalf("waiter %d = %+v, %v; want admitted", i, got.verdict, got.err)
		}
		defer got.res.release()
	}
	l.loads.Wait()
	if n := r.loads(rolloverFeb); n != 1 {
		t.Fatalf("February loads = %d, want 1", n)
	}
}

// B1 end to end: a chat request right after midnight UTC on the 1st, for a
// key whose January budget is used up, is admitted on February's usage
// instead of getting 503 budget_unavailable. A failing February load gives
// 503 when failing closed and 200 when failing open.
func TestBudgetMonthBoundaryChatRequest(t *testing.T) {
	cases := []struct {
		name     string
		failFeb  bool
		failOpen bool
		want     int
	}{
		{name: "february loaded", want: http.StatusOK},
		{name: "february load fails closed", failFeb: true, want: http.StatusServiceUnavailable},
		{name: "february load fails open", failFeb: true, failOpen: true, want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newGatewayTestStore(t)
			registry := provider.NewRegistry()
			registry.Register(gatewayProvider{name: "test", model: "test-model"})
			handler := New(registry, router.New(registry), st, guardrail.NewEngine())
			handler.SetBudgetFailOpen(tc.failOpen)
			r := newRolloverHarness(1) // January's budget is used up
			if tc.failFeb {
				r.failFeb = errors.New("connection refused")
			}
			r.attach(handler.budget.ledger)
			key := store.APIKey{ID: "key_test", MonthlyCostBudget: 1}

			send := func() *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`))
				req = req.WithContext(withAPIKeyBudget(req.Context(), key))
				req.Header.Set("x-omniswitch-key-id", key.ID)
				handler.ChatCompletions(rec, req)
				return rec
			}
			release := r.startStaleRefresh(t, func() {
				if rec := send(); rec.Code != http.StatusPaymentRequired {
					t.Fatalf("January request = %d %s, want 402", rec.Code, rec.Body.String())
				}
			})
			var rec *httptest.ResponseRecorder
			finished := make(chan struct{})
			r.arm(1)
			go func() {
				rec = send()
				close(finished)
			}()
			<-r.nowCalls
			release()
			<-finished
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusServiceUnavailable && !strings.Contains(rec.Body.String(), `"code":"budget_unavailable"`) {
				t.Fatalf("body = %s, want budget_unavailable", rec.Body.String())
			}
			handler.budget.ledger.loads.Wait()
		})
	}
}
