package gateway

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

// fakeBudgetStore is an in-memory store the ledger loads from. It plays the
// role of the database shared by several gateway replicas.
type fakeBudgetStore struct {
	mu       sync.Mutex
	limits   budgetLimits
	lifetime float64
	monthly  map[time.Time]budgetAmount
	err      error
	loads    int
	// block, when set, makes a load wait until it is closed.
	block chan struct{}
}

func newFakeBudgetStore(limits budgetLimits) *fakeBudgetStore {
	return &fakeBudgetStore{limits: limits, monthly: map[time.Time]budgetAmount{}}
}

func (f *fakeBudgetStore) load(ctx context.Context, _ budgetScope, month time.Time, withMonthly bool) (budgetSnapshot, error) {
	f.mu.Lock()
	block := f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return budgetSnapshot{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	if f.err != nil {
		return budgetSnapshot{}, f.err
	}
	snap := budgetSnapshot{Found: true, Limits: f.limits, LifetimeCost: f.lifetime}
	if withMonthly || f.limits.monthly() {
		snap.Month = month
		snap.Monthly = f.monthly[month]
	}
	return snap, nil
}

// write persists a charge like logRequest does.
func (f *fakeBudgetStore) write(c budgetCharge) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lifetime += c.Lifetime
	usage := f.monthly[c.Month]
	usage.add(c.Monthly)
	f.monthly[c.Month] = usage
}

func (f *fakeBudgetStore) loadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loads
}

func newTestLedger(f *fakeBudgetStore, start time.Time) (*budgetLedger, *fakeClock) {
	clock := &fakeClock{now: start}
	l := newBudgetLedger(f.load)
	l.now = clock.Now
	return l, clock
}

var testKeyScope = budgetScope{Kind: budgetScopeAPIKey, ID: "key_test"}

func mustAdmit(t *testing.T, l *budgetLedger, hint *budgetLimits, estimate budgetAmount) *budgetReservation {
	t.Helper()
	res, verdict, err := l.admit(context.Background(), testKeyScope, hint, estimate)
	if err != nil || verdict.rejected() {
		t.Fatalf("admit() = %+v, %v; want admitted", verdict, err)
	}
	return res
}

// charge settles res with c and persists it to f, like logRequest.
func charge(l *budgetLedger, f *fakeBudgetStore, res *budgetReservation, c budgetCharge) {
	pending := l.beginCharge(testKeyScope, res, c)
	f.write(c)
	pending.finish(true, true)
}

func committedNow(l *budgetLedger) (float64, budgetAmount) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[testKeyScope]
	if e == nil {
		return 0, budgetAmount{}
	}
	return e.committed(budgetMonthStart(l.now()))
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestBudgetLedgerReservationsBoundConcurrentAdmission(t *testing.T) {
	limits := budgetLimits{LifetimeCost: 1}
	f := newFakeBudgetStore(limits)
	l, _ := newTestLedger(f, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	estimate := budgetAmount{Cost: 0.3, Tokens: 10}

	var held []*budgetReservation
	for range 3 {
		held = append(held, mustAdmit(t, l, &limits, estimate))
	}
	if _, verdict, _ := l.admit(context.Background(), testKeyScope, &limits, estimate); !verdict.Denied {
		t.Fatalf("4th admission = %+v, want denied by the reservations of running requests", verdict)
	}
	held[0].release()
	held[0].release() // idempotent
	res := mustAdmit(t, l, &limits, estimate)

	// Charging releases the reservation and counts the actual cost at once.
	charge(l, f, res, budgetCharge{Lifetime: 0.5, Month: budgetMonthStart(l.now())})
	if lifetime, _ := committedNow(l); !approx(lifetime, 0.5) {
		t.Fatalf("committed lifetime = %v, want 0.5", lifetime)
	}
	// 0.5 committed + 2 x 0.3 reserved: nothing else fits.
	if _, verdict, _ := l.admit(context.Background(), testKeyScope, &limits, estimate); !verdict.Denied {
		t.Fatalf("admission = %+v, want denied", verdict)
	}
	held[1].release()
	held[2].release()
	mustAdmit(t, l, &limits, estimate).release()
	if f.loadCount() != 1 {
		t.Fatalf("store loads = %d, want 1 (one snapshot, no per-request reads)", f.loadCount())
	}
}

func TestBudgetLedgerCommittedReachedDeniesWithoutPrice(t *testing.T) {
	limits := budgetLimits{LifetimeCost: 1}
	f := newFakeBudgetStore(limits)
	f.lifetime = 1
	l, _ := newTestLedger(f, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	_, verdict, _ := l.admit(context.Background(), testKeyScope, &limits, budgetAmount{})
	if !verdict.Denied || verdict.Reason != "cost budget exceeded: 1.0000 of 1.0000 USD used" {
		t.Fatalf("verdict = %+v, want the existing denial message", verdict)
	}
}

func TestBudgetLedgerRefreshPicksUpOtherReplicasWithoutDoubleCounting(t *testing.T) {
	limits := budgetLimits{LifetimeCost: 10, MonthlyCost: 10, MonthlyTokens: 1000}
	f := newFakeBudgetStore(limits)
	l, clock := newTestLedger(f, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	month := budgetMonthStart(clock.Now())

	charge(l, f, mustAdmit(t, l, &limits, budgetAmount{Cost: 1}), budgetCharge{Lifetime: 1, Month: month, Monthly: budgetAmount{Cost: 1, Tokens: 100}})
	// Another replica spends 3 USD.
	f.write(budgetCharge{Lifetime: 3, Month: month, Monthly: budgetAmount{Cost: 3, Tokens: 300}})
	if lifetime, monthly := committedNow(l); !approx(lifetime, 1) || monthly.Tokens != 100 {
		t.Fatalf("before refresh = %v/%+v, want only the local charge", lifetime, monthly)
	}

	clock.Advance(defaultBudgetRefreshInterval)
	mustAdmit(t, l, &limits, budgetAmount{}).release() // starts an async refresh
	l.loads.Wait()
	if lifetime, monthly := committedNow(l); !approx(lifetime, 4) || !approx(monthly.Cost, 4) || monthly.Tokens != 400 {
		t.Fatalf("after refresh = %v/%+v, want 4 USD and 400 tokens (store value, local charge not counted twice)", lifetime, monthly)
	}

	// A charge that finishes while a refresh runs may or may not be in the
	// snapshot. It is kept until the next refresh, so it is never missed.
	f.mu.Lock()
	f.block = make(chan struct{})
	block := f.block
	f.mu.Unlock()
	clock.Advance(defaultBudgetRefreshInterval)
	res := mustAdmit(t, l, &limits, budgetAmount{Cost: 1})
	charge(l, f, res, budgetCharge{Lifetime: 1, Month: month, Monthly: budgetAmount{Cost: 1, Tokens: 100}})
	f.mu.Lock()
	f.block = nil
	f.mu.Unlock()
	close(block)
	l.loads.Wait()
	if lifetime, _ := committedNow(l); !approx(lifetime, 6) {
		t.Fatalf("after racing refresh = %v, want 6 (store 5 + the in-doubt charge)", lifetime)
	}
	clock.Advance(defaultBudgetRefreshInterval)
	mustAdmit(t, l, &limits, budgetAmount{}).release()
	l.loads.Wait()
	if lifetime, monthly := committedNow(l); !approx(lifetime, 5) || monthly.Tokens != 500 {
		t.Fatalf("after next refresh = %v/%+v, want exactly the store's 5 USD / 500 tokens", lifetime, monthly)
	}

	// Spend from another replica that exhausts the budget denies after
	// the refresh interval.
	f.write(budgetCharge{Lifetime: 5, Month: month, Monthly: budgetAmount{Cost: 5}})
	clock.Advance(defaultBudgetRefreshInterval)
	mustAdmit(t, l, &limits, budgetAmount{}).release()
	l.loads.Wait()
	if _, verdict, _ := l.admit(context.Background(), testKeyScope, &limits, budgetAmount{}); !verdict.Denied {
		t.Fatalf("verdict = %+v, want denied after the refresh", verdict)
	}
}

func TestBudgetLedgerMonthRollover(t *testing.T) {
	limits := budgetLimits{MonthlyCost: 1}
	f := newFakeBudgetStore(limits)
	jan := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f.monthly[jan] = budgetAmount{Cost: 0.9}
	l, clock := newTestLedger(f, time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC))

	charge(l, f, mustAdmit(t, l, &limits, budgetAmount{Cost: 0.05}), budgetCharge{Month: jan, Monthly: budgetAmount{Cost: 0.1}})
	if _, verdict, _ := l.admit(context.Background(), testKeyScope, &limits, budgetAmount{}); !verdict.Denied {
		t.Fatalf("January verdict = %+v, want denied", verdict)
	}
	// A charge still being written when the month ends belongs to January.
	pending := l.beginCharge(testKeyScope, mustAdmitAllowDenied(l, &limits), budgetCharge{Month: jan, Monthly: budgetAmount{Cost: 0.5}})

	clock.Advance(2 * time.Second) // February
	loadsBefore := f.loadCount()
	res := mustAdmit(t, l, &limits, budgetAmount{Cost: 0.5})
	if f.loadCount() != loadsBefore+1 {
		t.Fatalf("rollover did not reload the monthly usage synchronously")
	}
	if _, monthly := committedNow(l); monthly.Cost != 0 {
		t.Fatalf("February usage = %+v, want 0 (January charges do not count)", monthly)
	}
	pending.finish(true, true)
	res.release()
}

// mustAdmitAllowDenied returns a reservation or nil; the caller only needs
// a value to pass to beginCharge.
func mustAdmitAllowDenied(l *budgetLedger, limits *budgetLimits) *budgetReservation {
	res, _, _ := l.admit(context.Background(), testKeyScope, limits, budgetAmount{})
	return res
}

func TestBudgetLedgerEvictionIsBounded(t *testing.T) {
	limits := budgetLimits{LifetimeCost: 1}
	f := newFakeBudgetStore(limits)
	l, clock := newTestLedger(f, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	l.maxEntries = 3
	admit := func(id string) (*budgetReservation, budgetVerdict) {
		res, verdict, _ := l.admit(context.Background(), budgetScope{Kind: budgetScopeAPIKey, ID: id}, &limits, budgetAmount{Cost: 0.1})
		return res, verdict
	}
	for i := range 10 {
		res, verdict := admit(string(rune('a' + i)))
		if verdict.rejected() {
			t.Fatalf("admit %d = %+v", i, verdict)
		}
		res.release()
		if l.size() > 3 {
			t.Fatalf("ledger size = %d, want at most 3", l.size())
		}
	}

	// Entries with running requests are never evicted; when every entry
	// is busy a new scope cannot be tracked and is reported unavailable.
	var held []*budgetReservation
	for _, id := range []string{"x", "y", "z"} {
		res, verdict := admit(id)
		if verdict.rejected() {
			t.Fatalf("admit %s = %+v", id, verdict)
		}
		held = append(held, res)
	}
	if _, verdict := admit("w"); !verdict.Unavailable {
		t.Fatalf("admit on a full ledger = %+v, want unavailable", verdict)
	}
	for _, res := range held {
		res.release()
	}

	// Idle entries expire.
	clock.Advance(budgetIdleTTL + time.Second)
	res, _ := admit("fresh")
	res.release()
	if l.size() != 1 {
		t.Fatalf("ledger size after idle expiry = %d, want 1", l.size())
	}
}

func TestBudgetLedgerStoreErrors(t *testing.T) {
	limits := budgetLimits{LifetimeCost: 1}
	f := newFakeBudgetStore(limits)
	f.err = errors.New("database is down")
	l, clock := newTestLedger(f, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))

	if _, verdict, err := l.admit(context.Background(), testKeyScope, &limits, budgetAmount{}); !verdict.Unavailable || err == nil {
		t.Fatalf("verdict = %+v, %v; want unavailable", verdict, err)
	}
	// Within the retry backoff the store is not hammered.
	if _, verdict, _ := l.admit(context.Background(), testKeyScope, &limits, budgetAmount{}); !verdict.Unavailable || f.loadCount() != 1 {
		t.Fatalf("verdict = %+v, loads = %d; want unavailable without a new load", verdict, f.loadCount())
	}
	// A key without budgets never reads the store and is never blocked.
	if res, verdict, err := l.admit(context.Background(), testKeyScope, &budgetLimits{}, budgetAmount{Cost: 5}); res != nil || verdict.rejected() || err != nil {
		t.Fatalf("no-budget admission = %v, %+v, %v; want allowed", res, verdict, err)
	}

	// Once loaded, a failing refresh keeps the last snapshot until it is
	// too old to trust.
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	clock.Advance(budgetRetryBackoff)
	mustAdmit(t, l, &limits, budgetAmount{}).release()
	f.mu.Lock()
	f.err = errors.New("database is down")
	f.mu.Unlock()
	clock.Advance(defaultBudgetRefreshInterval)
	mustAdmit(t, l, &limits, budgetAmount{}).release()
	l.loads.Wait()
	clock.Advance(l.maxStale())
	if _, verdict, _ := l.admit(context.Background(), testKeyScope, &limits, budgetAmount{}); !verdict.Unavailable {
		t.Fatalf("verdict with a stale snapshot = %+v, want unavailable", verdict)
	}
}

func TestBudgetLedgerLostChargesKeepCounting(t *testing.T) {
	limits := budgetLimits{LifetimeCost: 1}
	f := newFakeBudgetStore(limits)
	l, clock := newTestLedger(f, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	month := budgetMonthStart(clock.Now())
	pending := l.beginCharge(testKeyScope, mustAdmit(t, l, &limits, budgetAmount{}), budgetCharge{Lifetime: 1, Month: month, Monthly: budgetAmount{Cost: 1}})
	pending.finish(false, false) // the store write failed
	clock.Advance(defaultBudgetRefreshInterval)
	_, _, _ = l.admit(context.Background(), testKeyScope, &limits, budgetAmount{})
	l.loads.Wait()
	if _, verdict, _ := l.admit(context.Background(), testKeyScope, &limits, budgetAmount{}); !verdict.Denied {
		t.Fatalf("verdict = %+v, want denied: an unpersisted charge still counts", verdict)
	}
}

func TestBudgetLedgerWithoutHintUsesStoredLimits(t *testing.T) {
	f := newFakeBudgetStore(budgetLimits{LifetimeCost: 1})
	f.lifetime = 2
	l, _ := newTestLedger(f, time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	if _, verdict, _ := l.admit(context.Background(), testKeyScope, nil, budgetAmount{}); !verdict.Denied {
		t.Fatalf("verdict = %+v, want denied from the stored limits", verdict)
	}
	f.mu.Lock()
	f.limits = budgetLimits{}
	f.err = errors.New("down")
	f.mu.Unlock()
	// The snapshot says "no budget" only after a reload; before that the
	// cached limits still apply and no extra load is made per request.
	loads := f.loadCount()
	_, _, _ = l.admit(context.Background(), testKeyScope, nil, budgetAmount{})
	if f.loadCount() != loads {
		t.Fatalf("store loaded again within the refresh interval")
	}
}
