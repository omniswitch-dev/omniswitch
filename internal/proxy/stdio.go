package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// stdioClient keeps one MCP stdio process per configured target. MCP stdio
// uses newline-delimited JSON-RPC. Requests are serialized because the bridge
// must preserve a deterministic request/response association on a shared
// process stream.
type stdioClient struct {
	command     string
	args        []string
	environment map[string]string

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

func newStdioClient(command string, args []string, environment map[string]string) (*stdioClient, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}
	return &stdioClient{command: command, args: append([]string(nil), args...), environment: cloneHeaders(environment)}, nil
}

func (client *stdioClient) Call(ctx context.Context, request []byte) ([]byte, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := client.start(); err != nil {
		return nil, err
	}
	requestID, err := jsonRPCID(request)
	if err != nil {
		return nil, err
	}
	if _, err := client.stdin.Write(append(append([]byte(nil), request...), '\n')); err != nil {
		client.stop()
		return nil, fmt.Errorf("write to stdio MCP server: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := client.stdout.ReadBytes('\n')
		if err != nil {
			client.stop()
			return nil, fmt.Errorf("read from stdio MCP server: %w", err)
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		responseID, hasID, err := jsonRPCIDOptional(line)
		if err != nil {
			// Ignore malformed stdout lines or notifications until the matching
			// response arrives. Proper MCP servers emit JSON-RPC only.
			continue
		}
		if hasID && responseID == requestID {
			return line, nil
		}
	}
}

func (client *stdioClient) start() error {
	if client.cmd != nil && client.cmd.Process != nil {
		return nil
	}
	command := exec.Command(client.command, client.args...)
	command.Env = os.Environ()
	for key, value := range client.environment {
		command.Env = append(command.Env, key+"="+value)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return fmt.Errorf("open stdio MCP stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("open stdio MCP stdout: %w", err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start stdio MCP server: %w", err)
	}
	client.cmd = command
	client.stdin = stdin
	client.stdout = bufio.NewReader(stdout)
	return nil
}

func (client *stdioClient) stop() {
	if client.stdin != nil {
		_ = client.stdin.Close()
	}
	if client.cmd != nil && client.cmd.Process != nil {
		_ = client.cmd.Process.Kill()
		_, _ = client.cmd.Process.Wait()
	}
	client.cmd = nil
	client.stdin = nil
	client.stdout = nil
}

func jsonRPCID(request []byte) (string, error) {
	id, present, err := jsonRPCIDOptional(request)
	if err != nil {
		return "", err
	}
	if !present {
		return "", fmt.Errorf("stdio MCP requests require a JSON-RPC id")
	}
	return id, nil
}

func jsonRPCIDOptional(payload []byte) (string, bool, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(payload, &value); err != nil {
		return "", false, err
	}
	id, present := value["id"]
	if !present {
		return "", false, nil
	}
	return string(bytes.TrimSpace(id)), true, nil
}
