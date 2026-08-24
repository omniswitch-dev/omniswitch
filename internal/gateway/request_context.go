package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/store"
)

const (
	userIDContextKey       contextKey = "user_id"
	metadataJSONContextKey contextKey = "metadata_json"
	tagsContextKey         contextKey = "tags"
	traceRecorderKey       contextKey = "trace_recorder"
)

func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDContextKey, strings.TrimSpace(id))
}

func UserIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(userIDContextKey).(string)
	return value
}

func WithMetadataJSON(ctx context.Context, metadata string) context.Context {
	return context.WithValue(ctx, metadataJSONContextKey, normalizedMetadataJSON(metadata))
}

func MetadataJSONFromContext(ctx context.Context) string {
	value, _ := ctx.Value(metadataJSONContextKey).(string)
	if strings.TrimSpace(value) == "" {
		return "{}"
	}
	return value
}

func WithTags(ctx context.Context, tags []string) context.Context {
	return context.WithValue(ctx, tagsContextKey, normalizedTags(tags))
}

func TagsFromContext(ctx context.Context) []string {
	tags, _ := ctx.Value(tagsContextKey).([]string)
	return append([]string(nil), tags...)
}

func tagsHeader(value string) []string {
	var tags []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			tags = append(tags, part)
		}
	}
	return tags
}

func normalizedTags(tags []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	return out
}

func normalizedMetadataJSON(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "{}"
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		fallback, _ := json.Marshal(map[string]any{"_invalid_metadata": value})
		return string(fallback)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

type traceRecorder struct {
	mu    sync.Mutex
	spans []store.TraceSpan
}

func newTraceRecorder() *traceRecorder {
	return &traceRecorder{}
}

func WithTraceRecorder(ctx context.Context, recorder *traceRecorder) context.Context {
	return context.WithValue(ctx, traceRecorderKey, recorder)
}

func TraceRecorderFromContext(ctx context.Context) *traceRecorder {
	recorder, _ := ctx.Value(traceRecorderKey).(*traceRecorder)
	return recorder
}

func recordTraceSpan(ctx context.Context, name, providerName, status string, start time.Time, metadata map[string]any) {
	recorder := TraceRecorderFromContext(ctx)
	if recorder == nil || name == "" {
		return
	}
	if status == "" {
		status = "ok"
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.spans = append(recorder.spans, store.TraceSpan{
		Name:       name,
		Provider:   providerName,
		Start:      start.UTC(),
		DurationMs: float64(time.Since(start).Microseconds()) / 1000,
		Status:     status,
		Metadata:   metadata,
	})
}

func spansFromContext(ctx context.Context) []store.TraceSpan {
	recorder := TraceRecorderFromContext(ctx)
	if recorder == nil {
		return nil
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]store.TraceSpan(nil), recorder.spans...)
}

func metadataWithSpans(metadataJSON string, spans []store.TraceSpan) string {
	var metadata map[string]any
	if err := json.Unmarshal([]byte(normalizedMetadataJSON(metadataJSON)), &metadata); err != nil {
		metadata = map[string]any{}
	}
	if len(spans) > 0 {
		metadata["_spans"] = spans
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return normalizedMetadataJSON(metadataJSON)
	}
	return string(encoded)
}
