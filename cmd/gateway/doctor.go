package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type doctorCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

type doctorReport struct {
	OK     bool          `json:"ok"`
	Checks []doctorCheck `json:"checks"`
}

// doctor makes read-only gateway requests. It neither calls inference APIs
// nor prints keys, server bodies, or transport errors that may contain them.
func runDoctor(args []string, stdout, stderr io.Writer, env func(string) string) int {
	flags := flag.NewFlagSet("omniswitch doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	base := flags.String("url", "http://127.0.0.1:8080", "Running gateway URL")
	keyEnv := flags.String("api-key-env", "OMNISWITCH_API_KEY", "Environment variable containing a gateway API key")
	timeout := flags.Duration("timeout", 10*time.Second, "Total diagnostic timeout")
	jsonOutput := flags.Bool("json", false, "Print a JSON report")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || *timeout <= 0 || *timeout > time.Minute {
		fmt.Fprintln(stderr, "doctor: use flags only, with --timeout greater than zero and at most 1m")
		return 2
	}
	u, err := url.Parse(*base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		fmt.Fprintln(stderr, "doctor: --url must be an HTTP(S) URL without credentials, query, or fragment")
		return 2
	}
	key := ""
	if env != nil {
		key = strings.TrimSpace(env(*keyEnv))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	report := diagnoseGateway(ctx, client, strings.TrimRight(u.String(), "/"), key)
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			fmt.Fprintln(stderr, "doctor: could not write report")
			return 1
		}
	} else {
		fmt.Fprintln(stdout, "OmniSwitch setup check (no inference requests)")
		for _, check := range report.Checks {
			status := "PASS"
			if !check.OK {
				status = "FAIL"
			}
			fmt.Fprintf(stdout, "%s %s: %s\n", status, check.Name, check.Detail)
			if check.Fix != "" {
				fmt.Fprintf(stdout, "  Fix: %s\n", check.Fix)
			}
		}
		if report.OK {
			fmt.Fprintln(stdout, "Gateway setup is reachable and models are visible. Provider credentials and inference have not been tested.")
		}
	}
	if !report.OK {
		return 1
	}
	return 0
}

func diagnoseGateway(ctx context.Context, client *http.Client, base, key string) doctorReport {
	report := doctorReport{OK: true}
	for _, target := range []struct{ name, path, status string }{
		{"health", "/healthz", "ok"}, {"readiness", "/readyz", "ready"}, {"models", "/v1/models", ""},
	} {
		check := doctorCheck{Name: target.name}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+target.path, nil)
		if err != nil {
			check.Detail = "could not construct diagnostic request"
		} else {
			if target.name == "models" && key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
			}
			response, err := client.Do(req)
			if err != nil {
				check.Detail = "gateway unreachable or diagnostic deadline exceeded"
				check.Fix = "Start omniswitch serve and check --url and network access."
			} else {
				check = inspectDoctorResponse(target.name, target.status, response)
				_ = response.Body.Close()
			}
		}
		if !check.OK {
			report.OK = false
		}
		report.Checks = append(report.Checks, check)
	}
	return report
}

func inspectDoctorResponse(name, expectedStatus string, response *http.Response) doctorCheck {
	check := doctorCheck{Name: name}
	if response.StatusCode != http.StatusOK {
		check.Detail = fmt.Sprintf("HTTP %d", response.StatusCode)
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			check.Fix = "Set OMNISWITCH_API_KEY to a permitted gateway key, or use --api-key-env NAME."
		case http.StatusServiceUnavailable:
			check.Fix = "Check gateway logs, storage connectivity, and provider configuration."
		case http.StatusNotFound:
			check.Fix = "Check --url points to OmniSwitch and includes any reverse proxy path prefix."
		default:
			check.Fix = "Check gateway logs and reverse proxy configuration; redirects are not followed."
		}
		return check
	}
	const maxBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil || len(body) > maxBytes {
		check.Detail = "unreadable or oversized diagnostic response"
		return check
	}
	var payload struct {
		Status string            `json:"status"`
		Data   []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		check.Detail = "expected a JSON gateway response"
		check.Fix = "Check --url and reverse proxy configuration."
		return check
	}
	if name == "models" {
		if len(payload.Data) == 0 {
			check.Detail = "no models visible to this key"
			check.Fix = "Configure a provider/model and check this key's allowed_models policy."
			return check
		}
		check.Detail = fmt.Sprintf("%d model(s) visible; inference not tested", len(payload.Data))
	} else {
		if payload.Status != expectedStatus {
			check.Detail = "unexpected gateway health status"
			return check
		}
		check.Detail = expectedStatus
	}
	check.OK = true
	return check
}
