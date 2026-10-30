package controlapi_test

// This opt-in gate composes real Core with explicitly trusted test-only platform
// and Agent dependencies. It does not attest production native qualification,
// model behavior, or host sandbox enforcement. Bootstrap uses real typed Core
// services; after initial Review admission, business writes use MCP HTTP.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime/pprof"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Instrumented functional gate only: production adapters retain their 15s
// default. The larger bounded handler budget is not latency qualification.
const (
	continuationRequestTimeout  = 60 * time.Second
	continuationPipelineTimeout = 90 * time.Second
	continuationContextTimeout  = 5 * time.Minute
)

type continuationGate struct {
	t                                                       *testing.T
	f                                                       *upperLifecycleFixture
	ctx                                                     context.Context
	gpt, local                                              *sdkmcp.ClientSession
	closeWire                                               func()
	evidence, launcher                                      string
	checks                                                  []string
	transcript                                              *os.File
	revision, executionID, implementationID, verificationID string
	frozenRuns                                              []string
	frozenFiles                                             map[string]string
	analysis                                                runanalysis.Source
	analysisInput                                           runanalysis.Input
	analysisReceipt                                         runanalysis.Receipt
	originalWork                                            task.WorkSpec
	originalVerification                                    task.VerificationSpec
	originalTargets                                         []workset.Target
	frozenDefinition                                        mcpserver.SurfaceDefinitionSource
	held                                                    task.Hold
	implementationSession                                   *execution.RunSession
	epochs                                                  []string
}

func TestSimulatedContinuationGate(t *testing.T) {
	if os.Getenv("DEVBRIDGE_SIMULATED_CONTINUATION") != "1" {
		t.Skip("explicit trusted Agent gate; run scripts/test-simulated-continuation.sh")
	}
	s := &continuationGate{t: t}
	evidence := os.Getenv("DEVBRIDGE_CONTINUATION_EVIDENCE_DIR")
	if evidence == "" {
		evidence = t.TempDir()
	}
	var err error
	s.evidence, err = filepath.Abs(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(s.evidence, 0700); err != nil {
		t.Fatal(err)
	}
	s.transcript, err = os.OpenFile(filepath.Join(s.evidence, "mcp.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	previousLogger := observability.Default()
	logger := observability.New(observability.Options{Process: "devbridge-test-only-continuation", Directory: s.evidence, Filename: "core.jsonl"})
	observability.SetDefault(logger)
	t.Cleanup(func() { observability.SetDefault(previousLogger); logger.Close() })
	started := time.Now().UTC()
	completed := false
	defer func() {
		_ = s.transcript.Close()
		status := "FAILED"
		if completed && !t.Failed() {
			status = "PASSED"
		}
		s.save("report.json", map[string]any{"schemaVersion": 1, "status": status, "startedAt": started, "finishedAt": time.Now().UTC(), "checks": s.checks, "trustBoundary": "TEST_ONLY_PROTOCOL_FIXTURE_V1: injected trusted resolver and deterministic Agent, not production native qualification", "bootstrap": "real typed project/session/confirmation/coordinator services; schema-only database template; no seeded business authority", "wire": "production GPT and Local MCP HTTP adapters after initial UPPER Review admission", "requestTimeoutSeconds": continuationRequestTimeout.Seconds(), "pipelineTimeoutSeconds": continuationPipelineTimeout.Seconds(), "postBootstrapTimeoutSeconds": continuationContextTimeout.Seconds(), "notCovered": []string{"real model", "native artifact qualification", "host sandbox enforcement", "unknown journal outcome or response-loss injection", "native conversation successor/resume", "acceptance, commit or merge"}})
	}()
	home := t.TempDir()
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "DEVBRIDGE_CODING_V1_ASK_USER", "DEVBRIDGE_CODING_V1_REVIEW_MUTATE", "DEVBRIDGE_CODING_V1_INTERRUPT_ONCE", "DEVBRIDGE_TEST_VERIFICATION_FAILURE", "DEVBRIDGE_TEST_REQUIRED_CUSTOM", "DEVBRIDGE_CODING_DUPLICATE_VERIFICATION_ROOT", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		t.Setenv(key, "")
	}
	// Remove all ambient Git redirection/config/hook-template inputs. Setenv
	// registers restoration before Unsetenv, including on failure.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, "")
			if e := os.Unsetenv(key); e != nil {
				t.Fatal(e)
			}
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("OTEL_SDK_DISABLED", "true")
	s.f = newUpperLifecycleFixtureScoped(t, "data-instance-local")
	var cancel context.CancelFunc
	signalContext, stop := signal.NotifyContext(s.f.ctx, os.Interrupt, syscall.SIGTERM)
	t.Cleanup(stop)
	s.ctx, cancel = context.WithTimeout(signalContext, continuationContextTimeout)
	t.Cleanup(cancel)
	t.Cleanup(func() {
		if s.closeWire != nil {
			s.closeWire()
		}
	})
	s.launcher = filepath.Join(home, "test-only-agent")
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nexec /usr/bin/env -i PATH=/usr/bin:/bin HOME=" + quote(home) + " CODEX_HOME=" + quote(home) + " DEVBRIDGE_CODING_V1_HELPER=1 DEVBRIDGE_TEST_FILE_VERIFICATION=1 DEVBRIDGE_TEST_UNIQUE_TURNS=1 DEVBRIDGE_CODING_V1_WIRE_LOG=" + quote(filepath.Join(s.evidence, "agent-wire.jsonl")) + " DEVBRIDGE_CODING_V1_RESPONSE_LOG=" + quote(filepath.Join(s.evidence, "agent-responses.jsonl")) + " " + quote(binary) + " -test.run=^TestMultiRepositoryCodingV1AppServerHelper$\n"
	if err = os.WriteFile(s.launcher, []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	s.install()
	s.connect()
	s.prepareExecution()
	s.awaitVerification()
	s.exerciseChoices()
	completed = true
}

func (s *continuationGate) check(ok bool, name string, detail any) {
	s.t.Helper()
	if !ok {
		s.t.Fatalf("%s: %+v", name, detail)
	}
	s.checks = append(s.checks, name)
}
func (s *continuationGate) save(name string, v any) {
	s.t.Helper()
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		s.t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(s.evidence, name), append(b, '\n'), 0600); e != nil {
		s.t.Fatal(e)
	}
}
func gateDecode(t *testing.T, v any, out any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e == nil {
		e = json.Unmarshal(b, out)
	}
	if e != nil {
		t.Fatal(e)
	}
}
func gateMap(t *testing.T, v any) map[string]any {
	t.Helper()
	var m map[string]any
	gateDecode(t, v, &m)
	return m
}
func (s *continuationGate) install() {
	f := s.f
	configs, e := f.plane.CodexConfigs.List(f.ctx)
	if e != nil {
		s.t.Fatal(e)
	}
	for _, v := range configs {
		s.check(v.CodexPath == "", "no ambient executable override", v.ID)
	}
	f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: s.launcher, AgentRuntime: f.plane.Runs.AgentRuntime}, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
		return codex.DynamicToolCallResponse{Success: false}, nil
	}}
	installProtocolFixturePlatform(s.t, f)
}
func (s *continuationGate) connect() {
	service, e := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test-only-continuation", Ready: true}, s.f.plane.AIContext, s.f.facade)
	if e != nil {
		s.t.Fatal(e)
	}
	g, e := mcpserver.New(mcpserver.Config{RequestTimeout: continuationRequestTimeout}, service)
	if e != nil {
		s.t.Fatal(e)
	}
	l, e := localmcp.New(localmcp.Config{RequestTimeout: continuationRequestTimeout}, service)
	if e != nil {
		s.t.Fatal(e)
	}
	gh, lh := httptest.NewServer(g.Handler()), httptest.NewServer(l.Handler())
	open := func(host *httptest.Server, name string) *sdkmcp.ClientSession {
		v, e := sdkmcp.NewClient(&sdkmcp.Implementation{Name: name, Version: "test-only"}, nil).Connect(s.ctx, &sdkmcp.StreamableClientTransport{Endpoint: host.URL + "/mcp", HTTPClient: host.Client(), DisableStandaloneSSE: true, MaxRetries: -1}, nil)
		if e != nil {
			s.t.Fatal(e)
		}
		return v
	}
	s.gpt, s.local = open(gh, "simulated-gpt"), open(lh, "simulated-local-agent")
	s.closeWire = func() { _ = s.gpt.Close(); _ = s.local.Close(); gh.Close(); lh.Close() }
}
func (s *continuationGate) call(actor, tool string, input any, wantError string) map[string]any {
	s.t.Helper()
	client := s.gpt
	if actor == "local" {
		client = s.local
	}
	startedAt := time.Now().UTC()
	result, e := client.CallTool(s.ctx, &sdkmcp.CallToolParams{Name: tool, Arguments: input})
	entry := map[string]any{"startedAt": startedAt, "finishedAt": time.Now().UTC(), "durationMillis": time.Since(startedAt).Milliseconds(), "actor": actor, "tool": tool, "input": input, "result": result}
	if e != nil {
		entry["transportError"] = e.Error()
	}
	if e2 := json.NewEncoder(s.transcript).Encode(entry); e2 != nil {
		s.t.Fatal(e2)
	}
	if e != nil {
		s.t.Fatalf("%s transport/schema error: %v", tool, e)
	}
	raw, _ := json.Marshal(result)
	if wantError != "" {
		var refused c.Result
		gateDecode(s.t, result.StructuredContent, &refused)
		s.check(result.IsError && refused.Failure != nil && refused.Failure.Code == wantError, tool+" rejects "+wantError, string(raw))
	} else if result.IsError && actor == "local" && tool == "execution_start" {
		envelope := gateMap(s.t, result.StructuredContent)
		var out mcpserver.SurfaceExecutionStartOutput
		var expected controlapi.ApprovedTaskStartInput
		gateDecode(s.t, envelope["result"], &out)
		gateDecode(s.t, input, &expected)
		s.check(exactPreparingContinuationAdmission(out, expected, s.f.project, s.f.plane.DB.RuntimeEpoch()), "durable exact start reservation; handoff still PREPARING (not execution success)", string(raw))
	} else {
		s.check(!result.IsError, tool+" succeeds", string(raw))
	}
	m := gateMap(s.t, result.StructuredContent)
	if v, ok := m["result"].(map[string]any); ok {
		return v
	}
	return m
}

// Only this exact durable admission may be observed to completion after an
// IsError response. No generic error allowance, new request or redispatch.
func exactPreparingContinuationAdmission(out mcpserver.SurfaceExecutionStartOutput, in controlapi.ApprovedTaskStartInput, project, epoch string) bool {
	r, h, p, v := out.Receipt, out.Handoff, out.Proposal, out.Run
	return out.ReservationCommitted && out.RefreshRequired && out.DispatchError != "" && r != nil &&
		r.ProjectID == project && r.TaskID == in.TaskID && r.TaskDefinitionRevisionID == in.ExpectedTarget.TaskDefinitionRevisionID &&
		r.OriginalExecutionID != "" && r.OriginalExecutionID == p.ID && r.OriginalRunID != "" && r.OriginalRunID == v.ID &&
		p.ProjectID == project && p.TaskID == in.TaskID && p.AISessionID == in.AISessionID && p.Status == "CONSUMED" && p.TaskDefinitionRevisionID == in.ExpectedTarget.TaskDefinitionRevisionID && p.ApprovedWorksetHash == in.ExpectedTarget.ApprovedWorksetHash &&
		v.ProjectID == project && v.TaskID == in.TaskID && v.ExecutionProposalID == p.ID &&
		h.State == executionproposal.HandoffPreparing && h.Code == "HANDOFF_IN_PROGRESS" && h.RecordExists && h.RecordedState == executionproposal.HandoffPreparing && h.RecordedCode == "HANDOFF_IN_PROGRESS" && h.RecordEpochMatchesRun && h.RunEpochCurrent &&
		h.TaskID == in.TaskID && h.ExecutionID == p.ID && h.RunID == v.ID && h.RuntimeEpoch == epoch && h.RecordedRuntimeEpoch == epoch && epoch != "" && h.OwnerKind == "IN_PROCESS_SUPERVISOR"
}

func (s *continuationGate) readWorkspace() mcpserver.WorkspaceGetResult {
	var v mcpserver.WorkspaceGetResult
	gateDecode(s.t, s.call("gpt", "workspace_get", map[string]any{"aiSessionId": s.f.session, "taskId": s.f.task, "featureAreaId": s.f.area, "include": []string{"TASK", "RUNTIME_CONFIG"}}, ""), &v)
	return v
}
func (s *continuationGate) workspace() mcpserver.WorkspaceGetResult {
	v := s.readWorkspace()
	s.check(v.TaskView != nil && v.TaskView.ID == s.f.task, "exact Task public view", v)
	return v
}
func (s *continuationGate) prepare(input map[string]any) c.Intent {
	var result c.Result
	gateDecode(s.t, s.call("gpt", "execution_prepare", input, ""), &result)
	s.check(result.Intent != nil && result.Intent.State == "PENDING", "prepare remains pending", result)
	return *result.Intent
}
func (s *continuationGate) commitInput(in c.Intent, request string) c.CommitInput {
	return c.CommitInput{AISessionID: s.f.session, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, Action: in.Action, RequestID: request, ContentHash: in.ContentHash, VariantID: in.Content.Variants[0].ID}
}
func (s *continuationGate) commit(in c.CommitInput) c.Intent {
	var v c.Result
	gateDecode(s.t, s.call("fixture_user", "confirmation_commit", in, ""), &v)
	s.check(v.Intent != nil && v.Intent.State == "COMPLETED", "explicit fixture-user consumed confirmation", v)
	return *v.Intent
}
func (s *continuationGate) prepareExecution() {
	f := s.f
	targets := workset.CloneTargets(f.targets)
	refs := []review.EvidenceRef{}
	for i, target := range targets {
		read := s.call("local", "file_read", map[string]any{"reviewTarget": f.access.Target, "repositoryId": target.RepositoryID, "memberId": target.MemberID, "memberVersion": target.MemberVersion, "path": "same.txt"}, "")
		var ref review.EvidenceRef
		gateDecode(s.t, read, &ref)
		ref.RepositoryID, ref.MemberID = target.RepositoryID, target.MemberID
		s.check(ref.EvidenceID != "" && len(ref.Digest) == 64, "real UPPER read evidence", read)
		refs = append(refs, ref)
		targets[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Exercise real continuation checkpoints with trusted simulated Agent", Constraints: []string{"only owned fixture members; do not commit"}, CodexInstruction: "Change both exact same.txt files; deterministic test-only Agent, no model.\n", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "Both exact fixture members", Checks: []task.VerificationCheck{}, AcceptanceCriteria: []string{"Both fixture same.txt files exist; substantive correctness is not asserted"}, CodexInstruction: "Run the two exact test -f same.txt checks without writing."}
	for _, target := range targets {
		verification.Checks = append(verification.Checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: target.RepositoryID, RepositoryIDs: []string{target.RepositoryID}, WorkingDirectory: "."})
	}
	s.originalWork = task.CloneWorkSpec(work)
	s.originalVerification = task.CloneVerificationSpec(verification)
	s.originalTargets = workset.CloneTargets(targets)
	report := review.SubmitInput{UpperTarget: f.access.Target, RequestID: "simulated-coding-review", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, Summary: "Test-only deterministic Coding proposal", Disposition: review.DispositionApprovalReady, Coverage: []string{"both owned same.txt files"}, Findings: []review.Finding{}, Limitations: []string{"trusted test-only Agent; not model or native qualification"}, RecommendedNextSteps: []string{"explicit fixture-user approval then separate start"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}}
	s.call("local", "review_result_submit", report, "")
	v := s.workspace().TaskView
	// Read-only typed capability lookup; business approval still crosses MCP.
	body := exactReviewApprovalBody(s.t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE")
	approval := s.prepare(map[string]any{"aiSessionId": f.session, "requestId": "approve-coding", "purpose": "EXECUTION", "action": "APPROVE", "taskActionGeneration": v.ActionNode.Generation, "execution": body, "variants": []any{map[string]any{"id": "approve", "label": "Approve", "summary": "Explicit fixture user approves RUN, without start", "implementationMode": work.ImplementationMode, "repositoryTargets": targets, "primaryMemberId": targets[0].MemberID, "workSpec": work, "verificationSpec": verification}}})
	s.commit(s.commitInput(approval, "approve-coding-commit"))
	view := s.workspace()
	s.check(view.TaskView.Phase == task.PhaseImplementationReady && view.TaskView.CurrentExecution == nil, "approval does not dispatch", view.TaskView)
	s.check(view.TaskView.ExecutionStartTarget != nil && view.RuntimeOptions != nil && len(view.RuntimeOptions.BackendCandidates) > 0, "public exact start target and explicit inventory", view)
	s.check(view.TaskSources != nil && view.TaskSources.DefinitionStatus == "AVAILABLE" && view.TaskSources.SelectedDefinition != nil, "public frozen approved definition available", view.TaskSources)
	gateDecode(s.t, view.TaskSources.SelectedDefinition, &s.frozenDefinition)
	s.check(reflect.DeepEqual(s.frozenDefinition.Definition.WorkSpec, s.originalWork) && reflect.DeepEqual(s.frozenDefinition.Definition.VerificationSpec, s.originalVerification) && reflect.DeepEqual(s.frozenDefinition.Definition.RepositoryTargets, s.originalTargets), "approval preserves independently authored specs and target set", s.frozenDefinition)
	backend := view.RuntimeOptions.BackendCandidates[0]
	runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	start := controlapi.ApprovedTaskStartInput{AISessionID: f.session, TaskID: f.task, RequestID: "start-coding-once", ExpectedTarget: *view.TaskView.ExecutionStartTarget, RuntimeConfig: runtime, Backend: codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}
	wireStart := gateMap(s.t, start)
	wireStart["runtimeConfig"] = map[string]any{"model": runtime.Model, "reasoningEffort": runtime.ReasoningEffort, "codexFastMode": false}
	var admitted executionproposal.StartResult
	gateDecode(s.t, s.call("local", "execution_start", wireStart, ""), &admitted)
	s.check(admitted.Proposal.ID != "" && admitted.Proposal.TaskID == f.task && admitted.Run.ID != "" && admitted.Run.ExecutionProposalID == admitted.Proposal.ID, "exact real Execution/Run admission receipt", admitted)
	s.executionID, s.implementationID = admitted.Proposal.ID, admitted.Run.ID
	s.implementationSession, _ = s.f.plane.Supervisor.GetSession(s.implementationID)
	s.revision = view.TaskView.ExecutionStartTarget.TaskDefinitionRevisionID
}
func (s *continuationGate) awaitVerification() {
	deadline := time.Now().Add(continuationPipelineTimeout)
	var v *p.TaskView
	for {
		v = s.readWorkspace().TaskView
		if v != nil && v.CurrentExecution != nil && v.ImplementationRun != nil && v.VerificationRun != nil && v.VerificationRun.TransportStatus == "SUCCEEDED" {
			break
		}
		if time.Now().After(deadline) {
			s.diagnosePipeline(v)
			s.t.Fatalf("real pipeline failed to finish: %+v", v)
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.check(v.CurrentExecution.ID == s.executionID && v.ImplementationRun.ID == s.implementationID, "terminal graph matches exact admitted Execution/Run", v)
	s.verificationID = v.VerificationRun.ID
	s.check(v.Phase == task.PhaseVerifying && v.ImplementationRun.TransportStatus == "SUCCEEDED" && v.VerificationRun.AnalysisState == "REQUIRED", "Run success is not acceptance; explicit analysis required", v)
	gateDecode(s.t, s.call("gpt", "run_analysis_get", map[string]any{"aiSessionId": s.f.session, "runId": s.verificationID}, ""), &s.analysis)
	a := s.analysis
	s.check(a.Run.ID == s.verificationID && a.Run.TaskID == s.f.task && a.Run.ExecutionProposalID == s.executionID && reflect.DeepEqual(a.VerificationSpec, s.originalVerification), "analysis bound to exact terminal Run and original authored contract", a)
	s.check(a.State == "REQUIRED" && a.Run.ResultProtocol == run.NaturalResultProtocol && a.Run.Status == run.StatusSucceeded && len(a.Commands) == 2 && len(a.ObservedChecks) == 2 && a.VerificationSpec.Mode == task.VerificationModeRun, "actual immutable evidence, no waiver", a)
	s.check(a.Run.RuntimeWorkset != nil && a.Run.RuntimeWorkset.TaskPermissions != nil, "actual verification workset and permission identity", a.Run.RuntimeWorkset)
	checks := []map[string]any{}
	for i, observed := range a.ObservedChecks {
		spec := a.VerificationSpec.Checks[i]
		s.check(observed != nil && observed.Source != nil && observed.Source.RunID == s.verificationID && observed.Source.RepositoryID == spec.RepositoryID && observed.Command == spec.Command && observed.ExitCode != nil && *observed.ExitCode == 0 && observed.Status == "PASSED", "exact per-member observed successful shell check", observed)
		var target *workset.Target
		for j := range s.originalTargets {
			if s.originalTargets[j].RepositoryID == spec.RepositoryID {
				target = &s.originalTargets[j]
			}
		}
		s.check(target != nil && observed.Source.MemberID == target.MemberID && observed.Source.MemberVersion == target.MemberVersion && observed.Source.RuntimeWorksetHash == a.Run.RuntimeWorkset.ContentHash && observed.Source.TaskPermissionHash == a.Run.RuntimeWorkset.TaskPermissions.ContentHash, "observation bound to exact original member/workset/permission", observed.Source)
		var command *run.ObservedCommand
		matches := 0
		for j := range a.Commands {
			if a.Commands[j].ItemID == observed.Source.ItemID {
				matches++
				command = &a.Commands[j]
			}
		}
		s.check(matches == 1 && command != nil && command.CWD == observed.Source.CWD && command.ExitCode != nil && *command.ExitCode == 0 && command.Command == spec.Command && command.Status == "completed", "actual exact command by immutable item ID/CWD, not array position or prose", command)

		checks = append(checks, map[string]any{"index": i, "status": "PASSED", "summary": "Simulated GPT inspected actual test -f exit 0", "evidence": observed.Source.ItemID, "reason": ""})
	}
	report, _ := json.Marshal(map[string]any{"version": 2, "summary": "Simulated GPT: only the two exact file-existence checks passed", "checks": checks, "acceptanceCriteria": []any{map[string]any{"index": 0, "status": "PASSED", "summary": "Both exact files exist", "evidence": "Two member-bound successful observed shell commands", "reason": ""}}})
	s.analysisInput = runanalysis.Input{AISessionID: s.f.session, RunID: s.verificationID, RequestID: "analyze-verification", SourceHash: a.SourceHash, Report: report}
	// Read/join real supervisor finalization; no forced release or DB mutation.
	for _, id := range []string{s.implementationID, s.verificationID} {
		if session, ok := s.f.plane.Supervisor.GetSession(id); ok {
			select {
			case <-session.Done():
			case <-s.ctx.Done():
				s.t.Fatal(s.ctx.Err())
			}
		}
	}
	gateDecode(s.t, s.call("gpt", "run_analysis_submit", s.analysisInput, ""), &s.analysisReceipt)
	s.frozenRuns = []string{s.f.run, s.implementationID, s.verificationID}
	s.frozenFiles = s.snapshot()
	s.save("checkpoint-files.json", s.frozenFiles)
	// Independently read exact admitted member roots after the actual subprocess
	// writes. Do not infer filesystem effects merely from a successful Run.
	s.check(a.Run.RuntimeWorkset != nil && len(a.Run.RuntimeWorkset.Members) == 2, "exact two execution members", a.Run.RuntimeWorkset)
	seen := map[string]bool{}
	for _, member := range a.Run.RuntimeWorkset.Members {
		var expected *workset.Target
		for i := range s.originalTargets {
			if s.originalTargets[i].MemberID == member.MemberID {
				expected = &s.originalTargets[i]
			}
		}
		s.check(expected != nil && !seen[member.MemberID] && member.RepositoryID == expected.RepositoryID && member.MemberVersion == expected.MemberVersion && member.OriginCommit == expected.OriginCommit && member.StartHEAD == expected.OriginCommit && member.Read == expected.Read && reflect.DeepEqual(member.Write, expected.Write), "bijective original member identity and scope", member)
		seen[member.MemberID] = true
		rel, e := filepath.Rel(filepath.Join(s.f.dataRoot, "worktrees"), member.CanonicalRoot)
		s.check(e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && member.CanonicalRoot != "", "admitted member root owned by fixture", member.CanonicalRoot)

		name := ""
		for n, id := range s.f.repos {
			if id == member.RepositoryID {
				name = n
			}
		}
		s.check(name == "app" || name == "sdk", "implementation member is approved, never backend", member)
		s.check(s.frozenFiles[filepath.Join(member.CanonicalRoot, "same.txt")] == "coded "+name+"\n" && s.frozenFiles[filepath.Join(member.CanonicalRoot, "<HEAD>")] == s.f.heads[name], "real expected file write without managed commit: "+name, member)
	}
	s.checkCheckpoint(false, false)
}

// Diagnostics are read-only and run before normal owned shutdown. A live wait
// remains live; no cancellation, fabricated terminal fact or recovery is issued.
func (s *continuationGate) diagnosePipeline(view *p.TaskView) {
	facts := map[string]any{"at": time.Now().UTC(), "view": view}
	if file, err := os.OpenFile(filepath.Join(s.evidence, "pipeline-goroutines.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
		_ = pprof.Lookup("goroutine").WriteTo(file, 2)
		_ = file.Close()
	}
	if session := s.implementationSession; session != nil {
		select {
		case <-session.Done():
			value, err := session.Wait(context.Background())
			facts["joinedSessionRun"] = value
			if err != nil {
				facts["joinedSessionError"] = err.Error()
			}
		default:
			facts["sessionStillLive"] = true
		}
	} else {
		facts["capturedSessionUnavailable"] = true
	}
	_, present := s.f.plane.Supervisor.GetSession(s.implementationID)
	facts["supervisorSessionPresent"] = present
	ctx, cancel := context.WithTimeout(s.f.ctx, 15*time.Second)
	defer cancel()
	value, err := s.f.facade.GetRunForSession(ctx, s.f.session, s.implementationID)
	if err != nil {
		facts["runReadError"] = err.Error()
	} else {
		facts["run"] = value
	}
	runtime, err := s.f.facade.AgentRuntimeStateForSession(ctx, s.f.session, s.implementationID)
	if err != nil {
		facts["runtimeReadError"] = err.Error()
	} else {
		facts["runtime"] = runtime
	}
	events, err := s.f.facade.ListEvents(ctx, controlapi.EventFilter{ProjectID: s.f.project, TaskID: s.f.task, Limit: 100})
	if err != nil {
		facts["eventReadError"] = err.Error()
	} else {
		facts["events"] = events
	}
	s.save("pipeline-diagnostics.json", facts)
}

// Snapshot every regular file and HEAD in all owned Git worktrees, not runtime
// scratch/copy directories that Core may legitimately retire after completion.
func (s *continuationGate) snapshot() map[string]string {
	s.t.Helper()
	out := map[string]string{}
	fixtureRoot := filepath.Dir(s.f.dataRoot)
	for _, name := range []string{"app", "sdk", "backend"} {
		source := filepath.Join(fixtureRoot, name)
		cmd := exec.Command("git", "worktree", "list", "--porcelain")
		cmd.Dir = source
		b, e := cmd.Output()
		if e != nil {
			s.t.Fatal(e)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.HasPrefix(line, "worktree ") {
				continue
			}
			root := strings.TrimPrefix(line, "worktree ")
			rel, e := filepath.Rel(fixtureRoot, root)
			if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				s.t.Fatalf("unowned fixture worktree: %s", root)
			}
			cmd = exec.Command("git", "rev-parse", "HEAD")
			cmd.Dir = root
			head, e := cmd.Output()
			if e != nil {
				s.t.Fatal(e)
			}
			out[filepath.Join(root, "<HEAD>")] = strings.TrimSpace(string(head))
			e = filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
				if e != nil {
					return e
				}
				if d.Name() == ".git" {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if d.IsDir() {
					return nil
				}
				if !d.Type().IsRegular() {
					s.t.Fatalf("unexpected non-regular fixture entry: %s", path)
				}
				data, e := os.ReadFile(path)
				if e != nil {
					return e
				}
				out[path] = string(data)
				return nil
			})
			if e != nil {
				s.t.Fatal(e)
			}
		}
	}
	return out
}

func (s *continuationGate) checkCheckpoint(held, continued bool) *p.TaskView {
	s.t.Helper()
	workspace := s.workspace()
	v := workspace.TaskView
	s.check(workspace.TaskSources != nil && workspace.TaskSources.DefinitionStatus == "AVAILABLE" && reflect.DeepEqual(workspace.TaskSources.SelectedDefinition, &s.frozenDefinition), "exact pre-start definition and approval receipt unchanged", workspace.TaskSources)
	d := v.VerificationDecisions
	s.check(v.Status == "ACTIVE" && v.Phase == task.PhaseAcceptancePending && v.CurrentExecution != nil && v.CurrentExecution.ID == s.executionID && v.CurrentExecution.TaskDefinitionRevisionID == s.revision, "same active unaccepted Task/Execution/revision", v)
	s.check(v.CurrentVerificationSpec != nil && reflect.DeepEqual(*v.CurrentVerificationSpec, s.analysis.VerificationSpec) && v.CurrentVerificationSpec.Mode == task.VerificationModeRun && v.CurrentVerificationSpec.Waiver == nil && v.VerificationReport != nil, "real fixture RUN evidence preserved", v)
	s.check(d != nil && d.TaskID == s.f.task && d.ExecutionID == s.executionID && d.RevisionID == s.revision && (d.Hold != nil) == held && d.ContinuationConfirmed == continued && d.Completion == nil, "exact hold/continuation state, no completion", d)
	s.check(v.CurrentExecutionAcceptance != nil && v.CurrentExecutionAcceptance.Status == "WAITING_ACCEPTANCE" && v.CurrentExecutionAcceptance.Receipt == nil, "choice is not engineering acceptance", v.CurrentExecutionAcceptance)
	s.check(v.CommitStatus == "UNCOMMITTED" && v.CommitSHA == "" && v.MergedCommit == "" && len(v.ExecutionHistory) == 1 && len(v.ReviewHistory) == 1, "no commit, merge, successor Execution or Review", v)
	s.check(v.ImplementationRun.ID == s.implementationID && v.VerificationRun.ID == s.verificationID && v.ImplementationRun.TransportStatus == "SUCCEEDED" && v.VerificationRun.TransportStatus == "SUCCEEDED" && v.VerificationRun.AnalysisState == "RECORDED", "same completed transport and analyzed verification", v)
	runs, e := s.f.facade.ListRunsForSession(s.f.ctx, s.f.session, controlapi.RunFilter{TaskID: s.f.task, ListOptions: controlapi.ListOptions{Limit: 100}})
	if e != nil {
		s.t.Fatal(e)
	}
	ids := map[string]bool{}
	for _, r := range runs {
		ids[r.ID] = true
	}
	s.check(len(runs) == 3 && ids[s.frozenRuns[0]] && ids[s.frozenRuns[1]] && ids[s.frozenRuns[2]], "read-only Core run inventory remains exactly three", runs)
	s.check(reflect.DeepEqual(s.snapshot(), s.frozenFiles), "checkpoint leaves all fixture same.txt bytes unchanged", s.snapshot())
	for _, name := range []string{"app", "sdk", "backend"} {
		path := filepath.Join(filepath.Dir(s.f.dataRoot), name)
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = path
		b, e := cmd.Output()
		s.check(e == nil && strings.TrimSpace(string(b)) == s.f.heads[name], "source HEAD unchanged: "+name, string(b))
		cmd = exec.Command("git", "status", "--porcelain")
		cmd.Dir = path
		b, e = cmd.Output()
		s.check(e == nil && len(b) == 0, "source clean: "+name, string(b))
	}
	if held {
		if s.held.TaskID == "" {
			s.held = *d.Hold
		}
		s.check(reflect.DeepEqual(*d.Hold, s.held), "hold identity and original HeldAt stable", d.Hold)
		available := true
		for _, choice := range d.Choices {
			if choice.Action == "COMPLETE_TASK" {
				available = choice.Available
			}
		}
		s.check(!available, "held Task completion capability unavailable", d.Choices)
		s.check(v.NextStep == nil && d.Hold.TaskID == s.f.task && d.Hold.ExecutionID == s.executionID && d.Hold.RevisionID == s.revision && d.Hold.Note == "Retain exact verified fixture evidence; not acceptance or permission to merge.", "explicit user hold suppresses next step", v.NextStep)
	}
	return v
}
func (s *continuationGate) choice(action, request, note string, generation int) map[string]any {
	out := map[string]any{"aiSessionId": s.f.session, "requestId": request, "purpose": "EXECUTION", "action": action, "taskActionGeneration": generation, "execution": map[string]any{"action": action, "taskId": s.f.task, "featureAreaId": s.f.area, "executionId": s.executionID, "reviewId": s.f.review, "taskDefinitionRevisionId": s.revision, "decisionId": "TASK_ACCEPTANCE:" + s.executionID, "note": note}, "variants": []any{map[string]any{"id": "choice", "label": action, "summary": "Explicit fixture-user " + action}}}
	if note == "" {
		delete(out["execution"].(map[string]any), "note")
	}
	return out
}

func (s *continuationGate) replay(in c.CommitInput, receipt c.Intent) {
	got := s.commit(in)
	s.check(got.IntentID == receipt.IntentID && got.Version == receipt.Version && got.Purpose == receipt.Purpose && got.Action == receipt.Action && got.ContentHash == receipt.ContentHash && reflect.DeepEqual(got.Content, receipt.Content), "exact immutable historical intent identity and content", got)
	s.check(reflect.DeepEqual(got.Steps, receipt.Steps) && reflect.DeepEqual(got.Commit, receipt.Commit), "exact historical confirmation business receipt", got)
}
func (s *continuationGate) cold() {
	priorEpoch := s.f.plane.DB.RuntimeEpoch()
	s.closeWire()
	s.closeWire = nil
	if e := s.f.plane.Close(); e != nil {
		s.t.Fatal(e)
	}
	plane, e := controlplane.Open(s.f.ctx, controlplane.Config{DataRoot: s.f.dataRoot})
	if e != nil {
		s.t.Fatal(e)
	}
	s.f.plane = plane
	currentEpoch := plane.DB.RuntimeEpoch()
	s.check(priorEpoch != "" && currentEpoch != "" && currentEpoch != priorEpoch, "cold reopen creates a distinct real Core epoch", []string{priorEpoch, currentEpoch})
	if len(s.epochs) == 0 {
		s.epochs = append(s.epochs, priorEpoch)
	}
	s.epochs = append(s.epochs, currentEpoch)
	s.save("cold-epochs.json", s.epochs)
	s.install()
	s.connect()
}
func (s *continuationGate) exerciseChoices() {
	v := s.checkCheckpoint(false, false)
	note := "Retain exact verified fixture evidence; not acceptance or permission to merge."
	s.call("gpt", "execution_prepare", s.choice("DEFER_TASK", "stale-defer", note, int(v.ActionNode.Generation)+1), "TASK_ACTION_STALE")
	s.checkCheckpoint(false, false)
	s.call("gpt", "execution_prepare", s.choice("DEFER_TASK", "empty-defer", " ", int(v.ActionNode.Generation)), "INVALID_ARGUMENT")
	s.checkCheckpoint(false, false)
	prepared := s.prepare(s.choice("DEFER_TASK", "prepare-defer", note, int(v.ActionNode.Generation)))
	s.checkCheckpoint(false, false)
	in := s.commitInput(prepared, "commit-defer")
	bad := in
	bad.VariantID = "missing"
	bad.RequestID = "commit-defer-invalid-selection"
	s.call("fixture_user", "confirmation_commit", bad, "CONFIRMATION_VARIANT_UNAVAILABLE")
	s.checkCheckpoint(false, false)
	deferred := s.commit(in)
	s.check(len(deferred.Steps) == 1 && deferred.Steps[0].Kind == "RECORD_TASK_CHOICE" && deferred.Steps[0].RunID == "" && deferred.Steps[0].ExecutionID == s.executionID && deferred.Steps[0].TaskID == s.f.task && deferred.Steps[0].RevisionID == s.revision && deferred.Steps[0].State == "COMPLETED" && deferred.Steps[0].Effect == "COMMAND_COMPLETED", "DEFER is a choice only, not dispatch", deferred.Steps)
	s.checkCheckpoint(true, false)
	s.replay(in, deferred)
	s.checkCheckpoint(true, false)
	s.cold()
	v = s.checkCheckpoint(true, false)
	s.replay(in, deferred)
	s.checkCheckpoint(true, false)
	s.call("gpt", "execution_prepare", s.choice("COMPLETE_TASK", "held-complete", "", int(v.ActionNode.Generation)), "CONFIRMATION_FACTS_UNAVAILABLE")
	s.checkCheckpoint(true, false)
	prepared = s.prepare(s.choice("CONTINUE_TASK", "prepare-continue", "", int(v.ActionNode.Generation)))
	s.checkCheckpoint(true, false)
	resumedInput := s.commitInput(prepared, "commit-continue")
	resumed := s.commit(resumedInput)
	s.check(len(resumed.Steps) == 1 && resumed.Steps[0].Kind == "RECORD_TASK_CHOICE" && resumed.Steps[0].RunID == "" && resumed.Steps[0].ExecutionID == s.executionID && resumed.Steps[0].TaskID == s.f.task && resumed.Steps[0].RevisionID == s.revision && resumed.Steps[0].State == "COMPLETED" && resumed.Steps[0].Effect == "COMMAND_COMPLETED", "CONTINUE is a checkpoint only, not successor dispatch", resumed.Steps)
	s.checkCheckpoint(false, true)
	s.replay(in, deferred)
	s.checkCheckpoint(false, true)
	s.replay(resumedInput, resumed)
	s.checkCheckpoint(false, true)
	changed := resumedInput
	changed.VariantID = "different"
	s.call("fixture_user", "confirmation_commit", changed, "CONFIRMATION_REQUEST_CONFLICT")
	s.checkCheckpoint(false, true)
	s.cold()
	s.checkCheckpoint(false, true)
	s.replay(in, deferred)
	s.replay(resumedInput, resumed)
	s.checkCheckpoint(false, true)
	var receipt runanalysis.Receipt
	gateDecode(s.t, s.call("gpt", "run_analysis_submit", s.analysisInput, ""), &receipt)
	s.check(receipt == s.analysisReceipt, "historical analysis receipt unchanged after choices and restart", receipt)
	var source runanalysis.Source
	gateDecode(s.t, s.call("gpt", "run_analysis_get", map[string]any{"aiSessionId": s.f.session, "runId": s.verificationID}, ""), &source)
	s.check(source.State == "RECORDED" && source.Run.ID == s.verificationID && source.SourceHash == s.analysis.SourceHash && source.RecordedSourceHash == s.analysis.SourceHash && reflect.DeepEqual(source.VerificationSpec, s.originalVerification), "cold recorded analysis retains exact Run/source hash/original contract", source)
	s.check(source.Run.Result.AgentClaim.Message == s.analysis.Run.Result.AgentClaim.Message && reflect.DeepEqual(source.Commands, s.analysis.Commands) && reflect.DeepEqual(source.ObservedChecks, s.analysis.ObservedChecks) && reflect.DeepEqual(source.Run.RuntimeWorkset, s.analysis.Run.RuntimeWorkset), "raw simulated Agent text and actual observed evidence immutable", source)
	s.checkCheckpoint(false, true)
	s.save("final-workspace.json", s.workspace())
}

// Pure oracle test: these values are never inserted into Core or used to grant
// authority. Unknown/foreign/partial receipts must not relax the wire assertion.
func TestSimulatedContinuationPreparingReceipt(t *testing.T) {
	in := controlapi.ApprovedTaskStartInput{AISessionID: "session", TaskID: "task"}
	in.ExpectedTarget.TaskDefinitionRevisionID = "revision"
	in.ExpectedTarget.ApprovedWorksetHash = "workset"
	var valid mcpserver.SurfaceExecutionStartOutput
	valid.ReservationCommitted, valid.RefreshRequired, valid.DispatchError = true, true, "runtime handoff PREPARING: HANDOFF_IN_PROGRESS"
	valid.Receipt = &executionproposal.StartReceiptIdentity{ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", OriginalExecutionID: "execution", OriginalRunID: "run"}
	valid.Proposal.ID, valid.Proposal.AISessionID, valid.Proposal.ProjectID, valid.Proposal.TaskID = "execution", "session", "project", "task"
	valid.Proposal.Status, valid.Proposal.TaskDefinitionRevisionID, valid.Proposal.ApprovedWorksetHash = "CONSUMED", "revision", "workset"
	valid.Run.ID, valid.Run.ProjectID, valid.Run.TaskID, valid.Run.ExecutionProposalID = "run", "project", "task", "execution"
	valid.Handoff = executionproposal.Handoff{TaskID: "task", ExecutionID: "execution", RunID: "run", RuntimeEpoch: "epoch", RecordedRuntimeEpoch: "epoch", State: executionproposal.HandoffPreparing, RecordedState: executionproposal.HandoffPreparing, Code: "HANDOFF_IN_PROGRESS", RecordedCode: "HANDOFF_IN_PROGRESS", RecordExists: true, RecordEpochMatchesRun: true, RunEpochCurrent: true, OwnerKind: "IN_PROCESS_SUPERVISOR"}
	if !exactPreparingContinuationAdmission(valid, in, "project", "epoch") {
		t.Fatal("exact PREPARING reservation should permit read-only observation")
	}
	cases := map[string]func(*mcpserver.SurfaceExecutionStartOutput){
		"uncommitted":            func(v *mcpserver.SurfaceExecutionStartOutput) { v.ReservationCommitted = false },
		"missing receipt":        func(v *mcpserver.SurfaceExecutionStartOutput) { v.Receipt = nil },
		"foreign receipt":        func(v *mcpserver.SurfaceExecutionStartOutput) { v.Receipt.TaskID = "other" },
		"different run":          func(v *mcpserver.SurfaceExecutionStartOutput) { v.Receipt.OriginalRunID = "other" },
		"foreign session":        func(v *mcpserver.SurfaceExecutionStartOutput) { v.Proposal.AISessionID = "other" },
		"different revision":     func(v *mcpserver.SurfaceExecutionStartOutput) { v.Proposal.TaskDefinitionRevisionID = "other" },
		"different workset":      func(v *mcpserver.SurfaceExecutionStartOutput) { v.Proposal.ApprovedWorksetHash = "other" },
		"unknown handoff":        func(v *mcpserver.SurfaceExecutionStartOutput) { v.Handoff.State = executionproposal.HandoffUnknown },
		"rejected code":          func(v *mcpserver.SurfaceExecutionStartOutput) { v.Handoff.Code = "REJECTED" },
		"missing durable record": func(v *mcpserver.SurfaceExecutionStartOutput) { v.Handoff.RecordExists = false },
		"old epoch":              func(v *mcpserver.SurfaceExecutionStartOutput) { v.Handoff.RecordedRuntimeEpoch = "old" },
		"different handoff run":  func(v *mcpserver.SurfaceExecutionStartOutput) { v.Handoff.RunID = "other" },
		"wrong owner":            func(v *mcpserver.SurfaceExecutionStartOutput) { v.Handoff.OwnerKind = "unknown" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var value mcpserver.SurfaceExecutionStartOutput
			gateDecode(t, valid, &value)
			mutate(&value)
			if exactPreparingContinuationAdmission(value, in, "project", "epoch") {
				t.Fatal("inexact/unknown receipt accepted")
			}
		})
	}
}
