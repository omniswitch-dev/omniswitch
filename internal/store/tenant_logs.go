package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// LogScope limits the request logs, metrics, analytics, traces and feedback
// that a dashboard read returns. The zero value returns everything.
//
// WorkspaceID and OrganizationID are the caller's tenant: a non-empty
// WorkspaceID limits the read to that workspace (OrganizationID is then
// ignored), otherwise a non-empty OrganizationID limits it to that
// organization. Rows written before the tenant columns existed have no
// tenant, so only an unscoped read returns them. APIKeyID further limits
// request logs to one API key; feedback has no key and ignores it.
type LogScope struct {
	APIKeyID       string
	WorkspaceID    string
	OrganizationID string
}

// where appends the scope's conditions to base and args.
func (s LogScope) where(base string, args []any) (string, []any) {
	where := base
	if s.APIKeyID != "" {
		where += " AND api_key_id = ?"
		args = append(args, s.APIKeyID)
	}
	switch {
	case s.WorkspaceID != "":
		where += " AND workspace_id = ?"
		args = append(args, s.WorkspaceID)
	case s.OrganizationID != "":
		where += " AND organization_id = ?"
		args = append(args, s.OrganizationID)
	}
	return where, args
}

// tenantOnly drops the API key condition, for tables without api_key_id.
func (s LogScope) tenantOnly() LogScope {
	return LogScope{WorkspaceID: s.WorkspaceID, OrganizationID: s.OrganizationID}
}

// ListLogsInScope returns paginated request logs that scope may see,
// newest first, and their total count.
func (s *Store) ListLogsInScope(ctx context.Context, scope LogScope, limit, offset int, provider, status string) ([]RequestLog, int, error) {
	return s.listLogs(ctx, limit, offset, provider, status, scope)
}

// GetMetricsInScope aggregates the request logs that scope may see.
func (s *Store) GetMetricsInScope(ctx context.Context, since time.Time, scope LogScope) (Metrics, error) {
	return s.getMetrics(ctx, since, scope)
}

// GetProviderMetricsInScope aggregates per provider the request logs that
// scope may see.
func (s *Store) GetProviderMetricsInScope(ctx context.Context, since time.Time, scope LogScope) ([]ProviderMetrics, error) {
	return s.getProviderMetrics(ctx, since, scope)
}

// GetCostAnalyticsInScope groups the cost of the request logs that scope
// may see.
func (s *Store) GetCostAnalyticsInScope(ctx context.Context, since time.Time, groupBy string, scope LogScope) ([]CostAnalyticsRow, error) {
	return s.getCostAnalytics(ctx, since, groupBy, scope)
}

// GetTraceInScope returns the part of a trace that scope may see. It
// returns sql.ErrNoRows when scope sees no log of the trace, so a trace of
// another tenant looks the same as a missing one.
func (s *Store) GetTraceInScope(ctx context.Context, traceID string, scope LogScope) (TraceDetail, error) {
	return s.getTrace(ctx, traceID, scope)
}

// ListFeedbackInScope returns the feedback in scope's tenant, newest first.
func (s *Store) ListFeedbackInScope(ctx context.Context, scope LogScope, limit int, requestID, traceID string) ([]Feedback, error) {
	return s.listFeedback(ctx, limit, requestID, traceID, scope)
}

// RequestLogInScope reports whether a request log with this id exists and
// scope may see it.
func (s *Store) RequestLogInScope(ctx context.Context, requestID string, scope LogScope) (bool, error) {
	where, args := scope.where("id = ?", []any{requestID})
	return s.logExists(ctx, where, args)
}

// TraceInScope reports whether scope may see at least one request log of
// the trace.
func (s *Store) TraceInScope(ctx context.Context, traceID string, scope LogScope) (bool, error) {
	where, args := scope.where("trace_id = ?", []any{traceID})
	return s.logExists(ctx, where, args)
}

func (s *Store) logExists(ctx context.Context, where string, args []any) (bool, error) {
	var one int
	err := s.queryRow(ctx, "SELECT 1 FROM request_logs WHERE "+where+" LIMIT 1", args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
