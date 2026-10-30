// Package piagent adapts Pi's JSONL RPC transport. A successful RPC response is
// admission, not execution success. Runtime permissions remain Core's concern.
package piagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentpolicy"
)

var (
	ErrPromptHandled = errors.New("pi prompt was handled without starting a run")
	ErrPromptAborted = errors.New("pi run was aborted")
	ErrProtocol      = errors.New("pi RPC protocol evidence is incomplete")
)

type Config struct {
	PiPath        string
	CWD           string
	Model         string
	ThinkingLevel string
	SessionDir    string
	ExtraArgs     []string
	policy        *agentpolicy.Envelope
}

type Client struct {
	cmd          *exec.Cmd
	cwd          string
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	stderr       io.ReadCloser
	mu           sync.Mutex
	writeMu      sync.Mutex
	reqID        atomic.Uint64
	pending      map[string]chan *rpcResponse
	closed       bool
	readErr      error
	closeErr     error
	closeOnce    sync.Once
	closeDone    chan struct{}
	promptMu     sync.Mutex
	unusable     bool // guarded by promptMu; an unknown prompt must not be retried
	events       chan *rpcEvent
	done         chan struct{}
	stderrMu     sync.Mutex
	stderrOutput strings.Builder
	stderrDone   chan struct{}
	policyInput  policyInput
}

type rpcCommand struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}
type rpcResponse struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command,omitempty"`
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}
type rpcEvent struct {
	Type                  string                 `json:"type"`
	AssistantMessageEvent *assistantMessageEvent `json:"assistantMessageEvent,omitempty"`
	Message               *assistantMessage      `json:"message,omitempty"`
	Aborted               bool                   `json:"aborted,omitempty"`
	Success               bool                   `json:"success,omitempty"`
	FinalError            string                 `json:"finalError,omitempty"`
	ErrorMessage          string                 `json:"errorMessage,omitempty"`
	WillRetry             bool                   `json:"willRetry,omitempty"`
}
type assistantMessageEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta,omitempty"`
}
type assistantMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"content"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}
type promptResult struct {
	TurnID  string
	Message string
	State   agent.TurnState
}

// Start only launches the local process; StartThread performs a no-model RPC
// handshake. --no-session deliberately means there is no durable resume.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	if err := validateExtraArgs(cfg.ExtraArgs); err != nil {
		return nil, err
	}
	var err error
	if cfg.CWD == "" {
		cfg.CWD, err = os.Getwd()
	} else {
		cfg.CWD, err = filepath.Abs(cfg.CWD)
	}
	if err != nil {
		return nil, fmt.Errorf("pi cwd: %w", err)
	}
	if cfg.PiPath == "" {
		cfg.PiPath = Resolve("pi")
	}
	args := []string{"--mode", "rpc", "--no-session"}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.ThinkingLevel != "" {
		args = append(args, "--thinking", cfg.ThinkingLevel)
	}
	if cfg.SessionDir != "" {
		args = append(args, "--session-dir", cfg.SessionDir)
	}
	args = append(args, cfg.ExtraArgs...)
	input, err := preparePolicyInput(cfg.policy)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			_ = input.remove()
		}
	}()
	if input.path != "" {
		args = append(args, "--append-system-prompt", input.path)
	}
	cmd := exec.CommandContext(ctx, cfg.PiPath, args...)
	cmd.Dir = cfg.CWD
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pi stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("pi stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("pi stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("pi start: %w", err)
	}
	c := &Client{cmd: cmd, cwd: cfg.CWD, stdin: stdin, stdout: stdout, stderr: stderr,
		pending: make(map[string]chan *rpcResponse), events: make(chan *rpcEvent, 256),
		done: make(chan struct{}), closeDone: make(chan struct{}), stderrDone: make(chan struct{}), policyInput: input}
	started = true
	go c.readLoop(stdout)
	go c.readStderr(stderr)
	return c, nil
}

func (c *Client) setReadError(err error) {
	c.mu.Lock()
	if c.readErr == nil {
		c.readErr = err
	}
	c.mu.Unlock()
}
func (c *Client) readFailure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return c.readErr
	}
	return errors.New("pi subprocess exited before the operation completed")
}

func (c *Client) readLoop(r io.Reader) {
	defer close(c.done)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var head struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(line, &head); err != nil || head.Type == "" {
			c.setReadError(fmt.Errorf("%w: malformed record", ErrProtocol))
			return
		}
		if head.Type == "response" {
			var response rpcResponse
			if err := json.Unmarshal(line, &response); err != nil {
				c.setReadError(fmt.Errorf("%w: malformed response", ErrProtocol))
				return
			}
			c.mu.Lock()
			ch := c.pending[response.ID]
			delete(c.pending, response.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- &response
			}
			continue
		}
		var event rpcEvent
		// User messages have string content; only assistant message_end is an
		// outcome. Do not decode other messages into an assistant-only shape.
		if head.Type == "message_end" {
			var envelope struct {
				Message struct {
					Role string `json:"role"`
				} `json:"message"`
			}
			if err := json.Unmarshal(line, &envelope); err != nil {
				c.setReadError(fmt.Errorf("%w: malformed message", ErrProtocol))
				return
			}
			if envelope.Message.Role != "assistant" {
				continue
			}
		}
		if head.Type != "message_update" && head.Type != "message_end" && head.Type != "agent_settled" && head.Type != "auto_retry_end" && head.Type != "compaction_end" {
			continue
		}
		if err := json.Unmarshal(line, &event); err != nil {
			c.setReadError(fmt.Errorf("%w: malformed event", ErrProtocol))
			return
		}
		select {
		case c.events <- &event:
		default:
			// Never silently drop terminal or output evidence. A saturated
			// consumer makes the outcome unknown and must fail closed.
			c.setReadError(fmt.Errorf("%w: event buffer overflow", ErrProtocol))
			return
		}
	}
	if err := scanner.Err(); err != nil {
		c.setReadError(fmt.Errorf("pi read: %w", err))
	}
}

func (c *Client) readStderr(r io.Reader) {
	defer close(c.stderrDone)
	buffer := make([]byte, 4096)
	for {
		n, err := r.Read(buffer)
		c.stderrMu.Lock()
		remaining := (64 << 10) - c.stderrOutput.Len()
		if n > remaining {
			n = remaining
		}
		if n > 0 {
			_, _ = c.stderrOutput.Write(buffer[:n])
		}
		c.stderrMu.Unlock()
		if err != nil {
			return
		}
	}
}

func (c *Client) send(ctx context.Context, command rpcCommand) (*rpcResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("pi client closed")
	}
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return nil, err
	}
	if command.ID == "" {
		command.ID = fmt.Sprintf("req-%d", c.reqID.Add(1))
	}
	ch := make(chan *rpcResponse, 1)
	c.pending[command.ID] = ch
	data, err := json.Marshal(command)
	c.mu.Unlock()
	if err == nil {
		written := make(chan error, 1)
		go func() {
			c.writeMu.Lock()
			defer c.writeMu.Unlock()
			_, writeErr := c.stdin.Write(append(data, '\n'))
			written <- writeErr
		}()
		select {
		case err = <-written:
		case <-ctx.Done():
			// Unblock a full pipe without pretending the command was not sent.
			_ = c.stdin.Close()
			err = ctx.Err()
		}
	}
	if err != nil {
		c.mu.Lock()
		delete(c.pending, command.ID)
		c.mu.Unlock()
	}
	if err != nil {
		return nil, fmt.Errorf("pi write: %w", err)
	}
	defer func() { c.mu.Lock(); delete(c.pending, command.ID); c.mu.Unlock() }()
	var response *rpcResponse
	select {
	case response = <-ch:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		// An immediate exit must not hide the final already-read response.
		select {
		case response = <-ch:
		default:
			return nil, c.readFailure()
		}
	}
	if response.Command != command.Type {
		return nil, fmt.Errorf("%w: mismatched response command", ErrProtocol)
	}
	return response, nil
}

func (c *Client) Prompt(ctx context.Context, message string) (string, error) {
	result, err := c.prompt(ctx, message, nil)
	return result.Message, err
}

func (c *Client) prompt(ctx context.Context, message string, onStarted func(string) error) (result promptResult, returnedErr error) {
	if !c.promptMu.TryLock() {
		return result, errors.New("pi prompt already active")
	}
	defer c.promptMu.Unlock()
	defer func() {
		if returnedErr != nil && result.State == "" {
			c.unusable = true
		}
	}()
	if c.unusable {
		return result, errors.New("pi prior prompt outcome is unknown; session cannot be reused")
	}
	// A prior unknown outcome may not be followed by another prompt.
	select {
	case <-c.done:
		return result, c.readFailure()
	default:
	}
	select {
	case <-c.events:
		return result, fmt.Errorf("%w: unowned events preceded prompt admission", ErrProtocol)
	default:
	}
	id := fmt.Sprintf("prompt-%d", c.reqID.Add(1))
	response, err := c.send(ctx, rpcCommand{ID: id, Type: "prompt", Message: message})
	if err != nil {
		return result, err
	}
	if !response.Success {
		return result, fmt.Errorf("pi prompt rejected: %s", response.Error)
	}
	var data struct {
		Disposition string `json:"disposition"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		return result, fmt.Errorf("%w: missing prompt disposition", ErrProtocol)
	}
	if data.Disposition == "handled" {
		return result, ErrPromptHandled
	}
	if data.Disposition != "started" {
		return result, fmt.Errorf("%w: unexpected prompt disposition %q", ErrProtocol, data.Disposition)
	}
	// Pi exposes no backend turn ID. This exact accepted RPC request ID is
	// adapter-local, scoped to the observed session; it is not resumable.
	result.TurnID = id
	if onStarted != nil {
		if err := onStarted(id); err != nil {
			return result, err
		}
	}
	var last *assistantMessage
	var stream strings.Builder
	var terminalFailure string
	for {
		var event *rpcEvent
		select {
		case event = <-c.events:
		case <-ctx.Done():
			return result, ctx.Err()
		case <-c.done:
			// Drain already-read events in order before observing EOF.
			select {
			case event = <-c.events:
			default:
				return result, c.readFailure()
			}
		}
		switch event.Type {
		case "message_end":
			last = event.Message
			if last == nil {
				return result, fmt.Errorf("%w: missing final message", ErrProtocol)
			}
			stream.Reset()
			for _, block := range last.Content {
				if block.Type == "text" {
					stream.WriteString(block.Text)
				}
			}
			result.Message = stream.String()
			terminalFailure = ""
		case "auto_retry_end":
			if !event.Success {
				terminalFailure = event.FinalError
			}
		case "compaction_end":
			if !event.WillRetry && event.ErrorMessage != "" {
				terminalFailure = event.ErrorMessage
			}
		case "agent_settled":
			c.mu.Lock()
			readErr := c.readErr
			c.mu.Unlock()
			if readErr != nil {
				return result, readErr
			}
			if event.Aborted || last != nil && last.StopReason == "aborted" {
				result.State = agent.TurnInterrupted
				return result, ErrPromptAborted
			}
			if terminalFailure != "" || last != nil && last.StopReason == "error" {
				if terminalFailure == "" {
					terminalFailure = last.ErrorMessage
				}
				result.State = agent.TurnFailed
				return result, fmt.Errorf("pi run failed: %s", terminalFailure)
			}
			if last == nil || (last.StopReason != "stop" && last.StopReason != "length") {
				result.State = agent.TurnTerminal
				return result, fmt.Errorf("%w: settled without a final assistant outcome", ErrProtocol)
			}
			result.State = agent.TurnCompleted
			return result, nil
		}
	}
}

func (c *Client) GetState(ctx context.Context) (json.RawMessage, error) {
	response, err := c.send(ctx, rpcCommand{Type: "get_state"})
	if err != nil {
		return nil, err
	}
	if !response.Success {
		return nil, fmt.Errorf("pi get_state: %s", response.Error)
	}
	return response.Data, nil
}

// Abort requests interruption, but its acknowledgement is not terminal proof.
// The session does not advertise Core interruption/recovery support.
func (c *Client) Abort(ctx context.Context) error {
	response, err := c.send(ctx, rpcCommand{Type: "abort"})
	if err != nil {
		return err
	}
	if !response.Success {
		return fmt.Errorf("pi abort: %s", response.Error)
	}
	return nil
}

// Close waits for process exit. A forced termination is returned as an error,
// rather than falsely certifying orderly transport quiescence.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		defer close(c.closeDone)
		c.mu.Lock()
		c.closed = true
		c.closeErr = c.stdin.Close()
		c.mu.Unlock()
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-c.done:
		case <-timer.C:
			c.closeErr = errors.Join(c.closeErr, errors.New("pi shutdown deadline exceeded"))
			_ = c.cmd.Process.Kill()
			_ = c.stdout.Close()
			_ = c.stderr.Close()
			<-c.done
		}
		// EOF can precede process exit. Wait is bounded independently.
		waited := make(chan error, 1)
		go func() { waited <- c.cmd.Wait() }()
		select {
		case err := <-waited:
			c.closeErr = errors.Join(c.closeErr, err)
		case <-time.After(2 * time.Second):
			_ = c.cmd.Process.Kill()
			c.closeErr = errors.Join(c.closeErr, errors.New("pi exit deadline exceeded"), <-waited)
		}
		_ = c.stderr.Close()
		<-c.stderrDone
		c.closeErr = errors.Join(c.closeErr, c.policyInput.remove())
	})
	<-c.closeDone
	return c.closeErr
}

// Events must not be consumed concurrently with Prompt.
func (c *Client) Events() <-chan *rpcEvent { return c.events }
func (c *Client) Diagnostics() string {
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	return c.stderrOutput.String()
}

// Stderr is a diagnostic snapshot, not a competing reader of the process pipe.
func (c *Client) Stderr() io.ReadCloser { return io.NopCloser(strings.NewReader(c.Diagnostics())) }
