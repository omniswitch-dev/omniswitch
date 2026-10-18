package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

type fakeMaintenanceStore struct {
	mu          sync.Mutex
	cacheCalls  []time.Time
	recordCalls []time.Time
}

func (f *fakeMaintenanceStore) PurgeExpiredCache(_ context.Context, now time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cacheCalls = append(f.cacheCalls, now)
	return 0, nil
}

func (f *fakeMaintenanceStore) PurgeRecordsBefore(_ context.Context, cutoff time.Time) (store.RetentionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCalls = append(f.recordCalls, cutoff)
	return store.RetentionResult{}, nil
}

func (f *fakeMaintenanceStore) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cacheCalls), len(f.recordCalls)
}

func TestRetentionCutoffNeverEntersCurrentMonth(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		days int
		want time.Time
	}{
		{days: 7, want: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{days: 17, want: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{days: 18, want: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)},
		{days: 90, want: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		if got := retentionCutoff(now, tt.days); !got.Equal(tt.want) {
			t.Errorf("retentionCutoff(%d days) = %s, want %s", tt.days, got, tt.want)
		}
	}
}

func TestRunMaintenanceKeepsRecordsWhenRetentionIsOff(t *testing.T) {
	st := &fakeMaintenanceStore{}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	runMaintenance(context.Background(), st, 0, now)
	if len(st.cacheCalls) != 1 || !st.cacheCalls[0].Equal(now) {
		t.Fatalf("cache purge calls = %v, want one call at %s", st.cacheCalls, now)
	}
	if len(st.recordCalls) != 0 {
		t.Fatalf("records purged with retention off: %v", st.recordCalls)
	}

	runMaintenance(context.Background(), st, 90, now)
	if want := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC); len(st.recordCalls) != 1 || !st.recordCalls[0].Equal(want) {
		t.Fatalf("record purge calls = %v, want one call at %s", st.recordCalls, want)
	}
}

func TestStartMaintenanceRunsPeriodicallyAndStops(t *testing.T) {
	st := &fakeMaintenanceStore{}
	ctx, cancel := context.WithCancel(context.Background())
	done := startMaintenance(ctx, st, 30, 5*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for {
		cacheCalls, recordCalls := st.counts()
		if cacheCalls >= 2 && recordCalls >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("maintenance ran %d cache and %d record purges, want at least 2 each", cacheCalls, recordCalls)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance loop did not stop after cancel")
	}
}

func TestRunMaintenanceDeletesOldRecords(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, entry := range []store.RequestLog{
		{ID: "old", Timestamp: now.AddDate(0, 0, -100), Provider: "openai", Model: "gpt-4o", Status: "success"},
		{ID: "new", Timestamp: now, Provider: "openai", Model: "gpt-4o", Status: "success"},
	} {
		if err := st.InsertLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}

	runMaintenance(ctx, st, 30, now)

	logs, total, err := st.ListLogs(ctx, 10, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(logs) != 1 || logs[0].ID != "new" {
		t.Fatalf("logs after retention = %+v (total %d), want only the new row", logs, total)
	}
}

func TestLoadRuntimeSettingsLogRetentionDays(t *testing.T) {
	clearGatewayEnv(t)
	settings, err := loadRuntimeSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.logRetentionDays != 0 {
		t.Fatalf("default logRetentionDays = %d, want 0 (keep forever)", settings.logRetentionDays)
	}
	for value, want := range map[string]int{"0": 0, "30": 30} {
		t.Setenv("OMNISWITCH_LOG_RETENTION_DAYS", value)
		settings, err := loadRuntimeSettings()
		if err != nil || settings.logRetentionDays != want {
			t.Fatalf("OMNISWITCH_LOG_RETENTION_DAYS=%s: got %d, %v; want %d", value, settings.logRetentionDays, err, want)
		}
	}
	for _, value := range []string{"-1", "thirty"} {
		t.Setenv("OMNISWITCH_LOG_RETENTION_DAYS", value)
		if _, err := loadRuntimeSettings(); err == nil || !strings.Contains(err.Error(), "OMNISWITCH_LOG_RETENTION_DAYS") {
			t.Fatalf("OMNISWITCH_LOG_RETENTION_DAYS=%s: error = %v, want a parse error", value, err)
		}
	}
}
