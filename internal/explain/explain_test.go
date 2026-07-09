package explain

import (
	"strings"
	"testing"
	"time"

	"sentinel/internal/model"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		name     string
		decision model.Decision
		contains []string
	}{
		{
			name: "denied",
			decision: model.Decision{
				Allowed:        false,
				Rule:           "production-delete",
				Reason:         "Repository payments-prod is protected.",
				EvaluationTime: 540 * time.Microsecond,
			},
			contains: []string{"✓ DENIED", "Matched Rule:", "production-delete", "Evaluation:", "0.54 ms"},
		},
		{
			name: "allowed",
			decision: model.Decision{
				Allowed:        true,
				Rule:           "none",
				Reason:         "No policy violation detected.",
				EvaluationTime: time.Millisecond,
			},
			contains: []string{"✓ ALLOWED", "none", "1.00 ms"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := Format(tt.decision)
			for _, want := range tt.contains {
				if !strings.Contains(output, want) {
					t.Fatalf("Format() = %q, want substring %q", output, want)
				}
			}
		})
	}
}
