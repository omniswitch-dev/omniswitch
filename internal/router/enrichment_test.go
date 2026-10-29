package router

import (
	"reflect"
	"testing"

	"github.com/omniswitch-dev/omniswitch/internal/provider"
)

func TestPromptEnrichmentOrderAndIsolation(t *testing.T) {
	rtr := New(provider.NewRegistry())
	prepend := []provider.Message{{Role: "system", Content: "operator instructions"}}
	appendMessages := []provider.Message{{Role: "user", Content: "answer concisely"}}
	rtr.SetRoute("logical", Route{Provider: "test", PrependMessages: prepend, AppendMessages: appendMessages, DefaultParams: map[string]any{"temperature": 0.2}})
	input := provider.ChatRequest{Model: "logical", Messages: []provider.Message{{Role: "user", Content: "original question"}}}
	want := []provider.Message{prepend[0], input.Messages[0], appendMessages[0]}
	first, err := rtr.TransformRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Messages, want) || first.Temperature == nil || *first.Temperature != 0.2 {
		t.Fatalf("transformed = %+v", first)
	}
	first.Messages[0].Content = "mutated"
	first.Messages[1].Content = "mutated"
	second, err := rtr.TransformRequest(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second.Messages, want) || len(input.Messages) != 1 || prepend[0].Content != "operator instructions" {
		t.Fatalf("enrichment leaked across requests: %+v", second.Messages)
	}
	rtr.ReplaceRoutes(map[string]Route{"logical": {Provider: "test"}})
	plain, err := rtr.TransformRequest(input)
	if err != nil || !reflect.DeepEqual(plain.Messages, input.Messages) {
		t.Fatalf("removed enrichment still applied: %+v/%v", plain, err)
	}
}
