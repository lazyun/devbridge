package platformcollector

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rpcFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}
type probeRPC struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	frames chan rpcFrame
	done   chan struct{}
	err    error
	seq    int
	events []rpcFrame
	mu     sync.Mutex
	trace  []TranscriptEntry
	stderr boundedOutput
}
type boundedOutput struct {
	mu    sync.Mutex
	bytes []byte
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(b.bytes) < 65536 {
		remaining := 65536 - len(b.bytes)
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.bytes = append(b.bytes, p...)
	}
	return n, nil
}
func (b *boundedOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.bytes) }

func startRPC(ctx context.Context, native, home string) (*probeRPC, error) {
	p := &probeRPC{frames: make(chan rpcFrame, 256), done: make(chan struct{})}
	p.cmd = exec.CommandContext(ctx, native, "app-server")
	p.cmd.Env = isolatedEnv(home)
	p.cmd.Dir = home
	p.cmd.Stderr = &p.stderr
	var err error
	p.in, err = p.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = p.cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		defer close(p.frames)
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			raw := append([]byte(nil), scanner.Bytes()...)
			var f rpcFrame
			if err := json.Unmarshal(raw, &f); err != nil {
				p.err = err
				return
			}
			p.mu.Lock()
			p.trace = append(p.trace, TranscriptEntry{"received", raw})
			p.mu.Unlock()
			select {
			case p.frames <- f:
			case <-p.done:
				return
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			p.err = err
		}
	}()
	return p, nil
}
func (p *probeRPC) close() error {
	select {
	case <-p.done:
		return nil
	default:
		close(p.done)
	}
	_ = p.in.Close()
	completed := make(chan error, 1)
	go func() { completed <- p.cmd.Wait() }()
	select {
	case err := <-completed:
		return err
	case <-time.After(3 * time.Second):
		_ = p.cmd.Process.Kill()
		return <-completed
	}
}
func (p *probeRPC) snapshot() []TranscriptEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]TranscriptEntry(nil), p.trace...)
}
func (p *probeRPC) send(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.trace = append(p.trace, TranscriptEntry{"sent", append([]byte(nil), raw...)})
	p.mu.Unlock()
	raw = append(raw, '\n')
	_, err = p.in.Write(raw)
	return err
}
func (p *probeRPC) next(ctx context.Context) (rpcFrame, error) {
	select {
	case f, ok := <-p.frames:
		if !ok {
			return f, fmt.Errorf("native RPC ended: %v: %.800s", p.err, p.stderr.String())
		}
		if f.Method != "" && len(f.ID) > 0 {
			_ = p.send(map[string]any{"id": f.ID, "error": map[string]any{"code": -32601, "message": "collector denies unexpected native request"}})
			return f, fmt.Errorf("unmeasured server request %s", f.Method)
		}
		return f, nil
	case <-ctx.Done():
		return rpcFrame{}, ctx.Err()
	}
}
func (p *probeRPC) call(ctx context.Context, method string, params any, out any) error {
	p.seq++
	id := p.seq
	if err := p.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		f, err := p.next(ctx)
		if err != nil {
			return err
		}
		if string(f.ID) == strconv.Itoa(id) {
			if len(f.Error) > 0 && string(f.Error) != "null" {
				return fmt.Errorf("%s: %s", method, f.Error)
			}
			if out != nil {
				return json.Unmarshal(f.Result, out)
			}
			return nil
		}
		if f.Method != "" {
			p.events = append(p.events, f)
			if len(p.events) > 4096 {
				return errors.New("native event limit exceeded")
			}
		}
	}
}
func (p *probeRPC) initialize(ctx context.Context) error {
	var out struct {
		UserAgent string `json:"userAgent"`
	}
	if err := p.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "devbridge-platform-collector", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}, &out); err != nil {
		return err
	}
	if out.UserAgent == "" {
		return errors.New("native initialize lacks identity")
	}
	return p.send(map[string]any{"method": "initialized", "params": map[string]any{}})
}

type turnObservation struct {
	Status   string
	Text     string
	Commands []commandObservation
}
type commandObservation struct {
	ExitCode *int   `json:"exitCode"`
	Status   string `json:"status"`
	Command  string `json:"command"`
}

func (p *probeRPC) waitTurn(ctx context.Context, threadID, turnID string) (turnObservation, error) {
	var out turnObservation
	for {
		var f rpcFrame
		if len(p.events) > 0 {
			f = p.events[0]
			p.events = p.events[1:]
		} else {
			var err error
			f, err = p.next(ctx)
			if err != nil {
				return out, err
			}
		}
		var params struct {
			ThreadID string          `json:"threadId"`
			TurnID   string          `json:"turnId"`
			Item     json.RawMessage `json:"item"`
			Turn     struct {
				ID     string          `json:"id"`
				Status string          `json:"status"`
				Error  json.RawMessage `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(f.Params, &params) != nil || params.ThreadID != threadID {
			continue
		}
		if f.Method == "item/completed" && params.TurnID == turnID {
			var item struct {
				Type string `json:"type"`
				Text string `json:"text"`
				commandObservation
			}
			if json.Unmarshal(params.Item, &item) != nil {
				return out, errors.New("malformed native item")
			}
			if item.Type == "agentMessage" {
				out.Text = item.Text
			}
			if item.Type == "commandExecution" {
				out.Commands = append(out.Commands, item.commandObservation)
			}
		}
		if f.Method == "turn/completed" && params.Turn.ID == turnID {
			out.Status = params.Turn.Status
			if len(params.Turn.Error) > 0 && string(params.Turn.Error) != "null" {
				return out, fmt.Errorf("native turn failed: %s", params.Turn.Error)
			}
			return out, nil
		}
	}
}

type responsesProbe struct {
	mu                               sync.Mutex
	phase                            string
	step                             int
	nonce, writeCommand, denyCommand string
	requests                         []json.RawMessage
	started                          chan struct{}
	unexpected                       string
}

// Denied exec calls need not emit commandExecution items. The raw tool output
// sent by the native process to our provider is independent of the synthetic
// assistant response and is bound to the exact collector-generated call ID.
func (p *responsesProbe) readonlyDenial() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, raw := range p.requests {
		var request struct {
			Input []struct {
				Type, Output string
				CallID       string `json:"call_id"`
			} `json:"input"`
		}
		if json.Unmarshal(raw, &request) != nil {
			continue
		}
		for _, item := range request.Input {
			if item.Type == "function_call_output" && item.CallID == "collector-readonly" && strings.Contains(item.Output, "Process exited with code 1") && (strings.Contains(item.Output, "operation not permitted") || strings.Contains(item.Output, "Permission denied")) {
				return item.Output, true
			}
		}
	}
	return "", false
}

func (p *responsesProbe) setPhase(phase string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = phase
	p.step = 0
	p.started = make(chan struct{})
}
func (p *responsesProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
		http.Error(w, "collector permits only local Responses", http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, "read failed", 400)
		return
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, raw)
	p.step++
	step := p.step
	phase := p.phase
	started := p.started
	if body["model"] != "gpt-5.1-codex" {
		p.unexpected = "native changed explicit model"
	}
	if step == 1 {
		close(started)
	}
	p.mu.Unlock()
	if phase == "interrupt" {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"collector-interrupt\"}}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
		return
	}
	if step > 2 {
		http.Error(w, "unexpected extra provider roundtrip", 500)
		return
	}
	var item map[string]any
	if step == 1 {
		cmd := p.writeCommand
		if phase == "readonly" {
			cmd = p.denyCommand
		}
		args, _ := json.Marshal(map[string]any{"cmd": cmd, "max_output_tokens": 1000})
		item = map[string]any{"type": "function_call", "call_id": "collector-" + phase, "name": "exec_command", "arguments": string(args)}
	} else {
		text, _ := json.Marshal(map[string]string{"nonce": p.nonce, "status": "ok"})
		item = map[string]any{"id": "collector-result", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": string(text)}}}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range []map[string]any{{"type": "response.created", "response": map[string]any{"id": "collector-" + phase}}, {"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": "collector-" + phase, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}} {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], b)
	}
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func runConformance(ctx context.Context, provider *Provider, report *Report) (resultErr error) {
	root, err := os.MkdirTemp(provider.artifacts.root, ".conformance-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	home := filepath.Join(root, "home")
	a := filepath.Join(root, "member-a")
	b := filepath.Join(root, "member-b")
	for _, dir := range []string{home, a, b} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	nonce := hex.EncodeToString(random[:])
	fileA, fileB := filepath.Join(a, "observed.txt"), filepath.Join(b, "observed.txt")
	probe := &responsesProbe{nonce: nonce, writeCommand: "printf %s " + shellQuote(nonce) + " > " + shellQuote(fileA) + " && printf %s " + shellQuote(nonce) + " > " + shellQuote(fileB), denyCommand: "printf denied > " + shellQuote(fileA)}
	probe.setPhase("write")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: probe, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	config := fmt.Sprintf("model=\"gpt-5.1-codex\"\nmodel_provider=\"core_conformance\"\n[model_providers.core_conformance]\nname=\"Core offline conformance\"\nbase_url=%q\nwire_api=\"responses\"\nrequires_openai_auth=false\nrequest_max_retries=0\nstream_max_retries=0\n[features]\nresponses_websockets=false\nresponses_websockets_v2=false\nplugins=false\nremote_plugin=false\n[analytics]\nenabled=false\n[feedback]\nenabled=false\n", "http://"+listener.Addr().String()+"/v1")
	if err = os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		return err
	}
	var clients []*probeRPC
	defer func() {
		for _, p := range clients {
			_ = p.close()
			report.Transcript = append(report.Transcript, p.snapshot()...)
			if text := p.stderr.String(); text != "" {
				report.Observations[fmt.Sprintf("stderr-%d", len(report.Transcript))] = text
			}
		}
		probe.mu.Lock()
		defer probe.mu.Unlock()
		for _, raw := range probe.requests {
			report.Transcript = append(report.Transcript, TranscriptEntry{"loopback-provider-request", raw})
		}
		report.Observations["providerRequestCount"] = strconv.Itoa(len(probe.requests))
		if resultErr == nil && probe.unexpected != "" {
			resultErr = errors.New(probe.unexpected)
		}
	}()
	p, err := startRPC(ctx, provider.manifest.NativePath, home)
	if err != nil {
		return err
	}
	clients = append(clients, p)
	if err = p.initialize(ctx); err != nil {
		return err
	}
	report.Checks["initialize"] = true
	roots := []string{a, b}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err = p.call(ctx, "thread/start", map[string]any{"cwd": a, "model": "gpt-5.1-codex", "modelProvider": "core_conformance", "sandbox": "workspace-write", "runtimeWorkspaceRoots": roots, "approvalPolicy": "never"}, &started); err != nil {
		return err
	}
	threadID := started.Thread.ID
	if threadID == "" {
		return errors.New("empty native thread")
	}
	turnParams := func(readonly bool) map[string]any {
		policy := map[string]any{"type": "workspaceWrite", "writableRoots": roots, "networkAccess": false}
		if readonly {
			policy = map[string]any{"type": "readOnly"}
		}
		return map[string]any{"threadId": threadID, "input": []map[string]string{{"type": "text", "text": "Core offline conformance " + nonce}}, "model": "gpt-5.1-codex", "effort": "low", "serviceTier": nil, "runtimeWorkspaceRoots": roots, "sandboxPolicy": policy, "outputSchema": map[string]any{"type": "object", "properties": map[string]any{"nonce": map[string]string{"type": "string"}, "status": map[string]string{"type": "string"}}, "required": []string{"nonce", "status"}, "additionalProperties": false}}
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err = p.call(ctx, "turn/start", turnParams(false), &turn); err != nil {
		return err
	}
	observed, err := p.waitTurn(ctx, threadID, turn.Turn.ID)
	if err != nil {
		return err
	}
	if observed.Status != "completed" || len(observed.Commands) != 1 || observed.Commands[0].ExitCode == nil || *observed.Commands[0].ExitCode != 0 {
		return fmt.Errorf("native write command not observed: %+v", observed)
	}
	for _, path := range []string{fileA, fileB} {
		raw, e := os.ReadFile(path)
		if e != nil || string(raw) != nonce {
			return fmt.Errorf("native command did not write exact nonce in both roots: %v", e)
		}
	}
	report.Checks["two_root_command_write"] = true
	var final map[string]string
	if json.Unmarshal([]byte(observed.Text), &final) != nil || len(final) != 2 || final["nonce"] != nonce || final["status"] != "ok" {
		return fmt.Errorf("native structured result mismatch: %q", observed.Text)
	}
	report.Checks["structured_result"] = true
	report.Observations["writeOutputSHA256"] = digest([]byte(nonce))
	report.Observations["threadId"] = threadID
	_ = p.close()
	p, err = startRPC(ctx, provider.manifest.NativePath, home)
	if err != nil {
		return err
	}
	clients = append(clients, p)
	if err = p.initialize(ctx); err != nil {
		return err
	}
	var resumed struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err = p.call(ctx, "thread/resume", map[string]any{"threadId": threadID, "cwd": a, "model": "gpt-5.1-codex", "modelProvider": "core_conformance", "sandbox": "read-only", "runtimeWorkspaceRoots": roots, "approvalPolicy": "never"}, &resumed); err != nil {
		return err
	}
	if resumed.Thread.ID != threadID {
		return errors.New("native resume replaced thread")
	}
	report.Checks["cold_thread_resume"] = true
	probe.setPhase("readonly")
	if err = p.call(ctx, "turn/start", turnParams(true), &turn); err != nil {
		return err
	}
	observed, err = p.waitTurn(ctx, threadID, turn.Turn.ID)
	if err != nil {
		return err
	}
	denial, denied := probe.readonlyDenial()
	if observed.Status != "completed" || !denied {
		return fmt.Errorf("native readonly write was not denied: %+v", observed)
	}
	report.Observations["readonlyNativeToolOutput"] = denial
	for _, path := range []string{fileA, fileB} {
		raw, e := os.ReadFile(path)
		if e != nil || string(raw) != nonce {
			return errors.New("readonly turn changed workset")
		}
	}
	report.Checks["readonly_command_denied"] = true
	probe.setPhase("interrupt")
	if err = p.call(ctx, "turn/start", turnParams(true), &turn); err != nil {
		return err
	}
	select {
	case <-probe.started:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err = p.call(ctx, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turn.Turn.ID}, nil); err != nil {
		return err
	}
	observed, err = p.waitTurn(ctx, threadID, turn.Turn.ID)
	if err != nil {
		return err
	}
	if observed.Status != "interrupted" {
		return fmt.Errorf("native interrupt status %q", observed.Status)
	}
	report.Checks["turn_interrupt"] = true
	return nil
}
