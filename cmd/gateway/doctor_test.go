package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoctorUsesOnlyReadOnlyEndpointsAndKeepsKeyPrivate(t *testing.T) {
	const secret = "sk-test-private-do-not-print"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		switch r.URL.Path {
		case "/prefix/healthz", "/prefix/readyz":
			if r.Header.Get("Authorization") != "" {
				t.Error("key sent to public health endpoint")
			}
			status := "ok"
			if r.URL.Path == "/prefix/readyz" {
				status = "ready"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
		case "/prefix/v1/models":
			if r.Header.Get("Authorization") != "Bearer "+secret {
				t.Error("missing gateway key")
			}
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"test-model"}]}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer server.Close()
	for _, jsonOutput := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		args := []string{"doctor", "--url", server.URL + "/prefix/", "--api-key-env", "TEST_GATEWAY_KEY"}
		if jsonOutput {
			args = append(args, "--json")
		}
		action, code := dispatch(args, &stdout, &stderr, func(name string) string {
			if name == "TEST_GATEWAY_KEY" {
				return secret
			}
			return ""
		})
		if action != actionExit || code != 0 || strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Fatalf("result = %v/%d/%s/%s", action, code, &stdout, &stderr)
		}
		if jsonOutput {
			var report doctorReport
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil || !report.OK || len(report.Checks) != 3 {
				t.Fatalf("report: %s / %v", &stdout, err)
			}
		}
	}
	if calls.Load() != 6 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestDoctorReportsActionableFailuresWithoutServerBodies(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
	}{
		{"auth", 401, "secret-body", "OMNISWITCH_API_KEY"},
		{"policy", 403, "secret-body", "permitted gateway key"},
		{"readiness", 503, "secret-body", "storage connectivity"},
		{"wrong URL", 404, "secret-body", "reverse proxy path prefix"},
		{"no models", 200, `{"data":[]}`, "allowed_models"},
		{"HTML", 200, "<html>secret-body</html>", "expected a JSON"},
		{"oversized", 200, strings.Repeat("x", (1<<20)+1), "oversized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			if code := runDoctor([]string{"--url", server.URL}, &stdout, &stderr, nil); code != 1 || !strings.Contains(stdout.String(), tc.want) || strings.Contains(stdout.String(), "secret-body") {
				t.Fatalf("code/report = %d/%s/%s", code, &stdout, &stderr)
			}
		})
	}
}

func TestDoctorRejectsRedirectsAndEnforcesDeadline(t *testing.T) {
	var forwarded atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Store(true) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer redirect.Close()
	var stdout, stderr bytes.Buffer
	if code := runDoctor([]string{"--url", redirect.URL}, &stdout, &stderr, func(string) string { return "secret-key" }); code != 1 || forwarded.Load() {
		t.Fatalf("redirect followed or passed: %d/%v", code, forwarded.Load())
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	report := diagnoseGateway(ctx, slow.Client(), slow.URL, "")
	if report.OK || len(report.Checks) != 3 || !strings.Contains(report.Checks[0].Detail, "deadline") {
		t.Fatalf("timeout report = %+v", report)
	}
}

func TestDoctorRejectsUnsafeURLAndInvalidFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--url", "https://user:secret@example.com"}, {"--url", "https://example.com?key=secret"},
		{"--url", "file:///tmp/gateway"}, {"--url", "https://example.com#secret"}, {"--url", ":bad"},
		{"--timeout", "0s"}, {"--timeout", "2m"}, {"extra"}, {"--unknown"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runDoctor(args, &stdout, &stderr, nil); code != 2 || strings.Contains(stderr.String(), "secret") {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, &stderr)
		}
	}
}
