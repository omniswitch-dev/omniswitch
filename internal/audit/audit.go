package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/model"
)

type Event struct {
	Timestamp  time.Time     `json:"timestamp"`
	DecisionID string        `json:"decision_id"`
	AgentID    string        `json:"agent_id"`
	Tool       string        `json:"tool"`
	Action     string        `json:"action"`
	Resource   string        `json:"resource"`
	Decision   string        `json:"decision"`
	Rule       string        `json:"rule"`
	Reason     string        `json:"reason"`
	Latency    time.Duration `json:"latency"`
	// Method is the MCP JSON-RPC method (for example "tools/call").
	Method string `json:"method,omitempty"`
	// Target is the MCP target that received (or would have received) the call.
	Target string `json:"target,omitempty"`
	// KeyID and Workspace identify the authenticated caller.
	KeyID     string `json:"key_id,omitempty"`
	Workspace string `json:"workspace_id,omitempty"`
	Session   string `json:"session_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
}

type Logger interface {
	Log(ctx context.Context, event Event) error
}

type StdoutLogger struct {
	mu     sync.Mutex
	writer io.Writer
}

func NewStdoutLogger(writer io.Writer) *StdoutLogger {
	return &StdoutLogger{writer: writer}
}

func NewEvent(req model.ToolRequest, decision model.Decision) Event {
	return Event{
		Timestamp:  time.Now().UTC(),
		DecisionID: decision.ID,
		AgentID:    req.Agent.ID,
		Tool:       req.Tool.Name,
		Action:     req.Action.Name,
		Resource:   req.Resource.Name,
		Decision:   decision.Status(),
		Rule:       decision.Rule,
		Reason:     decision.Reason,
		Latency:    decision.EvaluationTime,
		Target:     req.Target.Name,
		KeyID:      req.Agent.ID,
		Session:    req.Session.ID,
	}
}

func (l *StdoutLogger) Log(ctx context.Context, event Event) error {
	if l == nil || l.writer == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal audit event: %w", err)
	}
	if _, err := fmt.Fprintln(l.writer, string(payload)); err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}
