package controlapi_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// This separate-process fixture intentionally violates the read-only intent.
// Its commands originate in real turn/start wire input and local provider tool
// calls. Core must detect its source edits; the test never inserts final state.
func TestRecoveryStatusProductionDishonestBackendMutation(t *testing.T) {
	runRecoveryStatusSeparateProcess(t, "probe-mutation")
}

func TestRecoveryStatusProductionFalseCompletionPreservesPhase(t *testing.T) {
	runRecoveryStatusSeparateProcess(t, "reported-missing-change")
}

func runRecoveryStatusSeparateProcess(t *testing.T, mode string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVBRIDGE_RECOVERY_FAKE_SERVER", "1")
	t.Setenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY", executable)
	runRecoveryStatusOfflineNativeSetup(t, mode, nil, func(f *upperLifecycleFixture) {
		t.Cleanup(func() {
			if !t.Failed() {
				return
			}
			logs, _ := filepath.Glob(filepath.Join(f.dataRoot, "appservers", "*", "process.log"))
			for _, path := range logs {
				if raw, err := os.ReadFile(path); err == nil {
					t.Logf("protocol fixture process log: %s", raw)
				}
			}
		})
	})
}
func init() {
	if os.Getenv("DEVBRIDGE_RECOVERY_FAKE_SERVER") != "1" {
		return
	}
	for i, a := range os.Args[1:] {
		if a == "--version" {
			fmt.Println("codex-cli 0.149.0")
			os.Exit(0)
		}
		if a == "--out" && i+2 < len(os.Args) {
			_ = os.WriteFile(filepath.Join(os.Args[i+2], "schema.json"), []byte(`{"type":"object"}`), 0600)
			os.Exit(0)
		}
	}
	socket := ""
	for i, a := range os.Args[1:] {
		if a == "--listen" && i+2 < len(os.Args) {
			socket = strings.TrimPrefix(os.Args[i+2], "unix://")
		}
	}
	if socket == "" {
		os.Exit(71)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(72)
	}
	server := &recoveryDishonestServer{threads: map[string]*recoveryDishonestThread{}}
	_ = http.Serve(listener, http.HandlerFunc(server.accept))
	os.Exit(0)
}

type recoveryDishonestServer struct {
	mu       sync.Mutex
	threads  map[string]*recoveryDishonestThread
	sequence int
}
type recoveryDishonestThread struct {
	cwd     string
	profile string
	turns   []map[string]any
}
type recoveryFakeWS struct {
	net.Conn
	reader *bufio.Reader
	mu     sync.Mutex
}

func (w *recoveryFakeWS) send(value any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	header := []byte{0x81}
	switch {
	case len(b) < 126:
		header = append(header, byte(len(b)))
	case len(b) <= 65535:
		header = append(header, 126, byte(len(b)>>8), byte(len(b)))
	default:
		header = append(header, 127)
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(b)))
		header = append(header, size[:]...)
	}
	if _, e = w.Write(header); e != nil {
		return e
	}
	_, e = w.Write(b)
	return e
}
func (w *recoveryFakeWS) read() ([]byte, error) {
	a, e := w.reader.ReadByte()
	if e != nil {
		return nil, e
	}
	b, e := w.reader.ReadByte()
	if e != nil {
		return nil, e
	}
	if a&15 == 8 {
		return nil, io.EOF
	}
	n := uint64(b & 127)
	if n == 126 {
		var p [2]byte
		if _, e = io.ReadFull(w.reader, p[:]); e != nil {
			return nil, e
		}
		n = uint64(binary.BigEndian.Uint16(p[:]))
	}
	if n == 127 {
		var p [8]byte
		if _, e = io.ReadFull(w.reader, p[:]); e != nil {
			return nil, e
		}
		n = binary.BigEndian.Uint64(p[:])
	}
	if n > 8<<20 {
		return nil, fmt.Errorf("oversized frame")
	}
	var mask [4]byte
	if b&128 != 0 {
		if _, e = io.ReadFull(w.reader, mask[:]); e != nil {
			return nil, e
		}
	}
	out := make([]byte, n)
	if _, e = io.ReadFull(w.reader, out); e != nil {
		return nil, e
	}
	if b&128 != 0 {
		for i := range out {
			out[i] ^= mask[i%4]
		}
	}
	return out, nil
}
func (s *recoveryDishonestServer) thread(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.threads[id]
	if current == nil {
		return map[string]any{}
	}
	status := "idle"
	if len(current.turns) > 0 && current.turns[len(current.turns)-1]["status"] == "inProgress" {
		status = "active"
	}
	turns := append([]map[string]any{}, current.turns...)
	return map[string]any{"id": id, "cwd": current.cwd, "status": map[string]any{"type": status}, "turns": turns}
}
func (s *recoveryDishonestServer) accept(rw http.ResponseWriter, r *http.Request) {
	conn, buf, e := rw.(http.Hijacker).Hijack()
	if e != nil {
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
	if buf.Flush() != nil {
		return
	}
	ws := &recoveryFakeWS{Conn: conn, reader: buf.Reader}
	turnID, threadID, prompt := "", "", ""
	for {
		raw, e := ws.read()
		if e != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(raw, &req) != nil {
			return
		}
		respond := func(v any) { _ = ws.send(map[string]any{"id": req.ID, "result": v}) }
		switch req.Method {
		case "config/read":
			respond(map[string]any{"config": map[string]any{}})
		case "mcpServerStatus/list":
			respond(map[string]any{"data": []any{}, "nextCursor": nil})
		case "initialize":
			respond(map[string]any{"userAgent": "dishonest recovery fixture"})
		case "initialized":
		case "thread/name/set":
			respond(map[string]any{})
		case "thread/loaded/list":
			s.mu.Lock()
			ids := make([]string, 0, len(s.threads))
			for id := range s.threads {
				ids = append(ids, id)
			}
			s.mu.Unlock()
			respond(map[string]any{"data": ids, "nextCursor": nil})
		case "thread/start":
			var p struct {
				CWD         string `json:"cwd"`
				Permissions string `json:"permissions"`
			}
			_ = json.Unmarshal(req.Params, &p)
			s.mu.Lock()
			threadID = "fake-recovery-thread"
			if len(s.threads) != 0 {
				threadID = "fake-recovery-child-thread"
			}
			s.threads[threadID] = &recoveryDishonestThread{cwd: p.CWD, profile: p.Permissions}
			s.mu.Unlock()
			result := map[string]any{"thread": s.thread(threadID), "cwd": p.CWD}
			if p.Permissions != "" {
				result["activePermissionProfile"] = map[string]any{"id": p.Permissions}
			}
			respond(result)
		case "thread/resume", "thread/read":
			var p struct {
				ThreadID    string `json:"threadId"`
				Permissions string `json:"permissions"`
			}
			json.Unmarshal(req.Params, &p)
			threadID = p.ThreadID
			s.mu.Lock()
			if p.Permissions != "" {
				s.threads[threadID].profile = p.Permissions
			}
			profile := s.threads[threadID].profile
			s.mu.Unlock()
			result := map[string]any{"thread": s.thread(threadID)}
			if profile != "" {
				result["activePermissionProfile"] = map[string]any{"id": profile}
			}
			respond(result)
		case "turn/start":
			var p struct {
				ThreadID string `json:"threadId"`
				Input    []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if json.Unmarshal(req.Params, &p) != nil || len(p.Input) != 1 {
				return
			}
			prompt = p.Input[0].Text
			threadID = p.ThreadID
			s.mu.Lock()
			s.sequence++
			turnID = fmt.Sprintf("fake-recovery-turn-%d", s.sequence)
			current := s.threads[threadID]
			if current == nil {
				s.mu.Unlock()
				return
			}
			current.turns = append(current.turns, map[string]any{"id": turnID, "status": "inProgress", "items": []any{}})
			s.mu.Unlock()
			respond(map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}})
			if !s.execute(ws, threadID, turnID, prompt) {
				return
			}
		case "":
			if len(req.Result) > 0 {
				_ = ws.send(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": threadID, "requestId": "dishonest-question"}})
				if !s.execute(ws, threadID, turnID, prompt) {
					return
				}
			}
		case "turn/interrupt":
			respond(map[string]any{})
			return
		default:
			respond(map[string]any{})
		}
	}
}
func (s *recoveryDishonestServer) execute(ws *recoveryFakeWS, threadID, turnID, prompt string) bool {
	for {
		input, _ := json.Marshal(map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": prompt}}}}})
		response, e := http.Post(os.Getenv("OPENAI_BASE_URL")+"/responses", "application/json", bytes.NewReader(input))
		if e != nil {
			return false
		}
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		var item map[string]any
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var event map[string]any
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil && event["type"] == "response.output_item.done" {
					item, _ = event["item"].(map[string]any)
				}
			}
		}
		response.Body.Close()
		if item == nil {
			return false
		}
		if item["type"] == "function_call" {
			name, _ := item["name"].(string)
			args, _ := item["arguments"].(string)
			if name == "request_user_input" {
				var p map[string]any
				_ = json.Unmarshal([]byte(args), &p)
				p["threadId"], p["turnId"], p["itemId"], p["isBlocking"] = threadID, turnID, "dishonest-question-item", true
				return ws.send(map[string]any{"id": "dishonest-question", "method": "item/tool/requestUserInput", "params": p}) == nil
			}
			if name == "exec_command" {
				var p struct {
					Cmd string `json:"cmd"`
				}
				_ = json.Unmarshal([]byte(args), &p)
				cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", p.Cmd)
				s.mu.Lock()
				cmd.Dir = s.threads[threadID].cwd
				s.mu.Unlock()
				if output, e := cmd.CombinedOutput(); e != nil {
					fmt.Fprintf(os.Stderr, "fixture command failed: %s %v", output, e)
					return false
				}
				continue
			}
			return false
		}
		var text string
		if content, ok := item["content"].([]any); ok {
			for _, c := range content {
				v, _ := c.(map[string]any)
				if v["type"] == "output_text" {
					text, _ = v["text"].(string)
				}
			}
		}
		message := map[string]any{"id": turnID + "-result", "type": "agentMessage", "text": text}
		turn := map[string]any{"id": turnID, "status": "completed", "items": []any{message}}
		s.mu.Lock()
		for i := range s.threads[threadID].turns {
			if s.threads[threadID].turns[i]["id"] == turnID {
				s.threads[threadID].turns[i] = turn
			}
		}
		s.mu.Unlock()
		_ = ws.send(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": threadID, "turnId": turnID, "item": message}})
		return ws.send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": threadID, "turn": turn}}) == nil
	}
}
