package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const (
	openAPIFetchTimeout = 30 * time.Second
	openAPIMaxSpecBytes = 10 << 20
	openAPIMaxRefDepth  = 32
)

// openAPIParam records where an operation expects one argument.
type openAPIParam struct {
	Name     string
	In       string
	Required bool
}

// openAPITool is an MCP tool synthesized from an OpenAPI operation.
type openAPITool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
	Method      string         `json:"-"`
	Path        string         `json:"-"`
	Params      []openAPIParam `json:"-"`
	HasBody     bool           `json:"-"`
}

// loadOpenAPISpec reads an OpenAPI 3.x document (YAML or JSON) from a local
// path or URL and synthesizes MCP tools from its operations.
func loadOpenAPISpec(location string) ([]openAPITool, error) {
	var (
		raw []byte
		err error
	)
	if strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
		raw, err = fetchOpenAPISpec(location)
	} else {
		raw, err = os.ReadFile(location)
	}
	if err != nil {
		return nil, err
	}
	return parseOpenAPISpec(raw)
}

func fetchOpenAPISpec(location string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), openAPIFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch OpenAPI spec: %w", err)
	}
	resp, err := (&http.Client{Timeout: openAPIFetchTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch OpenAPI spec: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch OpenAPI spec: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, openAPIMaxSpecBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch OpenAPI spec: %w", err)
	}
	if len(raw) > openAPIMaxSpecBytes {
		return nil, fmt.Errorf("fetch OpenAPI spec: document exceeds %d bytes", openAPIMaxSpecBytes)
	}
	return raw, nil
}

var openAPIMethods = []string{"get", "put", "post", "delete", "patch"}

// parseOpenAPISpec accepts YAML or JSON (JSON is valid YAML). Local $ref
// pointers ("#/components/...") are resolved for path items, parameters,
// request bodies and schemas; path-level parameters are merged into each
// operation. Tools are sorted by name and duplicate names are rejected.
func parseOpenAPISpec(raw []byte) ([]openAPITool, error) {
	var document any
	if err := yaml.Unmarshal(bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}), &document); err != nil {
		return nil, fmt.Errorf("parse OpenAPI spec: %w", err)
	}
	root, ok := normalizeYAML(document).(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parse OpenAPI spec: document must be an object")
	}
	resolver := specResolver{root: root}
	paths, _ := root["paths"].(map[string]any)

	tools := []openAPITool{}
	seen := map[string]string{}
	for _, path := range sortedKeys(paths) {
		item, err := resolver.deref(paths[path])
		if err != nil {
			return nil, fmt.Errorf("path %s: %w", path, err)
		}
		pathItem, _ := item.(map[string]any)
		if pathItem == nil {
			continue
		}
		for _, method := range openAPIMethods {
			op, err := resolver.deref(pathItem[method])
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			operation, _ := op.(map[string]any)
			if operation == nil {
				continue
			}
			tool, err := resolver.buildTool(method, path, pathItem["parameters"], operation)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}
			if previous, exists := seen[tool.Name]; exists {
				return nil, fmt.Errorf("duplicate OpenAPI tool name %q (%s and %s %s)", tool.Name, previous, tool.Method, tool.Path)
			}
			seen[tool.Name] = tool.Method + " " + tool.Path
			tools = append(tools, tool)
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools, nil
}

func (r specResolver) buildTool(method, path string, pathParams any, operation map[string]any) (openAPITool, error) {
	name := strings.TrimSpace(stringField(operation, "operationId"))
	if name == "" {
		name = sanitizeToolName(method + "_" + path)
	}

	type paramKey struct{ name, in string }
	merged := map[paramKey]map[string]any{}
	var order []paramKey
	for _, list := range []any{pathParams, operation["parameters"]} {
		items, _ := list.([]any)
		for _, item := range items {
			resolved, err := r.deref(item)
			if err != nil {
				return openAPITool{}, err
			}
			param, _ := resolved.(map[string]any)
			if param == nil {
				continue
			}
			key := paramKey{name: stringField(param, "name"), in: strings.ToLower(stringField(param, "in"))}
			if key.name == "" {
				return openAPITool{}, errors.New("parameter without a name")
			}
			if _, exists := merged[key]; !exists {
				order = append(order, key)
			}
			merged[key] = param // operation-level parameters override path-level ones
		}
	}

	properties := map[string]any{}
	var required []string
	var params []openAPIParam
	for _, key := range order {
		param := merged[key]
		switch key.in {
		case "path", "query", "header", "cookie":
		default:
			continue
		}
		if _, exists := properties[key.name]; exists {
			return openAPITool{}, fmt.Errorf("parameter %q is declared in more than one location", key.name)
		}
		schema, _ := r.resolveSchema(param["schema"], 0).(map[string]any)
		if schema == nil {
			schema = map[string]any{"type": "string"}
		}
		if description := stringField(param, "description"); description != "" {
			schema["description"] = description
		}
		isRequired, _ := param["required"].(bool)
		if key.in == "path" {
			isRequired = true
		}
		properties[key.name] = schema
		if isRequired {
			required = append(required, key.name)
		}
		params = append(params, openAPIParam{Name: key.name, In: key.in, Required: isRequired})
	}

	hasBody := false
	if rawBody, ok := operation["requestBody"]; ok {
		resolved, err := r.deref(rawBody)
		if err != nil {
			return openAPITool{}, err
		}
		body, _ := resolved.(map[string]any)
		content, _ := body["content"].(map[string]any)
		mediaType := ""
		if _, ok := content["application/json"]; ok {
			mediaType = "application/json"
		} else {
			for _, candidate := range sortedKeys(content) {
				if strings.Contains(candidate, "json") {
					mediaType = candidate
					break
				}
			}
		}
		if mediaType != "" {
			media, _ := content[mediaType].(map[string]any)
			schema, _ := r.resolveSchema(media["schema"], 0).(map[string]any)
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			if _, exists := properties["body"]; exists {
				return openAPITool{}, errors.New(`a parameter named "body" conflicts with the request body argument`)
			}
			properties["body"] = schema
			if bodyRequired, _ := body["required"].(bool); bodyRequired {
				required = append(required, "body")
			}
			hasBody = true
		}
	}

	inputSchema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		inputSchema["required"] = required
	}
	return openAPITool{
		Name:        name,
		Description: firstNonEmptyStr(stringField(operation, "summary"), stringField(operation, "description"), method+" "+path),
		InputSchema: inputSchema,
		Method:      strings.ToUpper(method),
		Path:        path,
		Params:      params,
		HasBody:     hasBody,
	}, nil
}

// specResolver resolves local JSON pointers inside one OpenAPI document.
type specResolver struct {
	root map[string]any
}

// deref follows a chain of local $ref objects. Non-local references are
// rejected because the gateway never fetches additional documents.
func (r specResolver) deref(node any) (any, error) {
	for depth := 0; depth < openAPIMaxRefDepth; depth++ {
		object, ok := node.(map[string]any)
		if !ok {
			return node, nil
		}
		ref, ok := object["$ref"].(string)
		if !ok {
			return node, nil
		}
		target, err := r.lookup(ref)
		if err != nil {
			return nil, err
		}
		node = target
	}
	return nil, errors.New("$ref chain is too deep")
}

func (r specResolver) lookup(ref string) (any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("unsupported $ref %q: only local references are resolved", ref)
	}
	var node any = r.root
	for _, token := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		object, ok := node.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unresolvable $ref %q", ref)
		}
		node, ok = object[token]
		if !ok {
			return nil, fmt.Errorf("unresolvable $ref %q", ref)
		}
	}
	return node, nil
}

// resolveSchema returns a deep copy of a schema with local $refs inlined.
// Recursive schemas are cut off at openAPIMaxRefDepth with a permissive
// object schema; unresolvable refs are dropped the same way so the published
// inputSchema never contains dangling pointers.
func (r specResolver) resolveSchema(node any, depth int) any {
	switch typed := node.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			if depth >= openAPIMaxRefDepth {
				return map[string]any{"type": "object"}
			}
			target, err := r.lookup(ref)
			if err != nil {
				return map[string]any{"type": "object"}
			}
			return r.resolveSchema(target, depth+1)
		}
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = r.resolveSchema(value, depth)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, value := range typed {
			out[index] = r.resolveSchema(value, depth)
		}
		return out
	default:
		return typed
	}
}

// normalizeYAML converts map[any]any nodes (YAML allows non-string keys such
// as unquoted response codes) into map[string]any.
func normalizeYAML(node any) any {
	switch typed := node.(type) {
	case map[string]any:
		for key, value := range typed {
			typed[key] = normalizeYAML(value)
		}
		return typed
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[fmt.Sprint(key)] = normalizeYAML(value)
		}
		return out
	case []any:
		for index, value := range typed {
			typed[index] = normalizeYAML(value)
		}
		return typed
	default:
		return typed
	}
}

// executeOpenAPICall maps MCP tool arguments onto an HTTP request. Arguments
// are routed by their declared location: path values are escaped (values
// that are "." or contain '/', '\' or '..' are rejected), query, header and
// cookie values go where declared, and arguments["body"] is sent as the
// whole JSON request body.
// Undeclared arguments become query parameters.
func executeOpenAPICall(ctx context.Context, client *http.Client, baseURL string, headers map[string]string, tool *openAPITool, args map[string]any) (*http.Response, error) {
	path := tool.Path
	query := url.Values{}
	headerValues := http.Header{}
	var cookies []*http.Cookie
	declared := map[string]bool{}

	substitute := func(name string, value any) error {
		text := toStringValue(value)
		// "." and ".." are dot segments that servers normalizing paths
		// would resolve to a different resource.
		if text == "" || text == "." || strings.Contains(text, "/") || strings.Contains(text, "\\") || strings.Contains(text, "..") {
			return fmt.Errorf("invalid value for path parameter %q", name)
		}
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(text))
		return nil
	}

	for _, param := range tool.Params {
		declared[param.Name] = true
		value, present := args[param.Name]
		if !present || value == nil {
			if param.Required {
				return nil, fmt.Errorf("missing required argument %q", param.Name)
			}
			continue
		}
		switch param.In {
		case "path":
			if err := substitute(param.Name, value); err != nil {
				return nil, err
			}
		case "query":
			addQueryValue(query, param.Name, value)
		case "header":
			text := toStringValue(value)
			if strings.ContainsAny(text, "\r\n") {
				return nil, fmt.Errorf("invalid value for header parameter %q", param.Name)
			}
			headerValues.Set(param.Name, text)
		case "cookie":
			cookies = append(cookies, &http.Cookie{Name: param.Name, Value: toStringValue(value)})
		}
	}

	var bodyReader io.Reader
	if tool.HasBody {
		declared["body"] = true
		if value, ok := args["body"]; ok && value != nil {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			bodyReader = bytes.NewReader(encoded)
		}
	}
	for _, key := range sortedKeys(args) {
		if declared[key] {
			continue
		}
		if strings.Contains(path, "{"+key+"}") {
			// Specs that omit the path parameter declaration still get escaping.
			if err := substitute(key, args[key]); err != nil {
				return nil, err
			}
			continue
		}
		addQueryValue(query, key, args[key])
	}
	if strings.Contains(path, "{") {
		return nil, fmt.Errorf("missing path parameter in %s", path)
	}

	req, err := http.NewRequestWithContext(ctx, tool.Method, strings.TrimRight(baseURL, "/")+path, bodyReader)
	if err != nil {
		return nil, err
	}
	for key, values := range headerValues {
		req.Header[key] = values
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	// Operator-configured headers win over caller-supplied header arguments.
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	injectTraceHeaders(ctx, req.Header)
	req.URL.RawQuery = query.Encode()
	return client.Do(req)
}

func addQueryValue(query url.Values, key string, value any) {
	if list, ok := value.([]any); ok {
		for _, item := range list {
			query.Add(key, toStringValue(item))
		}
		return
	}
	query.Set(key, toStringValue(value))
}

func sanitizeToolName(s string) string {
	replacer := strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_", ".", "_")
	return strings.ToLower(replacer.Replace(strings.TrimPrefix(s, "/")))
}

func findOpenAPITool(tools []openAPITool, name string) *openAPITool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func toStringValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
