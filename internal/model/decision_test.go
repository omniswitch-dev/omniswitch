package model

import "testing"

func TestDecisionStatus(t *testing.T) {
	tests := []struct {
		name     string
		decision Decision
		want     string
	}{
		{name: "allowed", decision: Decision{Allowed: true}, want: "ALLOW"},
		{name: "denied", decision: Decision{Allowed: false}, want: "DENY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.decision.Status(); got != tt.want {
				t.Fatalf("Status() = %q, want %q", got, tt.want)
			}
		})
	}
}
