package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// seedTenants stores two organizations: org_a with workspaces ws_a1 and
// ws_a2, org_b with ws_b. Users: ua (ws_a1), ua2 (ws_a2), ub (ws_b),
// shared (ws_a1 and ws_b) and loose (no membership).
func seedTenants(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"org_a", "org_b"} {
		if err := st.InsertOrganization(ctx, Organization{ID: id, Name: id, CreatedAt: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	for i, ws := range []Workspace{{ID: "ws_a1", OrganizationID: "org_a"}, {ID: "ws_a2", OrganizationID: "org_a"}, {ID: "ws_b", OrganizationID: "org_b"}} {
		ws.Name = ws.ID
		ws.CreatedAt = base.Add(time.Duration(i) * time.Minute)
		if err := st.InsertWorkspace(ctx, ws); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{"ua", "ua2", "ub", "shared", "loose"} {
		if err := st.InsertUser(ctx, User{ID: id, Email: id + "@example.com", Name: id, CreatedAt: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []WorkspaceMember{
		{WorkspaceID: "ws_a1", UserID: "ua"}, {WorkspaceID: "ws_a2", UserID: "ua2"}, {WorkspaceID: "ws_b", UserID: "ub"},
		{WorkspaceID: "ws_a1", UserID: "shared"}, {WorkspaceID: "ws_b", UserID: "shared"},
	} {
		m.Role = "viewer"
		if err := st.UpsertWorkspaceMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
}

func sortedIDs[T any](items []T, id func(T) string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, id(item))
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTenantFilteredLists(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		seedTenants(t, st)
		ctx := context.Background()
		cases := []struct {
			name      string
			filter    TenantFilter
			orgs      []string
			ws        []string
			members   []string
			users     []string
			wsFilter  string // workspace_id filter for members
			orgFilter string // organization_id filter for workspaces
		}{
			{name: "global", filter: TenantFilter{}, orgs: []string{"org_a", "org_b"}, ws: []string{"ws_a1", "ws_a2", "ws_b"},
				members: []string{"ws_a1/shared", "ws_a1/ua", "ws_a2/ua2", "ws_b/shared", "ws_b/ub"},
				users:   []string{"loose", "shared", "ua", "ua2", "ub"}},
			{name: "organization", filter: TenantFilter{OrganizationID: "org_a"}, orgs: []string{"org_a"}, ws: []string{"ws_a1", "ws_a2"},
				members: []string{"ws_a1/shared", "ws_a1/ua", "ws_a2/ua2"}, users: []string{"shared", "ua", "ua2"}},
			{name: "workspace", filter: TenantFilter{WorkspaceID: "ws_a1", OrganizationID: "org_a"}, orgs: []string{"org_a"}, ws: []string{"ws_a1"},
				members: []string{"ws_a1/shared", "ws_a1/ua"}, users: []string{"shared", "ua"}},
			// A workspace caller's organization comes from the workspaces
			// table, not from a forged claim.
			{name: "workspace with forged organization", filter: TenantFilter{WorkspaceID: "ws_b", OrganizationID: "org_a"}, orgs: []string{"org_b"}, ws: []string{"ws_b"},
				members: []string{"ws_b/shared", "ws_b/ub"}, users: []string{"shared", "ub"}},
			{name: "organization filters other tenant", filter: TenantFilter{OrganizationID: "org_a"}, orgFilter: "org_b", wsFilter: "ws_b",
				orgs: []string{"org_a"}, ws: []string{}, members: []string{}, users: []string{"shared", "ua", "ua2"}},
			{name: "workspace filters sibling", filter: TenantFilter{WorkspaceID: "ws_a1"}, wsFilter: "ws_a2",
				orgs: []string{"org_a"}, ws: []string{"ws_a1"}, members: []string{}, users: []string{"shared", "ua"}},
			{name: "unknown organization", filter: TenantFilter{OrganizationID: "org_x"}, orgs: []string{}, ws: []string{}, members: []string{}, users: []string{}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				orgs, err := st.ListOrganizationsForTenant(ctx, tc.filter)
				if err != nil {
					t.Fatal(err)
				}
				if got := sortedIDs(orgs, func(o Organization) string { return o.ID }); !equalStrings(got, tc.orgs) {
					t.Errorf("organizations = %v, want %v", got, tc.orgs)
				}
				ws, err := st.ListWorkspacesForTenant(ctx, tc.filter, tc.orgFilter)
				if err != nil {
					t.Fatal(err)
				}
				if got := sortedIDs(ws, func(w Workspace) string { return w.ID }); !equalStrings(got, tc.ws) {
					t.Errorf("workspaces = %v, want %v", got, tc.ws)
				}
				members, err := st.ListWorkspaceMembersForTenant(ctx, tc.filter, tc.wsFilter)
				if err != nil {
					t.Fatal(err)
				}
				if got := sortedIDs(members, func(m WorkspaceMember) string { return m.WorkspaceID + "/" + m.UserID }); !equalStrings(got, tc.members) {
					t.Errorf("members = %v, want %v", got, tc.members)
				}
				users, err := st.ListUsersForTenant(ctx, tc.filter)
				if err != nil {
					t.Fatal(err)
				}
				if got := sortedIDs(users, func(u User) string { return u.ID }); !equalStrings(got, tc.users) {
					t.Errorf("users = %v, want %v", got, tc.users)
				}
			})
		}
	})
}

func TestAddUserToWorkspaceNeverChangesExistingUser(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		seedTenants(t, st)
		ctx := context.Background()
		user, member, err := st.AddUserToWorkspace(ctx,
			User{ID: "new_id", Email: "shared@example.com", Name: "renamed", Metadata: map[string]string{"x": "y"}},
			WorkspaceMember{WorkspaceID: "ws_a2", Role: "member"})
		if err != nil {
			t.Fatal(err)
		}
		if user.ID != "shared" || user.Name != "shared" || member.UserID != "shared" {
			t.Fatalf("AddUserToWorkspace() = %+v, %+v; want the stored user unchanged", user, member)
		}
		stored, err := st.GetUserByID(ctx, "shared")
		if err != nil || stored.Name != "shared" || len(stored.Metadata) != 0 {
			t.Fatalf("stored user = %+v, %v; want unchanged", stored, err)
		}
		if got, err := st.GetWorkspaceMember(ctx, "ws_a2", "shared"); err != nil || got.Role != "member" {
			t.Fatalf("membership = %+v, %v", got, err)
		}

		fresh, _, err := st.AddUserToWorkspace(ctx, User{ID: "u_new", Email: "new@example.com", Name: "New"}, WorkspaceMember{WorkspaceID: "ws_b", Role: "viewer"})
		if err != nil || fresh.ID != "u_new" || fresh.Name != "New" {
			t.Fatalf("new user = %+v, %v", fresh, err)
		}
		users, err := st.ListUsersForTenant(ctx, TenantFilter{WorkspaceID: "ws_b"})
		if err != nil {
			t.Fatal(err)
		}
		if got := sortedIDs(users, func(u User) string { return u.ID }); !equalStrings(got, []string{"shared", "u_new", "ub"}) {
			t.Fatalf("ws_b users = %v", got)
		}
		if _, err := st.GetUserByID(ctx, "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetUserByID(missing) error = %v, want ErrNotFound", err)
		}
		if _, err := st.GetWorkspaceMember(ctx, "ws_b", "ua"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetWorkspaceMember(missing) error = %v, want ErrNotFound", err)
		}
	})
}

func TestWorkspacePromptNamespaces(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st *Store) {
		ctx := context.Background()
		for _, p := range []Prompt{
			{ID: "g1", Name: "support", Template: "global"},
			{ID: "a1", WorkspaceID: "ws_a", Name: "support", Template: "a v1"},
			{ID: "a2", WorkspaceID: "ws_a", Name: "support", Template: "a v2"},
			{ID: "b1", WorkspaceID: "ws_b", Name: "support", Template: "b v1"},
		} {
			got, err := st.CreatePromptVersion(ctx, p)
			if err != nil {
				t.Fatalf("CreatePromptVersion(%s) error = %v", p.ID, err)
			}
			want := map[string]int{"g1": 1, "a1": 1, "a2": 2, "b1": 1}[p.ID]
			if got.Version != want {
				t.Fatalf("CreatePromptVersion(%s) version = %d, want %d", p.ID, got.Version, want)
			}
		}
		check := func(name string, prompts []Prompt, err error, want []string) {
			t.Helper()
			if err != nil {
				t.Fatalf("%s error = %v", name, err)
			}
			if got := sortedIDs(prompts, func(p Prompt) string { return p.ID }); !equalStrings(got, want) {
				t.Fatalf("%s = %v, want %v", name, got, want)
			}
		}
		prompts, err := st.ListWorkspacePrompts(ctx, "")
		check("ListWorkspacePrompts('')", prompts, err, []string{"g1"})
		prompts, err = st.ListWorkspacePrompts(ctx, "ws_a")
		check("ListWorkspacePrompts(ws_a)", prompts, err, []string{"a1", "a2"})
		prompts, err = st.ListWorkspacePromptVersions(ctx, "ws_b", "support")
		check("ListWorkspacePromptVersions(ws_b)", prompts, err, []string{"b1"})
		prompts, err = st.ListPromptVersions(ctx, "support")
		check("ListPromptVersions", prompts, err, []string{"g1"})

		if p, err := st.GetWorkspacePrompt(ctx, "ws_a", "a2"); err != nil || p.WorkspaceID != "ws_a" || p.Template != "a v2" {
			t.Fatalf("GetWorkspacePrompt(ws_a, a2) = %+v, %v", p, err)
		}
		for _, tc := range []struct{ ws, id string }{{"ws_b", "a1"}, {"", "a1"}, {"ws_a", "g1"}, {"ws_a", "missing"}} {
			if _, err := st.GetWorkspacePrompt(ctx, tc.ws, tc.id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetWorkspacePrompt(%q, %q) error = %v, want ErrNotFound", tc.ws, tc.id, err)
			}
		}
		// Same workspace, name and version is still rejected.
		if err := st.InsertPrompt(ctx, Prompt{ID: "dup", WorkspaceID: "ws_a", Name: "support", Version: 2, Template: "t"}); !isUniqueViolation(err) {
			t.Fatalf("duplicate (workspace_id, name, version) error = %v, want unique violation", err)
		}
	})
}

// legacyPromptsDDL is the prompts table and unique index of releases
// before prompts had a workspace.
var legacyPromptsDDL = []string{
	`CREATE TABLE prompts (id TEXT PRIMARY KEY, name TEXT NOT NULL, version INTEGER DEFAULT 1, template TEXT NOT NULL,
		variables TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, metadata TEXT)`,
	`CREATE UNIQUE INDEX idx_prompts_name_version ON prompts(name, version)`,
	`INSERT INTO prompts (id, name, version, template, created_at, updated_at) VALUES ('old1', 'support', 1, 'old {{x}}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	`INSERT INTO prompts (id, name, version, template, created_at, updated_at) VALUES ('old2', 'support', 2, 'old2', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z')`,
}

// checkUpgradedPrompts verifies a store opened on legacyPromptsDDL.
func checkUpgradedPrompts(t *testing.T, st *Store, indexExists func(name string) bool) {
	t.Helper()
	ctx := context.Background()
	if !indexExists("idx_prompts_workspace_name_version") {
		t.Fatal("idx_prompts_workspace_name_version missing after upgrade")
	}
	if indexExists("idx_prompts_name_version") {
		t.Fatal("idx_prompts_name_version still exists after upgrade")
	}
	prompts, err := st.ListWorkspacePrompts(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := sortedIDs(prompts, func(p Prompt) string { return p.ID }); !equalStrings(got, []string{"old1", "old2"}) {
		t.Fatalf("existing prompts = %v, want them in the global namespace", got)
	}
	next, err := st.CreatePromptVersion(ctx, Prompt{ID: "g3", Name: "support", Template: "t"})
	if err != nil || next.Version != 3 {
		t.Fatalf("next global version = %d, %v; want 3", next.Version, err)
	}
	// Two workspaces may now hold the same name and version.
	for _, ws := range []string{"ws_a", "ws_b"} {
		if err := st.InsertPrompt(ctx, Prompt{ID: "p_" + ws, WorkspaceID: ws, Name: "support", Version: 1, Template: ws}); err != nil {
			t.Fatalf("InsertPrompt(%s, support, 1) error = %v", ws, err)
		}
	}
	if err := st.InsertPrompt(ctx, Prompt{ID: "dup", WorkspaceID: "ws_a", Name: "support", Version: 1, Template: "x"}); !isUniqueViolation(err) {
		t.Fatalf("duplicate in one workspace error = %v, want unique violation", err)
	}
	if p, err := st.GetWorkspacePrompt(ctx, "ws_b", "p_ws_b"); err != nil || p.Template != "ws_b" {
		t.Fatalf("GetWorkspacePrompt(ws_b) = %+v, %v", p, err)
	}
}

func TestPromptsUpgradeSQLite(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "sentinel.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range legacyPromptsDDL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	db.Close()

	for i := 0; i < 2; i++ { // the second open is a plain restart
		st, err := New(dir)
		if err != nil {
			t.Fatalf("New() on legacy prompts error = %v", err)
		}
		if i == 0 {
			checkUpgradedPrompts(t, st, func(name string) bool {
				var n int
				if err := st.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?", name).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n == 1
			})
		}
		st.Close()
	}
}

func TestPromptsUpgradePostgres(t *testing.T) {
	dsn := newPostgresTestDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range legacyPromptsDDL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	for i := 0; i < 2; i++ {
		st, err := OpenPostgres(dsn)
		if err != nil {
			t.Fatalf("OpenPostgres() on legacy prompts error = %v", err)
		}
		if i == 0 {
			checkUpgradedPrompts(t, st, func(name string) bool {
				exists, _, err := pgIndexState(context.Background(), db, name)
				if err != nil {
					t.Fatal(err)
				}
				return exists
			})
		}
		st.Close()
	}
}
