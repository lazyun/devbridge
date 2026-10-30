package piagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
)

// This executable fixture handles local JSONL only. It cannot invoke a model.
func TestPiRPCFixture(t *testing.T) {
	mode := os.Getenv("DEVBRIDGE_PI_FIXTURE")
	if mode == "" {
		return
	}
	if path := os.Getenv("DEVBRIDGE_PI_CAPTURE"); path != "" {
		launch := fixtureLaunch{Args: os.Args}
		for i, arg := range os.Args {
			if arg == "--append-system-prompt" && i+1 < len(os.Args) {
				launch.PolicyFile = os.Args[i+1]
				data, err := os.ReadFile(launch.PolicyFile)
				if err != nil {
					os.Exit(4)
				}
				launch.PolicyText = string(data)
			}
		}
		data, _ := json.Marshal(launch)
		if os.WriteFile(path+".launch.json", data, 0600) != nil {
			os.Exit(5)
		}
	}
	emit := func(value any) { data, _ := json.Marshal(value); fmt.Println(string(data)) }
	message := func(reason, text string) {
		emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "assistant", "stopReason": reason, "errorMessage": "fixture provider failure", "content": []any{map[string]any{"type": "text", "text": text}}}})
	}
	scanner := bufio.NewScanner(os.Stdin)
	stateReads := 0
	for scanner.Scan() {
		var command rpcCommand
		if json.Unmarshal(scanner.Bytes(), &command) != nil {
			os.Exit(2)
		}
		if path := os.Getenv("DEVBRIDGE_PI_CAPTURE"); path != "" {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(3)
			}
			_, _ = f.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
			_ = f.Close()
		}
		response := map[string]any{"type": "response", "command": command.Type, "id": command.ID, "success": true}
		switch command.Type {
		case "get_state":
			stateReads++
			if mode == "mutate-launcher" && stateReads == 1 {
				path := os.Getenv("DEVBRIDGE_PI_MUTATE_LAUNCHER")
				data, err := os.ReadFile(path)
				if err != nil {
					os.Exit(6)
				}
				if os.WriteFile(path, append(data, []byte("\n# replaced after launch\n")...), 0700) != nil {
					os.Exit(7)
				}
			}
			sessionID := "fixture-session"
			if mode == "missing-session" {
				sessionID = ""
			}
			response["data"] = map[string]any{"sessionId": sessionID, "model": map[string]any{"id": "fixture-model", "provider": "fixture"}, "thinkingLevel": "medium"}
			if mode == "busy" {
				response["data"].(map[string]any)["isStreaming"] = true
			}
			emit(response)
			if mode == "unsolicited" && stateReads == 1 {
				message("stop", "unowned output")
				emit(map[string]any{"type": "agent_settled", "aborted": false})
			}
			if mode == "block-input" && stateReads == 2 {
				for {
					time.Sleep(time.Second)
				}
			}
		case "abort":
			emit(response)
			if mode == "await-abort" {
				emit(map[string]any{"type": "agent_settled", "aborted": true})
			}
		case "clear_queue":
			emit(response)
		case "prompt":
			if mode == "exit" {
				os.Exit(0)
			}
			if mode == "malformed" {
				fmt.Println("not JSON")
				continue
			}
			disposition := "started"
			if mode == "handled" || mode == "queued" {
				disposition = mode
			}
			response["data"] = map[string]any{"disposition": disposition}
			if mode == "missing-disposition" {
				delete(response, "data")
			}
			if mode == "rejected" {
				response["success"] = false
				response["error"] = "rejected before start"
			}
			if mode == "overflow" {
				for i := 0; i < 300; i++ {
					emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "x"}})
				}
			}
			if mode != "fast" {
				emit(response)
			}
			if mode == "handled" || mode == "queued" || mode == "missing-disposition" || mode == "rejected" || mode == "overflow" || mode == "await-abort" {
				continue
			}
			if mode == "delay" {
				time.Sleep(200 * time.Millisecond)
			}
			if mode == "retry" {
				message("error", "retry failure")
				emit(map[string]any{"type": "agent_end", "willRetry": true})
			}
			// The final message is authoritative, not this stale partial text.
			emit(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "stale partial"}})
			if mode != "no-final" && mode != "abort" {
				reason := "stop"
				if mode == "failure" {
					reason = "error"
				}
				message(reason, "fixture final output")
			}
			if mode == "unterminated" {
				os.Exit(0)
			}
			emit(map[string]any{"type": "agent_settled", "aborted": mode == "abort"})
			if mode == "fast" {
				emit(response)
			}
		}
	}
	if mode == "hang-close" {
		for {
			time.Sleep(time.Second)
		}
	}
	os.Exit(0)
}

func fixtureFactory(t *testing.T, mode string) (*Factory, string) {
	t.Helper()
	t.Setenv("DEVBRIDGE_PI_FIXTURE", mode)
	directory := t.TempDir()
	capture := filepath.Join(directory, "requests.jsonl")
	t.Setenv("DEVBRIDGE_PI_CAPTURE", capture)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(directory, "pi-fixture")
	quoted := strings.ReplaceAll(binary, "'", "'\\''")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec '"+quoted+"' -test.run '^TestPiRPCFixture$' -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return &Factory{PiPath: launcher}, capture
}

func fixtureSession(t *testing.T, mode string) (*Session, string, string) {
	t.Helper()
	factory, capture := fixtureFactory(t, mode)
	s := factory.newSession()
	t.Cleanup(func() { _ = s.Close() })
	id, err := s.StartThread(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s, id, capture
}

func TestPiCapabilityDeclarationIsTruthful(t *testing.T) {
	var session any = &Session{}
	for name, supported := range map[string]bool{
		"resume": implements[agent.ThreadResumer](session), "schema": implements[agent.StructuredSession](session),
		"schema-started": implements[agent.StructuredTurnStarter](session), "naming": implements[agent.ThreadNamer](session),
		"runtime-binding": implements[agent.RuntimeBinder](session),
		"host-tools":      implements[agent.ToolConfigurer](session),
	} {
		if supported {
			t.Errorf("unsupported capability %s advertised", name)
		}
	}
	d := (&Factory{}).AgentDescriptor()
	if d.ID != "pi" || !reflect.DeepEqual(d.Capabilities, []agent.Capability{agent.CapabilityCooperativePolicy, agent.CapabilityTurnStarted, agent.CapabilityInterrupt}) {
		t.Fatalf("descriptor=%+v", d)
	}
	for _, ceiling := range []codex.AuthorizationCeiling{codex.AuthorizationReadOnly, codex.AuthorizationWorkspaceWrite} {
		s, err := (&Factory{}).StartForOperation(context.Background(), codexconfig.Config{}, "", "", ceiling)
		if s != nil || !errors.Is(err, agent.ErrCapabilityUnsupported) {
			t.Fatalf("ceiling %s: session=%v error=%v", ceiling, s, err)
		}
	}
}

func implements[T any](value any) bool { _, ok := value.(T); return ok }

func TestPiSessionIdentityAndFrozenPrompt(t *testing.T) {
	s, id, capture := fixtureSession(t, "success")
	if id != "fixture-session" {
		t.Fatal(id)
	}
	if _, err := s.StartThread(context.Background(), t.TempDir()); err == nil {
		t.Fatal("changed cwd accepted")
	}
	if _, err := s.StartTurn(context.Background(), "foreign-session", "not sent"); err == nil {
		t.Fatal("foreign thread accepted")
	}
	prompt := "Frozen natural-language instruction.\nDo not append a schema."
	var started string
	result, err := s.StartTurnWithStarted(context.Background(), id, prompt, func(turn string) error { started = turn; return nil })
	if err != nil || result.Message != "fixture final output" || result.TurnID == "" || started != result.TurnID || started == id || result.TokenUsage != nil {
		t.Fatalf("result=%+v started=%q err=%v", result, started, err)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var sent []rpcCommand
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var command rpcCommand
		if json.Unmarshal([]byte(line), &command) != nil {
			t.Fatal(line)
		}
		if command.Type == "prompt" {
			sent = append(sent, command)
		}
	}
	if len(sent) != 1 || sent[0].Message != prompt || sent[0].ID != result.TurnID {
		t.Fatalf("sent=%+v", sent)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartThread(context.Background(), s.cwd); err == nil {
		t.Fatal("closed session resumed")
	}
}

func TestPiTerminalOutcomesAreNotTransportSuccess(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		state   agent.TurnState
		wantErr bool
	}{
		{"success", agent.TurnCompleted, false}, {"fast", agent.TurnCompleted, false}, {"retry", agent.TurnCompleted, false},
		{"failure", agent.TurnFailed, true}, {"abort", agent.TurnInterrupted, true}, {"no-final", agent.TurnTerminal, true},
		{"handled", "", true}, {"queued", "", true}, {"missing-disposition", "", true}, {"rejected", "", true},
		{"exit", "", true}, {"unterminated", "", true}, {"malformed", "", true}, {"overflow", "", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			s, id, _ := fixtureSession(t, tc.mode)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			result, err := s.StartTurn(ctx, id, "fixture prompt")
			if (err != nil) != tc.wantErr {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			var terminal *agent.TerminalError
			if tc.state != "" && tc.wantErr {
				if !errors.As(err, &terminal) || terminal.State != tc.state || !terminal.Matches(id, result.TurnID) {
					t.Fatalf("terminal=%+v err=%v", terminal, err)
				}
			} else if errors.As(err, &terminal) {
				t.Fatalf("transport/no-run failure falsely terminal: %+v", terminal)
			}
		})
	}
}

func TestPiStartedCallbackFailureIsNotSwallowedOrRetried(t *testing.T) {
	s, id, capture := fixtureSession(t, "success")
	want := errors.New("could not persist started receipt")
	result, err := s.StartTurnWithStarted(context.Background(), id, "exact instruction", func(string) error { return want })
	if !errors.Is(err, want) || result.TurnID == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := s.StartTurn(context.Background(), id, "must not dispatch"); err == nil {
		t.Fatal("unknown prompt retried")
	}
	raw, _ := os.ReadFile(capture)
	if strings.Count(string(raw), `"type":"prompt"`) != 1 {
		t.Fatalf("unexpected prompt requests: %s", raw)
	}
}

func TestPiSelectionHandshakeFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, mode, model, thinking string }{
		{"missing identity", "missing-session", "", ""}, {"different model", "success", "another-model", ""}, {"clamped thinking", "success", "fixture/fixture-model", "high"},
		{"busy runtime", "busy", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory, _ := fixtureFactory(t, tc.mode)
			factory.Model = tc.model
			factory.ThinkingLevel = tc.thinking
			s := factory.newSession()
			defer s.Close()
			if _, err := s.StartThread(context.Background(), t.TempDir()); err == nil {
				t.Fatal("invalid handshake accepted")
			}
		})
	}
}

func TestPiCloseHasBoundedHonestFailure(t *testing.T) {
	s, _, _ := fixtureSession(t, "hang-close")
	started := time.Now()
	err := s.Close()
	if err == nil || time.Since(started) > 5*time.Second {
		t.Fatalf("close=%v duration=%s", err, time.Since(started))
	}
	if again := s.Close(); again == nil || again.Error() != err.Error() {
		t.Fatalf("repeat close=%v first=%v", again, err)
	}
}

func TestPiConcurrentTurnAndCanceledPromptDoNotReplay(t *testing.T) {
	s, id, capture := fixtureSession(t, "delay")
	started := make(chan struct{})
	finished := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, err := s.StartTurnWithStarted(ctx, id, "only accepted prompt", func(string) error { close(started); return nil })
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("prompt not accepted")
	}
	if _, err := s.StartTurn(context.Background(), id, "concurrent prompt"); err == nil {
		t.Fatal("concurrent turn accepted")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not return")
	}
	if _, err := s.StartTurn(context.Background(), id, "replay after cancellation"); err == nil {
		t.Fatal("canceled unknown outcome replayed")
	}
	raw, _ := os.ReadFile(capture)
	if strings.Count(string(raw), `"type":"prompt"`) != 1 {
		t.Fatalf("unexpected prompts: %s", raw)
	}
}

func TestPiFactoryLaunchOverridesCannotStartHiddenWork(t *testing.T) {
	for _, args := range [][]string{{"--session", "prior-session"}, {"--model", "another-model"}, {"hidden initial prompt"}} {
		client, err := Start(context.Background(), Config{PiPath: "/must/not/execute", ExtraArgs: args})
		if client != nil || err == nil || !strings.Contains(err.Error(), "extra arguments") {
			t.Fatalf("args=%v client=%v err=%v", args, client, err)
		}
	}
}

func TestPiCanceledWriteCannotHangOrBecomeTerminal(t *testing.T) {
	s, id, _ := fixtureSession(t, "block-input")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := s.StartTurn(ctx, id, strings.Repeat("x", 512<<10))
	var terminal *agent.TerminalError
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &terminal) || result.TurnID != "" || time.Since(started) > time.Second {
		t.Fatalf("result=%+v error=%v elapsed=%s", result, err, time.Since(started))
	}
}

func TestPiUnownedEventsCannotCompleteANewPrompt(t *testing.T) {
	s, id, capture := fixtureSession(t, "unsolicited")
	_, err := s.StartTurn(context.Background(), id, "must not send")
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("error=%v", err)
	}
	raw, _ := os.ReadFile(capture)
	if strings.Contains(string(raw), `"type":"prompt"`) {
		t.Fatalf("sent prompt after unowned events: %s", raw)
	}
}

func TestPiInterruptRequiresExactActiveTurnAndObservedSettlement(t *testing.T) {
	s, id, capture := fixtureSession(t, "await-abort")
	started := make(chan string, 1)
	finished := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		_, err := s.StartTurnWithStarted(ctx, id, "wait for interruption", func(turn string) error { started <- turn; return nil })
		finished <- err
	}()
	var turn string
	select {
	case turn = <-started:
	case <-ctx.Done():
		t.Fatal("no started callback")
	}
	if err := s.InterruptTurn(ctx, "foreign", turn); err == nil {
		t.Fatal("foreign session interrupted")
	}
	if err := s.InterruptTurn(ctx, id, "foreign"); err == nil {
		t.Fatal("foreign turn interrupted")
	}
	if err := s.InterruptTurn(ctx, id, turn); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		var terminal *agent.TerminalError
		if !errors.As(err, &terminal) || terminal.State != agent.TurnInterrupted || !terminal.Matches(id, turn) {
			t.Fatalf("terminal=%+v error=%v", terminal, err)
		}
	case <-ctx.Done():
		t.Fatal("abort did not settle")
	}
	if err := s.InterruptTurn(ctx, id, turn); err == nil {
		t.Fatal("completed turn interrupted")
	}
	raw, _ := os.ReadFile(capture)
	text := string(raw)
	clearIndex, abortIndex := strings.Index(text, `"type":"clear_queue"`), strings.Index(text, `"type":"abort"`)
	if clearIndex < 0 || abortIndex <= clearIndex || strings.Count(text, `"type":"abort"`) != 1 {
		t.Fatalf("interruption requests=%s", raw)
	}
}
