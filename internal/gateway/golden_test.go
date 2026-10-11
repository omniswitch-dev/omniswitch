package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
	"github.com/omniswitch-dev/omniswitch/internal/provider"
	"github.com/omniswitch-dev/omniswitch/internal/router"
)

// goldenFixture is a request fixture authored from the OpenAI API reference
// (openai-python/openai-node SDK request shapes), not captured from a live
// API call. Each fixture documents the reference it was derived from and the
// date it was checked against current OpenAI docs (see verified_on).
type goldenFixture struct {
	Description string          `json:"description"`
	Source      string          `json:"source"`
	VerifiedOn  string          `json:"verified_on"`
	Endpoint    string          `json:"endpoint"` // "chat" or "embeddings"
	Request     json.RawMessage `json:"request"`
}

// TestGoldenFixturesForwardByteForByteToOpenAICompatibleUpstream is the A7
// conformance suite: it replays each testdata/golden/*.json fixture through
// the real gateway handler into a fake OpenAI-compatible upstream and
// asserts the upstream received a JSON body that is a superset of the
// fixture (every fixture key present with an equal value). This proves
// passthrough fidelity end-to-end, including unmodeled/beta fields carried
// through ChatRequest.Extra.
func TestGoldenFixturesForwardByteForByteToOpenAICompatibleUpstream(t *testing.T) {
	files, err := filepath.Glob("testdata/golden/*.json")
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no golden fixtures found under testdata/golden")
	}

	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var fixture goldenFixture
			if err := json.Unmarshal(raw, &fixture); err != nil {
				t.Fatalf("decode fixture: %v", err)
			}
			if fixture.Source == "" || fixture.VerifiedOn == "" {
				t.Fatalf("fixture %s must document source and verified_on since it is reference-derived, not captured from a live API call", file)
			}

			var wantFields map[string]any
			if err := json.Unmarshal(fixture.Request, &wantFields); err != nil {
				t.Fatalf("decode fixture request: %v", err)
			}

			var gotBody []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotBody = body

				switch {
				case strings.HasSuffix(r.URL.Path, "/embeddings"):
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"golden-embedding-model","usage":{"prompt_tokens":2,"completion_tokens":0,"total_tokens":2}}`))
				case wantFields["stream"] == true:
					w.Header().Set("Content-Type", "text/event-stream")
					chunk := `{"id":"chunk_golden","object":"chat.completion.chunk","created":1,"model":"golden-chat-model","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`
					fmt.Fprintf(w, "data: %s\n\n", chunk)
					fmt.Fprint(w, "data: [DONE]\n\n")
				default:
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"chat_golden","object":"chat.completion","created":1,"model":"golden-chat-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
				}
			}))
			defer upstream.Close()

			registry := provider.NewRegistry()
			registry.Register(provider.NewCustom("golden", upstream.URL, "test-key"))
			st := newGatewayTestStore(t)
			handler := New(registry, router.New(registry), st, guardrail.NewEngine())

			httpReq := httptest.NewRequest(http.MethodPost, goldenEndpointPath(fixture.Endpoint), bytes.NewReader(fixture.Request))
			httpReq.Header.Set("x-omniswitch-provider", "golden")
			rec := httptest.NewRecorder()

			switch fixture.Endpoint {
			case "embeddings":
				handler.Embeddings(rec, httpReq)
			case "chat":
				handler.ChatCompletions(rec, httpReq)
			default:
				t.Fatalf("unknown fixture endpoint %q", fixture.Endpoint)
			}

			if rec.Code != http.StatusOK {
				t.Fatalf("gateway status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if len(gotBody) == 0 {
				t.Fatal("upstream never received a request body")
			}

			var gotFields map[string]any
			if err := json.Unmarshal(gotBody, &gotFields); err != nil {
				t.Fatalf("decode upstream body: %v (body = %s)", err, gotBody)
			}

			assertJSONSuperset(t, gotFields, wantFields, "")
		})
	}
}

func goldenEndpointPath(endpoint string) string {
	if endpoint == "embeddings" {
		return "/v1/embeddings"
	}
	return "/v1/chat/completions"
}

// assertJSONSuperset fails the test unless every key in want is present in
// got with an equal (deep) value. got may contain additional keys not in
// want (e.g. defaults added by intermediate encoding); it must not be
// missing or alter any key that the fixture specified.
func assertJSONSuperset(t *testing.T, got, want map[string]any, path string) {
	t.Helper()
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		wantValue := want[key]
		gotValue, ok := got[key]
		fullPath := path + "." + key
		if !ok {
			t.Fatalf("upstream body missing key %q (fixture required it); got keys = %v", fullPath, got)
		}
		wantMap, wantIsMap := wantValue.(map[string]any)
		gotMap, gotIsMap := gotValue.(map[string]any)
		if wantIsMap && gotIsMap {
			assertJSONSuperset(t, gotMap, wantMap, fullPath)
			continue
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf("upstream body key %q = %#v, want %#v", fullPath, gotValue, wantValue)
		}
	}
}
