package codexagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexagent"
	"github.com/lazyun/devbridge/internal/codexusage"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepermission"
)

type fakeClient struct {
	start                                                             codex.ThreadOptions
	resume                                                            codex.ThreadResumeOptions
	turn                                                              codex.TurnOptions
	rejoin                                                            codex.TurnRejoinOptions
	result                                                            codex.TurnResult
	err                                                               error
	startCalls, rejoinCalls, closeCalls                               int
	invokeStarted                                                     bool
	namedThread, name, interruptedThread, interruptedTurn, resolvedID string
	approval                                                          codex.ApprovalRequest
	decision                                                          codex.ApprovalDecision
}

func (f *fakeClient) StartThread(_ context.Context, v codex.ThreadOptions) (codex.Thread, error) {
	f.start = v
	return codex.Thread{ID: "new-thread"}, f.err
}
func (f *fakeClient) ResumeThread(_ context.Context, v codex.ThreadResumeOptions) (codex.Thread, error) {
	f.resume = v
	return codex.Thread{ID: v.ThreadID}, f.err
}
func (f *fakeClient) SetThreadName(_ context.Context, id, name string) error {
	f.namedThread = id
	f.name = name
	return f.err
}
func (f *fakeClient) StartTurn(_ context.Context, v codex.TurnOptions) (codex.TurnResult, error) {
	f.startCalls++
	f.turn = v
	if f.invokeStarted {
		if err := v.OnStarted(v.ThreadID, f.result.TurnID); err != nil {
			return f.result, err
		}
	}
	return f.result, f.err
}
func (f *fakeClient) RejoinTurn(_ context.Context, v codex.TurnRejoinOptions) (codex.TurnResult, error) {
	f.rejoinCalls++
	f.rejoin = v
	return f.result, f.err
}
func (f *fakeClient) Close() error { f.closeCalls++; return f.err }
func (f *fakeClient) InterruptTurn(_ context.Context, thread, turn string) error {
	f.interruptedThread = thread
	f.interruptedTurn = turn
	return f.err
}
func (f *fakeClient) NextApproval(context.Context) (codex.ApprovalRequest, error) {
	return f.approval, f.err
}
func (f *fakeClient) ResolveApproval(_ context.Context, id string, v codex.ApprovalDecision) error {
	f.resolvedID = id
	f.decision = v
	return f.err
}
func (*fakeClient) Executable() string { return "/verified/native" }

func configuredSession(f *fakeClient) (*codexagent.Session, agent.RuntimeBinding, []agent.ToolSpec) {
	s := codexagent.NewSession(f, codexagent.Options{Model: "frozen-model", ReasoningEffort: "high", ServiceTier: "fast", AuthorizationCeiling: codex.AuthorizationReadOnly})
	binding := agent.RuntimeBinding{RuntimeWorkspaceRoots: []string{"/source", "/scratch"}, WritableRoots: []string{"/scratch"}, TaskPermissions: &runtimepermission.TaskPermissions{ContentHash: "frozen-permissions"}}
	specs := []agent.ToolSpec{{Type: "function", Name: "core_observation", Description: "exact description", InputSchema: json.RawMessage(` {"type":"object"} `), DeferLoading: true}}
	s.SetRuntimeBinding(binding)
	s.SetDynamicTools(specs)
	s.SetUsageRunID("original-run")
	return s, binding, specs
}

func TestSessionForwardsFrozenThreadAndResumeInputs(t *testing.T) {
	f := &fakeClient{}
	s, binding, specs := configuredSession(f)
	ctx := context.Background()
	wantTools := []codex.DynamicToolSpec{codex.DynamicToolSpec(specs[0])}
	// Registrations have the same value-copy behavior as the original adapter.
	specs[0].Name = "changed caller registration"
	id, err := s.StartThread(ctx, "/source")
	wantStart := codex.ThreadOptions{CWD: "/source", RuntimeBinding: codex.RuntimeBinding(binding), DynamicTools: wantTools, Model: "frozen-model", ReasoningEffort: "high", ServiceTier: "fast"}
	if err != nil || id != "new-thread" || !reflect.DeepEqual(f.start, wantStart) {
		t.Fatalf("thread=%q err=%v options=%+v", id, err, f.start)
	}
	baseline := &run.TokenCounts{InputTokens: 11, CachedInputTokens: 2, CacheWriteInputTokens: 3, OutputTokens: 17, ReasoningOutputTokens: 5, TotalTokens: 28}
	id, err = s.ResumeThread(ctx, "original-thread", "/source", baseline)
	wantResume := codex.ThreadResumeOptions{ThreadID: "original-thread", CWD: "/source", RuntimeBinding: codex.RuntimeBinding(binding), DynamicTools: wantTools, Model: "frozen-model", ReasoningEffort: "high", ServiceTier: "fast", UsageBaseline: &codex.TokenCounts{InputTokens: 11, CachedInputTokens: 2, CacheWriteInputTokens: 3, OutputTokens: 17, ReasoningOutputTokens: 5, TotalTokens: 28}}
	if err != nil || id != "original-thread" || !reflect.DeepEqual(f.resume, wantResume) {
		t.Fatalf("resume=%q err=%v options=%+v", id, err, f.resume)
	}
	if _, err = s.ResumeThread(ctx, "original-thread", "/source", nil); err != nil || f.resume.UsageBaseline != nil {
		t.Fatalf("missing baseline became invented usage: %+v, %v", f.resume, err)
	}
}

func TestSessionForwardsExactTurnAndStartedFailure(t *testing.T) {
	f := &fakeClient{result: codex.TurnResult{TurnID: "original-turn"}, invokeStarted: true}
	s, binding, _ := configuredSession(f)
	prompt := " \t检查 e\u0301\r\n\n<$&`{}\\> \n "
	schema := json.RawMessage(" {\n\"type\":\"object\"} \n")
	failure := errors.New("durable turn binding failed")
	calls := 0
	result, err := s.StartTurnWithSchemaAndStarted(context.Background(), "original-thread", prompt, schema, func(id string) error {
		calls++
		if id != "original-turn" {
			t.Fatalf("wrong started identity: %q", id)
		}
		return failure
	})
	if !errors.Is(err, failure) || result.TurnID != "original-turn" || calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
	got := f.turn
	if got.ThreadID != "original-thread" || got.RunID != "original-run" || got.Prompt != prompt || string(got.OutputSchema) != string(schema) || got.Model != "frozen-model" || got.ReasoningEffort != "high" || got.ServiceTier == nil || *got.ServiceTier != "fast" || got.AuthorizationCeiling != codex.AuthorizationReadOnly || !reflect.DeepEqual(got.RuntimeBinding, codex.RuntimeBinding(binding)) {
		t.Fatalf("frozen turn inputs changed: %+v", got)
	}
	var taskErr *agent.ExecutionError
	if errors.As(err, &taskErr) {
		t.Fatal("persistence callback failure became terminal agent failure")
	}
}

func TestSessionBasicAndStructuredTurnsDoNotInventTierOrCallbacks(t *testing.T) {
	f := &fakeClient{invokeStarted: true}
	s := codexagent.NewSession(f, codexagent.Options{})
	if _, err := s.StartTurn(context.Background(), "thread", "prompt"); err != nil || f.turn.ServiceTier != nil || f.turn.OutputSchema != nil {
		t.Fatalf("basic turn=%+v err=%v", f.turn, err)
	}
	schema := json.RawMessage(`{"type":"string"}`)
	if _, err := s.StartTurnWithSchema(context.Background(), "thread", "prompt", schema); err != nil || string(f.turn.OutputSchema) != string(schema) {
		t.Fatalf("structured turn=%+v err=%v", f.turn, err)
	}
}

func observedResult() codex.TurnResult {
	code := 7
	duration := int64(123)
	start := time.Date(2026, 10, 30, 1, 2, 3, 4, time.UTC)
	end := start.Add(time.Second)
	window := int64(400000)
	return codex.TurnResult{TurnID: "original-turn", FinalMessage: "agent claim, not evidence", Commands: []codex.CommandExecution{{ItemID: "item-1", Command: "go test ./...", CWD: "/source", Status: "completed", Actions: []string{"test", "read"}, ExitCode: &code, Output: "exact output\n", OutputTruncated: true, DurationMS: &duration, StartedAt: &start, FinishedAt: &end}}, TokenUsage: &codex.TurnTokenUsage{Attribution: codexusage.RunUsageAttribution{RunID: "original-run", ThreadID: "original-thread", TurnID: "original-turn", StartEpoch: 2, EndEpoch: 3, Observed: true, Reliable: false, Complete: true, Reason: "boundary-reset"}, StartCumulative: codex.TokenCounts{InputTokens: 1, CachedInputTokens: 2, CacheWriteInputTokens: 3, OutputTokens: 4, ReasoningOutputTokens: 5, TotalTokens: 6}, EndCumulative: codex.TokenCounts{InputTokens: 7, CachedInputTokens: 8, CacheWriteInputTokens: 9, OutputTokens: 10, ReasoningOutputTokens: 11, TotalTokens: 12}, ModelContextWindow: &window, Reliable: false}}
}
func expectedCommand(v codex.CommandExecution) run.ObservedCommand {
	return run.ObservedCommand{ItemID: v.ItemID, Command: v.Command, CWD: v.CWD, Status: v.Status, Actions: v.Actions, ExitCode: v.ExitCode, Output: v.Output, OutputTruncated: v.OutputTruncated, DurationMillis: v.DurationMS, StartedAt: v.StartedAt, FinishedAt: v.FinishedAt}
}

func TestSessionPreservesObservedEvidenceAndUsage(t *testing.T) {
	original := observedResult()
	f := &fakeClient{result: original}
	s := codexagent.NewSession(f, codexagent.Options{})
	result, err := s.StartTurn(context.Background(), "original-thread", "prompt")
	want := agent.TurnResult{TurnID: original.TurnID, Message: original.FinalMessage, Commands: []run.ObservedCommand{expectedCommand(original.Commands[0])}, TokenUsage: &run.TokenUsageObservation{Attribution: &original.TokenUsage.Attribution, StartCumulative: run.TokenCounts{InputTokens: 1, CachedInputTokens: 2, CacheWriteInputTokens: 3, OutputTokens: 4, ReasoningOutputTokens: 5, TotalTokens: 6}, EndCumulative: run.TokenCounts{InputTokens: 7, CachedInputTokens: 8, CacheWriteInputTokens: 9, OutputTokens: 10, ReasoningOutputTokens: 11, TotalTokens: 12}, ModelContextWindow: original.TokenUsage.ModelContextWindow, Reliable: false}}
	if err != nil || !reflect.DeepEqual(result, want) {
		t.Fatalf("result=%+v usage=%+v err=%v", result, result.TokenUsage, err)
	}
	original.Commands[0].Actions[0] = "mutated"
	if result.Commands[0].Actions[0] != "test" {
		t.Fatal("observed action list aliases protocol result")
	}
	f.result = codex.TurnResult{}
	result, err = s.StartTurn(context.Background(), "original-thread", "prompt")
	if err != nil || result.TokenUsage != nil {
		t.Fatalf("missing usage became known: %+v %v", result, err)
	}
}

func TestSessionRejoinPreservesOriginalIdentityWithoutStartingTurn(t *testing.T) {
	f := &fakeClient{result: observedResult()}
	s, binding, specs := configuredSession(f)
	runtime := &agentruntime.Session{}
	result, err := s.RejoinTurn(context.Background(), "original-thread", "original-turn", runtime)
	v := f.rejoin
	if err != nil || f.startCalls != 0 || f.rejoinCalls != 1 || v.ThreadID != "original-thread" || v.TurnID != "original-turn" || v.RunID != "original-run" || v.RuntimeSession != runtime || v.Prompt != "" || v.OutputSchema != nil || v.OnStarted != nil || v.Model != "frozen-model" || v.ReasoningEffort != "high" || v.ServiceTier == nil || *v.ServiceTier != "fast" || v.AuthorizationCeiling != codex.AuthorizationReadOnly || !reflect.DeepEqual(v.RuntimeBinding, codex.RuntimeBinding(binding)) || !reflect.DeepEqual(v.DynamicTools, []codex.DynamicToolSpec{codex.DynamicToolSpec(specs[0])}) {
		t.Fatalf("rejoin=%+v err=%v starts=%d", v, err, f.startCalls)
	}
	if result.TurnID != f.result.TurnID || result.Message != f.result.FinalMessage || !reflect.DeepEqual(result.Commands, []run.ObservedCommand{expectedCommand(f.result.Commands[0])}) {
		t.Fatalf("rejoin evidence changed: %+v", result)
	}
	// Deliberately preserve existing recovery behavior: the original adapter did
	// not reconstruct token accounting from a rejoined transport's result.
	if result.TokenUsage != nil {
		t.Fatalf("rejoin fabricated accounting: %+v", result.TokenUsage)
	}
}

func TestSessionErrorClassificationRetainsProtocolCauseAndEvidence(t *testing.T) {
	terminal := &codex.TurnError{ThreadID: "original-thread", TurnID: "original-turn", Status: "interrupted", Message: "stopped"}
	for _, operation := range []string{"start", "rejoin"} {
		for _, tc := range []struct {
			name     string
			err      error
			terminal bool
		}{{"terminal", fmt.Errorf("wrapped: %w", terminal), true}, {"transport", codex.ErrAppServerExited, false}} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				f := &fakeClient{result: observedResult(), err: tc.err}
				s := codexagent.NewSession(f, codexagent.Options{})
				var result agent.TurnResult
				var err error
				if operation == "start" {
					result, err = s.StartTurn(context.Background(), "original-thread", "prompt")
				} else {
					result, err = s.RejoinTurn(context.Background(), "original-thread", "original-turn", nil)
				}
				var taskErr *agent.ExecutionError
				if errors.As(err, &taskErr) != tc.terminal || !errors.Is(err, tc.err) || result.TurnID != "original-turn" || len(result.Commands) != 1 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if tc.terminal {
					var protocolErr *codex.TurnError
					if !errors.As(err, &protocolErr) || protocolErr != terminal {
						t.Fatal("exact terminal protocol cause lost")
					}
				}
			})
		}
	}
}

func TestSessionPassthroughOperationsPreserveIdentityAndErrors(t *testing.T) {
	sentinel := errors.New("transport failure")
	f := &fakeClient{err: sentinel, approval: codex.ApprovalRequest{ID: "exact-request", ThreadID: "thread", TurnID: "turn", RequestPayload: json.RawMessage(`{"exact":true}`)}}
	s := codexagent.NewSession(f, codexagent.Options{})
	ctx := context.Background()
	if err := s.SetThreadName(ctx, "thread", "exact name"); err != sentinel || f.namedThread != "thread" || f.name != "exact name" {
		t.Fatal("name passthrough changed")
	}
	if err := s.InterruptTurn(ctx, "thread", "turn"); err != sentinel || f.interruptedThread != "thread" || f.interruptedTurn != "turn" {
		t.Fatal("interrupt identity changed")
	}
	request, err := s.NextApproval(ctx)
	if err != sentinel || !reflect.DeepEqual(request, f.approval) {
		t.Fatal("approval request changed")
	}
	decision := codex.ApprovalDecision{Type: codex.DecisionDecline, Permissions: json.RawMessage(`{"network":false}`)}
	if err = s.ResolveApproval(ctx, "exact-request", decision); err != sentinel || f.resolvedID != "exact-request" || !reflect.DeepEqual(f.decision, decision) {
		t.Fatal("approval resolution changed")
	}
	if err = s.Close(); err != sentinel || f.closeCalls != 1 {
		t.Fatal("close error changed")
	}
	if s.Executable() != "/verified/native" {
		t.Fatal("executable identity changed")
	}
}

func TestSessionClearingToolsPreservesAbsentRegistrations(t *testing.T) {
	for _, specs := range [][]agent.ToolSpec{nil, {}} {
		f := &fakeClient{}
		s, _, _ := configuredSession(f)
		s.SetDynamicTools(specs)
		if _, err := s.StartThread(context.Background(), "/source"); err != nil || f.start.DynamicTools != nil {
			t.Fatalf("cleared registrations became present: %+v %v", f.start.DynamicTools, err)
		}
		if _, err := s.ResumeThread(context.Background(), "original-thread", "/source", nil); err != nil || f.resume.DynamicTools != nil {
			t.Fatalf("resume retained registrations: %+v %v", f.resume.DynamicTools, err)
		}
	}
}
