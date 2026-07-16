package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/omniswitch-dev/omniswitch/internal/explain"
	"github.com/omniswitch-dev/omniswitch/internal/model"
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  ToolCallParams  `json:"params"`
}

type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

type ToolResult struct {
	IsError bool           `json:"isError"`
	Content []ContentBlock `json:"content"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func Decode(reader io.Reader) (Request, error) {
	var req Request
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(&req); err != nil {
		return Request{}, fmt.Errorf("decode MCP JSON-RPC request: %w", err)
	}
	if req.Params.Arguments == nil {
		req.Params.Arguments = map[string]any{}
	}
	return req, nil
}

func (r Request) ValidateToolCall() error {
	if r.JSONRPC != "2.0" {
		return errors.New("jsonrpc must be 2.0")
	}
	if r.Method != "tools/call" {
		return fmt.Errorf("unsupported method %q", r.Method)
	}
	if strings.TrimSpace(r.Params.Name) == "" {
		return errors.New("params.name is required")
	}
	return nil
}

func (r Request) ToToolRequest() (model.ToolRequest, error) {
	if err := r.ValidateToolCall(); err != nil {
		return model.ToolRequest{}, err
	}

	toolName, operation := splitToolName(r.Params.Name)
	resourceName := firstString(r.Params.Arguments, "repo", "repository", "table", "database", "resource", "name")
	resourceType := firstString(r.Params.Arguments, "resource_type", "type")
	if resourceType == "" {
		resourceType = inferResourceType(operation, r.Params.Arguments)
	}

	environment := firstString(r.Params.Arguments, "environment", "env")
	if environment == "" {
		environment = inferEnvironment(resourceName)
	}

	actionName := firstString(r.Params.Arguments, "action", "operation")
	if actionName == "" {
		actionName = inferAction(operation)
	}

	metadata := map[string]string{"mcp.name": r.Params.Name}
	for key, value := range r.Params.Arguments {
		if text, ok := stringify(value); ok {
			metadata[key] = text
		}
	}

	return model.ToolRequest{
		Agent: model.Agent{
			ID:         firstString(r.Params.Arguments, "agent_id"),
			Department: firstString(r.Params.Arguments, "agent_department"),
			Role:       firstString(r.Params.Arguments, "agent_role"),
		},
		Tool: model.Tool{
			Name: toolName,
		},
		Action: model.Action{
			Name: actionName,
		},
		Resource: model.Resource{
			Type:        resourceType,
			Name:        resourceName,
			Environment: environment,
		},
		Session: model.Session{
			ID: firstString(r.Params.Arguments, "session_id"),
		},
		Metadata: metadata,
	}, nil
}

// ToToolRequestWithIdentity binds policy input to gateway-authenticated
// identity instead of caller-controlled tool arguments. Optional argument
// values still enrich the request but cannot impersonate another agent.
func (r Request) ToToolRequestWithIdentity(agent model.Agent, sessionID string) (model.ToolRequest, error) {
	request, err := r.ToToolRequest()
	if err != nil {
		return model.ToolRequest{}, err
	}
	if agent.ID != "" {
		request.Agent.ID = agent.ID
	}
	if agent.Department != "" {
		request.Agent.Department = agent.Department
	}
	if agent.Role != "" {
		request.Agent.Role = agent.Role
	}
	if sessionID != "" {
		request.Session.ID = sessionID
	}
	return request, nil
}

func DeniedResponse(id json.RawMessage, decision model.Decision) Response {
	return Response{
		JSONRPC: "2.0",
		ID:      responseID(id),
		Result: ToolResult{
			IsError: true,
			Content: []ContentBlock{
				{Type: "text", Text: explain.Inline(decision)},
			},
		},
	}
}

func ErrorResponse(id json.RawMessage, code int, message string, err error) Response {
	data := ""
	if err != nil {
		data = err.Error()
	}
	return Response{
		JSONRPC: "2.0",
		ID:      responseID(id),
		Error: &Error{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

func responseID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func splitToolName(name string) (string, string) {
	parts := strings.Split(name, ".")
	if len(parts) == 1 {
		return name, ""
	}
	tool := strings.Join(parts[:len(parts)-1], ".")
	operation := parts[len(parts)-1]
	return tool, operation
}

func inferAction(operation string) string {
	switch {
	case operation == "":
		return "call"
	case strings.HasPrefix(operation, "delete_"):
		return "delete"
	case strings.HasPrefix(operation, "remove_"):
		return "delete"
	default:
		return operation
	}
}

func inferResourceType(operation string, arguments map[string]any) string {
	switch {
	case hasAny(arguments, "repo", "repository") || strings.HasSuffix(operation, "_repo"):
		return "repository"
	case hasAny(arguments, "table") || strings.HasSuffix(operation, "_table"):
		return "table"
	case hasAny(arguments, "database", "db") || strings.HasSuffix(operation, "_database"):
		return "database"
	default:
		return "unknown"
	}
}

func inferEnvironment(resourceName string) string {
	normalized := strings.ToLower(resourceName)
	switch {
	case strings.Contains(normalized, "production"), strings.Contains(normalized, "prod"):
		return "production"
	case strings.Contains(normalized, "staging"), strings.Contains(normalized, "stage"):
		return "staging"
	case strings.Contains(normalized, "development"), strings.Contains(normalized, "dev"):
		return "development"
	default:
		return "unknown"
	}
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			if text, ok := stringify(value); ok {
				return text
			}
		}
	}
	return ""
}

func stringify(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(typed), true
	default:
		return "", false
	}
}

func hasAny(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := values[key]; ok {
			return true
		}
	}
	return false
}
