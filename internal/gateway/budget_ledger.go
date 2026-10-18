package gateway

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// The budget ledger admits requests against spend budgets atomically inside
// one gateway process. For every budget scope (today only API keys) it keeps:
//
//   - a snapshot of the committed usage read from the store, refreshed on a
//     short interval so spend written by other replicas becomes visible;
//   - the estimated cost of every admitted, still running request
//     (reservations);
//   - the actual charges this process made since the snapshot was read.
//
// Admission compares snapshot + local charges + reservations + the new
// request's estimate to the limits under one lock, so concurrent requests
// cannot jointly pass a budget that only fits some of them.
//
// Local charges are grouped by refresh generation. A charge whose store write
// finished before a refresh started is part of that refresh's snapshot and is
// dropped when the refresh completes. A charge that finished while the
// refresh ran may or may not be in the snapshot; it is kept (counted twice
// for at most one refresh interval) so a charge is never missed.

const (
	defaultBudgetRefreshInterval = 5 * time.Second
	// budgetMaxStaleFactor: a snapshot older than factor x refresh interval
	// (because refreshes keep failing) is no longer trusted.
	budgetMaxStaleFactor = 12
	budgetMinMaxStale    = time.Minute
	budgetLoadTimeout    = 3 * time.Second
	budgetRetryBackoff   = time.Second
	budgetIdleTTL        = 10 * time.Minute
	budgetMaxEntries     = 10000
	// budgetEvictScan bounds how many LRU entries one insertion inspects
	// when the ledger is full.
	budgetEvictScan = 64
)

// budgetScopeKind names what a budget applies to. Workspace and model
// budgets can be added as further kinds with their own loader.
type budgetScopeKind string

const budgetScopeAPIKey budgetScopeKind = "api_key"

type budgetScope struct {
	Kind budgetScopeKind
	ID   string
}

// budgetLimits are the caps of one scope. Zero means "no limit".
type budgetLimits struct {
	// LifetimeCost caps the lifetime spend (api_keys.budget_usd).
	LifetimeCost float64
	// MonthlyCost and MonthlyTokens cap usage since the start of the
	// current UTC month.
	MonthlyCost   float64
	MonthlyTokens int64
}

func (l budgetLimits) any() bool {
	return l.LifetimeCost > 0 || l.monthly()
}

func (l budgetLimits) monthly() bool {
	return l.MonthlyCost > 0 || l.MonthlyTokens > 0
}

// budgetAmount is a cost and token quantity.
type budgetAmount struct {
	Cost   float64
	Tokens int64
}

func (a *budgetAmount) add(b budgetAmount) {
	a.Cost += b.Cost
	a.Tokens += b.Tokens
}

// budgetSnapshot is the committed usage of one scope read from the store.
type budgetSnapshot struct {
	// Found is false when the scope does not exist in the store; it then
	// has no limits.
	Found        bool
	Limits       budgetLimits
	LifetimeCost float64
	// Month is the start of the UTC month Monthly covers, or zero when the
	// monthly usage was not loaded.
	Month   time.Time
	Monthly budgetAmount
}

// budgetLoader reads a scope's snapshot. withMonthly asks it to load the
// usage of the month that starts at month even when the stored limits have
// no monthly budget (the caller knows newer limits).
type budgetLoader func(ctx context.Context, scope budgetScope, month time.Time, withMonthly bool) (budgetSnapshot, error)

// budgetCharge is the actual usage of one finished request.
type budgetCharge struct {
	// Lifetime counts toward the lifetime spend.
	Lifetime float64
	// Month is the UTC month start the monthly part belongs to.
	Month   time.Time
	Monthly budgetAmount
}

type budgetDoneKey struct {
	gen   uint64
	month time.Time
}

type budgetEntry struct {
	scope    budgetScope
	elem     *list.Element
	lastUsed time.Time

	// limits are the most recent limits seen for this scope, used for
	// budget alerts at charge time.
	limits budgetLimits

	snap    budgetSnapshot
	hasSnap bool
	snapAt  time.Time

	loading   chan struct{}
	loadErrAt time.Time

	// gen is the current refresh generation; a load started at gen g
	// advances it to g+1.
	gen          uint64
	reserved     budgetAmount
	reservations int
	pending      map[*budgetPending]struct{}
	done         map[budgetDoneKey]*budgetCharge
	// lost holds charges whose store write failed, keyed by month. They
	// are counted until the entry is evicted.
	lost map[time.Time]*budgetCharge
}

func (e *budgetEntry) busy() bool {
	return e.reservations > 0 || len(e.pending) > 0 || e.loading != nil
}

// committed returns the lifetime spend and the usage of month known to this
// process: snapshot plus local charges. Callers hold the ledger lock.
func (e *budgetEntry) committed(month time.Time) (float64, budgetAmount) {
	lifetime := e.snap.LifetimeCost
	var monthly budgetAmount
	if e.snap.Month.Equal(month) {
		monthly = e.snap.Monthly
	}
	addCharge := func(c *budgetCharge) {
		lifetime += c.Lifetime
		if c.Month.Equal(month) {
			monthly.add(c.Monthly)
		}
	}
	for p := range e.pending {
		addCharge(&p.charge)
	}
	for _, c := range e.done {
		addCharge(c)
	}
	for _, c := range e.lost {
		addCharge(c)
	}
	return lifetime, monthly
}

// budgetVerdict is the result of an admission check.
type budgetVerdict struct {
	// Denied is set when a budget would be exceeded (HTTP 402).
	Denied bool
	// Unavailable is set when the budget state could not be read (HTTP
	// 503 unless the gateway fails open).
	Unavailable bool
	Reason      string
}

// budgetReservation holds the estimate of one admitted request until the
// request is charged or ends.
type budgetReservation struct {
	ledger   *budgetLedger
	entry    *budgetEntry
	amount   budgetAmount
	released bool // guarded by ledger.mu
}

// release returns the reservation without a charge. It is idempotent and
// safe on a nil reservation.
func (r *budgetReservation) release() {
	if r == nil {
		return
	}
	r.ledger.mu.Lock()
	defer r.ledger.mu.Unlock()
	r.releaseLocked()
}

func (r *budgetReservation) releaseLocked() {
	if r.released {
		return
	}
	r.released = true
	r.entry.reserved.Cost -= r.amount.Cost
	r.entry.reserved.Tokens -= r.amount.Tokens
	r.entry.reservations--
	if r.entry.reservations == 0 {
		// Drop float rounding left over from the subtractions.
		r.entry.reserved = budgetAmount{}
	}
}

// budgetPending is a charge whose store write is in progress.
type budgetPending struct {
	ledger *budgetLedger
	entry  *budgetEntry
	charge budgetCharge
	done   bool
}

type budgetLedger struct {
	load budgetLoader
	now  func() time.Time

	refreshNanos atomic.Int64
	maxEntries   int
	idleTTL      time.Duration
	retryBackoff time.Duration

	mu      sync.Mutex
	entries map[budgetScope]*budgetEntry
	lru     *list.List // front = most recently used

	// loads tracks running store loads so tests can wait for them.
	loads sync.WaitGroup
}

func newBudgetLedger(load budgetLoader) *budgetLedger {
	l := &budgetLedger{
		load:         load,
		now:          time.Now,
		maxEntries:   budgetMaxEntries,
		idleTTL:      budgetIdleTTL,
		retryBackoff: budgetRetryBackoff,
		entries:      make(map[budgetScope]*budgetEntry),
		lru:          list.New(),
	}
	l.refreshNanos.Store(int64(defaultBudgetRefreshInterval))
	return l
}

func (l *budgetLedger) setRefreshInterval(interval time.Duration) {
	if interval <= 0 {
		interval = defaultBudgetRefreshInterval
	}
	l.refreshNanos.Store(int64(interval))
}

func (l *budgetLedger) refreshInterval() time.Duration {
	return time.Duration(l.refreshNanos.Load())
}

func (l *budgetLedger) maxStale() time.Duration {
	stale := budgetMaxStaleFactor * l.refreshInterval()
	if stale < budgetMinMaxStale {
		stale = budgetMinMaxStale
	}
	return stale
}

func budgetMonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// entryLocked returns the scope's entry, creating it when needed. It
// returns nil when the ledger is full of busy entries.
func (l *budgetLedger) entryLocked(scope budgetScope, now time.Time) *budgetEntry {
	if e, ok := l.entries[scope]; ok {
		e.lastUsed = now
		l.lru.MoveToFront(e.elem)
		return e
	}
	l.evictIdleLocked(now)
	if len(l.entries) >= l.maxEntries && !l.evictOneLocked() {
		return nil
	}
	e := &budgetEntry{
		scope:    scope,
		lastUsed: now,
		pending:  make(map[*budgetPending]struct{}),
		done:     make(map[budgetDoneKey]*budgetCharge),
		lost:     make(map[time.Time]*budgetCharge),
	}
	e.elem = l.lru.PushFront(e)
	l.entries[scope] = e
	return e
}

// evictIdleLocked removes entries that were not used for idleTTL, oldest
// first, stopping at the first one that is still recent or busy.
func (l *budgetLedger) evictIdleLocked(now time.Time) {
	for elem := l.lru.Back(); elem != nil; {
		e := elem.Value.(*budgetEntry)
		if now.Sub(e.lastUsed) < l.idleTTL || e.busy() {
			return
		}
		prev := elem.Prev()
		l.removeLocked(e)
		elem = prev
	}
}

// evictOneLocked removes the least recently used entry that is not busy.
func (l *budgetLedger) evictOneLocked() bool {
	scanned := 0
	for elem := l.lru.Back(); elem != nil && scanned < budgetEvictScan; elem = elem.Prev() {
		scanned++
		if e := elem.Value.(*budgetEntry); !e.busy() {
			l.removeLocked(e)
			return true
		}
	}
	return false
}

func (l *budgetLedger) removeLocked(e *budgetEntry) {
	l.lru.Remove(e.elem)
	delete(l.entries, e.scope)
}

// startLoadLocked marks a load of e as running and returns its done channel
// and the generation it started at.
func (l *budgetLedger) startLoadLocked(e *budgetEntry) (chan struct{}, uint64) {
	ch := make(chan struct{})
	e.loading = ch
	startGen := e.gen
	e.gen++
	l.loads.Add(1)
	return ch, startGen
}

// runLoad reads the snapshot and installs it. It must be called without the
// lock, after startLoadLocked.
func (l *budgetLedger) runLoad(e *budgetEntry, ch chan struct{}, startGen uint64, month time.Time, withMonthly bool) error {
	defer l.loads.Done()
	startedAt := l.now()
	ctx, cancel := context.WithTimeout(context.Background(), budgetLoadTimeout)
	snap, err := l.load(ctx, e.scope, month, withMonthly)
	cancel()

	l.mu.Lock()
	defer l.mu.Unlock()
	defer close(ch)
	e.loading = nil
	if err != nil {
		e.loadErrAt = l.now()
		return err
	}
	e.snap = snap
	e.hasSnap = true
	e.snapAt = startedAt
	e.loadErrAt = time.Time{}
	for key := range e.done {
		if key.gen <= startGen {
			delete(e.done, key)
		}
	}
	return nil
}

// errBudgetLedgerFull reports that no entry could be allocated.
var errBudgetLedgerFull = errors.New("budget ledger is full")

// admit checks the scope's budgets and reserves estimate when the request
// fits. hint carries limits the caller already knows (from the auth
// middleware); nil means "read them from the store". A nil reservation with
// an allowing verdict means the scope has no budget.
func (l *budgetLedger) admit(ctx context.Context, scope budgetScope, hint *budgetLimits, estimate budgetAmount) (*budgetReservation, budgetVerdict, error) {
	if hint != nil && !hint.any() {
		return nil, budgetVerdict{}, nil
	}
	// ranLoad is set once this goroutine ran a load itself; waits counts
	// the shared loads it waited for. A shared load may have been started
	// for an older state (a background refresh that began before midnight
	// on the 1st loads the previous month), so a waiter whose snapshot is
	// still unusable starts or joins one more load. Admission ends after
	// its own load or after two shared loads, so the loop cannot spin.
	ranLoad := false
	waits := 0
	l.mu.Lock()
	for {
		now := l.now()
		month := budgetMonthStart(now)
		e := l.entryLocked(scope, now)
		if e == nil {
			l.mu.Unlock()
			return nil, budgetVerdict{Unavailable: true}, errBudgetLedgerFull
		}
		limits := e.snap.Limits
		if hint != nil {
			limits = *hint
		}
		needMonthly := limits.monthly()
		usable := e.hasSnap && now.Sub(e.snapAt) <= l.maxStale() &&
			(!needMonthly || e.snap.Month.Equal(month))
		if hint == nil && e.hasSnap && !e.snap.Limits.any() && now.Sub(e.snapAt) <= l.maxStale() {
			// A scope known to have no budget is never blocked.
			usable = true
		}

		if !usable {
			if ranLoad || waits >= 2 {
				l.mu.Unlock()
				return nil, budgetVerdict{Unavailable: true}, fmt.Errorf("budget state for %s %q is unavailable", scope.Kind, scope.ID)
			}
			if ch := e.loading; ch != nil {
				l.mu.Unlock()
				select {
				case <-ch:
				case <-ctx.Done():
					return nil, budgetVerdict{Unavailable: true}, ctx.Err()
				}
				waits++
				l.mu.Lock()
				continue
			}
			if !e.loadErrAt.IsZero() && now.Sub(e.loadErrAt) < l.retryBackoff {
				l.mu.Unlock()
				return nil, budgetVerdict{Unavailable: true}, fmt.Errorf("budget state for %s %q is unavailable (recent load failed)", scope.Kind, scope.ID)
			}
			ch, startGen := l.startLoadLocked(e)
			l.mu.Unlock()
			err := l.runLoad(e, ch, startGen, month, needMonthly)
			if err != nil {
				return nil, budgetVerdict{Unavailable: true}, err
			}
			ranLoad = true
			l.mu.Lock()
			continue
		}

		if e.loading == nil && now.Sub(e.snapAt) >= l.refreshInterval() &&
			(e.loadErrAt.IsZero() || now.Sub(e.loadErrAt) >= l.retryBackoff) {
			ch, startGen := l.startLoadLocked(e)
			go func() { _ = l.runLoad(e, ch, startGen, month, needMonthly) }()
		}
		if hint == nil {
			limits = e.snap.Limits
		}
		e.limits = limits
		if !limits.any() {
			l.mu.Unlock()
			return nil, budgetVerdict{}, nil
		}
		lifetime, monthly := e.committed(month)
		if reason, denied := budgetDenial(limits, lifetime, monthly, e.reserved, estimate); denied {
			l.mu.Unlock()
			return nil, budgetVerdict{Denied: true, Reason: reason}, nil
		}
		e.reserved.add(estimate)
		e.reservations++
		res := &budgetReservation{ledger: l, entry: e, amount: estimate}
		l.mu.Unlock()
		return res, budgetVerdict{}, nil
	}
}

// budgetDenial decides whether a request with estimate fits. A budget is
// exceeded when committed usage already reached it, or when committed usage
// plus the reservations of running requests plus this request's estimate
// would go over it.
func budgetDenial(limits budgetLimits, lifetime float64, monthly, reserved, estimate budgetAmount) (string, bool) {
	if limits.LifetimeCost > 0 {
		if lifetime >= limits.LifetimeCost {
			return fmt.Sprintf("cost budget exceeded: %.4f of %.4f USD used", lifetime, limits.LifetimeCost), true
		}
		if estimate.Cost > 0 && lifetime+reserved.Cost+estimate.Cost > limits.LifetimeCost {
			return fmt.Sprintf("cost budget exceeded: %.4f of %.4f USD used, %.4f USD reserved by running requests, this request may cost up to %.4f USD",
				lifetime, limits.LifetimeCost, reserved.Cost, estimate.Cost), true
		}
	}
	if limits.MonthlyCost > 0 {
		if monthly.Cost >= limits.MonthlyCost {
			return fmt.Sprintf("monthly cost budget exceeded: %.4f of %.4f USD used", monthly.Cost, limits.MonthlyCost), true
		}
		if estimate.Cost > 0 && monthly.Cost+reserved.Cost+estimate.Cost > limits.MonthlyCost {
			return fmt.Sprintf("monthly cost budget exceeded: %.4f of %.4f USD used, %.4f USD reserved by running requests, this request may cost up to %.4f USD",
				monthly.Cost, limits.MonthlyCost, reserved.Cost, estimate.Cost), true
		}
	}
	if limits.MonthlyTokens > 0 {
		if monthly.Tokens >= limits.MonthlyTokens {
			return fmt.Sprintf("monthly token budget exceeded: %d of %d used", monthly.Tokens, limits.MonthlyTokens), true
		}
		if estimate.Tokens > 0 && monthly.Tokens+reserved.Tokens+estimate.Tokens > limits.MonthlyTokens {
			return fmt.Sprintf("monthly token budget exceeded: %d of %d used, %d reserved by running requests, this request may use up to %d",
				monthly.Tokens, limits.MonthlyTokens, reserved.Tokens, estimate.Tokens), true
		}
	}
	return "", false
}

// beginCharge records the actual usage of a request before it is written to
// the store, and releases the request's reservation in the same step, so
// the usage is always counted by the next admission check. res may be nil
// (or belong to the same scope). It returns nil when the scope is not
// tracked.
func (l *budgetLedger) beginCharge(scope budgetScope, res *budgetReservation, charge budgetCharge) *budgetPending {
	l.mu.Lock()
	defer l.mu.Unlock()
	var e *budgetEntry
	if res != nil && !res.released && res.entry.scope == scope {
		// The reservation pins the entry, so it is still in the ledger.
		e = res.entry
		res.releaseLocked()
		e.lastUsed = l.now()
		l.lru.MoveToFront(e.elem)
	} else {
		if res != nil {
			res.releaseLocked()
		}
		// Without a reservation only an existing entry needs the charge:
		// a scope this process does not track yet reads its usage from the
		// store when it is first admitted.
		var ok bool
		if e, ok = l.entries[scope]; !ok {
			return nil
		}
	}
	p := &budgetPending{ledger: l, entry: e, charge: charge}
	e.pending[p] = struct{}{}
	return p
}

// finish moves the charge out of the pending set once the store writes have
// ended. monthlyStored and lifetimeStored report whether the request log
// and the lifetime spend were written; parts that were not are kept as lost
// charges. It returns the scope's limits and its usage after the charge.
func (p *budgetPending) finish(monthlyStored, lifetimeStored bool) (budgetLimits, float64, budgetAmount) {
	if p == nil {
		return budgetLimits{}, 0, budgetAmount{}
	}
	l, e := p.ledger, p.entry
	l.mu.Lock()
	defer l.mu.Unlock()
	if p.done {
		return e.limits, 0, budgetAmount{}
	}
	p.done = true
	delete(e.pending, p)
	stored := budgetCharge{Month: p.charge.Month}
	lost := budgetCharge{Month: p.charge.Month}
	if monthlyStored {
		stored.Monthly = p.charge.Monthly
	} else {
		lost.Monthly = p.charge.Monthly
	}
	if lifetimeStored {
		stored.Lifetime = p.charge.Lifetime
	} else {
		lost.Lifetime = p.charge.Lifetime
	}
	if stored.Lifetime != 0 || stored.Monthly != (budgetAmount{}) {
		key := budgetDoneKey{gen: e.gen, month: stored.Month}
		bucket := e.done[key]
		if bucket == nil {
			bucket = &budgetCharge{Month: stored.Month}
			e.done[key] = bucket
		}
		bucket.Lifetime += stored.Lifetime
		bucket.Monthly.add(stored.Monthly)
	}
	month := budgetMonthStart(l.now())
	if lost.Lifetime != 0 || lost.Monthly != (budgetAmount{}) {
		l.addLostLocked(e, lost, month)
	}
	lifetime, monthly := e.committed(month)
	return e.limits, lifetime, monthly
}

// addLostLocked keeps a charge whose store write failed. Lost charges of
// past months only keep their lifetime part, merged into one record, so the
// map holds at most a few months.
func (l *budgetLedger) addLostLocked(e *budgetEntry, c budgetCharge, month time.Time) {
	for key, old := range e.lost {
		if !key.IsZero() && key.Before(month) {
			delete(e.lost, key)
			past := e.lost[time.Time{}]
			if past == nil {
				past = &budgetCharge{}
				e.lost[time.Time{}] = past
			}
			past.Lifetime += old.Lifetime
		}
	}
	key := c.Month
	if !key.IsZero() && key.Before(month) {
		key = time.Time{}
		c.Monthly = budgetAmount{}
	}
	bucket := e.lost[key]
	if bucket == nil {
		bucket = &budgetCharge{Month: key}
		e.lost[key] = bucket
	}
	bucket.Lifetime += c.Lifetime
	bucket.Monthly.add(c.Monthly)
}
