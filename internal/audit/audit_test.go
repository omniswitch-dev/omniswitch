package audit

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/model"
)

func TestStdoutLoggerLog(t *testing.T) {
	tests := []struct {
		name     string
		event    Event
		contains []string
	}{
		{
			name: "writes json event",
			event: NewEvent(
				model.ToolRequest{
					Agent:    model.Agent{ID: "agent-1"},
					Tool:     model.Tool{Name: "github"},
					Action:   model.Action{Name: "delete"},
					Resource: model.Resource{Name: "payments-prod"},
				},
				model.Decision{Allowed: false, Rule: "production-delete", Reason: "blocked"},
			),
			contains: []string{`"agent_id":"agent-1"`, `"decision":"DENY"`, `"rule":"production-delete"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := NewStdoutLogger(&buf)
			if err := logger.Log(context.Background(), tt.event); err != nil {
				t.Fatalf("Log() error = %v", err)
			}
			output := buf.String()
			for _, want := range tt.contains {
				if !strings.Contains(output, want) {
					t.Fatalf("output = %q, want substring %q", output, want)
				}
			}
		})
	}
}

type failingLogger struct{ calls *int }

func (f failingLogger) Log(context.Context, Event) error {
	*f.calls++
	return errors.New("boom")
}

func TestMultiLoggerTriesEveryLogger(t *testing.T) {
	calls := 0
	var buf bytes.Buffer
	logger := NewMultiLogger(failingLogger{calls: &calls}, nil, NewStdoutLogger(&buf), failingLogger{calls: &calls})
	err := logger.Log(context.Background(), Event{Tool: "github"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Log() error = %v, want joined errors", err)
	}
	if calls != 2 || !strings.Contains(buf.String(), `"tool":"github"`) {
		t.Fatalf("calls = %d, stdout = %q; want every logger tried", calls, buf.String())
	}
}

func TestStoreRequestLogCarriesAttribution(t *testing.T) {
	event := NewEvent(model.ToolRequest{
		Agent:   model.Agent{ID: "key_1"},
		Tool:    model.Tool{Name: "delete_repo"},
		Target:  model.Target{Name: "github"},
		Session: model.Session{ID: "sess-1"},
	}, model.Decision{ID: "dec_1", Allowed: true, Rule: "none"})
	event.Method = "tools/call"
	event.Workspace = "ws_1"
	log := storeRequestLog(event)
	if log.APIKeyID != "key_1" || log.SessionID != "sess-1" || log.Provider != "mcp" {
		t.Fatalf("log = %+v", log)
	}
	for _, want := range []string{`"method":"tools/call"`, `"target":"github"`, `"workspace_id":"ws_1"`} {
		if !strings.Contains(log.Metadata, want) {
			t.Fatalf("metadata = %s, want %s", log.Metadata, want)
		}
	}
}
