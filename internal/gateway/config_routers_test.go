package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// fakeConfigStore serves gateway configs from memory and counts reads.
type fakeConfigStore struct {
	mu      sync.Mutex
	records map[string]store.GatewayConfigRecord // by id
	reads   atomic.Int64
	gate    chan struct{} // when set, reads block until it is closed
	fail    error
}

func (f *fakeConfigStore) put(id, name, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.records == nil {
		f.records = map[string]store.GatewayConfigRecord{}
	}
	f.records[id] = store.GatewayConfigRecord{ID: id, Name: name, Format: "yaml", Body: body, Enabled: true, UpdatedAt: time.Now()}
}

func (f *fakeConfigStore) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, id)
}

func (f *fakeConfigStore) load(_ context.Context, ref string) (store.GatewayConfigRecord, error) {
	f.reads.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return store.GatewayConfigRecord{}, f.fail
	}
	for _, record := range f.records {
		if record.ID == ref || record.Name == ref {
			return record, nil
		}
	}
	return store.GatewayConfigRecord{}, sql.ErrNoRows
}

// fakeClock is a manually advanced clock for TTL tests.
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
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func routesBody(model, providerName string) string {
	return fmt.Sprintf("apiVersion: omniswitch.dev/v1\nkind: GatewayConfig\nroutes:\n  %s:\n    provider: %s\n", model, providerName)
}

func newConfigRouterTestHandler(t *testing.T) (*Handler, *fakeConfigStore, *fakeClock) {
	t.Helper()
	reg := provider.NewRegistry()
	rtr := router.New(reg)
	rtr.SetRoute("base-model", router.Route{Provider: "alpha"})
	h := New(reg, rtr, nil, guardrail.NewEngine())
	fs := &fakeConfigStore{}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	h.configRouters.load = fs.load
	h.configRouters.now = clock.Now
	return h, fs, clock
}

func routeProvider(t *testing.T, rtr *router.Router, model string) string {
	t.Helper()
	route, ok := rtr.RouteFor(model)
	if !ok {
		return ""
	}
	return route.Provider
}

// GW-19: a cache hit, by name or by id, does not read the store.
func TestConfigRouterCacheHitSkipsStore(t *testing.T) {
	h, fs, _ := newConfigRouterTestHandler(t)
	fs.put("cfg_1", "team-a", routesBody("m", "beta"))
	first, err := h.routerForConfig(context.Background(), "team-a")
	if err != nil {
		t.Fatalf("routerForConfig() error = %v", err)
	}
	if got := routeProvider(t, first, "m"); got != "beta" {
		t.Fatalf("stored route provider = %q, want beta", got)
	}
	if got := routeProvider(t, first, "base-model"); got != "alpha" {
		t.Fatalf("base route provider = %q, want alpha", got)
	}
	for _, ref := range []string{"team-a", "cfg_1", "team-a"} {
		again, err := h.routerForConfig(context.Background(), ref)
		if err != nil || again != first {
			t.Fatalf("routerForConfig(%q) = %p, %v; want the cached router %p", ref, again, err, first)
		}
	}
	if reads := fs.reads.Load(); reads != 1 {
		t.Fatalf("store reads = %d, want 1", reads)
	}
}

// GW-19: after the TTL an update is picked up and a deleted config is
// evicted under both its name and its id.
func TestConfigRouterTTLPicksUpUpdatesAndDeletions(t *testing.T) {
	h, fs, clock := newConfigRouterTestHandler(t)
	fs.put("cfg_1", "team-a", routesBody("m", "beta"))
	if _, err := h.routerForConfig(context.Background(), "team-a"); err != nil {
		t.Fatalf("routerForConfig() error = %v", err)
	}
	fs.put("cfg_1", "team-a", routesBody("m", "gamma"))
	clock.Advance(configRouterTTL - time.Millisecond)
	cached, _ := h.routerForConfig(context.Background(), "team-a")
	if got := routeProvider(t, cached, "m"); got != "beta" {
		t.Fatalf("provider within the TTL = %q, want the cached beta", got)
	}
	clock.Advance(2 * time.Millisecond)
	updated, err := h.routerForConfig(context.Background(), "team-a")
	if err != nil || routeProvider(t, updated, "m") != "gamma" {
		t.Fatalf("after the TTL router = %v, err = %v; want the gamma route", updated, err)
	}

	fs.remove("cfg_1")
	clock.Advance(configRouterTTL)
	for _, ref := range []string{"team-a", "cfg_1"} {
		if _, err := h.routerForConfig(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("routerForConfig(%q) after deletion error = %v, want not found", ref, err)
		}
	}
}

// GW-19: invalidation (the /api/configs hook) takes effect at once and
// drops both keys of a renamed config.
func TestConfigRouterInvalidateDropsAllKeys(t *testing.T) {
	h, fs, _ := newConfigRouterTestHandler(t)
	fs.put("cfg_1", "old-name", routesBody("m", "beta"))
	if _, err := h.routerForConfig(context.Background(), "old-name"); err != nil {
		t.Fatalf("routerForConfig() error = %v", err)
	}
	fs.put("cfg_1", "new-name", routesBody("m", "gamma"))
	h.InvalidateConfigRouters("cfg_1", "new-name")
	if _, err := h.routerForConfig(context.Background(), "old-name"); err == nil {
		t.Fatalf("old name still resolves after rename and invalidation")
	}
	renamed, err := h.routerForConfig(context.Background(), "new-name")
	if err != nil || routeProvider(t, renamed, "m") != "gamma" {
		t.Fatalf("new name router = %v, err = %v; want gamma", renamed, err)
	}
}

// GW-19: unknown names are remembered briefly, and the negative cache
// expires so a config created later is found.
func TestConfigRouterNegativeCache(t *testing.T) {
	h, fs, clock := newConfigRouterTestHandler(t)
	for i := 0; i < 5; i++ {
		_, err := h.routerForConfig(context.Background(), "missing")
		if err == nil || err.Error() != `dynamic config "missing" not found` {
			t.Fatalf("routerForConfig(missing) error = %v, want not found", err)
		}
	}
	if reads := fs.reads.Load(); reads != 1 {
		t.Fatalf("store reads for a repeated unknown name = %d, want 1", reads)
	}
	fs.put("cfg_2", "missing", routesBody("m", "beta"))
	clock.Advance(configRouterNegativeTTL)
	if _, err := h.routerForConfig(context.Background(), "missing"); err != nil {
		t.Fatalf("config created after the negative TTL: error = %v", err)
	}
	if reads := fs.reads.Load(); reads != 2 {
		t.Fatalf("store reads = %d, want 2", reads)
	}
}

// GW-19: database errors are reported like before but not cached.
func TestConfigRouterDoesNotCacheStoreErrors(t *testing.T) {
	h, fs, _ := newConfigRouterTestHandler(t)
	fs.fail = errors.New("connection refused")
	for i := 0; i < 2; i++ {
		if _, err := h.routerForConfig(context.Background(), "team-a"); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("error = %v, want not found", err)
		}
	}
	if reads := fs.reads.Load(); reads != 2 {
		t.Fatalf("store reads = %d, want 2 (errors are not cached)", reads)
	}
}

// GW-19: an invalid stored config keeps its error message and is cached.
func TestConfigRouterInvalidConfigError(t *testing.T) {
	h, fs, _ := newConfigRouterTestHandler(t)
	fs.put("cfg_bad", "bad", "routes: [not, a, map")
	for i := 0; i < 3; i++ {
		_, err := h.routerForConfig(context.Background(), "bad")
		if err == nil || !strings.HasPrefix(err.Error(), `dynamic config "bad" is invalid: `) {
			t.Fatalf("error = %v, want invalid config", err)
		}
	}
	if reads := fs.reads.Load(); reads != 1 {
		t.Fatalf("store reads = %d, want 1", reads)
	}
}

// GW-19: the caches stay bounded.
func TestConfigRouterCacheIsBounded(t *testing.T) {
	h, fs, _ := newConfigRouterTestHandler(t)
	for i := 0; i < maxCachedConfigKeys; i++ {
		fs.put(fmt.Sprintf("cfg_%d", i), fmt.Sprintf("name-%d", i), routesBody("m", "beta"))
	}
	for i := 0; i < maxCachedConfigKeys; i++ {
		if _, err := h.routerForConfig(context.Background(), fmt.Sprintf("name-%d", i)); err != nil {
			t.Fatalf("routerForConfig() error = %v", err)
		}
	}
	for i := 0; i < 2*maxNegativeConfigRefs; i++ {
		_, _ = h.routerForConfig(context.Background(), fmt.Sprintf("unknown-%d", i))
	}
	_, _ = h.routerForConfig(context.Background(), strings.Repeat("x", maxNegativeConfigRefBytes+1))
	h.configRouters.mu.Lock()
	positive, negative := h.configRouters.positive.len(), h.configRouters.negative.len()
	h.configRouters.mu.Unlock()
	if positive > maxCachedConfigKeys || negative > maxNegativeConfigRefs {
		t.Fatalf("cache sizes = %d positive, %d negative; caps %d and %d", positive, negative, maxCachedConfigKeys, maxNegativeConfigRefs)
	}
	if positive != maxCachedConfigKeys || negative != maxNegativeConfigRefs {
		t.Fatalf("cache sizes = %d positive, %d negative; want them full", positive, negative)
	}
}

// GW-19: concurrent misses for the same config share one store read.
func TestConfigRouterConcurrentMissesShareOneRead(t *testing.T) {
	h, fs, _ := newConfigRouterTestHandler(t)
	fs.put("cfg_1", "team-a", routesBody("m", "beta"))
	fs.gate = make(chan struct{})
	const callers = 16
	var wg sync.WaitGroup
	routers := make([]*router.Router, callers)
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			routers[i], errs[i] = h.routerForConfig(context.Background(), "team-a")
		}(i)
	}
	// Wait until the leader is inside the store read and the others queued.
	deadline := time.Now().Add(5 * time.Second)
	for fs.reads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(fs.gate)
	wg.Wait()
	for i := 0; i < callers; i++ {
		if errs[i] != nil || routers[i] != routers[0] {
			t.Fatalf("caller %d: router %p err %v; want shared router %p", i, routers[i], errs[i], routers[0])
		}
	}
	if reads := fs.reads.Load(); reads != 1 {
		t.Fatalf("store reads = %d, want 1", reads)
	}
}

// GW-19: a hot reload of the base routes reaches a cached config router
// without a store read, and the stored routes still win.
func TestConfigRouterFollowsBaseReload(t *testing.T) {
	h, fs, _ := newConfigRouterTestHandler(t)
	fs.put("cfg_1", "team-a", routesBody("m", "beta"))
	before, err := h.routerForConfig(context.Background(), "team-a")
	if err != nil {
		t.Fatalf("routerForConfig() error = %v", err)
	}
	h.router.ReplaceRoutes(map[string]router.Route{
		"base-model": {Provider: "delta"},
		"new-model":  {Provider: "delta"},
		"m":          {Provider: "alpha"},
	})
	after, err := h.routerForConfig(context.Background(), "team-a")
	if err != nil {
		t.Fatalf("routerForConfig() error = %v", err)
	}
	if after == before {
		t.Fatalf("config router was not rebuilt after the base routes changed")
	}
	if got := routeProvider(t, after, "new-model"); got != "delta" {
		t.Fatalf("new base route provider = %q, want delta", got)
	}
	if got := routeProvider(t, after, "base-model"); got != "delta" {
		t.Fatalf("reloaded base route provider = %q, want delta", got)
	}
	if got := routeProvider(t, after, "m"); got != "beta" {
		t.Fatalf("stored route provider = %q, want beta (stored routes override base routes)", got)
	}
	if reads := fs.reads.Load(); reads != 1 {
		t.Fatalf("store reads = %d, want 1", reads)
	}
	breaker := router.NewCircuitBreaker(2, time.Minute)
	h.router.SetCircuitBreaker(breaker)
	again, _ := h.routerForConfig(context.Background(), "team-a")
	if again.CircuitBreaker() != breaker || after.CircuitBreaker() != breaker {
		t.Fatalf("config routers do not use the current base breaker")
	}
}

// GW-19: end to end through ChatCompletions with the x-omniswitch-config
// header and a real store: the stored route is used, a base reload is seen
// for other models, and deleting the config through the invalidation hook
// returns the usual 400.
func TestChatCompletionsDynamicConfigEndToEnd(t *testing.T) {
	alpha := scriptedProvider{name: "alpha", model: "m", content: "from alpha"}
	beta := scriptedProvider{name: "beta", model: "m", content: "from beta"}
	h, st := newHardeningHandler(t, nil, alpha, beta)
	h.router.ReplaceRoutes(map[string]router.Route{"m": {Provider: "alpha"}, "n": {Provider: "alpha"}})
	ctx := context.Background()
	if err := st.UpsertGatewayConfig(ctx, store.GatewayConfigRecord{
		ID: "cfg_e2e", Name: "team-b", Format: "yaml", Body: routesBody("m", "beta"), Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertGatewayConfig() error = %v", err)
	}
	headers := map[string]string{"x-omniswitch-config": "team-b"}
	calls := 0
	chat := func(model string) (int, string) {
		// A distinct prompt per call keeps the response cache out of it.
		calls++
		rec := doRequest(h.ChatCompletions, "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"question %d"}]}`, model, calls), headers)
		return rec.Code, rec.Body.String()
	}
	if code, body := chat("m"); code != http.StatusOK || !strings.Contains(body, "from beta") {
		t.Fatalf("stored route: status %d body %s, want beta", code, body)
	}
	if code, body := chat("n"); code != http.StatusOK || !strings.Contains(body, "from alpha") {
		t.Fatalf("base route: status %d body %s, want alpha", code, body)
	}
	h.router.ReplaceRoutes(map[string]router.Route{"m": {Provider: "alpha"}, "n": {Provider: "beta"}})
	if code, body := chat("n"); code != http.StatusOK || !strings.Contains(body, "from beta") {
		t.Fatalf("reloaded base route: status %d body %s, want beta", code, body)
	}
	if err := st.DeleteGatewayConfig(ctx, "team-b"); err != nil {
		t.Fatalf("DeleteGatewayConfig() error = %v", err)
	}
	h.InvalidateConfigRouters("team-b")
	if code, body := chat("m"); code != http.StatusBadRequest || !strings.Contains(body, `dynamic config \"team-b\" not found`) {
		t.Fatalf("deleted config: status %d body %s, want 400 not found", code, body)
	}
}
