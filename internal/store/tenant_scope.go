package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TenantFilter limits a control-plane query to one tenant. It mirrors
// gateway.TenantScope (the store does not import the gateway package): the
// zero value is global and matches every row; a WorkspaceID limits the query
// to that workspace; an OrganizationID without a WorkspaceID limits it to
// the organization and the workspaces in it. All filtering happens in SQL.
type TenantFilter struct {
	WorkspaceID    string
	OrganizationID string
}

// Global reports whether the filter matches every tenant.
func (f TenantFilter) Global() bool {
	return f.WorkspaceID == "" && f.OrganizationID == ""
}

// ListOrganizationsForTenant returns the organizations the filter can see:
// every organization for a global filter, the caller's own organization for
// an organization filter, and the organization that owns the workspace for
// a workspace filter. The workspace's organization is read from the
// workspaces table, not from the caller's claims.
func (s *Store) ListOrganizationsForTenant(ctx context.Context, f TenantFilter) ([]Organization, error) {
	if f.Global() {
		return s.ListOrganizations(ctx)
	}
	where, arg := "id = ?", f.OrganizationID
	if f.WorkspaceID != "" {
		where, arg = "id IN (SELECT organization_id FROM workspaces WHERE id = ?)", f.WorkspaceID
	}
	rows, err := s.query(ctx,
		"SELECT id, name, created_at, COALESCE(metadata, '') FROM organizations WHERE "+where+" ORDER BY created_at DESC", arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var organizations []Organization
	for rows.Next() {
		var org Organization
		var createdAt, metadata string
		if err := rows.Scan(&org.ID, &org.Name, &createdAt, &metadata); err != nil {
			return nil, err
		}
		org.CreatedAt = parseTime(createdAt)
		_ = json.Unmarshal([]byte(metadata), &org.Metadata)
		organizations = append(organizations, org)
	}
	return organizations, rows.Err()
}

// ListWorkspacesForTenant returns the workspaces the filter can see,
// optionally narrowed to one organization.
func (s *Store) ListWorkspacesForTenant(ctx context.Context, f TenantFilter, organizationID string) ([]Workspace, error) {
	if f.Global() {
		return s.ListWorkspaces(ctx, organizationID)
	}
	conds := []string{}
	args := []any{}
	if f.WorkspaceID != "" {
		conds = append(conds, "id = ?")
		args = append(args, f.WorkspaceID)
	} else {
		conds = append(conds, "organization_id = ?")
		args = append(args, f.OrganizationID)
	}
	if organizationID != "" {
		conds = append(conds, "organization_id = ?")
		args = append(args, organizationID)
	}
	rows, err := s.query(ctx,
		"SELECT id, organization_id, name, created_at, COALESCE(metadata, '') FROM workspaces WHERE "+
			strings.Join(conds, " AND ")+" ORDER BY created_at DESC",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workspaces []Workspace
	for rows.Next() {
		var workspace Workspace
		var createdAt, metadata string
		if err := rows.Scan(&workspace.ID, &workspace.OrganizationID, &workspace.Name, &createdAt, &metadata); err != nil {
			return nil, err
		}
		workspace.CreatedAt = parseTime(createdAt)
		_ = json.Unmarshal([]byte(metadata), &workspace.Metadata)
		workspaces = append(workspaces, workspace)
	}
	return workspaces, rows.Err()
}

// ListAPIKeysForTenant returns the API keys the filter can see: every key
// for a global filter, the keys of the workspace for a workspace filter, and
// the keys of the organization's workspaces for an organization filter.
// Keys without a workspace are global and only a global filter sees them.
func (s *Store) ListAPIKeysForTenant(ctx context.Context, f TenantFilter) ([]APIKey, error) {
	if f.Global() {
		return s.ListAPIKeys(ctx)
	}
	if f.WorkspaceID != "" {
		return s.listAPIKeys(ctx, "workspace_id = ?", f.WorkspaceID)
	}
	return s.listAPIKeys(ctx, "workspace_id IN (SELECT id FROM workspaces WHERE organization_id = ?)", f.OrganizationID)
}

// memberTenantCondition returns a SQL condition, over a workspace_members
// row aliased m, that holds when the membership belongs to a workspace the
// filter can see. It must not be called with a global filter.
func memberTenantCondition(f TenantFilter) (string, any) {
	if f.WorkspaceID != "" {
		return "m.workspace_id = ?", f.WorkspaceID
	}
	return "m.workspace_id IN (SELECT id FROM workspaces WHERE organization_id = ?)", f.OrganizationID
}

// ListWorkspaceMembersForTenant returns the memberships of the workspaces
// the filter can see, optionally narrowed to one workspace.
func (s *Store) ListWorkspaceMembersForTenant(ctx context.Context, f TenantFilter, workspaceID string) ([]WorkspaceMember, error) {
	if f.Global() {
		return s.ListWorkspaceMembers(ctx, workspaceID)
	}
	cond, arg := memberTenantCondition(f)
	conds := []string{cond}
	args := []any{arg}
	if workspaceID != "" {
		conds = append(conds, "m.workspace_id = ?")
		args = append(args, workspaceID)
	}
	rows, err := s.query(ctx,
		"SELECT m.workspace_id, m.user_id, m.role, m.created_at FROM workspace_members m WHERE "+
			strings.Join(conds, " AND ")+" ORDER BY m.created_at DESC",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []WorkspaceMember
	for rows.Next() {
		var member WorkspaceMember
		var createdAt string
		if err := rows.Scan(&member.WorkspaceID, &member.UserID, &member.Role, &createdAt); err != nil {
			return nil, err
		}
		member.CreatedAt = parseTime(createdAt)
		members = append(members, member)
	}
	return members, rows.Err()
}

// ListUsersForTenant returns the users the filter can see. Users are shared
// identities, so a scoped filter sees only users who are members of at
// least one workspace it can see.
func (s *Store) ListUsersForTenant(ctx context.Context, f TenantFilter) ([]User, error) {
	if f.Global() {
		return s.ListUsers(ctx)
	}
	cond, arg := memberTenantCondition(f)
	rows, err := s.query(ctx,
		"SELECT u.id, u.email, COALESCE(u.name, ''), u.created_at, COALESCE(u.metadata, '') FROM users u "+
			"WHERE EXISTS (SELECT 1 FROM workspace_members m WHERE m.user_id = u.id AND "+cond+") ORDER BY u.created_at DESC",
		arg,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var user User
		var createdAt, metadata string
		if err := rows.Scan(&user.ID, &user.Email, &user.Name, &createdAt, &metadata); err != nil {
			return nil, err
		}
		user.CreatedAt = parseTime(createdAt)
		_ = json.Unmarshal([]byte(metadata), &user.Metadata)
		users = append(users, user)
	}
	return users, rows.Err()
}

// GetUserByID returns one user, or ErrNotFound.
func (s *Store) GetUserByID(ctx context.Context, id string) (User, error) {
	return s.getUser(ctx, "id", id)
}

// GetUserByEmail returns the user with email, or ErrNotFound.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	return s.getUser(ctx, "email", email)
}

func (s *Store) getUser(ctx context.Context, column, value string) (User, error) {
	var user User
	var createdAt, metadata string
	err := s.queryRow(ctx,
		"SELECT id, email, COALESCE(name, ''), created_at, COALESCE(metadata, '') FROM users WHERE "+column+" = ?", value,
	).Scan(&user.ID, &user.Email, &user.Name, &createdAt, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return user, ErrNotFound
	}
	if err != nil {
		return user, err
	}
	user.CreatedAt = parseTime(createdAt)
	_ = json.Unmarshal([]byte(metadata), &user.Metadata)
	return user, nil
}

// GetWorkspaceMember returns one membership, or ErrNotFound.
func (s *Store) GetWorkspaceMember(ctx context.Context, workspaceID, userID string) (WorkspaceMember, error) {
	member := WorkspaceMember{WorkspaceID: workspaceID, UserID: userID}
	var createdAt string
	err := s.queryRow(ctx,
		"SELECT role, created_at FROM workspace_members WHERE workspace_id = ? AND user_id = ?", workspaceID, userID,
	).Scan(&member.Role, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return member, ErrNotFound
	}
	if err != nil {
		return member, err
	}
	member.CreatedAt = parseTime(createdAt)
	return member, nil
}

// AddUserToWorkspace creates user if its email is new and adds it to
// member.WorkspaceID with member.Role, in one transaction. Unlike UpsertUser
// it never changes an existing user: users are shared between tenants, so
// a tenant-scoped admin must not rename or relabel an identity that other
// tenants also use. It returns the stored user (with the stored ID when the
// email already existed) and the stored membership.
func (s *Store) AddUserToWorkspace(ctx context.Context, user User, member WorkspaceMember) (User, WorkspaceMember, error) {
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}
	if member.CreatedAt.IsZero() {
		member.CreatedAt = user.CreatedAt
	}
	metadata, _ := json.Marshal(user.Metadata)
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return user, member, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.txSQL(
		`INSERT INTO users (id, email, name, created_at, metadata) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(email) DO NOTHING`),
		user.ID, user.Email, user.Name, formatTime(user.CreatedAt), string(metadata),
	); err != nil {
		return user, member, err
	}
	var createdAt, storedMetadata string
	if err := tx.QueryRowContext(ctx, s.txSQL(
		"SELECT id, COALESCE(name, ''), created_at, COALESCE(metadata, '') FROM users WHERE email = ?"), user.Email,
	).Scan(&user.ID, &user.Name, &createdAt, &storedMetadata); err != nil {
		return user, member, fmt.Errorf("read user: %w", err)
	}
	user.CreatedAt = parseTime(createdAt)
	user.Metadata = nil
	_ = json.Unmarshal([]byte(storedMetadata), &user.Metadata)
	member.UserID = user.ID
	if _, err := tx.ExecContext(ctx, s.txSQL(
		`INSERT INTO workspace_members (workspace_id, user_id, role, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(workspace_id, user_id) DO UPDATE SET role = excluded.role`),
		member.WorkspaceID, member.UserID, member.Role, formatTime(member.CreatedAt),
	); err != nil {
		return user, member, err
	}
	return user, member, tx.Commit()
}

// promptColumns selects a prompts row in scanPrompt order.
const promptColumns = "id, COALESCE(workspace_id, ''), name, COALESCE(version, 1), template, COALESCE(variables, ''), created_at, updated_at"

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPrompt(row rowScanner) (Prompt, error) {
	var p Prompt
	var createdAt, updatedAt, varsStr string
	if err := row.Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Version, &p.Template, &varsStr, &createdAt, &updatedAt); err != nil {
		return p, err
	}
	p.CreatedAt = parseTime(createdAt)
	p.UpdatedAt = parseTime(updatedAt)
	_ = json.Unmarshal([]byte(varsStr), &p.Variables)
	return p, nil
}

func (s *Store) listPrompts(ctx context.Context, query string, args ...any) ([]Prompt, error) {
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var prompts []Prompt
	for rows.Next() {
		p, err := scanPrompt(rows)
		if err != nil {
			return nil, err
		}
		prompts = append(prompts, p)
	}
	return prompts, rows.Err()
}

// ListWorkspacePrompts returns the prompts of one workspace namespace. The
// empty workspace ID is the global namespace, which holds every prompt
// created before prompts had a workspace.
func (s *Store) ListWorkspacePrompts(ctx context.Context, workspaceID string) ([]Prompt, error) {
	return s.listPrompts(ctx,
		"SELECT "+promptColumns+" FROM prompts WHERE workspace_id = ? ORDER BY updated_at DESC", workspaceID)
}

// ListWorkspacePromptVersions returns every version of one prompt name in
// one workspace namespace, newest first.
func (s *Store) ListWorkspacePromptVersions(ctx context.Context, workspaceID, name string) ([]Prompt, error) {
	return s.listPrompts(ctx,
		"SELECT "+promptColumns+" FROM prompts WHERE workspace_id = ? AND name = ? ORDER BY version DESC", workspaceID, name)
}

// GetWorkspacePrompt returns a prompt by ID if it belongs to the given
// workspace namespace, and ErrNotFound otherwise, so a prompt in another
// workspace looks exactly like a missing one.
func (s *Store) GetWorkspacePrompt(ctx context.Context, workspaceID, id string) (Prompt, error) {
	p, err := scanPrompt(s.queryRow(ctx,
		"SELECT "+promptColumns+" FROM prompts WHERE id = ? AND workspace_id = ?", id, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}
