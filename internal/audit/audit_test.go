package audit

import (
	"bytes"
	"context"
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
