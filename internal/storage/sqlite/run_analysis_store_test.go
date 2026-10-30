package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/task"
)

func TestNaturalReviewExecutionAndGPTAnalysisAreSeparate(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := aisession.WithCallerScope(context.Background(), "owned-caller")
	a := f.start(t)
	setAgentRunState(t, f.db, a.Run.ID, run.StatusPreparing, "", "")
	conversations := NewAgentConversationStore(f.db)
	if _, err := conversations.BeginRun(ctx, a.Run.ID); err != nil {
		t.Fatal(err)
	}
	if err := conversations.BindThreadWithRuntimeProvenance(ctx, a.Run.ID, "natural-thread", f.candidate.Model, "natural-analysis-fixture", *f.candidate.BackendSelection); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, a.Run.ID, run.StatusRunning, "natural-thread", "")
	store := NewRunStore(f.db)
	if err := store.PrepareNaturalResult(ctx, a.Run.ID); err != nil {
		t.Fatal(err)
	}
	if err := conversations.MarkTurnPending(ctx, a.Run.ID); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, a.Run.ID, run.StatusRunning, "natural-thread", "natural-turn")
	captured, _ := json.Marshal(run.ExecutionEvidence{Commands: []run.ObservedCommand{}})
	if err := store.RecordExecutionCommands(ctx, a.Run.ID, captured); err != nil {
		t.Fatal(err)
	}
	const raw = "审查结束。建议按现有边界实施；这是一段自然语言，不是 JSON。"
	f.complete(t, a, raw)
	if err := conversations.FinishRun(ctx, a.Run.ID, agentconversation.KnownAdvance, "natural-turn", true); err != nil {
		t.Fatal(err)
	}
	f.guidance(t, "RUN_ANALYSIS_REQUIRED", "ANALYZE_RUN_RESULT")
	if n := ownedCount(t, f, "review_result_materializations"); n != 0 {
		t.Fatalf("native text materialized: %d", n)
	}
	if n := ownedCount(t, f, "review_approval_decisions"); n != 0 {
		t.Fatalf("premature user decision: %d", n)
	}
	service := &runanalysis.Service{Store: NewRunAnalysisStore(f.db), Analyze: execution.AnalyzeRunReport}
	source, err := service.Get(ctx, f.session, a.Run.ID)
	if err != nil || source.State != "REQUIRED" || source.Kind != "REVIEW" || source.Run.Result.AgentClaim.Message != raw {
		t.Fatalf("source %+v %v", source, err)
	}
	execution.DescribeRunAnalysis(&source)
	// Author against the returned scalar wire contract, rather than submitting
	// a canonical historical result that bypasses the public alias DTO.
	var report map[string]any
	if err := json.Unmarshal([]byte(approvedResult(t)), &report); err != nil {
		t.Fatal(err)
	}
	report["repositoryTargets"], report["primaryTargetAlias"] = []any{}, ""
	for _, item := range report["verificationSpec"].(map[string]any)["checks"].([]any) {
		check := item.(map[string]any)
		check["targetAlias"], check["targetAliases"], check["workingDirectory"] = "", []any{}, ""
		check["instruction"], check["paths"], check["packages"] = "", []any{}, []any{}
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(source.ReportSchema, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(report); err != nil {
		t.Fatalf("returned scalar report contract: %v", err)
	}
	rawReport, _ := json.Marshal(report)
	in := runanalysis.Input{AISessionID: f.session, RunID: a.Run.ID, RequestID: "gpt-analysis", SourceHash: source.SourceHash, Report: rawReport}
	invalid := in
	invalid.Report = []byte(strings.Replace(string(rawReport), `"primaryTargetAlias":""`, `"primaryTargetAlias":"target-1"`, 1))
	if _, err = service.Submit(ctx, invalid); !errors.Is(err, runanalysis.ErrInvalid) {
		t.Fatalf("invented scalar alias: %v", err)
	}
	bad := in
	bad.SourceHash = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err = service.Submit(ctx, bad); !errors.Is(err, runanalysis.ErrStale) {
		t.Fatalf("stale analysis: %v", err)
	}
	if _, err = service.Get(aisession.WithCallerScope(context.Background(), "foreign"), f.session, a.Run.ID); !errors.Is(err, aisession.ErrScopeMismatch) {
		t.Fatalf("scope leak: %v", err)
	}
	receipt, err := service.Submit(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	f.guidance(t, "REVIEW_APPROVAL_PENDING", "REVIEW_CONFIRMATION_REQUIRED")
	replay, err := service.Submit(ctx, in)
	if err != nil || replay != receipt {
		t.Fatalf("replay %+v %v", replay, err)
	}
	changed := in
	changed.RequestID = "different-request"
	if _, err = service.Submit(ctx, changed); !errors.Is(err, runanalysis.ErrConflict) {
		t.Fatalf("conflicting analysis %v", err)
	}
	current, err := store.Get(ctx, a.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != run.StatusSucceeded || current.Result.AgentClaim.Message != raw || current.AnalysisState != "RECORDED" {
		t.Fatalf("native result rewritten: %+v", current)
	}
	if n := ownedCount(t, f, "execution_proposals"); n != 0 {
		t.Fatalf("analysis started execution: %d", n)
	}
	// Same-Project readers may inspect history without borrowing the author's
	// identity. Read the persisted canonical result, not the native Chinese text.
	reader := aisession.Session{ID: "natural-review-history-reader", ProjectID: f.project, CreatedAt: f.now, UpdatedAt: f.now}
	seedBoundSession(t, f.db, reader)
	before := ownedCount(t, f, "run_analyses")
	for _, session := range []string{f.session, reader.ID} {
		readCtx := ctx
		if session == reader.ID {
			readCtx = aisession.WithCallerScope(context.Background(), "storage-fixture-"+reader.ID)
		}
		stored, e := service.Get(readCtx, session, a.Run.ID)
		if e != nil || stored.State != "RECORDED" || stored.Receipt == nil || *stored.Receipt != receipt || stored.RecordedSourceHash != in.SourceHash || string(stored.Report) != string(rawReport) || stored.ReviewResult == nil || stored.ReviewResult.ReviewDisposition != review.DispositionApprovalReady {
			t.Fatalf("recorded report read: %+v %v", stored, e)
		}
		var canonical string
		if e = f.db.sql.QueryRow(`SELECT review_result_json FROM run_analyses WHERE run_id=?`, a.Run.ID).Scan(&canonical); e != nil {
			t.Fatal(e)
		}
		expected, e := review.ParseResult(canonical)
		if e != nil || stored.ReviewResult.Summary != expected.Summary || stored.Run.Result.AgentClaim.Message != raw {
			t.Fatalf("reinterpreted native output: %+v %v", stored, e)
		}
	}
	foreignProject := reader
	foreignProject.ID, foreignProject.ProjectID = "natural-review-foreign-project", "other-project"
	// A valid caller with an existing different Project binding cannot read it.
	if e := NewProjectStore(f.db).Create(ctx, project.Project{ID: foreignProject.ProjectID, Name: "Other", CreatedAt: f.now, UpdatedAt: f.now}); e == nil {
		seedBoundSession(t, f.db, foreignProject)
		if _, e = service.Get(aisession.WithCallerScope(context.Background(), "storage-fixture-"+foreignProject.ID), foreignProject.ID, a.Run.ID); e == nil {
			t.Fatal("cross-Project analysis leaked")
		}
	} else {
		t.Fatal(e)
	}
	if n := ownedCount(t, f, "run_analyses"); n != before {
		t.Fatal("read mutated analysis history")
	}
	// Advancing the Task beyond Review approval must not redirect a historical
	// result read or require a second report submission.
	successor, err := f.store.Resolve(ctx, f.input(a, "APPROVE"))
	if err != nil || successor.Task == nil || successor.Task.Phase == task.PhaseReviewing {
		t.Fatalf("Task approval: %+v %v", successor, err)
	}
	history, err := service.Get(aisession.WithCallerScope(context.Background(), "storage-fixture-"+reader.ID), reader.ID, a.Run.ID)
	if err != nil || history.Review.ID != a.Review.ID || history.Receipt.RequestID != in.RequestID || history.ReviewResult.Summary == "" || string(history.Report) != string(rawReport) || history.RecordedSourceHash != in.SourceHash {
		t.Fatalf("historical report redirected: %+v %v", history, err)
	}
}
