package controlapi_test

// This single sequential scenario uses real Core and GPT/Local MCP HTTP with
// the published TEST_ONLY_PROTOCOL_FIXTURE_V1 Agent. It does not qualify a real
// model/native artifact, exercise a successor Run, or accept/merge delivery.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSimulatedDeferredVerificationAnalysis(t *testing.T) {
	if os.Getenv("DEVBRIDGE_SIMULATED_CONTINUATION") != "1" {
		t.Skip("explicit trusted Agent fixture; set DEVBRIDGE_SIMULATED_CONTINUATION=1")
	}
	s := &continuationGate{t: t}
	s.evidence = os.Getenv("DEVBRIDGE_DEFERRED_ANALYSIS_EVIDENCE_DIR")
	if s.evidence == "" {
		s.evidence = t.TempDir()
	}
	var err error
	s.evidence, err = filepath.Abs(s.evidence)
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
	logger := observability.New(observability.Options{Process: "devbridge-test-only-deferred-analysis", Directory: s.evidence, Filename: "core.jsonl"})
	observability.SetDefault(logger)
	t.Cleanup(func() { observability.SetDefault(previousLogger); logger.Close() })
	started := time.Now().UTC()
	completed := false
	defer func() {
		_ = s.transcript.Close()
		s.save("report.json", map[string]any{
			"scenario": t.Name(), "passed": completed && !t.Failed(), "startedAt": started, "finishedAt": time.Now().UTC(), "checks": s.checks,
			"trustBoundary": "TEST_ONLY_PROTOCOL_FIXTURE_V1; real Core and MCP adapters, deterministic Agent, no real model or native qualification",
			"scope":         "first successful Verification analysis while held, transaction rollback observed through committed state, same input after CONTINUE",
			"notCovered":    []string{"internal SQL statement tracing", "concurrency", "crash/restart", "new Run preparation", "acceptance/commit/merge"},
		})
	}()

	// The child executable is always the published test helper, never a host
	// Codex installation. Isolate inherited settings before fixture bootstrap.
	home := t.TempDir()
	for _, key := range []string{"HOME", "CODEX_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "DEVBRIDGE_CODING_V1_ASK_USER", "DEVBRIDGE_CODING_V1_REVIEW_MUTATE", "DEVBRIDGE_CODING_V1_INTERRUPT_ONCE", "DEVBRIDGE_TEST_VERIFICATION_FAILURE", "DEVBRIDGE_TEST_REQUIRED_CUSTOM", "DEVBRIDGE_CODING_DUPLICATE_VERIFICATION_ROOT", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY"} {
		t.Setenv(key, "")
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, "")
			if err = os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("OTEL_SDK_DISABLED", "true")
	s.f = newUpperLifecycleFixtureScoped(t, "data-instance-local")
	var cancel context.CancelFunc
	s.ctx, cancel = context.WithTimeout(s.f.ctx, continuationContextTimeout)
	t.Cleanup(cancel)
	t.Cleanup(func() {
		if s.closeWire != nil {
			s.closeWire()
		}
	})
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
	s.launcher = filepath.Join(home, "test-only-agent")
	wrapper := "#!/bin/sh\nexec /usr/bin/env -i PATH=/usr/bin:/bin HOME=" + quote(home) + " CODEX_HOME=" + quote(home) + " DEVBRIDGE_CODING_V1_HELPER=1 DEVBRIDGE_TEST_FILE_VERIFICATION=1 DEVBRIDGE_TEST_UNIQUE_TURNS=1 DEVBRIDGE_CODING_V1_WIRE_LOG=" + quote(filepath.Join(s.evidence, "agent-wire.jsonl")) + " DEVBRIDGE_CODING_V1_RESPONSE_LOG=" + quote(filepath.Join(s.evidence, "agent-responses.jsonl")) + " " + quote(binary) + " -test.run=^TestMultiRepositoryCodingV1AppServerHelper$\n"
	if err = os.WriteFile(s.launcher, []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	s.install()
	s.connect()
	s.prepareExecution()
	// Do not use awaitVerification: that helper intentionally submits analysis.
	deadline := time.Now().Add(continuationPipelineTimeout)
	for {
		v := s.readWorkspace().TaskView
		if v != nil && v.VerificationRun != nil && v.VerificationRun.TransportStatus == "SUCCEEDED" {
			s.verificationID = v.VerificationRun.ID
			break
		}
		if time.Now().After(deadline) {
			s.diagnosePipeline(v)
			t.Fatalf("fixture pipeline did not finish its original Verification: %+v", v)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Observe normal supervisor completion before touching the hold/analysis
	// boundary. No forced release, cancellation, fault injection or SQL writes.
	for _, id := range []string{s.implementationID, s.verificationID} {
		if session, ok := s.f.plane.Supervisor.GetSession(id); ok {
			select {
			case <-session.Done():
			case <-s.ctx.Done():
				t.Fatal(s.ctx.Err())
			}
		}
	}
	var activeClaims int
	if err = s.f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE feature_area_id=?`, s.f.area).Scan(&activeClaims); err != nil {
		t.Fatal(err)
	}
	s.check(activeClaims == 0, "normal supervisor finalization released original Agent claims", activeClaims)
	view := deferredAnalysisView(s, false, false, false)
	gateDecode(t, s.call("gpt", "run_analysis_get", map[string]any{"aiSessionId": s.f.session, "runId": s.verificationID}, ""), &s.analysis)
	a := s.analysis
	s.check(a.State == "REQUIRED" && a.Kind == "VERIFICATION" && a.Run.ID == s.verificationID && a.Run.ExecutionProposalID == s.executionID && a.Run.TaskID == s.f.task && a.Run.Status == run.StatusSucceeded && a.Run.ResultProtocol == run.NaturalResultProtocol && a.Run.VerificationReport == nil && a.Receipt == nil && len(a.Report) == 0 && len(a.SourceHash) == 64 && json.Valid(a.ReportSchema), "unrecorded original natural Verification exposes exact source and schema", a)
	s.check(reflect.DeepEqual(a.VerificationSpec, s.originalVerification) && a.VerificationSpec.Mode == task.VerificationModeRun && a.VerificationSpec.Waiver == nil && len(a.Commands) == 2 && len(a.ObservedChecks) == 2 && a.Run.RuntimeWorkset != nil && a.Run.RuntimeWorkset.TaskPermissions != nil, "original RUN contract has two real observed checks and no waiver", a)
	checks := []map[string]any{}
	for i, observed := range a.ObservedChecks {
		spec := a.VerificationSpec.Checks[i]
		s.check(observed != nil && observed.Source != nil && observed.Source.RunID == s.verificationID && observed.Source.RepositoryID == spec.RepositoryID && observed.Command == spec.Command && observed.Status == "PASSED" && observed.ExitCode != nil && *observed.ExitCode == 0, "member-bound successful observed check", observed)
		matches := 0
		for _, command := range a.Commands {
			if command.ItemID == observed.Source.ItemID && command.CWD == observed.Source.CWD && command.Command == spec.Command && command.Status == "completed" && command.ExitCode != nil && *command.ExitCode == 0 {
				matches++
			}
		}
		s.check(matches == 1, "observed check refers to exactly one actual completed command", observed)
		checks = append(checks, map[string]any{"index": i, "status": "PASSED", "summary": "Simulated GPT inspected actual exit 0", "evidence": observed.Source.ItemID, "reason": ""})
	}
	report, err := json.Marshal(map[string]any{"version": 2, "summary": "The two exact fixture file-existence checks passed", "checks": checks, "acceptanceCriteria": []any{map[string]any{"index": 0, "status": "PASSED", "summary": "Both exact fixture files exist", "evidence": "Two member-bound successful observed shell commands", "reason": ""}}})
	if err != nil {
		t.Fatal(err)
	}
	s.analysisInput = runanalysis.Input{AISessionID: s.f.session, RunID: s.verificationID, RequestID: "deferred-original-verification-analysis", SourceHash: a.SourceHash, Report: report}
	inputBefore, err := json.Marshal(s.analysisInput)
	if err != nil {
		t.Fatal(err)
	}
	s.save("analysis-input.json", s.analysisInput)
	s.save("analysis-source.json", a)
	s.frozenFiles = s.snapshot()
	s.save("original-files.json", s.frozenFiles)
	for _, member := range a.Run.RuntimeWorkset.Members {
		name := ""
		for n, id := range s.f.repos {
			if id == member.RepositoryID {
				name = n
			}
		}
		s.check((name == "app" || name == "sdk") && s.frozenFiles[filepath.Join(member.CanonicalRoot, "same.txt")] == "coded "+name+"\n" && s.frozenFiles[filepath.Join(member.CanonicalRoot, "<HEAD>")] == s.f.heads[name], "normal implementation files remain uncommitted: "+name, member)
	}
	initial := deferredAnalysisSnapshot(s)
	s.check(len(initial["runs"]) == 3 && len(initial["reviews"]) == 1 && len(initial["execution_proposals"]) == 1 && len(initial["run_analyses"]) == 0, "original Review and first Implementation/Verification only; no earlier analysis", initial["run_analyses"])
	s.save("initial-db.json", initial)

	prepared := s.prepare(s.choice("DEFER_TASK", "deferred-analysis-prepare-hold", "Keep original successful Run pending its first analysis.", int(view.ActionNode.Generation)))
	deferred := s.commit(s.commitInput(prepared, "deferred-analysis-commit-hold"))
	deferredAnalysisChoice(s, deferred)
	deferredAnalysisView(s, true, false, false)
	deferredAnalysisSourceUnchanged(s, "local")
	beforeRejected := deferredAnalysisSnapshot(s)
	s.save("held-before-submit-db.json", beforeRejected)
	// The public adapter may map the SQLite hold guard to INTERNAL_ERROR. Test
	// a real business refusal, not an invented dedicated TASK_DEFERRED code.
	submissionStarted := time.Now().UTC()
	refused, callErr := s.gpt.CallTool(s.ctx, &sdkmcp.CallToolParams{Name: "run_analysis_submit", Arguments: s.analysisInput})
	entry := map[string]any{"startedAt": submissionStarted, "finishedAt": time.Now().UTC(), "actor": "gpt", "tool": "run_analysis_submit", "input": s.analysisInput, "result": refused}
	if callErr != nil {
		entry["transportError"] = callErr.Error()
	}
	if err = json.NewEncoder(s.transcript).Encode(entry); err != nil {
		t.Fatal(err)
	}
	s.check(callErr == nil && refused != nil, "held submission reaches real MCP business handler, not transport/schema failure", callErr)
	var failureText []string
	for _, content := range refused.Content {
		if value, ok := content.(*sdkmcp.TextContent); ok && value.Text != "" {
			failureText = append(failureText, value.Text)
		}
	}
	s.check(refused.IsError && len(failureText) > 0, "held original first analysis is rejected with a real MCP error", refused)
	t.Logf("observed held run_analysis_submit failure: %s", strings.Join(failureText, "\n"))
	s.save("held-analysis-refusal.json", refused)
	afterRejected := deferredAnalysisSnapshot(s)
	s.save("held-after-submit-db.json", afterRejected)
	s.check(reflect.DeepEqual(beforeRejected, afterRejected), "held refusal preserves every selected business row and column, including analysis/result/phase/generation/hold/decisions", deferredAnalysisChangedTables(beforeRejected, afterRejected))
	s.check(reflect.DeepEqual(s.snapshot(), s.frozenFiles), "held analysis refusal does not revert or change original implementation files or HEADs", nil)
	view = deferredAnalysisView(s, true, false, false)
	deferredAnalysisSourceUnchanged(s, "gpt")

	prepared = s.prepare(s.choice("CONTINUE_TASK", "deferred-analysis-prepare-continue", "", int(view.ActionNode.Generation)))
	continued := s.commit(s.commitInput(prepared, "deferred-analysis-commit-continue"))
	deferredAnalysisChoice(s, continued)
	view = deferredAnalysisView(s, false, true, false)
	deferredAnalysisSourceUnchanged(s, "local")
	beforeAccepted := deferredAnalysisSnapshot(s)
	s.save("continued-before-submit-db.json", beforeAccepted)
	for _, table := range []string{"tasks", "runs", "reviews", "execution_proposals", "run_analyses", "run_repository_bindings", "worktrees", "decision_resolution_receipts", "task_completion_intents", "task_delivery_operations", "task_commit_receipts", "task_commit_members"} {
		s.check(reflect.DeepEqual(initial[table], beforeAccepted[table]), "DEFER/CONTINUE preserves original "+table, nil)
	}
	s.check(len(beforeAccepted["task_holds"]) == 0 && len(beforeAccepted["task_hold_receipts"]) == 2, "CONTINUE removes only hold and records the two real user choices", beforeAccepted["task_hold_receipts"])
	generation := view.ActionNode.Generation
	inputAfter, err := json.Marshal(s.analysisInput)
	if err != nil {
		t.Fatal(err)
	}
	s.check(string(inputBefore) == string(inputAfter), "same requestId/sourceHash/report bytes reused after CONTINUE", string(inputAfter))
	gateDecode(t, s.call("gpt", "run_analysis_submit", s.analysisInput, ""), &s.analysisReceipt)
	s.check(s.analysisReceipt.RunID == s.verificationID && s.analysisReceipt.RequestID == s.analysisInput.RequestID && s.analysisReceipt.Kind == "VERIFICATION" && s.analysisReceipt.PayloadSHA256 == runanalysis.Hash(s.analysisInput) && !s.analysisReceipt.SubmittedAt.IsZero(), "identical previously rejected analysis now has one exact successful receipt", s.analysisReceipt)
	view = deferredAnalysisView(s, false, true, true)
	s.check(view.ActionNode.Generation == generation+2, "one analysis and one phase transition advance generation exactly twice", view.ActionNode)
	var recorded runanalysis.Source
	gateDecode(t, s.call("local", "run_analysis_get", map[string]any{"aiSessionId": s.f.session, "runId": s.verificationID}, ""), &recorded)
	s.check(recorded.State == "RECORDED" && recorded.SourceHash == a.SourceHash && recorded.RecordedSourceHash == a.SourceHash && reflect.DeepEqual(recorded.ReportSchema, a.ReportSchema) && reflect.DeepEqual(recorded.Report, s.analysisInput.Report) && recorded.Receipt != nil && *recorded.Receipt == s.analysisReceipt, "both MCP sides observe the exact source/schema/report/receipt", recorded)
	s.check(reflect.DeepEqual(recorded.Run.Result, a.Run.Result) && reflect.DeepEqual(recorded.Commands, a.Commands) && reflect.DeepEqual(recorded.ObservedChecks, a.ObservedChecks) && reflect.DeepEqual(recorded.Run.RuntimeWorkset, a.Run.RuntimeWorkset) && reflect.DeepEqual(recorded.VerificationSpec, a.VerificationSpec), "analysis preserves original native result and immutable verification evidence", nil)
	afterAccepted := deferredAnalysisSnapshot(s)
	s.save("accepted-analysis-db.json", afterAccepted)
	for _, table := range []string{"reviews", "execution_proposals", "run_repository_bindings", "worktrees", "task_holds", "task_hold_receipts", "decision_resolution_receipts", "confirmation_intents", "confirmation_commit_requests", "task_action_confirmation_bindings", "task_action_conflict_resolutions", "task_action_releases", "task_completion_intents", "task_delivery_operations", "task_commit_receipts", "task_commit_members", "feature_area_agent_claims"} {
		s.check(reflect.DeepEqual(beforeAccepted[table], afterAccepted[table]), "successful original analysis leaves "+table+" unchanged", nil)
	}
	s.check(reflect.DeepEqual(deferredAnalysisWithout(beforeAccepted["runs"], s.verificationID, "verification_result_json"), deferredAnalysisWithout(afterAccepted["runs"], s.verificationID, "verification_result_json")), "all original Run columns unchanged except original verification report", nil)
	s.check(reflect.DeepEqual(deferredAnalysisWithout(beforeAccepted["tasks"], s.f.task, "phase", "updated_at"), deferredAnalysisWithout(afterAccepted["tasks"], s.f.task, "phase", "updated_at")), "original Task identity and definition unchanged except analysis phase/timestamp", nil)
	s.check(len(afterAccepted["run_analyses"]) == 1, "one successful analysis row, no consumed idempotency key from failed submission", afterAccepted["run_analyses"])
	row := afterAccepted["run_analyses"][0]
	s.check(row["run_id"] == s.verificationID && row["ai_session_id"] == s.f.session && row["request_id"] == s.analysisInput.RequestID && row["source_hash"] == a.SourceHash && row["payload_hash"] == runanalysis.Hash(s.analysisInput) && row["kind"] == "VERIFICATION" && row["report_json"] == string(s.analysisInput.Report), "SQLite row is bound to the same original analysis input", row)
	s.check(reflect.DeepEqual(s.snapshot(), s.frozenFiles), "successful analysis/CONTINUE leaves all original implementation files and HEADs intact", nil)
	s.save("final-workspace.json", s.workspace())
	completed = true
}

func deferredAnalysisView(s *continuationGate, held, continued, recorded bool) *p.TaskView {
	s.t.Helper()
	workspace := s.workspace()
	v := workspace.TaskView
	phase, state := task.PhaseVerifying, "REQUIRED"
	if recorded {
		phase, state = task.PhaseAcceptancePending, "RECORDED"
	}
	s.check(v.Status == "ACTIVE" && v.Phase == phase && v.CurrentExecution != nil && v.CurrentExecution.ID == s.executionID && v.CurrentExecution.TaskDefinitionRevisionID == s.revision && v.ActionNode != nil, "same active original Execution and expected phase", v)
	s.check(v.ImplementationRun != nil && v.ImplementationRun.ID == s.implementationID && v.ImplementationRun.TransportStatus == "SUCCEEDED" && v.VerificationRun != nil && v.VerificationRun.ID == s.verificationID && v.VerificationRun.TransportStatus == "SUCCEEDED" && v.VerificationRun.AnalysisState == state, "same successful original Runs and expected analysis state", v)
	s.check(workspace.TaskSources != nil && reflect.DeepEqual(workspace.TaskSources.SelectedDefinition, &s.frozenDefinition) && len(v.ReviewHistory) == 1 && len(v.ExecutionHistory) == 1, "frozen approved definition and sole Review/Execution preserved", workspace.TaskSources)
	d := v.VerificationDecisions
	s.check(d != nil && d.TaskID == s.f.task && d.ExecutionID == s.executionID && d.RevisionID == s.revision && (d.Hold != nil) == held && d.ContinuationConfirmed == continued && d.Completion == nil, "exact hold/continuation without completion authority", d)
	if held {
		if s.held.TaskID == "" {
			s.held = *d.Hold
		}
		s.check(reflect.DeepEqual(*d.Hold, s.held) && v.NextStep == nil, "original exact hold remains stable and suppresses next step", d.Hold)
	}
	found := 0
	for _, choice := range d.Choices {
		if choice.Action == "COMPLETE_TASK" {
			found++
			s.check(choice.Available == recorded, "COMPLETE eligibility requires recorded analysis and no hold", choice)
		}
	}
	s.check(found == 1 && v.CommitStatus == "UNCOMMITTED" && v.CommitSHA == "" && v.MergedCommit == "", "one completion capability; no acceptance, commit or merge", v)
	if recorded {
		s.check(v.VerificationReport != nil && v.CurrentVerificationSpec != nil && p.VerificationCompletionReady(*v) && v.CurrentExecutionAcceptance != nil && v.CurrentExecutionAcceptance.Status == "WAITING_ACCEPTANCE" && v.CurrentExecutionAcceptance.Receipt == nil, "restored COMPLETE eligibility is not user acceptance", v)
	} else {
		s.check(v.VerificationReport != nil && v.VerificationReport.Availability == run.VerificationReportUnavailable && len(v.VerificationReport.Checks) == 0 && len(v.VerificationReport.AcceptanceCriteria) == 0 && !p.VerificationCompletionReady(*v), "public report remains UNAVAILABLE before successful first analysis", v.VerificationReport)
	}
	return v
}

func deferredAnalysisSourceUnchanged(s *continuationGate, actor string) {
	s.t.Helper()
	var source runanalysis.Source
	gateDecode(s.t, s.call(actor, "run_analysis_get", map[string]any{"aiSessionId": s.f.session, "runId": s.verificationID}, ""), &source)
	s.check(reflect.DeepEqual(source, s.analysis), "same REQUIRED source/schema/native result after hold choice or rejected analysis", source)
}

func deferredAnalysisChoice(s *continuationGate, intent c.Intent) {
	s.t.Helper()
	s.check(len(intent.Steps) == 1 && intent.Steps[0].Kind == "RECORD_TASK_CHOICE" && intent.Steps[0].TaskID == s.f.task && intent.Steps[0].ExecutionID == s.executionID && intent.Steps[0].RevisionID == s.revision && intent.Steps[0].RunID == "" && intent.Steps[0].State == "COMPLETED" && intent.Steps[0].Effect == "COMMAND_COMPLETED", "formal user hold choice does not dispatch a Run or accept delivery", intent)
}

// Snapshot all columns of each relevant business table in one read-only SQL
// transaction. MCP call logs/session access timestamps are deliberately outside
// this business rollback oracle. This observes committed facts, not SQL tracing.
func deferredAnalysisSnapshot(s *continuationGate) map[string][]map[string]any {
	s.t.Helper()
	tx, err := s.f.plane.DB.SQL().BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.t.Fatal(err)
	}
	defer tx.Rollback()
	out := map[string][]map[string]any{}
	for _, table := range []string{"tasks", "runs", "reviews", "execution_proposals", "run_analyses", "task_action_heads", "task_holds", "task_hold_receipts", "decision_resolution_receipts", "confirmation_intents", "confirmation_commit_requests", "task_action_confirmation_bindings", "task_action_conflict_resolutions", "task_action_releases", "task_completion_intents", "task_delivery_operations", "task_commit_receipts", "task_commit_members", "run_repository_bindings", "worktrees", "feature_area_agent_claims", "events"} {
		rows, err := tx.QueryContext(s.ctx, `SELECT * FROM `+table)
		if err != nil {
			s.t.Fatalf("snapshot %s: %v", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			s.t.Fatal(err)
		}
		out[table] = []map[string]any{}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				rows.Close()
				s.t.Fatal(err)
			}
			row := map[string]any{}
			for i, key := range columns {
				if value, ok := values[i].([]byte); ok {
					row[key] = string(value)
				} else {
					row[key] = values[i]
				}
			}
			out[table] = append(out[table], row)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			s.t.Fatal(err)
		}
		if err = rows.Close(); err != nil {
			s.t.Fatal(err)
		}
		sort.Slice(out[table], func(i, j int) bool {
			a, _ := json.Marshal(out[table][i])
			b, _ := json.Marshal(out[table][j])
			return string(a) < string(b)
		})
	}
	return out
}

func deferredAnalysisChangedTables(a, b map[string][]map[string]any) []string {
	var changed []string
	for table := range a {
		if !reflect.DeepEqual(a[table], b[table]) {
			changed = append(changed, table)
		}
	}
	sort.Strings(changed)
	return changed
}

func deferredAnalysisWithout(rows []map[string]any, id string, columns ...string) []map[string]any {
	out := make([]map[string]any, len(rows))
	for i, row := range rows {
		out[i] = map[string]any{}
		for key, value := range row {
			out[i][key] = value
		}
		if row["id"] == id {
			for _, column := range columns {
				delete(out[i], column)
			}
		}
	}
	// Column removal can change the JSON sort order even when identity is stable.
	sort.Slice(out, func(i, j int) bool {
		a, _ := json.Marshal(out[i])
		b, _ := json.Marshal(out[j])
		return string(a) < string(b)
	})
	return out
}
