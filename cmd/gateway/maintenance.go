package main

import (
	"context"
	"log"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// maintenanceInterval is how often the gateway deletes expired cache entries
// and, when OMNISWITCH_LOG_RETENTION_DAYS is set, old records.
const maintenanceInterval = time.Hour

// maintenanceStore is the part of *store.Store that maintenance uses.
type maintenanceStore interface {
	PurgeExpiredCache(ctx context.Context, now time.Time) (int64, error)
	PurgeRecordsBefore(ctx context.Context, cutoff time.Time) (store.RetentionResult, error)
}

// startMaintenance runs one maintenance pass at once and then one per
// interval until ctx is done. The returned channel closes when the loop has
// stopped, so the caller can wait for it before it closes the store.
func startMaintenance(ctx context.Context, st maintenanceStore, retentionDays int, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			runMaintenance(ctx, st, retentionDays, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}

// runMaintenance deletes expired cache entries and, when retentionDays is
// positive, records older than the retention cutoff.
func runMaintenance(ctx context.Context, st maintenanceStore, retentionDays int, now time.Time) {
	purged, err := st.PurgeExpiredCache(ctx, now)
	switch {
	case err != nil && ctx.Err() == nil:
		log.Printf("maintenance: delete expired cache entries: %v", err)
	case purged > 0:
		log.Printf("maintenance: deleted %d expired cache entries", purged)
	}
	if retentionDays <= 0 {
		return
	}
	cutoff := retentionCutoff(now, retentionDays)
	result, err := st.PurgeRecordsBefore(ctx, cutoff)
	if err != nil && ctx.Err() == nil {
		log.Printf("maintenance: delete records before %s: %v", cutoff.Format(time.RFC3339), err)
	}
	if result.RequestLogs+result.GuardrailEvents+result.ShadowRequests > 0 {
		log.Printf("maintenance: deleted %d request logs, %d guardrail events and %d shadow requests from before %s",
			result.RequestLogs, result.GuardrailEvents, result.ShadowRequests, cutoff.Format(time.RFC3339))
	}
}

// retentionCutoff returns the time before which records are deleted. The
// cutoff is never inside the current UTC month, because monthly key budgets
// are computed from request_logs.
func retentionCutoff(now time.Time, retentionDays int) time.Time {
	now = now.UTC()
	cutoff := now.AddDate(0, 0, -retentionDays)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if cutoff.After(monthStart) {
		return monthStart
	}
	return cutoff
}
