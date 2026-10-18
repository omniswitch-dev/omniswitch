package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mcpProtocolVersion = "2025-06-18"
	// stdioMaxLineBytes caps one newline-delimited JSON-RPC message from a
	// stdio server. A longer line kills the process and fails pending calls.
	stdioMaxLineBytes = 16 << 20
	// defaultCallTimeout bounds one forwarded call when the target does not
	// configure its own timeout.
	defaultCallTimeout = 2 * time.Minute
)

var errStdioClosed = errors.New("stdio MCP target is closed")

// stdioClient keeps one MCP stdio process per configured target. MCP stdio
// uses newline-delimited JSON-RPC. Every forwarded request gets a
// bridge-unique id, so concurrent and abandoned requests can never receive
// each other's responses; one reader goroutine dispatches responses to their
// waiters and restores the client's original id. The bridge performs the MCP
// initialize handshake automatically on first use (and after every restart),
// so clients can call tools without managing protocol lifecycle themselves.
type stdioClient struct {
	command     string
	args        []string
	environment map[string]string
	timeout     time.Duration

	// startLock serializes process start and handshake. It is a channel so
	// waiters can give up when their context ends.
	startLock chan struct{}

	mu     sync.Mutex
	proc   *stdioProcess
	closed bool

	nextID atomic.Uint64

	// inflight maps a client request (caller scope plus the client's id)
	// to the bridge id of the call in flight, so a client's
	// notifications/cancelled can be rewritten to the id the server saw.
	inflightMu sync.Mutex
	inflight   map[string]json.RawMessage
}

type stdioProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]chan []byte
	err     error
	exited  bool

	killOnce sync.Once
	done     chan struct{}

	initResult json.RawMessage
}

func newStdioClient(command string, args []string, environment map[string]string) (*stdioClient, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}
	return &stdioClient{
		command:     command,
		args:        append([]string(nil), args...),
		environment: cloneHeaders(environment),
		timeout:     defaultCallTimeout,
		startLock:   make(chan struct{}, 1),
		inflight:    map[string]json.RawMessage{},
	}, nil
}

// Call forwards one JSON-RPC request (which must carry an id) and returns the
// matching response with the client's id restored. When scope is not empty
// the call is registered under scope and the client's id while it is in
// flight, so Cancel can map the client's cancellation to it.
func (client *stdioClient) Call(ctx context.Context, request []byte, scope string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(request, &fields); err != nil {
		return nil, fmt.Errorf("decode stdio MCP request: %w", err)
	}
	clientID, ok := fields["id"]
	if !ok {
		return nil, fmt.Errorf("stdio MCP requests require a JSON-RPC id")
	}
	process, err := client.ensureProcess(ctx)
	if err != nil {
		return nil, err
	}
	track := ""
	if scope != "" {
		track = inflightKey(scope, clientID)
	}
	response, err := client.roundTrip(ctx, process, fields, track)
	if err != nil {
		return nil, err
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(response, &decoded); err != nil {
		return nil, fmt.Errorf("decode stdio MCP response: %w", err)
	}
	decoded["id"] = clientID
	return json.Marshal(decoded)
}

// Notify writes a message without an id (a notification) and does not wait
// for any response.
func (client *stdioClient) Notify(ctx context.Context, message []byte) error {
	process, err := client.ensureProcess(ctx)
	if err != nil {
		return err
	}
	return client.write(ctx, process, message)
}

// Cancel forwards a client's notifications/cancelled for a call made through
// Call with the same scope, rewriting params.requestId to the call's bridge
// id. It reports false (and sends nothing) when no such call is in flight:
// the server never saw the client's id, so forwarding it would cancel
// nothing or, worse, an unrelated request.
func (client *stdioClient) Cancel(ctx context.Context, scope string, message []byte) (bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(message, &fields); err != nil {
		return false, nil
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(fields["params"], &params); err != nil || params == nil {
		return false, nil
	}
	requestID, ok := params["requestId"]
	if !ok {
		return false, nil
	}
	client.inflightMu.Lock()
	bridgeID, ok := client.inflight[inflightKey(scope, requestID)]
	client.inflightMu.Unlock()
	if !ok {
		return false, nil
	}
	params["requestId"] = bridgeID
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return true, err
	}
	fields["params"] = encodedParams
	line, err := json.Marshal(fields)
	if err != nil {
		return true, err
	}
	client.mu.Lock()
	process := client.proc
	client.mu.Unlock()
	if process == nil || process.hasExited() {
		return true, nil // the call fails on its own when the process exits
	}
	return true, client.write(ctx, process, line)
}

func inflightKey(scope string, clientID json.RawMessage) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, clientID); err != nil {
		return scope + "\x00" + string(clientID)
	}
	return scope + "\x00" + compact.String()
}

// InitializeResult returns the server's cached initialize result, starting
// the process and performing the handshake if needed.
func (client *stdioClient) InitializeResult(ctx context.Context) (json.RawMessage, error) {
	process, err := client.ensureProcess(ctx)
	if err != nil {
		return nil, err
	}
	return process.initResult, nil
}

func (client *stdioClient) callTimeout() time.Duration {
	if client.timeout > 0 {
		return client.timeout
	}
	return defaultCallTimeout
}

func (client *stdioClient) bridgeID() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`"omniswitch-%d"`, client.nextID.Add(1)))
}

// roundTrip assigns a bridge id, writes the request and waits for the
// response, the context, the per-call timeout, or process exit.
func (client *stdioClient) roundTrip(ctx context.Context, process *stdioProcess, fields map[string]json.RawMessage, track string) ([]byte, error) {
	id := client.bridgeID()
	if track != "" {
		client.inflightMu.Lock()
		client.inflight[track] = id
		client.inflightMu.Unlock()
		defer func() {
			client.inflightMu.Lock()
			if bytes.Equal(client.inflight[track], id) {
				delete(client.inflight, track)
			}
			client.inflightMu.Unlock()
		}()
	}
	outgoing := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		outgoing[key] = value
	}
	outgoing["id"] = id
	line, err := json.Marshal(outgoing)
	if err != nil {
		return nil, err
	}
	key := string(id)
	waiter := make(chan []byte, 1)
	process.mu.Lock()
	if process.exited {
		process.mu.Unlock()
		return nil, process.failure()
	}
	process.pending[key] = waiter
	process.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, client.callTimeout())
	defer cancel()
	if err := client.write(ctx, process, line); err != nil {
		process.forget(key)
		return nil, err
	}
	select {
	case response := <-waiter:
		return response, nil
	case <-process.done:
		return nil, process.failure()
	case <-ctx.Done():
		// The response may still arrive later; the reader drops it because
		// the id is no longer pending. Tell the server to stop working on it.
		process.forget(key)
		cancelled := fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":%s,"reason":"request abandoned by gateway"}}`, id)
		go func() { _ = process.writeLine([]byte(cancelled)) }()
		return nil, fmt.Errorf("stdio MCP call: %w", ctx.Err())
	}
}

// write sends one line, giving up (and killing the process, which has
// stopped reading its input) when ctx ends first.
func (client *stdioClient) write(ctx context.Context, process *stdioProcess, line []byte) error {
	result := make(chan error, 1)
	go func() { result <- process.writeLine(line) }()
	select {
	case err := <-result:
		if err != nil {
			process.kill(fmt.Errorf("write to stdio MCP server: %w", err))
			return fmt.Errorf("write to stdio MCP server: %w", err)
		}
		return nil
	case <-ctx.Done():
		process.kill(errors.New("stdio MCP server stopped reading its input"))
		return fmt.Errorf("write to stdio MCP server: %w", ctx.Err())
	}
}

func (client *stdioClient) ensureProcess(ctx context.Context) (*stdioProcess, error) {
	select {
	case client.startLock <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-client.startLock }()

	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		return nil, errStdioClosed
	}
	current := client.proc
	client.mu.Unlock()
	if current != nil && !current.hasExited() {
		return current, nil
	}

	process, err := client.spawn()
	if err != nil {
		return nil, err
	}
	if err := client.handshake(ctx, process); err != nil {
		process.kill(err)
		return nil, fmt.Errorf("MCP initialize handshake: %w", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed {
		process.kill(errStdioClosed)
		return nil, errStdioClosed
	}
	client.proc = process
	return process, nil
}

func (client *stdioClient) spawn() (*stdioProcess, error) {
	command := exec.Command(client.command, client.args...)
	command.Env = os.Environ()
	for key, value := range client.environment {
		command.Env = append(command.Env, key+"="+value)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdio MCP stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open stdio MCP stdout: %w", err)
	}
	command.Stderr = os.Stderr
	configureStdioCommand(command)
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start stdio MCP server: %w", err)
	}
	process := &stdioProcess{
		cmd:     command,
		stdin:   stdin,
		pending: map[string]chan []byte{},
		done:    make(chan struct{}),
	}
	go process.readLoop(stdout)
	return process, nil
}

// handshake performs the MCP initialize exchange and the initialized
// notification so protocol-aware stdio servers accept subsequent calls.
func (client *stdioClient) handshake(ctx context.Context, process *stdioProcess) error {
	params := `{"protocolVersion":"` + mcpProtocolVersion + `","capabilities":{},"clientInfo":{"name":"omniswitch","version":"1.0"}}`
	response, err := client.roundTrip(ctx, process, map[string]json.RawMessage{
		"jsonrpc": json.RawMessage(`"2.0"`),
		"method":  json.RawMessage(`"initialize"`),
		"params":  json.RawMessage(params),
	}, "")
	if err != nil {
		return err
	}
	var decoded struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(response, &decoded); err != nil {
		return fmt.Errorf("decode initialize response: %w", err)
	}
	if len(decoded.Error) > 0 && string(decoded.Error) != "null" {
		return fmt.Errorf("initialize failed: %s", decoded.Error)
	}
	process.initResult = decoded.Result
	return client.write(ctx, process, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
}

// close permanently stops the target. In-flight calls fail promptly.
func (client *stdioClient) close() {
	client.mu.Lock()
	client.closed = true
	process := client.proc
	client.proc = nil
	client.mu.Unlock()
	if process == nil {
		return
	}
	process.kill(errStdioClosed)
	select {
	case <-process.done:
	case <-time.After(5 * time.Second):
	}
}

func (process *stdioProcess) writeLine(line []byte) error {
	process.writeMu.Lock()
	defer process.writeMu.Unlock()
	_, err := process.stdin.Write(append(append([]byte(nil), line...), '\n'))
	return err
}

func (process *stdioProcess) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), stdioMaxLineBytes)
	for scanner.Scan() {
		process.dispatch(scanner.Bytes())
	}
	err := scanner.Err()
	switch {
	case errors.Is(err, bufio.ErrTooLong):
		err = fmt.Errorf("stdio MCP server sent a message larger than %d bytes", stdioMaxLineBytes)
	case err == nil:
		err = errors.New("stdio MCP server exited")
	default:
		err = fmt.Errorf("read from stdio MCP server: %w", err)
	}
	process.kill(err)
	_ = process.cmd.Wait()
	process.mu.Lock()
	process.exited = true
	process.pending = map[string]chan []byte{}
	process.mu.Unlock()
	close(process.done)
}

// dispatch routes one line from the server. Responses (result or error, no
// method) go to the waiter registered for their bridge id; responses for
// abandoned ids are dropped. Server-initiated requests are answered so the
// server does not wait forever: ping succeeds, anything else is rejected.
func (process *stdioProcess) dispatch(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(line, &message); err != nil {
		return // non-JSON noise on stdout
	}
	id, hasID := message["id"]
	if rawMethod, isRequest := message["method"]; isRequest {
		if !hasID {
			return // server notification
		}
		var method string
		_ = json.Unmarshal(rawMethod, &method)
		reply := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not supported by the OmniSwitch stdio bridge"}}`, id)
		if method == "ping" {
			reply = fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{}}`, id)
		}
		go func() { _ = process.writeLine([]byte(reply)) }()
		return
	}
	_, hasResult := message["result"]
	_, hasError := message["error"]
	if !hasID || (!hasResult && !hasError) {
		return
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, id); err != nil {
		return
	}
	key := compact.String()
	process.mu.Lock()
	waiter := process.pending[key]
	delete(process.pending, key)
	process.mu.Unlock()
	if waiter != nil {
		waiter <- append([]byte(nil), line...)
	}
}

func (process *stdioProcess) forget(key string) {
	process.mu.Lock()
	delete(process.pending, key)
	process.mu.Unlock()
}

func (process *stdioProcess) kill(reason error) {
	process.killOnce.Do(func() {
		process.mu.Lock()
		if process.err == nil {
			process.err = reason
		}
		process.mu.Unlock()
		_ = process.stdin.Close()
		killStdioCommand(process.cmd)
	})
}

func (process *stdioProcess) hasExited() bool {
	select {
	case <-process.done:
		return true
	default:
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.err != nil
}

func (process *stdioProcess) failure() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.err != nil {
		return process.err
	}
	return errors.New("stdio MCP server exited")
}
