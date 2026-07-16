package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

func TestStoreLogsAndMetrics(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	logs := []RequestLog{
		{
			ID:           "req_1",
			Timestamp:    now.Add(-time.Minute),
			Provider:     "openai",
			Model:        "gpt-4o-mini",
			Status:       "success",
			Decision:     "ALLOW",
			TotalTokens:  10,
			LatencyMs:    42,
			Cost:         0.01,
			RequestBody:  `{"model":"gpt-4o-mini"}`,
			ResponseBody: `{"id":"chat_1"}`,
			Cached:       true,
		},
		{
			ID:             "req_2",
			Timestamp:      now,
			Provider:       "openai",
			Model:          "gpt-4o-mini",
			Status:         "denied",
			Decision:       "DENY",
			DecisionReason: "blocked",
			LatencyMs:      3,
		},
	}
	for _, entry := range logs {
		if err := st.InsertLog(ctx, entry); err != nil {
			t.Fatalf("InsertLog() error = %v", err)
		}
	}

	got, total, err := st.ListLogs(ctx, 10, 0, "openai", "")
	if err != nil {
		t.Fatalf("ListLogs() error = %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("ListLogs() total,len = %d,%d, want 2,2", total, len(got))
	}
	if got[0].ID != "req_2" {
		t.Fatalf("logs are not newest-first: first ID = %q", got[0].ID)
	}
	if got[1].RequestBody == "" || got[1].ResponseBody == "" {
		t.Fatalf("raw bodies were not returned in log listing: %+v", got[1])
	}

	metrics, err := st.GetMetrics(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetMetrics() error = %v", err)
	}
	if metrics.TotalRequests != 2 || metrics.AllowedCount != 1 || metrics.DeniedCount != 1 || metrics.CacheHits != 1 {
		t.Fatalf("metrics = %+v, want totals 2/1/1 and cache hit", metrics)
	}

	providerMetrics, err := st.GetProviderMetrics(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetProviderMetrics() error = %v", err)
	}
	if len(providerMetrics) != 1 || providerMetrics[0].Provider != "openai" || providerMetrics[0].RequestCount != 2 {
		t.Fatalf("provider metrics = %+v, want one openai row with two requests", providerMetrics)
	}
}

func TestStoreAPIKeysAndPrompts(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	rawKey := "sk-sentinel-test"
	hash := sha256.Sum256([]byte(rawKey))
	expiresAt := time.Now().UTC().Add(time.Hour)

	key := APIKey{
		ID:                 "key_1",
		Name:               "local",
		KeyHash:            hex.EncodeToString(hash[:]),
		KeyPrefix:          "sk-sentinel-...",
		WorkspaceID:        "ws_1",
		Role:               "admin",
		CreatedAt:          time.Now().UTC(),
		ExpiresAt:          &expiresAt,
		RateLimit:          7,
		MonthlyCostBudget:  1.5,
		MonthlyTokenBudget: 100,
		Enabled:            true,
		Metadata:           map[string]string{"env": "test"},
	}
	if err := st.InsertAPIKey(ctx, key); err != nil {
		t.Fatalf("InsertAPIKey() error = %v", err)
	}

	loaded, err := st.GetAPIKeyByHash(ctx, key.KeyHash)
	if err != nil {
		t.Fatalf("GetAPIKeyByHash() error = %v", err)
	}
	if loaded.ID != key.ID || loaded.RateLimit != 7 || loaded.MonthlyCostBudget != 1.5 || loaded.MonthlyTokenBudget != 100 || loaded.WorkspaceID != "ws_1" || loaded.Role != "admin" || !loaded.Enabled {
		t.Fatalf("loaded key = %+v, want original enabled key", loaded)
	}

	keys, err := st.ListAPIKeys(ctx)
	if err != nil {
		t.Fatalf("ListAPIKeys() error = %v", err)
	}
	if len(keys) != 1 || keys[0].KeyHash != "" || keys[0].WorkspaceID != "ws_1" {
		t.Fatalf("ListAPIKeys() = %+v, want one key without hash", keys)
	}

	if err := st.DeleteAPIKey(ctx, key.ID); err != nil {
		t.Fatalf("DeleteAPIKey() error = %v", err)
	}
	disabled, err := st.GetAPIKeyByHash(ctx, key.KeyHash)
	if err != nil {
		t.Fatalf("GetAPIKeyByHash(disabled) error = %v", err)
	}
	if disabled.Enabled {
		t.Fatalf("disabled key Enabled = true, want false")
	}

	prompt := Prompt{
		ID:        "prompt_1",
		Name:      "support",
		Version:   1,
		Template:  "Hello {{name}}",
		Variables: []string{"name"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := st.InsertPrompt(ctx, prompt); err != nil {
		t.Fatalf("InsertPrompt() error = %v", err)
	}
	nextVersion, err := st.NextPromptVersion(ctx, "support")
	if err != nil {
		t.Fatalf("NextPromptVersion() error = %v", err)
	}
	if nextVersion != 2 {
		t.Fatalf("NextPromptVersion() = %d, want 2", nextVersion)
	}
	prompts, err := st.ListPrompts(ctx)
	if err != nil {
		t.Fatalf("ListPrompts() error = %v", err)
	}
	if len(prompts) != 1 || prompts[0].Variables[0] != "name" {
		t.Fatalf("prompts = %+v, want stored variables", prompts)
	}
	loadedPrompt, err := st.GetPrompt(ctx, prompt.ID)
	if err != nil {
		t.Fatalf("GetPrompt() error = %v", err)
	}
	if loadedPrompt.Template != prompt.Template {
		t.Fatalf("Template = %q, want %q", loadedPrompt.Template, prompt.Template)
	}
	versions, err := st.ListPromptVersions(ctx, "support")
	if err != nil {
		t.Fatalf("ListPromptVersions() error = %v", err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("versions = %+v, want one version 1 prompt", versions)
	}
}

func TestStoreSemanticCacheBudgetAndShadowLogs(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.InsertSemanticCache(ctx, CacheEntry{
		ID:        "cache_1",
		Key:       "exact-key",
		CreatedAt: time.Now().UTC(),
		Model:     "test-model",
		Prompt:    "hello world",
		Vector:    map[string]float64{"hello": 1},
		Response: provider.ChatResponse{
			ID:      "chat_cached",
			Model:   "test-model",
			Choices: []provider.Choice{{Message: provider.Message{Role: "assistant", Content: "cached"}}},
		},
	}); err != nil {
		t.Fatalf("InsertSemanticCache() error = %v", err)
	}
	entry, ok, err := st.FindSemanticCache(ctx, "test-model", map[string]float64{"hello": 1}, 0.99)
	if err != nil {
		t.Fatalf("FindSemanticCache() error = %v", err)
	}
	if !ok || entry.Response.ID != "chat_cached" {
		t.Fatalf("cache entry = %+v, ok = %v, want cached response", entry, ok)
	}
	exact, ok, err := st.GetExactCache(ctx, "exact-key")
	if err != nil {
		t.Fatalf("GetExactCache() error = %v", err)
	}
	if !ok || exact.Response.ID != "chat_cached" {
		t.Fatalf("exact cache = %+v, ok = %v, want cached response", exact, ok)
	}
	expired := time.Now().UTC().Add(-time.Minute)
	if err := st.InsertSemanticCache(ctx, CacheEntry{
		ID:        "cache_expired",
		Key:       "expired-key",
		CreatedAt: time.Now().UTC().Add(-time.Hour),
		ExpiresAt: &expired,
		Model:     "test-model",
		Prompt:    "expired",
		Vector:    map[string]float64{"expired": 1},
		Response:  provider.ChatResponse{ID: "expired"},
	}); err != nil {
		t.Fatalf("InsertSemanticCache(expired) error = %v", err)
	}
	if _, ok, err := st.GetExactCache(ctx, "expired-key"); err != nil || ok {
		t.Fatalf("expired exact cache ok,err = %v,%v, want false,nil", ok, err)
	}

	if err := st.InsertLog(ctx, RequestLog{
		ID:          "req_budget",
		Timestamp:   time.Now().UTC(),
		APIKeyID:    "key_budget",
		Status:      "success",
		Decision:    "ALLOW",
		TotalTokens: 42,
		Cost:        0.25,
	}); err != nil {
		t.Fatalf("InsertLog() error = %v", err)
	}
	usage, err := st.GetBudgetUsage(ctx, "key_budget", time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetBudgetUsage() error = %v", err)
	}
	if usage.Tokens != 42 || usage.Cost != 0.25 {
		t.Fatalf("usage = %+v, want 42 tokens and 0.25 cost", usage)
	}

	if err := st.InsertShadowLog(ctx, ShadowLog{
		ID:              "shadow_1",
		RequestID:       "req_1",
		TraceID:         "trace_1",
		Timestamp:       time.Now().UTC(),
		PrimaryProvider: "primary",
		ShadowProvider:  "shadow",
		Model:           "test-model",
		Status:          "success",
	}); err != nil {
		t.Fatalf("InsertShadowLog() error = %v", err)
	}
	shadowLogs, err := st.ListShadowLogs(ctx, "req_1")
	if err != nil {
		t.Fatalf("ListShadowLogs() error = %v", err)
	}
	if len(shadowLogs) != 1 || shadowLogs[0].ShadowProvider != "shadow" {
		t.Fatalf("shadow logs = %+v, want one shadow row", shadowLogs)
	}
}

func TestStoreOrganizationsWorkspacesAndMembers(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.InsertOrganization(ctx, Organization{
		ID:        "org_1",
		Name:      "Acme",
		CreatedAt: time.Now().UTC(),
		Metadata:  map[string]string{"tier": "enterprise"},
	}); err != nil {
		t.Fatalf("InsertOrganization() error = %v", err)
	}
	organizations, err := st.ListOrganizations(ctx)
	if err != nil {
		t.Fatalf("ListOrganizations() error = %v", err)
	}
	if len(organizations) != 1 || organizations[0].Metadata["tier"] != "enterprise" {
		t.Fatalf("organizations = %+v, want stored organization", organizations)
	}

	if err := st.InsertWorkspace(ctx, Workspace{
		ID:             "ws_1",
		OrganizationID: "org_1",
		Name:           "Production",
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertWorkspace() error = %v", err)
	}
	workspaces, err := st.ListWorkspaces(ctx, "org_1")
	if err != nil {
		t.Fatalf("ListWorkspaces() error = %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].Name != "Production" {
		t.Fatalf("workspaces = %+v, want production workspace", workspaces)
	}

	if err := st.InsertUser(ctx, User{
		ID:        "user_1",
		Email:     "ada@example.com",
		Name:      "Ada",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertUser() error = %v", err)
	}
	users, err := st.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if len(users) != 1 || users[0].Email != "ada@example.com" {
		t.Fatalf("users = %+v, want Ada", users)
	}

	if err := st.UpsertWorkspaceMember(ctx, WorkspaceMember{
		WorkspaceID: "ws_1",
		UserID:      "user_1",
		Role:        "admin",
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertWorkspaceMember() error = %v", err)
	}
	members, err := st.ListWorkspaceMembers(ctx, "ws_1")
	if err != nil {
		t.Fatalf("ListWorkspaceMembers() error = %v", err)
	}
	if len(members) != 1 || members[0].Role != "admin" {
		t.Fatalf("members = %+v, want admin membership", members)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	return st
}
