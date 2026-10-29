package eval

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

const replayRequests = `"requests":[{"agent":{"id":"a"},"tool":{"name":"github"},"action":{"name":"delete"},"resource":{"environment":"production"}}]`

func newReplayHandler(t *testing.T, root string) *Handler {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	handler := NewHandler(st)
	handler.SetPolicyRoot(root)
	return handler
}

func replay(handler *Handler, paths string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	body := `{"policy_paths":` + paths + `,` + replayRequests + `}`
	handler.ReplayPolicies(rec, httptest.NewRequest(http.MethodPost, "/api/evals/policy", strings.NewReader(body)))
	return rec
}

func TestReplayPoliciesRejectsPathsOutsidePolicyRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "policies")
	if err := os.MkdirAll(filepath.Join(root, "nested", "dir.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	const secret = "OPENAI_API_KEY=sk-live-SUPERSECRET123"
	secretCEL := filepath.Join(base, "secret.cel")
	secretTxt := filepath.Join(root, "secret.txt")
	for _, path := range []string{secretCEL, secretTxt} {
		if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "ok.cel"), []byte(`action.name == "delete"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(secretCEL, filepath.Join(root, "escape.cel")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(base, filepath.Join(root, "dirlink")); err != nil {
			t.Fatal(err)
		}
	}
	handler := newReplayHandler(t, root)

	cases := map[string]string{
		"absolute":            `["` + filepath.ToSlash(secretCEL) + `"]`,
		"proc environ":        `["/proc/self/environ"]`,
		"dot dot":             `["../secret.cel"]`,
		"nested dot dot":      `["nested/../../secret.cel"]`,
		"backslash dot dot":   `["nested\\..\\..\\secret.cel"]`,
		"wrong extension":     `["secret.txt"]`,
		"no extension":        `["ok"]`,
		"directory":           `["nested/dir.yaml"]`,
		"missing":             `["missing.cel"]`,
		"symlink escape":      `["escape.cel"]`,
		"symlink dir escape":  `["dirlink/secret.cel"]`,
		"one bad of many":     `["ok.cel","../secret.cel"]`,
		"empty":               `[""]`,
		"dev stdin":           `["/dev/stdin"]`,
		"prefixed dot dot":    `["policies/../secret.cel"]`,
		"policies prefix out": `["policies/../../secret.cel"]`,
	}
	for name, paths := range cases {
		t.Run(name, func(t *testing.T) {
			rec := replay(handler, paths)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "SUPERSECRET") {
				t.Fatalf("response leaked file content: %s", rec.Body.String())
			}
		})
	}
}

func TestReplayPoliciesResolvesNamesUnderPolicyRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "policies")
	if err := os.MkdirAll(filepath.Join(root, "team"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "team", "deny.cel"), []byte(`action.name == "delete"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "team", "deny.cel"), filepath.Join(root, "alias.cel")); err != nil {
			t.Fatal(err)
		}
	}
	handler := newReplayHandler(t, root)
	paths := []string{`["team/deny.cel"]`, `["policies/team/deny.cel"]`}
	if runtime.GOOS != "windows" {
		paths = append(paths, `["alias.cel"]`)
	}
	for _, p := range paths {
		rec := replay(handler, p)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"denied":1`) {
			t.Fatalf("paths %s: status = %d body = %s", p, rec.Code, rec.Body.String())
		}
	}
}

func TestReplayPoliciesDisabledWithoutRoot(t *testing.T) {
	handler := newReplayHandler(t, "")
	rec := replay(handler, `["production-delete.cel"]`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "disabled") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}
