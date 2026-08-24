package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// openAPITool is an MCP tool synthesized from an OpenAPI operation.
type openAPITool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
	Method      string
	Path        string
}

// loadOpenAPISpec reads an OpenAPI 3.x document from a local path or URL and
// synthesizes MCP tools from its operations. Operation IDs become tool names;
// parameters plus JSON request bodies become the input schema.
func loadOpenAPISpec(location string) ([]openAPITool, error) {
	var (
		raw []byte
		err error
	)
	if strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
		resp, fetchErr := http.Get(location)
		if fetchErr != nil {
			return nil, fmt.Errorf("fetch OpenAPI spec: %w", fetchErr)
		}
		defer resp.Body.Close()
		raw, err = io.ReadAll(resp.Body)
	} else {
		raw, err = os.ReadFile(location)
	}
	if err != nil {
		return nil, err
	}
	return parseOpenAPISpec(raw)
}

func parseOpenAPISpec(raw []byte) ([]openAPITool, error) {
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			Summary     string `json:"summary"`
			Description string `json:"description"`
			Parameters  []struct {
				Name        string         `json:"name"`
				In          string         `json:"in"`
				Required    bool           `json:"required"`
				Schema      map[string]any `json:"schema"`
				Description string         `json:"description"`
			} `json:"parameters"`
			RequestBody *struct {
				Description string `json:"description"`
				Content     map[string]struct {
					Schema map[string]any `json:"schema"`
				} `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("parse OpenAPI spec: %w", err)
	}

	tools := []openAPITool{}
	for path, operations := range spec.Paths {
		for method, op := range operations {
			switch strings.ToLower(method) {
			case "get", "post", "put", "patch", "delete":
			default:
				continue
			}
			name := strings.TrimSpace(op.OperationID)
			if name == "" {
				name = sanitizeToolName(method + "_" + path)
			}
			properties := map[string]any{}
			var required []string
			for _, param := range op.Parameters {
				schema := param.Schema
				if schema == nil {
					schema = map[string]any{"type": "string"}
				}
				if param.Description != "" {
					schema["description"] = param.Description
				}
				properties[param.Name] = schema
				if param.Required {
					required = append(required, param.Name)
				}
			}
			if op.RequestBody != nil {
				for contentType, media := range op.RequestBody.Content {
					if !strings.Contains(contentType, "json") {
						continue
					}
					if media.Schema != nil {
						properties["body"] = media.Schema
						required = append(required, "body")
					}
					break
				}
			}
			inputSchema := map[string]any{"type": "object", "properties": properties}
			if len(required) > 0 {
				inputSchema["required"] = required
			}
			description := firstNonEmptyStr(op.Summary, op.Description, method+" "+path)
			tools = append(tools, openAPITool{
				Name:        name,
				Description: description,
				InputSchema: inputSchema,
				Method:      strings.ToUpper(method),
				Path:        path,
			})
		}
	}
	return tools, nil
}

// executeOpenAPICall maps MCP tool arguments onto an HTTP request: path
// parameters are substituted, remaining arguments become the JSON request body
// for body-carrying methods or the query string otherwise.
func executeOpenAPICall(client *http.Client, baseURL string, headers map[string]string, tool *openAPITool, args map[string]any) (*http.Response, error) {
	path := tool.Path
	query := url.Values{}
	bodyArgs := map[string]any{}

	for key, value := range args {
		placeholder := "{" + key + "}"
		if strings.Contains(path, placeholder) {
			path = strings.ReplaceAll(path, placeholder, toStringValue(value))
			continue
		}
		bodyArgs[key] = value
	}

	var bodyReader io.Reader
	hasBody := len(bodyArgs) > 0 && tool.Method != http.MethodGet && tool.Method != http.MethodDelete && tool.Method != http.MethodHead
	if hasBody {
		encoded, err := json.Marshal(bodyArgs)
		if err != nil {
			return nil, err
		}
		bodyReader = strings.NewReader(string(encoded))
	} else {
		for key, value := range bodyArgs {
			query.Set(key, toStringValue(value))
		}
	}

	req, err := http.NewRequest(tool.Method, strings.TrimRight(baseURL, "/")+path, bodyReader)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	req.URL.RawQuery = query.Encode()
	return client.Do(req)
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

func toStringValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
