package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// a2aTask tracks one A2A v1 task through its lifecycle. Tasks are held
// in-process; they document execution state and support retrieval and best-
// effort cancellation after completion of the governing request.
type a2aTask struct {
	ID        string           `json:"id"`
	ContextID string           `json:"contextId,omitempty"`
	Status    a2aTaskStatus    `json:"status"`
	Artifacts []a2aArtifact    `json:"artifacts,omitempty"`
	History   []map[string]any `json:"history,omitempty"`
	CreatedAt time.Time        `json:"-"`
}

type a2aTaskStatus struct {
	State     string         `json:"state"`
	Timestamp string         `json:"timestamp,omitempty"`
	Message   map[string]any `json:"message,omitempty"`
}

type a2aArtifact struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Parts       []map[string]any  `json:"parts"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

const (
	a2aStateWorking   = "working"
	a2aStateCompleted = "completed"
	a2aStateCanceled  = "canceled"
	a2aStateFailed    = "failed"
)

// a2aTTL bounds memory usage for finished tasks.
const a2aTaskTTL = time.Hour

var a2aTasks sync.Map // task id -> *a2aTask

func newA2ATaskID() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return "task_" + hex.EncodeToString(buf)
}

func getA2ATask(id string) (*a2aTask, bool) {
	value, ok := a2aTasks.Load(strings.TrimSpace(id))
	if !ok {
		return nil, false
	}
	task, ok := value.(*a2aTask)
	return task, ok
}

func pruneA2ATasks() {
	cutoff := time.Now().Add(-a2aTaskTTL)
	a2aTasks.Range(func(key, value any) bool {
		if task, ok := value.(*a2aTask); ok && task.CreatedAt.Before(cutoff) {
			a2aTasks.Delete(key)
		}
		return true
	})
}

func setState(task *a2aTask, state string) {
	task.Status.State = state
	task.Status.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
}

func a2aTaskNotFoundError(id string) string {
	return fmt.Sprintf("task %q not found", id)
}
