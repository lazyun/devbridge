// Package piagent adapts PI Agent's RPC mode as a Codex execution backend.
//
// PI Agent runs as a long-lived subprocess controlled through JSONL records
// on stdin/stdout. This package implements the CodexSession and CodexFactory
// interfaces so the existing execution pipeline can dispatch to PI Agent
// without modification.
package piagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config controls how a PI Agent subprocess is launched.
type Config struct {
	// PiPath is the resolved path to the pi executable.
	PiPath string
	// CWD is the working directory for the subprocess.
	CWD string
	// Model selects the AI model (empty uses PI's default).
	Model string
	// ThinkingLevel controls reasoning depth (empty uses PI's default).
	ThinkingLevel string
	// SessionDir overrides the default session storage location.
	SessionDir string
	// ExtraArgs are additional CLI arguments passed to pi.
	ExtraArgs []string
}

// Client is a PI Agent RPC subprocess client.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr io.ReadCloser

	mu       sync.Mutex
	reqID    atomic.Uint64
	pending  map[string]chan *rpcResponse
	closed   bool
	closeErr error

	// events receives asynchronous session events.
	events chan *rpcEvent

	done chan struct{}

	// stderrOutput stores stderr for debugging.
	stderrOutput strings.Builder
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
	Type string `json:"type"`
	// AssistantMessageEvent is populated for message_update events.
	AssistantMessageEvent *assistantMessageEvent `json:"assistantMessageEvent,omitempty"`
	// AgentSettledData is populated for agent_settled events.
	AgentSettledData *agentSettledData `json:"-"`
}

type assistantMessageEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta,omitempty"`
	Text  string `json:"text,omitempty"`
}

type agentSettledData struct {
	Disposition string `json:"disposition,omitempty"`
}

// Start launches a PI Agent subprocess in RPC mode.
func Start(ctx context.Context, cfg Config) (*Client, error) {
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

	cmd := exec.CommandContext(ctx, cfg.PiPath, args...)
	if cfg.CWD != "" {
		cmd.Dir = cfg.CWD
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pi agent stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("pi agent stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("pi agent stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return nil, fmt.Errorf("pi agent start: %w", err)
	}

	// Give the process a moment to initialize.
	time.Sleep(100 * time.Millisecond)

	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		stderr:  stderr,
		pending: make(map[string]chan *rpcResponse),
		events:  make(chan *rpcEvent, 256),
		done:    make(chan struct{}),
	}

	go c.readLoop(stdout)
	go c.readStderr(stderr)

	return c, nil
}

// readLoop continuously reads JSONL records from stdout and dispatches them.
func (c *Client) readLoop(r io.Reader) {
	defer close(c.done)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20) // 1MB line buffer
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Peek at the type field to determine if this is a response or event.
		var peek struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(line, &peek); err != nil {
			continue // skip malformed lines
		}

		if peek.Type == "response" {
			var resp rpcResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				continue
			}
			c.mu.Lock()
			ch, ok := c.pending[resp.ID]
			if ok {
				delete(c.pending, resp.ID)
			}
			c.mu.Unlock()
			if ok {
				ch <- &resp
			}
		} else {
			// Session event
			var event rpcEvent
			if err := json.Unmarshal(line, &event); err != nil {
				continue
			}
			select {
			case c.events <- &event:
			default:
				// drop if buffer full (backpressure)
			}
		}
	}
}

// readStderr continuously reads stderr for debugging.
func (c *Client) readStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		c.stderrOutput.WriteString(scanner.Text())
		c.stderrOutput.WriteString("\n")
	}
}

// send writes a JSONL command to stdin and returns the correlated response.
func (c *Client) send(ctx context.Context, cmd rpcCommand) (*rpcResponse, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("pi agent client closed")
	}
	id := cmd.ID
	if id == "" {
		id = fmt.Sprintf("req-%d", c.reqID.Add(1))
		cmd.ID = id
	}
	ch := make(chan *rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	data, err := json.Marshal(cmd)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("pi agent marshal: %w", err)
	}
	data = append(data, '\n')

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("pi agent client closed")
	}
	_, err = c.stdin.Write(data)
	c.mu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("pi agent write: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, errors.New("pi agent subprocess exited")
	}
}

// Prompt sends a prompt and waits for the agent to settle.
// It returns the collected assistant text output.
func (c *Client) Prompt(ctx context.Context, message string) (string, error) {
	resp, err := c.send(ctx, rpcCommand{Type: "prompt", Message: message})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("pi agent prompt rejected: %s", resp.Error)
	}

	// Check disposition - "handled" means no run started.
	var data struct {
		Disposition string `json:"disposition"`
	}
	if len(resp.Data) > 0 {
		_ = json.Unmarshal(resp.Data, &data)
	}
	if data.Disposition == "handled" {
		return "", nil
	}

	// Collect events until agent_settled.
	return c.collectUntilSettled(ctx)
}

// collectUntilSettled reads events until agent_settled is received.
func (c *Client) collectUntilSettled(ctx context.Context) (string, error) {
	var output strings.Builder
	for {
		select {
		case event := <-c.events:
			if event.Type == "agent_settled" {
				return output.String(), nil
			}
			if event.Type == "message_update" && event.AssistantMessageEvent != nil {
				ame := event.AssistantMessageEvent
				switch ame.Type {
				case "text_delta":
					output.WriteString(ame.Delta)
				case "text":
					output.WriteString(ame.Text)
				}
			}
		case <-ctx.Done():
			return output.String(), ctx.Err()
		case <-c.done:
			return output.String(), errors.New("pi agent subprocess exited unexpectedly")
		}
	}
}

// GetState queries the current agent state.
func (c *Client) GetState(ctx context.Context) (json.RawMessage, error) {
	resp, err := c.send(ctx, rpcCommand{Type: "get_state"})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("pi agent get_state failed: %s", resp.Error)
	}
	return resp.Data, nil
}

// Abort stops the current agent operation.
func (c *Client) Abort(ctx context.Context) error {
	resp, err := c.send(ctx, rpcCommand{Type: "abort"})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("pi agent abort failed: %s", resp.Error)
	}
	return nil
}

// Close shuts down the PI Agent subprocess gracefully.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return c.closeErr
	}
	c.closed = true
	c.closeErr = c.stdin.Close()
	c.mu.Unlock()

	// Wait for the read loop to finish.
	<-c.done

	// Wait for the process to exit.
	waitErr := c.cmd.Wait()
	if c.closeErr == nil && waitErr != nil {
		c.closeErr = waitErr
	}
	return c.closeErr
}

// Events returns the event channel for external consumers.
func (c *Client) Events() <-chan *rpcEvent {
	return c.events
}

// Stderr returns a reader for the subprocess stderr (diagnostics only).
func (c *Client) Stderr() io.ReadCloser {
	return c.stderr
}
