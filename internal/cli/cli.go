package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/omniswitch-dev/omniswitch/internal/explain"
	"github.com/omniswitch-dev/omniswitch/internal/model"
	"github.com/omniswitch-dev/omniswitch/internal/policy"
	"github.com/omniswitch-dev/omniswitch/internal/trace"
)

func NewRootCommand(name string) *cobra.Command {
	root := &cobra.Command{
		Use:           name,
		Short:         "Evaluate and inspect OmniSwitch policies",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(newValidateCommand())
	root.AddCommand(newVerifyCommand())
	root.AddCommand(newTestCommand("test"))
	root.AddCommand(newTestCommand("explain"))
	root.AddCommand(newTraceCommand())
	root.AddCommand(newReplayCommand())
	root.AddCommand(newDiffCommand())
	return root
}

func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <policy_file>",
		Short: "Compile a OmniSwitch CEL or omniswitch.dev/v1 policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rule, err := policy.RuleFromFile(args[0])
			if err != nil {
				return err
			}
			if err := policy.ValidateRule(rule.Expression); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "✓ VALID\n\nPolicy:\n%s\n\nRule:\n%s\n", filepath.Base(args[0]), rule.Name)
			return nil
		},
	}
}

func newVerifyCommand() *cobra.Command {
	cmd := newValidateCommand()
	cmd.Use = "verify <policy_file>"
	cmd.Short = "Alias for validate"
	return cmd
}

func newTestCommand(name string) *cobra.Command {
	return &cobra.Command{
		Use:   name + " <policy_file> <request_file.json>",
		Short: "Evaluate a policy against a canonical tool request",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, decision, err := evaluate(context.Background(), args[0], args[1])
			if err != nil {
				return err
			}
			_ = req
			fmt.Fprint(cmd.OutOrStdout(), explain.Format(decision))
			return nil
		},
	}
}

func newTraceCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "trace <policy_file> <request_file.json>",
		Short: "Evaluate and emit a portable DecisionTrace document",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			req, decision, err := evaluate(context.Background(), args[0], args[1])
			if err != nil {
				return err
			}

			document := trace.NewDocument(req, decision)
			data, err := document.MarshalYAMLBytes()
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

func newReplayCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "replay <decision_trace.yaml> [policy_file]",
		Short: "Replay a previous decision trace",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			document, err := trace.Load(args[0])
			if err != nil {
				return err
			}

			if len(args) == 1 {
				printStoredTrace(cmd, document)
				return nil
			}

			engine, err := policy.NewEngineFromFiles(args[1])
			if err != nil {
				return err
			}
			decision, err := engine.Evaluate(context.Background(), document.Spec.Request)
			if err != nil {
				return err
			}
			current := trace.NewDocument(document.Spec.Request, decision)
			fmt.Fprint(cmd.OutOrStdout(), explain.Format(decision))
			printDiffs(cmd, trace.Diff(document, current))
			return nil
		},
	}
}

func newDiffCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <left_trace.yaml> <right_trace.yaml>",
		Short: "Show differences between two DecisionTrace documents",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			left, err := trace.Load(args[0])
			if err != nil {
				return err
			}
			right, err := trace.Load(args[1])
			if err != nil {
				return err
			}
			printDiffs(cmd, trace.Diff(left, right))
			return nil
		},
	}
}

func evaluate(ctx context.Context, policyPath string, requestPath string) (model.ToolRequest, model.Decision, error) {
	request, err := ReadToolRequest(requestPath)
	if err != nil {
		return model.ToolRequest{}, model.Decision{}, err
	}

	engine, err := policy.NewEngineFromFiles(policyPath)
	if err != nil {
		return model.ToolRequest{}, model.Decision{}, err
	}

	decision, err := engine.Evaluate(ctx, request)
	if err != nil {
		return model.ToolRequest{}, model.Decision{}, err
	}
	return request, decision, nil
}

func ReadToolRequest(path string) (model.ToolRequest, error) {
	requestBytes, err := os.ReadFile(path)
	if err != nil {
		return model.ToolRequest{}, fmt.Errorf("read request file: %w", err)
	}

	var request model.ToolRequest
	if err := json.Unmarshal(stripUTF8BOM(requestBytes), &request); err != nil {
		return model.ToolRequest{}, fmt.Errorf("parse request file: %w", err)
	}
	return request, nil
}

func stripUTF8BOM(data []byte) []byte {
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
}

func printStoredTrace(cmd *cobra.Command, document trace.Document) {
	status := "ALLOWED"
	if !document.Spec.Result.Allowed {
		status = "DENIED"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "✓ %s\n\nDecision ID:\n%s\n\nMatched Rule:\n%s\n\nReason:\n%s\n\nEvaluation:\n%.2f ms\n",
		status,
		document.Metadata.DecisionID,
		document.Spec.Policy.Name,
		document.Spec.Result.Reason,
		document.Spec.Result.EvaluationMs,
	)

	if len(document.Spec.Trace) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "\nPolicy Graph:")
	for _, item := range document.Spec.Trace {
		state := "FALSE"
		if item.Matched {
			state = "TRUE"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "↓\n%s\n%s\n", item.Rule, state)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "↓\nDecision\n%s\n", status)
}

func printDiffs(cmd *cobra.Command, diffs []string) {
	if len(diffs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nNo differences.")
		return
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\nDiff:")
	for _, diff := range diffs {
		fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", strings.TrimSpace(diff))
	}
}
