package model

type ToolRequest struct {
	Agent    Agent             `json:"agent" yaml:"agent"`
	Tool     Tool              `json:"tool" yaml:"tool"`
	Action   Action            `json:"action" yaml:"action"`
	Resource Resource          `json:"resource" yaml:"resource"`
	Session  Session           `json:"session" yaml:"session"`
	Metadata map[string]string `json:"metadata" yaml:"metadata"`
}

type Agent struct {
	ID         string `json:"id" yaml:"id"`
	Department string `json:"department" yaml:"department"`
	Role       string `json:"role" yaml:"role"`
}

type Tool struct {
	Name string `json:"name" yaml:"name"`
}

type Action struct {
	Name string `json:"name" yaml:"name"`
}

type Resource struct {
	Type        string `json:"type" yaml:"type"`
	Name        string `json:"name" yaml:"name"`
	Environment string `json:"environment" yaml:"environment"`
}

type Session struct {
	ID string `json:"id" yaml:"id"`
}
