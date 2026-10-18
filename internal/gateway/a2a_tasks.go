package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// a2aTask tracks one A2A v1 task through its lifecycle. Tasks are held
// in-process (they are lost on restart and not shared across replicas) and
// are visible only to the API key and workspace that created them. All
// mutable fields are guarded by mu; readers get snapshots.
type a2aTask struct {
	mu sync.Mutex

	id          string
	contextID   string
	createdAt   time.Time
	ownerKeyID  string
	workspaceID string

	status    a2aTaskStatus
	artifacts []a2aArtifact
	history   []map[string]any
	// cancel stops the inference that drives the task.
	cancel context.CancelFunc
}

type a2aTaskStatus struct {
	State     string         `json:"state"`
	Timestamp string         `json:"timestamp,omitempty"`
	Message   map[string]any `json:"message,omitempty"`
}

type a2aArtifact struct {
	ArtifactID  string            `json:"artifactId,omitempty"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Parts       []map[string]any  `json:"parts"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// a2aTaskView is an immutable snapshot of a task for responses.
type a2aTaskView struct {
	ID        string           `json:"id"`
	ContextID string           `json:"contextId,omitempty"`
	Kind      string           `json:"kind"`
	Status    a2aTaskStatus    `json:"status"`
	Artifacts []a2aArtifact    `json:"artifacts,omitempty"`
	History   []map[string]any `json:"history,omitempty"`
}

const (
	a2aStateWorking   = "working"
	a2aStateCompleted = "completed"
	a2aStateCanceled  = "canceled"
	a2aStateFailed    = "failed"
)

const (
	// a2aTaskTTL bounds how long tasks are retained.
	a2aTaskTTL = time.Hour
	// a2aMaxTasks caps the number of retained tasks.
	a2aMaxTasks = 10000
	// a2aPruneInterval rate-limits the expiry sweep.
	a2aPruneInterval = time.Minute
)

func newA2ATaskID() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return "task_" + hex.EncodeToString(buf)
}

func newA2ATask(contextID, ownerKeyID, workspaceID string, cancel context.CancelFunc) *a2aTask {
	now := time.Now().UTC()
	return &a2aTask{
		id:          newA2ATaskID(),
		contextID:   contextID,
		createdAt:   now,
		ownerKeyID:  ownerKeyID,
		workspaceID: workspaceID,
		status:      a2aTaskStatus{State: a2aStateWorking, Timestamp: now.Format(time.RFC3339Nano)},
		cancel:      cancel,
	}
}

func isTerminalA2AState(state string) bool {
	switch state {
	case a2aStateCompleted, a2aStateCanceled, a2aStateFailed:
		return true
	}
	return false
}

func (t *a2aTask) setStateLocked(state string) {
	t.status.State = state
	t.status.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
}

func (t *a2aTask) snapshotLocked() a2aTaskView {
	view := a2aTaskView{ID: t.id, ContextID: t.contextID, Kind: "task", Status: t.status}
	view.Artifacts = append([]a2aArtifact(nil), t.artifacts...)
	view.History = append([]map[string]any(nil), t.history...)
	return view
}

func (t *a2aTask) snapshot() a2aTaskView {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked()
}

func (t *a2aTask) ownedBy(keyID, workspaceID string) bool {
	return t.ownerKeyID == keyID && t.workspaceID == workspaceID
}

// requestCancel moves a working task to canceled and stops its inference.
// It reports false (with the current snapshot) for terminal tasks.
func (t *a2aTask) requestCancel() (a2aTaskView, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if isTerminalA2AState(t.status.State) {
		return t.snapshotLocked(), false
	}
	t.setStateLocked(a2aStateCanceled)
	if t.cancel != nil {
		t.cancel()
	}
	return t.snapshotLocked(), true
}

// complete records the agent's answer. A task canceled meanwhile stays
// canceled and the answer is discarded.
func (t *a2aTask) complete(message map[string]any, artifact a2aArtifact) a2aTaskView {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.State == a2aStateWorking {
		t.history = append(t.history, message)
		t.artifacts = append(t.artifacts, artifact)
		t.setStateLocked(a2aStateCompleted)
	}
	t.cancel = nil
	return t.snapshotLocked()
}

// fail records a failure unless the task was canceled meanwhile.
func (t *a2aTask) fail(text string) a2aTaskView {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status.State == a2aStateWorking {
		t.setStateLocked(a2aStateFailed)
		t.status.Message = map[string]any{
			"role": "ROLE_AGENT", "parts": []map[string]string{{"text": text}},
		}
	}
	t.cancel = nil
	return t.snapshotLocked()
}

func (t *a2aTask) terminal() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return isTerminalA2AState(t.status.State)
}

// a2aTaskStore is a bounded, TTL-expiring task registry.
type a2aTaskStore struct {
	mu        sync.Mutex
	tasks     map[string]*a2aTask
	lastPrune time.Time
	max       int
	ttl       time.Duration
}

func newA2ATaskStore(max int, ttl time.Duration) *a2aTaskStore {
	return &a2aTaskStore{tasks: map[string]*a2aTask{}, max: max, ttl: ttl}
}

var a2aTasks = newA2ATaskStore(a2aMaxTasks, a2aTaskTTL)

func (s *a2aTaskStore) put(task *a2aTask) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.lastPrune) >= a2aPruneInterval || len(s.tasks) >= s.max {
		s.pruneLocked(now)
	}
	for len(s.tasks) >= s.max {
		s.evictOldestLocked()
	}
	s.tasks[task.id] = task
}

func (s *a2aTaskStore) pruneLocked(now time.Time) {
	s.lastPrune = now
	cutoff := now.Add(-s.ttl)
	for id, task := range s.tasks {
		if task.createdAt.Before(cutoff) {
			delete(s.tasks, id)
		}
	}
}

// evictOldestLocked drops the oldest terminal task, or the oldest task when
// every retained task is still working.
func (s *a2aTaskStore) evictOldestLocked() {
	var oldestID, oldestTerminalID string
	var oldest, oldestTerminal time.Time
	for id, task := range s.tasks {
		if oldestID == "" || task.createdAt.Before(oldest) {
			oldestID, oldest = id, task.createdAt
		}
		if task.terminal() && (oldestTerminalID == "" || task.createdAt.Before(oldestTerminal)) {
			oldestTerminalID, oldestTerminal = id, task.createdAt
		}
	}
	if oldestTerminalID != "" {
		delete(s.tasks, oldestTerminalID)
		return
	}
	delete(s.tasks, oldestID)
}

// get returns the task only to the key and workspace that created it.
func (s *a2aTaskStore) get(id, keyID, workspaceID string) (*a2aTask, bool) {
	s.mu.Lock()
	task, ok := s.tasks[strings.TrimSpace(id)]
	s.mu.Unlock()
	if !ok || !task.ownedBy(keyID, workspaceID) {
		return nil, false
	}
	if time.Since(task.createdAt) > s.ttl {
		return nil, false
	}
	return task, true
}

func (s *a2aTaskStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tasks)
}

func a2aTaskNotFoundError(id string) string {
	return fmt.Sprintf("task %q not found", id)
}
