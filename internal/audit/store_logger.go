package audit

import (
	"context"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

type MultiLogger struct {
	loggers []Logger
}

func NewMultiLogger(loggers ...Logger) *MultiLogger {
	return &MultiLogger{loggers: loggers}
}

func (l *MultiLogger) Log(ctx context.Context, event Event) error {
	for _, logger := range l.loggers {
		if logger == nil {
			continue
		}
		if err := logger.Log(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

type StoreLogger struct {
	store *store.Store
}

func NewStoreLogger(st *store.Store) *StoreLogger {
	return &StoreLogger{store: st}
}

func (l *StoreLogger) Log(ctx context.Context, event Event) error {
	if l == nil || l.store == nil {
		return nil
	}
	status := "success"
	if event.Decision == "DENY" {
		status = "denied"
	}
	return l.store.InsertLog(ctx, store.RequestLog{
		ID:             event.DecisionID,
		Timestamp:      event.Timestamp,
		Provider:       "mcp",
		Model:          event.Tool,
		Status:         status,
		Decision:       event.Decision,
		DecisionReason: event.Reason,
		LatencyMs:      float64(event.Latency.Microseconds()) / 1000,
	})
}
