package audit

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

// MultiLogger fans one event out to several loggers. Every logger is tried
// even when an earlier one fails; the errors are joined.
type MultiLogger struct {
	loggers []Logger
}

func NewMultiLogger(loggers ...Logger) *MultiLogger {
	return &MultiLogger{loggers: loggers}
}

func (l *MultiLogger) Log(ctx context.Context, event Event) error {
	var errs []error
	for _, logger := range l.loggers {
		if logger == nil {
			continue
		}
		if err := logger.Log(ctx, event); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
	return l.store.InsertLog(ctx, storeRequestLog(event))
}

func storeRequestLog(event Event) store.RequestLog {
	status := "success"
	if event.Decision == "DENY" {
		status = "denied"
	}
	keyID := event.KeyID
	if keyID == "" {
		keyID = event.AgentID
	}
	metadata := map[string]string{"source": "mcp"}
	for key, value := range map[string]string{
		"method":       event.Method,
		"target":       event.Target,
		"workspace_id": event.Workspace,
		"action":       event.Action,
		"resource":     event.Resource,
		"rule":         event.Rule,
	} {
		if value != "" {
			metadata[key] = value
		}
	}
	encoded, _ := json.Marshal(metadata)
	return store.RequestLog{
		ID:             event.DecisionID,
		Timestamp:      event.Timestamp,
		TraceID:        event.TraceID,
		SessionID:      event.Session,
		Provider:       "mcp",
		Model:          event.Tool,
		APIKeyID:       keyID,
		Status:         status,
		Decision:       event.Decision,
		DecisionReason: event.Reason,
		LatencyMs:      float64(event.Latency.Microseconds()) / 1000,
		Metadata:       string(encoded),
	}
}
