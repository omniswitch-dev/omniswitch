package gatewayconfig

import (
	"strings"
	"testing"
	"time"
)

func TestValidateOIDCStaleIfError(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    time.Duration
		wantErr bool
	}{
		{value: "0s", want: 0},
		{value: "24h", want: 24 * time.Hour},
		{value: "168h", want: MaxOIDCStaleIfError},
		{value: "-1s", wantErr: true},
		{value: "169h", wantErr: true},
	} {
		cfg, err := Parse([]byte(`identity: {oidc: {jwks_url: https://issuer.example.test/keys, stale_if_error: `+tc.value+`}}`), ".yaml")
		if tc.wantErr {
			if err == nil || !strings.Contains(err.Error(), "identity.oidc.stale_if_error") {
				t.Errorf("stale_if_error %s: error = %v, want validation error", tc.value, err)
			}
			continue
		}
		if err != nil || cfg.Identity.OIDC.StaleIfError == nil || cfg.Identity.OIDC.StaleIfError.Duration != tc.want {
			t.Errorf("stale_if_error %s: cfg/err = %+v/%v, want %s", tc.value, cfg.Identity.OIDC, err, tc.want)
		}
	}
}

func TestRepositoryExampleConfigOIDCStaleIfError(t *testing.T) {
	cfg, err := LoadFile("../../examples/gateway-config.yaml")
	if err != nil {
		t.Fatalf("LoadFile(example) error = %v", err)
	}
	if cfg.Identity.OIDC.StaleIfError == nil || cfg.Identity.OIDC.StaleIfError.Duration != 24*time.Hour {
		t.Fatalf("example stale_if_error = %+v, want 24h", cfg.Identity.OIDC.StaleIfError)
	}
}
