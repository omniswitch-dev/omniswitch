package provider

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestBedrockSigV4MatchesAWSSDK pins Bedrock request signatures to values
// produced by the official aws-sdk-go-v2 v4 signer (v1.47.3,
// signer.SignHTTP) for the same credentials, time, headers and payload.
// The expected signatures were generated once in a scratch module outside
// this repository; the SDK is intentionally not a dependency. Model IDs with
// a ':' version suffix and inference-profile ARNs (':' and an escaped '/')
// exercise the double URI-encoding SigV4 requires for non-S3 services.
func TestBedrockSigV4MatchesAWSSDK(t *testing.T) {
	now := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	body := []byte(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`)
	cases := []struct {
		model         string
		token         string
		signedHeaders string
		signature     string
	}{
		{"amazon.nova-pro-v1:0", "", "content-type;host;x-amz-date", "08b201497b383d641ccf214dc5bfc2c773a43bbf4c6d08cf0cf184171e52abc5"},
		{"anthropic.claude-3-5-sonnet-20241022-v2:0", "", "content-type;host;x-amz-date", "0d42bf671c14f9e8128187a6cdc6b7a0f5b61283cabfd454cb43f73cdc38b8f2"},
		{"us.anthropic.claude-sonnet-4-20250514-v1:0", "SESSIONTOKEN", "content-type;host;x-amz-date;x-amz-security-token", "c9374ba6c6f668d7194bcd735e470ddbf7a141f8b43873359931bb7fad71d83c"},
		{"arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude-3-7-sonnet-20250219-v1:0", "", "content-type;host;x-amz-date", "53f1fd777534c87b4d85f978aa37650367fd0b3294ef900e89c2a70314f78f7f"},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			b := NewBedrock("bedrock", "us-east-1", "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", tc.token, nil)
			req, err := http.NewRequest(http.MethodPost, b.converseURL(tc.model), nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			if err := b.sign(req, body, now); err != nil {
				t.Fatal(err)
			}
			want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20250102/us-east-1/bedrock/aws4_request, SignedHeaders=" + tc.signedHeaders + ", Signature=" + tc.signature
			if got := req.Header.Get("Authorization"); got != want {
				t.Fatalf("Authorization =\n  %s\nwant\n  %s", got, want)
			}
		})
	}
}

func TestCanonicalAWSURIDoubleEncodes(t *testing.T) {
	u, err := url.Parse("https://h/model/" + url.PathEscape("arn:aws:bedrock:us-east-1:1:inference-profile/x.y-v1:0") + "/converse")
	if err != nil {
		t.Fatal(err)
	}
	got := canonicalAWSURI(u)
	want := "/model/arn%3Aaws%3Abedrock%3Aus-east-1%3A1%3Ainference-profile%252Fx.y-v1%3A0/converse"
	if got != want {
		t.Fatalf("canonicalAWSURI = %s, want %s", got, want)
	}
	if !strings.HasPrefix(canonicalAWSURI(&url.URL{}), "/") {
		t.Fatal("empty path must canonicalize to /")
	}
}

func TestConverseRequestMergesParallelToolResults(t *testing.T) {
	b := NewBedrock("bedrock", "us-east-1", "AKID", "SECRET", "", nil)
	out, err := b.toConverseRequest(ChatRequest{Model: "amazon.nova-pro-v1:0", Messages: []Message{
		{Role: "system", Content: "sys"},
		{Role: "developer", Content: "dev"},
		{Role: "user", Content: "weather?"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "a", Type: "function", Function: FunctionCall{Name: "w", Arguments: `{"c":"x"}`}},
			{ID: "b", Type: "function", Function: FunctionCall{Name: "w", Arguments: `{"c":"y"}`}},
		}},
		{Role: "tool", ToolCallID: "a", Content: "1"},
		{Role: "tool", ToolCallID: "b", Content: "2"},
		{Role: "user", Content: "thanks"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.System) != 2 {
		t.Fatalf("system blocks = %d, want 2", len(out.System))
	}
	roles := make([]string, len(out.Messages))
	for i, m := range out.Messages {
		roles[i] = m.Role
	}
	if len(out.Messages) != 3 || roles[0] != "user" || roles[1] != "assistant" || roles[2] != "user" {
		t.Fatalf("roles = %v, want strictly alternating [user assistant user]", roles)
	}
	results := 0
	for _, block := range out.Messages[2].Content {
		if block.ToolResult != nil {
			results++
		}
	}
	if results != 2 || len(out.Messages[2].Content) != 3 {
		t.Fatalf("final user turn = %+v, want 2 toolResult blocks + text", out.Messages[2].Content)
	}
}
