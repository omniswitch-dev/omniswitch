package gatewayconfig

import (
	"strings"
	"testing"
)

func TestExternalAuthorizationConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		url, timeout string
		valid        bool
	}{
		{"https://policy.example/check", "2s", true},
		{"http://127.0.0.1:8091/check", "", true},
		{"file:///tmp/policy", "", false},
		{"https://user:secret@example.com/check", "", false},
		{"https://example.com/check#fragment", "", false},
		{"https://example.com/check", "0s", false},
		{"https://example.com/check", "-1s", false},
		{"", "2s", false},
	} {
		body := "authorization:\n  external:\n    url: " + `"` + tc.url + `"` + "\n"
		if tc.timeout != "" {
			body += "    timeout: " + tc.timeout + "\n"
		}
		_, err := Parse([]byte(body), ".yaml")
		if (err == nil) != tc.valid {
			t.Errorf("%q %q valid/error = %v/%v", tc.url, tc.timeout, tc.valid, err)
		}
	}
}

func TestPromptEnrichmentConfig(t *testing.T) {
	body := `routes:
  support:
    provider: test
    prepend_messages:
      - role: system
        content: Answer in plain language.
    append_messages:
      - role: user
        content: Be concise.
`
	cfg, err := Parse([]byte(body), ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routes["support"].PrependMessages[0].Content != "Answer in plain language." {
		t.Fatalf("route = %+v", cfg.Routes["support"])
	}
	for _, bad := range []string{strings.Replace(body, "role: system", "role: tool", 1), strings.Replace(body, "Answer in plain language.", `""`, 1)} {
		if _, err := Parse([]byte(bad), ".yaml"); err == nil {
			t.Fatal("invalid enrichment accepted")
		}
	}
	// JSON and YAML share the same validation.
	if _, err := Parse([]byte(`{"routes":{"support":{"provider":"test","prepend_messages":[{"role":"system","content":"JSON instruction"}]}}}`), ".json"); err != nil {
		t.Fatal(err)
	}
}
