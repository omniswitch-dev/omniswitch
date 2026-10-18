package gateway

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/gatewayconfig"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// Stored gateway configs selected with the x-omniswitch-config header are
// resolved through a small in-memory cache, so the request path does not read
// the database in steady state.
const (
	// configRouterTTL is how long a resolved config is used before it is read
	// again. Updates, renames and deletions made on another replica are seen
	// within this time; changes made through /api/configs on the same
	// replica take effect at once.
	configRouterTTL = 5 * time.Second
	// configRouterNegativeTTL is how long an unknown config name is
	// remembered, so repeated requests for it do not each read the database.
	configRouterNegativeTTL = 2 * time.Second
	// maxCachedConfigKeys bounds the resolved configs kept in memory. Each
	// config is stored under its name, its id and the reference used; the
	// LRU evicts all keys of a config together.
	maxCachedConfigKeys = 512
	// maxNegativeConfigRefs bounds the remembered unknown config names.
	maxNegativeConfigRefs = 1024
	// maxNegativeConfigRefBytes is the longest unknown config name that is
	// remembered. Longer names are still resolved but not cached.
	maxNegativeConfigRefBytes = 256
)

// configRouterEntry is one resolved stored config. The parsed routes are
// kept, so the router can be rebuilt without a database read when the base
// router changes (hot reload of the file routes).
type configRouterEntry struct {
	keys     []string
	routes   map[string]router.Route
	err      error // cached "invalid config" error
	expires  time.Time
	notFound bool

	mu      sync.Mutex
	router  *router.Router
	baseGen uint64
}

// routerFor returns the config router built on base, rebuilding it when the
// routes or the breaker of base changed since it was built.
func (e *configRouterEntry) routerFor(base *router.Router) *router.Router {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Read the generation before cloning: if base changes during the clone,
	// the next request sees a newer generation and rebuilds again.
	gen := base.Generation()
	if e.router == nil || e.baseGen != gen {
		e.router = base.CloneWithRoutes(e.routes)
		e.baseGen = gen
	}
	return e.router
}

// configLookup is one in-flight database read shared by concurrent misses
// for the same config reference.
type configLookup struct {
	done  chan struct{}
	entry *configRouterEntry
	err   error
}

// configRouterCache caches resolved stored configs. The zero value is ready
// to use.
type configRouterCache struct {
	mu       sync.Mutex
	positive configLRU
	negative configLRU
	inflight map[string]*configLookup
	// epoch changes on every invalidation, so a lookup that started before
	// it does not store a result that may already be stale.
	epoch uint64

	// now and load are test hooks; nil means time.Now and the store.
	now  func() time.Time
	load func(ctx context.Context, ref string) (store.GatewayConfigRecord, error)
}

func (c *configRouterCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// InvalidateConfigRouters drops cached stored configs for the given names or
// ids, so the next request reads them again. The dashboard calls it after it
// creates, updates or deletes a config.
func (h *Handler) InvalidateConfigRouters(refs ...string) {
	h.configRouters.invalidate(refs...)
}

func (c *configRouterCache) invalidate(refs ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	for _, ref := range refs {
		if entry, ok := c.positive.peek(ref); ok {
			c.positive.removeEntry(entry)
		}
		c.positive.remove(ref)
		c.negative.remove(ref)
		delete(c.inflight, ref)
	}
}

func (h *Handler) routerForConfig(ctx context.Context, ref string) (*router.Router, error) {
	if h.store == nil && h.configRouters.load == nil {
		return nil, fmt.Errorf("dynamic config %q requested but no store is configured", ref)
	}
	entry, err := h.configRouters.lookup(ctx, ref, h.store)
	if err != nil {
		return nil, err
	}
	if entry.notFound {
		return nil, fmt.Errorf("dynamic config %q not found", ref)
	}
	if entry.err != nil {
		return nil, entry.err
	}
	return entry.routerFor(h.router), nil
}

// lookup returns the cached entry for ref, or reads it from the store. Only
// one read runs at a time for the same ref; other callers wait for it.
func (c *configRouterCache) lookup(ctx context.Context, ref string, st *store.Store) (*configRouterEntry, error) {
	c.mu.Lock()
	now := c.clock()
	if entry, ok := c.positive.get(ref); ok {
		if now.Before(entry.expires) {
			c.mu.Unlock()
			return entry, nil
		}
		// All keys of an entry expire together.
		c.positive.removeEntry(entry)
		c.positive.remove(ref)
	}
	if entry, ok := c.negative.get(ref); ok {
		if now.Before(entry.expires) {
			c.mu.Unlock()
			return entry, nil
		}
		c.negative.remove(ref)
	}
	if call, ok := c.inflight[ref]; ok {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.entry, call.err
		case <-ctx.Done():
			return nil, fmt.Errorf("dynamic config %q not found", ref)
		}
	}
	call := &configLookup{done: make(chan struct{})}
	if c.inflight == nil {
		c.inflight = make(map[string]*configLookup)
	}
	c.inflight[ref] = call
	epoch := c.epoch
	load := c.load
	c.mu.Unlock()

	if load == nil {
		load = st.GetGatewayConfig
	}
	// The read is shared with other requests, so one cancelled request
	// must not fail it for the others.
	loadCtx, cancel := persistContext(ctx)
	record, err := load(loadCtx, ref)
	cancel()

	c.mu.Lock()
	now = c.clock()
	switch {
	case err == nil:
		call.entry = newConfigRouterEntry(ref, record, now.Add(configRouterTTL))
	case errors.Is(err, sql.ErrNoRows):
		call.entry = &configRouterEntry{keys: []string{ref}, notFound: true, expires: now.Add(configRouterNegativeTTL)}
	default:
		// A database error is reported like a missing config, as before,
		// but not cached.
		call.err = fmt.Errorf("dynamic config %q not found", ref)
	}
	if c.inflight[ref] == call {
		delete(c.inflight, ref)
	}
	if call.entry != nil && c.epoch == epoch {
		if call.entry.notFound {
			if len(ref) <= maxNegativeConfigRefBytes {
				c.negative.put(ref, call.entry, maxNegativeConfigRefs)
			}
		} else {
			for _, key := range call.entry.keys {
				// A key held by another entry (an old name or id of a
				// renamed config) drops that entry completely.
				if old, ok := c.positive.peek(key); ok && old != call.entry {
					c.positive.removeEntry(old)
				}
				c.negative.remove(key)
			}
			for _, key := range call.entry.keys {
				c.positive.put(key, call.entry, maxCachedConfigKeys)
			}
		}
	}
	c.mu.Unlock()
	close(call.done)
	return call.entry, call.err
}

func newConfigRouterEntry(ref string, record store.GatewayConfigRecord, expires time.Time) *configRouterEntry {
	entry := &configRouterEntry{expires: expires}
	for _, key := range []string{record.Name, record.ID, ref} {
		if key != "" && !containsString(entry.keys, key) {
			entry.keys = append(entry.keys, key)
		}
	}
	cfg, err := gatewayconfig.Parse([]byte(record.Body), "."+record.Format)
	if err != nil {
		entry.err = fmt.Errorf("dynamic config %q is invalid: %w", record.Name, err)
		return entry
	}
	entry.routes = cfg.Routes
	return entry
}

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// configLRU is a least-recently-used map from config reference to entry.
// The zero value is ready to use.
type configLRU struct {
	order *list.List
	items map[string]*list.Element
}

type configLRUItem struct {
	key   string
	entry *configRouterEntry
}

func (l *configLRU) len() int { return len(l.items) }

func (l *configLRU) peek(key string) (*configRouterEntry, bool) {
	element, ok := l.items[key]
	if !ok {
		return nil, false
	}
	return element.Value.(*configLRUItem).entry, true
}

func (l *configLRU) get(key string) (*configRouterEntry, bool) {
	element, ok := l.items[key]
	if !ok {
		return nil, false
	}
	l.order.MoveToFront(element)
	return element.Value.(*configLRUItem).entry, true
}

func (l *configLRU) put(key string, entry *configRouterEntry, capacity int) {
	if l.items == nil {
		l.items = make(map[string]*list.Element)
		l.order = list.New()
	}
	if element, ok := l.items[key]; ok {
		element.Value.(*configLRUItem).entry = entry
		l.order.MoveToFront(element)
		return
	}
	l.items[key] = l.order.PushFront(&configLRUItem{key: key, entry: entry})
	for len(l.items) > capacity {
		oldest := l.order.Back().Value.(*configLRUItem)
		if oldest.entry == entry {
			// Only possible when capacity is smaller than one entry's keys.
			l.remove(oldest.key)
			continue
		}
		// Evict the whole entry, not just its least recently used key: an
		// entry is either cached under all its keys or not at all, so
		// invalidate by any of its names or ids always finds it.
		l.remove(oldest.key)
		l.removeEntry(oldest.entry)
	}
}

func (l *configLRU) remove(key string) {
	element, ok := l.items[key]
	if !ok {
		return
	}
	l.order.Remove(element)
	delete(l.items, key)
}

// removeEntry removes every key that still points at entry.
func (l *configLRU) removeEntry(entry *configRouterEntry) {
	for _, key := range entry.keys {
		if current, ok := l.peek(key); ok && current == entry {
			l.remove(key)
		}
	}
}
