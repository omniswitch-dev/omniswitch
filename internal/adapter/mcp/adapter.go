package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/omniswitch-dev/omniswitch/internal/explain"
	"github.com/omniswitch-dev/omniswitch/internal/model"
)

// Request is one MCP JSON-RPC message, parsed with exact (case-sensitive)
// member lookup. Parse rejects members that differ from a protocol member
// name only in case, so exact lookup never misses a member that a
// case-insensitive upstream decoder would read. Fields and ParamFields keep every member byte-for-byte so a
// forwarded request carries exactly the values the policy evaluated.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  ToolCallParams  `json:"params"`

	// HasID reports whether the message carried an "id" member. Messages
	// without one are notifications.
	HasID bool `json:"-"`
	// Fields holds every top-level member exactly as received.
	Fields map[string]json.RawMessage `json:"-"`
	// ParamFields holds the members of params when params is an object.
	ParamFields map[string]json.RawMessage `json:"-"`
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

// ParseError is a JSON-RPC protocol error detected while parsing a request.
// Code is -32700 (invalid JSON), -32600 (invalid request) or -32602 (invalid
// params). ID is the request id when it could be determined.
type ParseError struct {
	Code    int
	Message string
	ID      json.RawMessage
	Err     error
}

func (e *ParseError) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return e.Message + ": " + e.Err.Error()
}

func (e *ParseError) Unwrap() error { return e.Err }

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Decode reads and parses one JSON-RPC request. See Parse.
func Decode(reader io.Reader) (Request, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return Request{}, &ParseError{Code: CodeParseError, Message: "Parse error", Err: err}
	}
	return Parse(body)
}

// Parse validates and decodes one JSON-RPC message. It rejects batches and
// any object (at any depth) that has two members whose names are equal under
// case folding, because encoding/json and many upstream decoders resolve such
// duplicates differently, which would let the policy see different data than
// the upstream server.
func Parse(body []byte) (Request, error) {
	if !json.Valid(body) {
		return Request{}, &ParseError{Code: CodeParseError, Message: "Parse error", Err: errors.New("invalid JSON")}
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		if len(trimmed) > 0 && trimmed[0] == '[' {
			return Request{}, invalidRequest(nil, errors.New("batch requests are not supported"))
		}
		return Request{}, invalidRequest(nil, errors.New("request must be a JSON object"))
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return Request{}, invalidRequest(nil, err)
	}
	req := Request{Fields: fields}
	if rawID, ok := fields["id"]; ok {
		rawID = bytes.TrimSpace(rawID)
		switch {
		case len(rawID) == 0:
		case rawID[0] == '"', rawID[0] == '-', rawID[0] >= '0' && rawID[0] <= '9', string(rawID) == "null":
			req.ID = append(json.RawMessage(nil), rawID...)
			req.HasID = true
		default:
			return Request{}, invalidRequest(nil, errors.New("id must be a string, number or null"))
		}
	}
	if err := checkDuplicateKeys(trimmed); err != nil {
		return Request{}, invalidRequest(req.ID, err)
	}
	if err := checkReservedNames(fields, topLevelMembers, ""); err != nil {
		return Request{}, invalidRequest(req.ID, err)
	}
	if err := decodeString(fields, "jsonrpc", &req.JSONRPC); err != nil || req.JSONRPC != "2.0" {
		return Request{}, invalidRequest(req.ID, errors.New(`jsonrpc must be "2.0"`))
	}
	if _, ok := fields["method"]; ok {
		if err := decodeString(fields, "method", &req.Method); err != nil || req.Method == "" {
			return Request{}, invalidRequest(req.ID, errors.New("method must be a non-empty string"))
		}
	} else {
		// A client may POST a JSON-RPC response to a server-initiated request.
		_, hasResult := fields["result"]
		_, hasError := fields["error"]
		if !req.HasID || (!hasResult && !hasError) {
			return Request{}, invalidRequest(req.ID, errors.New("method is required"))
		}
	}

	rawParams, hasParams := fields["params"]
	rawParams = bytes.TrimSpace(rawParams)
	if hasParams && len(rawParams) > 0 && rawParams[0] == '{' {
		if err := json.Unmarshal(rawParams, &req.ParamFields); err != nil {
			return Request{}, invalidParams(req.ID, err)
		}
		if err := checkReservedNames(req.ParamFields, paramMembers, "params."); err != nil {
			return Request{}, invalidRequest(req.ID, err)
		}
	}
	if req.Method == "tools/call" {
		if req.ParamFields == nil {
			return Request{}, invalidParams(req.ID, errors.New("params must be an object"))
		}
		if err := decodeString(req.ParamFields, "name", &req.Params.Name); err != nil {
			return Request{}, invalidParams(req.ID, errors.New("params.name must be a string"))
		}
		if strings.TrimSpace(req.Params.Name) == "" {
			return Request{}, invalidParams(req.ID, errors.New("params.name is required"))
		}
		if rawArgs, ok := req.ParamFields["arguments"]; ok && string(bytes.TrimSpace(rawArgs)) != "null" {
			rawArgs = bytes.TrimSpace(rawArgs)
			if len(rawArgs) == 0 || rawArgs[0] != '{' {
				return Request{}, invalidParams(req.ID, errors.New("params.arguments must be an object"))
			}
			decoder := json.NewDecoder(bytes.NewReader(rawArgs))
			decoder.UseNumber()
			if err := decoder.Decode(&req.Params.Arguments); err != nil {
				return Request{}, invalidParams(req.ID, err)
			}
		}
	}
	if req.Params.Arguments == nil {
		req.Params.Arguments = map[string]any{}
	}
	return req, nil
}

func invalidRequest(id json.RawMessage, err error) *ParseError {
	return &ParseError{Code: CodeInvalidRequest, Message: "Invalid Request", ID: id, Err: err}
}

func invalidParams(id json.RawMessage, err error) *ParseError {
	return &ParseError{Code: CodeInvalidParams, Message: "Invalid params", ID: id, Err: err}
}

func decodeString(fields map[string]json.RawMessage, key string, out *string) error {
	raw, ok := fields[key]
	if !ok {
		return fmt.Errorf("%s is required", key)
	}
	return json.Unmarshal(raw, out)
}

// topLevelMembers and paramMembers are the protocol member names the gateway
// reads by exact name. A member that differs from one of them only in case
// is rejected: the gateway would not see it, but upstream decoders that match
// names case-insensitively (Go encoding/json, for example) would, so the
// policy and the upstream server would act on different data.
var (
	topLevelMembers = []string{"jsonrpc", "id", "method", "params", "result", "error"}
	paramMembers    = []string{"name", "arguments", "_meta", "requestId", "cursor", "uri"}
)

func checkReservedNames(fields map[string]json.RawMessage, reserved []string, prefix string) error {
	for key := range fields {
		folded := foldName(key)
		for _, name := range reserved {
			if key != name && folded == foldName(name) {
				return fmt.Errorf("member %q must be spelled %q", prefix+key, prefix+name)
			}
		}
	}
	return nil
}

// foldName returns the case-folded form of a member name. It matches the
// folding used by checkDuplicateKeys.
func foldName(name string) string {
	return strings.ToLower(strings.ToUpper(name))
}

// checkDuplicateKeys walks the whole document and fails when an object has
// two member names that are equal under Unicode case folding.
func checkDuplicateKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	return walkJSON(decoder)
}

func walkJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]string{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, _ := keyToken.(string)
			folded := foldName(key)
			if previous, exists := seen[folded]; exists {
				return fmt.Errorf("duplicate member names %q and %q", previous, key)
			}
			seen[folded] = key
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder); err != nil {
				return err
			}
		}
	}
	_, err = decoder.Token() // closing delimiter
	return err
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

// ToolContext carries the trusted attributes of a tool call: the identity
// the gateway authenticated, the routing target, operator-configured target
// metadata, and (for OpenAPI-derived tools) the HTTP method.
type ToolContext struct {
	Agent          model.Agent
	SessionID      string
	Target         string
	TargetMetadata map[string]string
	HTTPMethod     string
}

// ToToolRequest builds policy input without a trusted identity. The agent
// fields stay empty: identity never comes from tool arguments.
func (r Request) ToToolRequest() (model.ToolRequest, error) {
	return r.ToToolRequestWithContext(ToolContext{})
}

// ToToolRequestWithIdentity binds policy input to the gateway-authenticated
// identity. Tool arguments can never set or override agent fields.
func (r Request) ToToolRequestWithIdentity(agent model.Agent, sessionID string) (model.ToolRequest, error) {
	return r.ToToolRequestWithContext(ToolContext{Agent: agent, SessionID: sessionID})
}

// ToToolRequestWithContext validates a tools/call request and builds policy
// input from trusted context plus the tool name and arguments.
func (r Request) ToToolRequestWithContext(tc ToolContext) (model.ToolRequest, error) {
	if err := r.ValidateToolCall(); err != nil {
		return model.ToolRequest{}, err
	}
	return BuildToolRequest(r.Params.Name, r.Params.Arguments, tc), nil
}

// BuildToolRequest derives policy attributes. Values inferred from the tool
// name, the target and operator metadata win over caller-supplied arguments:
// arguments can only make a request look more sensitive (for example mark it
// production or destructive), never less. The raw arguments are kept in Args
// so policies can match them explicitly.
func BuildToolRequest(name string, arguments map[string]any, tc ToolContext) model.ToolRequest {
	if arguments == nil {
		arguments = map[string]any{}
	}
	toolName, operation, dotted := splitToolName(name)

	actionName := inferAction(operation, dotted, tc.HTTPMethod)
	if actionName != "delete" && isDestructiveVerb(firstString(arguments, "action", "operation")) {
		actionName = "delete"
	}

	resourceName := firstString(arguments, "repo", "repository", "table", "database", "resource", "name")
	resourceType := strings.TrimSpace(tc.TargetMetadata["resource_type"])
	if resourceType == "" {
		resourceType = inferResourceType(operation, arguments)
	}
	if resourceType == "unknown" {
		if supplied := firstString(arguments, "resource_type", "type"); supplied != "" {
			resourceType = supplied
		}
	}

	environment := resolveEnvironment(
		normalizeEnvironment(tc.TargetMetadata["environment"]),
		inferEnvironment(resourceName),
		inferEnvironment(tc.Target),
		normalizeEnvironment(firstString(arguments, "environment", "env")),
	)

	metadata := make(map[string]string, len(tc.TargetMetadata)+2)
	for key, value := range tc.TargetMetadata {
		metadata[key] = value
	}
	metadata["mcp.name"] = name
	if tc.Target != "" {
		metadata["mcp.target"] = tc.Target
	}

	return model.ToolRequest{
		Agent:    tc.Agent,
		Tool:     model.Tool{Name: toolName, FullName: name},
		Action:   model.Action{Name: actionName, Operation: operation},
		Resource: model.Resource{Type: resourceType, Name: resourceName, Environment: environment},
		Session:  model.Session{ID: tc.SessionID},
		Target:   model.Target{Name: tc.Target},
		Metadata: metadata,
		Args:     arguments,
	}
}

// resolveEnvironment returns "production" when any signal says so, and
// otherwise the first known value in precedence order.
func resolveEnvironment(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate == "production" {
			return candidate
		}
	}
	for _, candidate := range candidates {
		if candidate != "" && candidate != "unknown" {
			return candidate
		}
	}
	return "unknown"
}

func normalizeEnvironment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if inferred := inferEnvironment(value); inferred != "unknown" {
		return inferred
	}
	return strings.ToLower(value)
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

// splitToolName splits "tool.operation" names. Bare names (the common case
// for real MCP servers, for example "delete_repo") use the whole name as both
// the tool and the operation.
func splitToolName(name string) (string, string, bool) {
	index := strings.LastIndex(name, ".")
	if index <= 0 || index == len(name)-1 {
		return name, name, false
	}
	return name[:index], name[index+1:], true
}

var destructiveVerbs = map[string]bool{
	"delete": true, "del": true, "remove": true, "rm": true, "drop": true,
	"destroy": true, "purge": true, "truncate": true, "erase": true,
	"wipe": true, "terminate": true,
}

// inferAction normalizes an operation into an action name. Destructive verbs
// (delete_, remove_, drop_, destroy_, deleteRepo, ...) map to "delete". For
// OpenAPI tools the HTTP method decides otherwise.
func inferAction(operation string, dotted bool, httpMethod string) string {
	if isDestructiveVerb(operation) {
		return "delete"
	}
	switch strings.ToUpper(strings.TrimSpace(httpMethod)) {
	case "DELETE":
		return "delete"
	case "GET", "HEAD":
		return "read"
	case "POST":
		return "create"
	case "PUT", "PATCH":
		return "update"
	}
	if !dotted || operation == "" {
		return "call"
	}
	return operation
}

// isDestructiveVerb reports whether the first word of an identifier
// (snake_case, kebab-case or camelCase) is a destructive verb.
func isDestructiveVerb(identifier string) bool {
	return destructiveVerbs[firstWord(identifier)]
}

func firstWord(identifier string) string {
	identifier = strings.TrimSpace(identifier)
	var word strings.Builder
	for index, char := range identifier {
		if char == '_' || char == '-' || char == ' ' || char == '.' || char == '/' {
			if word.Len() > 0 {
				break
			}
			continue
		}
		if index > 0 && unicode.IsUpper(char) && word.Len() > 0 {
			break
		}
		word.WriteRune(unicode.ToLower(char))
	}
	return word.String()
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

// lookupFolded returns the argument whose name equals key under case
// folding. Parse rejects arguments with two names that fold to the same
// value, so the match is unambiguous; matching like a case-insensitive
// upstream decoder keeps the policy from missing a value the upstream uses.
func lookupFolded(values map[string]any, key string) (any, bool) {
	if value, ok := values[key]; ok {
		return value, true
	}
	folded := foldName(key)
	for name, value := range values {
		if foldName(name) == folded {
			return value, true
		}
	}
	return nil, false
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := lookupFolded(values, key); ok {
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
	case json.Number:
		return typed.String(), true
	case bool:
		return strconv.FormatBool(typed), true
	default:
		return "", false
	}
}

func hasAny(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := lookupFolded(values, key); ok {
			return true
		}
	}
	return false
}
