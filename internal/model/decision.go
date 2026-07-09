package model

import "time"

type Decision struct {
	ID             string        `json:"id" yaml:"id"`
	Allowed        bool          `json:"allowed" yaml:"allowed"`
	Rule           string        `json:"rule" yaml:"rule"`
	Reason         string        `json:"reason" yaml:"reason"`
	EvaluationTime time.Duration `json:"evaluation_time" yaml:"evaluationTime"`
	Trace          []RuleTrace   `json:"trace,omitempty" yaml:"trace,omitempty"`
}

func (d Decision) Status() string {
	if d.Allowed {
		return "ALLOW"
	}
	return "DENY"
}

type RuleTrace struct {
	Rule       string `json:"rule" yaml:"rule"`
	Expression string `json:"expression" yaml:"expression"`
	Effect     string `json:"effect" yaml:"effect"`
	Matched    bool   `json:"matched" yaml:"matched"`
	Reason     string `json:"reason,omitempty" yaml:"reason,omitempty"`
	Source     string `json:"source,omitempty" yaml:"source,omitempty"`
	Version    string `json:"version,omitempty" yaml:"version,omitempty"`
	Hash       string `json:"hash,omitempty" yaml:"hash,omitempty"`
}
