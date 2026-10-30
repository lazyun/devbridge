package controlapi_test

import (
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExecutionPrepareWirePreservesTaskActionGeneration(t *testing.T) {
	s := newCardLifecycleStack(t)
	f := s.fixture
	stored, err := f.facade.GetReviewReportForSession(f.ctx, f.session, f.review)
	if err != nil {
		t.Fatal(err)
	}
	report := stored.Report
	node, err := sqlite.NewConfirmationStore(f.plane.DB).ReadTaskActionNode(f.ctx, f.project, f.task, f.session)
	if err != nil || node.Generation < 1 {
		t.Fatalf("current generation unavailable: %+v, %v", node, err)
	}
	body := exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE")
	arguments := map[string]any{
		"aiSessionId": f.session, "requestId": s.nextID("stale-generation"), "purpose": "EXECUTION", "action": "APPROVE",
		"taskActionGeneration": node.Generation + 1,
		"execution":            body,
		"variants": []any{map[string]any{
			"id": "approve", "label": "Approve", "summary": "Approve the exact reviewed plan",
			"implementationMode": report.WorkSpec.ImplementationMode,
			"repositoryTargets":  report.RepositoryTargets, "primaryMemberId": report.PrimaryMemberID,
			"workSpec": report.WorkSpec, "verificationSpec": report.VerificationSpec,
		}},
	}
	var before int
	if err := f.plane.DB.SQL().QueryRow("SELECT count(*) FROM confirmation_intents").Scan(&before); err != nil {
		t.Fatal(err)
	}
	raw, rejected := s.call("execution_prepare", arguments)
	if !raw.IsError || rejected.Intent != nil || rejected.Failure == nil || rejected.Failure.Code != "TASK_ACTION_STALE" {
		t.Fatalf("generation did not reach Core unchanged: %s", s.lastRaw)
	}
	var after int
	if err := f.plane.DB.SQL().QueryRow("SELECT count(*) FROM confirmation_intents").Scan(&after); err != nil || after != before {
		t.Fatalf("stale generation prepared an intent: before=%d after=%d err=%v", before, after, err)
	}
	arguments["requestId"] = s.nextID("current-generation")
	arguments["taskActionGeneration"] = node.Generation
	raw, prepared := s.call("execution_prepare", arguments)
	if raw.IsError || prepared.Failure != nil || prepared.Intent == nil || prepared.Intent.State != "PENDING" || prepared.Intent.Content.TaskActionGeneration != node.Generation {
		t.Fatalf("current generation was not preserved in pending content: %s", s.lastRaw)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil || current.Phase != task.PhaseReviewApprovalPending || current.CurrentExecutionID != "" {
		t.Fatalf("prepare changed Task or started execution: %+v, %v", current, err)
	}
}

// The SDK validates the advertised input before Core sees it. Exercise both
// boundaries with the actual report, and prove prepare cannot consume approval.
func TestExecutionPrepareWireRequiresCompleteReviewedPlan(t *testing.T) {
	s := newCardLifecycleStack(t)
	f := s.fixture
	stored, err := f.facade.GetReviewReportForSession(f.ctx, f.session, f.review)
	if err != nil {
		t.Fatal(err)
	}
	report := stored.Report
	body := exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE")
	arguments := map[string]any{
		"aiSessionId": f.session, "requestId": s.nextID("approve"), "purpose": "EXECUTION", "action": "APPROVE",
		"execution": body,
		"variants":  []any{map[string]any{"id": "approve", "label": "Approve", "summary": "Approve the exact reviewed plan"}},
	}
	invalid, err := s.session.CallTool(s.ctx, &sdkmcp.CallToolParams{Name: "execution_prepare", Arguments: arguments})
	if err == nil && (invalid == nil || !invalid.IsError) {
		t.Fatal("decision-only approval passed public input validation")
	}
	arguments["requestId"] = s.nextID("complete-approval")
	arguments["variants"] = []any{map[string]any{
		"id": "approve", "label": "Approve", "summary": "Approve the exact reviewed plan",
		"implementationMode": report.WorkSpec.ImplementationMode,
		"repositoryTargets":  report.RepositoryTargets, "primaryMemberId": report.PrimaryMemberID,
		"workSpec": report.WorkSpec, "verificationSpec": report.VerificationSpec,
	}}
	// Required CUSTOM instructions retain their exact authored obligation. They
	// need explicit GPT analysis, without demanding a fabricated command.
	verification := task.CloneVerificationSpec(*report.VerificationSpec)
	for i := range verification.Checks {
		verification.Checks[i].Kind = task.VerificationCheckCustom
		verification.Checks[i].Command = ""
		verification.Checks[i].Instruction = "Read the approved files; no command execution"
	}
	variant := arguments["variants"].([]any)[0].(map[string]any)
	variant["verificationSpec"] = verification
	raw, rejected := s.call("execution_prepare", arguments)
	if raw.IsError || rejected.Failure != nil || rejected.Intent == nil || rejected.Intent.State != "PENDING" {
		t.Fatalf("required CUSTOM analysis was not prepared: %s", s.lastRaw)
	}
	for _, check := range rejected.Intent.Content.Variants[0].VerificationSpec.Checks {
		if !check.Required || check.Kind != task.VerificationCheckCustom || check.Command != "" || check.Instruction != "Read the approved files; no command execution" {
			t.Fatalf("prepare weakened or rewrote required CUSTOM: %+v", check)
		}
	}
	if _, err = f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(*rejected.Intent)); err != nil {
		t.Fatal(err)
	}
	arguments["requestId"] = s.nextID("invalid-command")
	verification.Checks[0].Kind = task.VerificationCheckTestCommand
	variant["verificationSpec"] = verification
	raw, rejected = s.call("execution_prepare", arguments)
	if !raw.IsError || rejected.Intent != nil || rejected.Failure == nil || rejected.Failure.Code != "UNOBSERVABLE_REQUIRED_CHECK" || rejected.Failure.NextAction != confirmation.PrepareNewVersion || !strings.Contains(rejected.Failure.Message, "needs a command") {
		t.Fatalf("required command without execution path admitted: %s", s.lastRaw)
	}
	arguments["requestId"] = s.nextID("observable-approval")
	variant["verificationSpec"] = report.VerificationSpec
	raw, result := s.call("execution_prepare", arguments)
	if raw.IsError || result.Failure != nil || result.Intent == nil || result.Intent.State != "PENDING" {
		t.Fatalf("complete reviewed plan was not prepared: %s", s.lastRaw)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil || current.Phase != task.PhaseReviewApprovalPending || current.CurrentExecutionID != "" {
		t.Fatalf("prepare changed the Task: %+v, %v", current, err)
	}
	for _, table := range []string{"direct_file_task_authorizations", "execution_proposals", "task_commit_receipts"} {
		var count int
		if err := f.plane.DB.SQL().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("prepare had a business side effect in %s: count=%d err=%v", table, count, err)
		}
	}
}
