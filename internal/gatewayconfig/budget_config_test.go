package gatewayconfig

import (
	"strings"
	"testing"
	"time"
)

func TestValidateBudgetSettings(t *testing.T) {
	cfg, err := Parse([]byte(`gateway: {budget_fail_open: true, budget_refresh_interval: 2s}`), ".yaml")
	if err != nil || cfg.Gateway.BudgetFailOpen == nil || !*cfg.Gateway.BudgetFailOpen ||
		cfg.Gateway.BudgetRefreshInterval == nil || cfg.Gateway.BudgetRefreshInterval.Duration != 2*time.Second {
		t.Fatalf("Parse() cfg/err = %+v/%v, want budget settings", cfg.Gateway, err)
	}
	for _, value := range []string{"0s", "10ms", "11m", "-1s"} {
		_, err := Parse([]byte(`gateway: {budget_refresh_interval: `+value+`}`), ".yaml")
		if err == nil || !strings.Contains(err.Error(), "gateway.budget_refresh_interval") {
			t.Fatalf("Parse(%s) error = %v, want budget_refresh_interval validation", value, err)
		}
	}
}
