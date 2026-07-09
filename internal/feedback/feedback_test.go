package feedback

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sentinel/internal/store"
)

func TestCreateAndListFeedback(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	handler := New(st)
	createReq := httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(`{
		"trace_id":"trace_1",
		"score":1,
		"rating":"up",
		"comment":"good answer",
		"user_id":"user_1",
		"metadata":{"screen":"chat"}
	}`))
	createRec := httptest.NewRecorder()
	handler.CreateFeedback(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("CreateFeedback() status = %d, body = %s", createRec.Code, createRec.Body.String())
	}

	listRec := httptest.NewRecorder()
	handler.ListFeedback(listRec, httptest.NewRequest(http.MethodGet, "/api/feedback?trace_id=trace_1", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("ListFeedback() status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var payload struct {
		Feedback []store.Feedback `json:"feedback"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(payload.Feedback) != 1 || payload.Feedback[0].Score != 1 || payload.Feedback[0].Metadata["screen"] != "chat" {
		t.Fatalf("feedback = %+v, want stored entry", payload.Feedback)
	}
}

func TestCreateFeedbackRequiresRequestOrTrace(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rec := httptest.NewRecorder()
	New(st).CreateFeedback(rec, httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(`{"score":1}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "request_id or trace_id") {
		t.Fatalf("status/body = %d/%s, want validation error", rec.Code, rec.Body.String())
	}
}
