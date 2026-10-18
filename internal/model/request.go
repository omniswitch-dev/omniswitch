package model

type ToolRequest struct {
	Agent    Agent             `json:"agent" yaml:"agent"`
	Tool     Tool              `json:"tool" yaml:"tool"`
	Action   Action            `json:"action" yaml:"action"`
	Resource Resource          `json:"resource" yaml:"resource"`
	Session  Session           `json:"session" yaml:"session"`
	Target   Target            `json:"target,omitempty" yaml:"target,omitempty"`
	Metadata map[string]string `json:"metadata" yaml:"metadata"`
	// Args holds the raw, caller-supplied tool arguments. They are exposed to
	// policies as the separate CEL variable `args` and never fill the trusted
	// agent, action, or resource attributes on their own.
	Args map[string]any `json:"args,omitempty" yaml:"args,omitempty"`
}

type Agent struct {
	ID         string `json:"id" yaml:"id"`
	Department string `json:"department" yaml:"department"`
	Role       string `json:"role" yaml:"role"`
}

type Tool struct {
	Name string `json:"name" yaml:"name"`
	// FullName is the tool name as the upstream server knows it (for example
	// "github.delete_repo" or "delete_repo"), without any federation prefix.
	FullName string `json:"full_name,omitempty" yaml:"full_name,omitempty"`
}

type Action struct {
	Name string `json:"name" yaml:"name"`
	// Operation is the raw operation segment of the tool name (for example
	// "drop_table"), before it is normalized into Name.
	Operation string `json:"operation,omitempty" yaml:"operation,omitempty"`
}

type Resource struct {
	Type        string `json:"type" yaml:"type"`
	Name        string `json:"name" yaml:"name"`
	Environment string `json:"environment" yaml:"environment"`
}

type Session struct {
	ID string `json:"id" yaml:"id"`
}

// Target identifies the MCP target (upstream server) that receives a call.
type Target struct {
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
}
