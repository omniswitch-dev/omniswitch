package explain

import (
	"fmt"
	"time"

	"sentinel/internal/model"
)

func Format(decision model.Decision) string {
	decisionID := decision.ID
	if decisionID == "" {
		decisionID = "unknown"
	}
	return fmt.Sprintf("✓ %s\n\nDecision ID:\n%s\n\nMatched Rule:\n%s\n\nReason:\n%s\n\nEvaluation:\n%.2f ms\n",
		displayStatus(decision),
		decisionID,
		decision.Rule,
		decision.Reason,
		milliseconds(decision.EvaluationTime),
	)
}

func Inline(decision model.Decision) string {
	if decision.ID == "" {
		return fmt.Sprintf("✓ %s | Rule: %s | Reason: %s",
			displayStatus(decision),
			decision.Rule,
			decision.Reason,
		)
	}
	return fmt.Sprintf("✓ %s | Decision: %s | Rule: %s | Reason: %s",
		displayStatus(decision),
		decision.ID,
		decision.Rule,
		decision.Reason,
	)
}

func displayStatus(decision model.Decision) string {
	if decision.Allowed {
		return "ALLOWED"
	}
	return "DENIED"
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}
