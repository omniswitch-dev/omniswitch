package gateway

import (
	"context"
	"fmt"
	"testing"
)

// fillConfigLRUKeepingName loads enough other configs to push the cache past
// its bound, looking up keepName between loads so only its name key stays
// recently used. Before C1 this evicted the id key of keepName alone.
func fillConfigLRUKeepingName(t *testing.T, h *Handler, fs *fakeConfigStore, keepName string) {
	t.Helper()
	for i := 0; i < maxCachedConfigKeys; i++ {
		fs.put(fmt.Sprintf("cfg_fill_%d", i), fmt.Sprintf("fill-%d", i), routesBody("m", "beta"))
		if _, err := h.routerForConfig(context.Background(), fmt.Sprintf("fill-%d", i)); err != nil {
			t.Fatalf("routerForConfig(fill-%d) error = %v", i, err)
		}
		if _, err := h.routerForConfig(context.Background(), keepName); err != nil {
			t.Fatalf("routerForConfig(%s) error = %v", keepName, err)
		}
	}
}

func configCacheKeys(h *Handler, keys ...string) (present []string, size int) {
	h.configRouters.mu.Lock()
	defer h.configRouters.mu.Unlock()
	for _, key := range keys {
		if _, ok := h.configRouters.positive.peek(key); ok {
			present = append(present, key)
		}
	}
	return present, h.configRouters.positive.len()
}

// C1: under LRU pressure an entry stays cached under all its keys or none,
// so invalidate by id (PATCH/DELETE ?id=) drops the name key too.
func TestConfigRouterInvalidateByIDAfterLRUPressure(t *testing.T) {
	h, fs, _ := newConfigRouterTestHandler(t)
	fs.put("cfg_keep", "keep", routesBody("m", "beta"))
	if _, err := h.routerForConfig(context.Background(), "keep"); err != nil {
		t.Fatalf("routerForConfig(keep) error = %v", err)
	}
	fillConfigLRUKeepingName(t, h, fs, "keep")

	present, size := configCacheKeys(h, "keep", "cfg_keep")
	if size > maxCachedConfigKeys {
		t.Fatalf("positive cache size = %d, cap %d", size, maxCachedConfigKeys)
	}
	if len(present) == 1 {
		t.Errorf("entry is split in the LRU: only %v cached", present)
	}

	// The config is deleted through /api/configs?id=cfg_keep.
	fs.remove("cfg_keep")
	h.InvalidateConfigRouters("cfg_keep")
	reads := fs.reads.Load()
	if _, err := h.routerForConfig(context.Background(), "keep"); err == nil {
		t.Fatalf("deleted config still resolves by name after invalidate by id")
	}
	if fs.reads.Load() != reads+1 {
		t.Fatalf("lookup by name after invalidate by id did not read the store")
	}
}

// C1: invalidate by name drops the id key and the ref key as well.
func TestConfigRouterInvalidateByNameDropsIDKey(t *testing.T) {
	h, fs, _ := newConfigRouterTestHandler(t)
	fs.put("cfg_x", "x-name", routesBody("m", "beta"))
	// Resolve by id first so the entry holds name and id.
	if _, err := h.routerForConfig(context.Background(), "cfg_x"); err != nil {
		t.Fatalf("routerForConfig(cfg_x) error = %v", err)
	}
	if present, _ := configCacheKeys(h, "x-name", "cfg_x"); len(present) != 2 {
		t.Fatalf("cached keys = %v, want name and id", present)
	}
	h.InvalidateConfigRouters("x-name")
	if present, _ := configCacheKeys(h, "x-name", "cfg_x"); len(present) != 0 {
		t.Fatalf("keys still cached after invalidate by name: %v", present)
	}
}

// C1: whole-entry eviction keeps the bound with entries of three keys (name,
// id and a different ref), and never leaves a partial entry behind.
func TestConfigLRUEvictsWholeEntries(t *testing.T) {
	var lru configLRU
	const capacity = 10
	var entries []*configRouterEntry
	for i := 0; i < 20; i++ {
		e := &configRouterEntry{keys: []string{fmt.Sprintf("n%d", i), fmt.Sprintf("id%d", i), fmt.Sprintf("r%d", i)}}
		entries = append(entries, e)
		for _, key := range e.keys {
			lru.put(key, e, capacity)
		}
		// Keep the name key of entry 0 hot.
		lru.get("n0")
		if lru.len() > capacity {
			t.Fatalf("len = %d after %d entries, cap %d", lru.len(), i+1, capacity)
		}
	}
	for i, e := range entries {
		n := 0
		for _, key := range e.keys {
			if got, ok := lru.peek(key); ok && got == e {
				n++
			}
		}
		if n != 0 && n != len(e.keys) {
			t.Fatalf("entry %d is cached under %d of %d keys", i, n, len(e.keys))
		}
	}
}
