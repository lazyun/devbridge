package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"strings"
	"testing"
	"time"
)

func TestTaskAggregateAllAttemptsAndAccountingAvailability(t *testing.T) {
	at := time.Unix(100, 0)
	aend := at.Add(23*time.Minute + 50*time.Second)
	bend := aend.Add(8*time.Minute + 2*time.Second)
	owner := task.Task{ID: "task", ProjectID: "p", FeatureAreaID: "a", Type: task.TypeDevelopment, Status: task.StatusSucceeded, ReviewApprovalID: "approval"}
	counts := &run.TokenCounts{InputTokens: 100, CachedInputTokens: 30, OutputTokens: 20, ReasoningOutputTokens: 10, TotalTokens: 120}
	runs := []run.Run{{ID: "a", AgentExecutionID: "e1", TaskID: "task", ProjectID: "p", FeatureAreaID: "a", Purpose: run.PurposeImplementation, Status: run.StatusSucceeded, StartedAt: &at, FinishedAt: &aend, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageAvailable, Usage: counts}}, {ID: "b", AgentExecutionID: "e2", TaskID: "task", ProjectID: "p", FeatureAreaID: "a", Purpose: run.PurposeVerification, Status: run.StatusSucceeded, StartedAt: &aend, FinishedAt: &bend, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageAvailable, Usage: counts}}}
	for _, status := range []run.TokenUsageStatus{run.TokenUsageAvailable, run.TokenUsagePending, run.TokenUsageUnavailable} {
		t.Run(string(status), func(t *testing.T) {
			runs[1].TokenUsage.Status = status
			v := BuildTask(owner, append(runs, runs[0], run.Run{ID: "foreign", TaskID: "other", ProjectID: "p", FeatureAreaID: "a"}), nil, bend)
			if v.Usage.DurationMS != (31*time.Minute+52*time.Second).Milliseconds() || v.Usage.ImplementationMS != (23*time.Minute+50*time.Second).Milliseconds() || v.Usage.VerificationMS != (8*time.Minute+2*time.Second).Milliseconds() || v.Usage.RunCount != 2 {
				t.Fatalf("duration/dedup/ownership: %+v", v.Usage)
			}
			if v.Usage.Status != status {
				t.Fatal(v.Usage)
			}
			if status == run.TokenUsageAvailable {
				if v.Usage.Tokens.Total != 240 || v.Usage.Tokens.NonCachedInput != 140 {
					t.Fatal(v.Usage.Tokens)
				}
			} else if v.Usage.Tokens != nil {
				t.Fatal("partial tokens presented as complete")
			}
			if !v.Terminal || v.Result.Status != "SUCCEEDED" {
				t.Fatal(v)
			}
		})
	}
	runs[0].TokenUsage.Status = run.TokenUsagePending
	runs[1].TokenUsage.Status = run.TokenUsageUnavailable
	if v := BuildTask(owner, runs, nil, bend); v.Usage.Status != run.TokenUsageUnavailable || len(v.Usage.UnavailableRuns) != 1 {
		t.Fatal(v.Usage)
	}
}

func TestTaskHistorySeparatesTransportReportAndAcceptance(t *testing.T) {
	now := time.Unix(500, 0)
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "repository", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"all required behavior is present"}, CodexInstruction: "verify"}
	owner := task.Task{ID: "task", ProjectID: "p", FeatureAreaID: "a", Type: task.TypeDevelopment, Status: task.StatusActive, Phase: task.PhaseAcceptancePending, CurrentExecutionID: "execution-1", ReviewApprovalID: "approval", VerificationSpec: verification, CreatedAt: now, UpdatedAt: now}
	execution := executionproposal.Proposal{ID: "execution-1", ProjectID: "p", TaskID: "task", Outcome: executionproposal.OutcomeSucceeded, ImplementationRunID: "implementation", VerificationRunID: "verification", VerificationSpec: verification, TaskDefinitionRevisionID: "revision-1", CreatedAt: now}
	report := &run.VerificationReport{Version: 2, Availability: run.VerificationReportAvailable, Summary: "environment dependency missing", Checks: []run.VerificationCheckOutcome{{ID: "check-1", Index: 0, Kind: string(task.VerificationCheckTestCommand), Required: true, DeclaredCommand: "go test ./...", Status: run.VerificationCheckBlocked, Reported: run.VerificationReportedFact{Status: run.VerificationCheckBlocked, Summary: "tool unavailable"}}}, AcceptanceCriteria: []run.VerificationCriterionOutcome{{Index: 0, Criterion: verification.AcceptanceCriteria[0], Reported: run.VerificationReportedFact{Status: run.VerificationCheckUnknown, Summary: "not established"}}}}
	runs := []run.Run{{ID: "implementation", TaskID: "task", ProjectID: "p", FeatureAreaID: "a", Purpose: run.PurposeImplementation, Status: run.StatusSucceeded, CreatedAt: now}, {ID: "verification", TaskID: "task", ProjectID: "p", FeatureAreaID: "a", Purpose: run.PurposeVerification, Status: run.StatusSucceeded, VerificationReport: report, CreatedAt: now.Add(time.Second)}}
	v := BuildTaskWithHistory(owner, runs, nil, []executionproposal.Proposal{execution}, nil, now.Add(time.Minute))
	if v.OverallState != "ACTIVE" || v.CurrentExecution == nil || v.CurrentExecution.Outcome != "SUCCEEDED" || v.VerificationRun == nil || v.VerificationRun.TransportStatus != "SUCCEEDED" || v.VerificationReport != report || v.CurrentExecutionAcceptance == nil || v.CurrentExecutionAcceptance.Status != "WAITING_ACCEPTANCE" {
		t.Fatalf("projection did not keep the four semantic layers separate: %+v", v)
	}
	want := map[string]bool{"ACCEPT": true, "REJECT": true, "REQUEST_CHANGES": true, "REQUEST_REVERIFICATION": true}
	for _, action := range v.CurrentExecutionAcceptance.AllowedActions {
		delete(want, action)
	}
	if len(want) != 0 {
		t.Fatalf("available report facts incorrectly constrained user options: missing=%v", want)
	}
	receipt := decisionresolution.Receipt{DecisionID: decisionresolution.TaskAcceptanceDecisionID(execution.ID), Kind: decisionresolution.KindTaskAcceptance, SubjectKind: decisionresolution.SubjectExecution, SubjectID: execution.ID, Option: "ACCEPT", ResultKind: decisionresolution.ResultTask, ResultID: owner.ID, ResolvedAt: now.Add(time.Minute)}
	owner.Status = task.StatusSucceeded
	accepted := BuildTaskWithHistory(owner, runs, nil, []executionproposal.Proposal{execution}, []decisionresolution.Receipt{receipt}, now.Add(2*time.Minute))
	if accepted.CurrentExecutionAcceptance == nil || accepted.CurrentExecutionAcceptance.Status != "ACCEPTED" || accepted.CurrentExecutionAcceptance.Receipt == nil || !accepted.Terminal {
		t.Fatalf("accepted receipt projection=%+v", accepted)
	}
}
func TestVerificationTransportIsNotSemanticPass(t *testing.T) {
	r := run.Run{Status: run.StatusSucceeded, Result: &run.Result{}}
	r.Result.AgentClaim.Message = "free-form agent narrative has no acceptance authority"
	if v := VerificationPresentation(r); v.Status != "REPORT_UNAVAILABLE" {
		t.Fatal(v)
	}
	r.VerificationReport = &run.VerificationReport{Version: 2, Availability: run.VerificationReportAvailable, Summary: "environment unavailable", Checks: []run.VerificationCheckOutcome{{Status: run.VerificationCheckBlocked}}}
	if v := VerificationPresentation(r); v.Status != "REPORT_AVAILABLE" || v.Summary != "environment unavailable" {
		t.Fatal(v)
	}
}
func TestTaskSemanticRailPlanAndGoalsIndependent(t *testing.T) {
	owner := task.Task{ID: "task", ProjectID: "p", Type: task.TypeDevelopment, Status: task.StatusActive, Phase: task.PhaseImplementing, Work: task.Work{RunID: "r"}, ReviewApprovalID: "approval", VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone}}
	r := run.Run{ID: "r", TaskID: "task", ProjectID: "p", FeatureAreaID: "area", ThreadID: "thread", TurnID: "turn", Purpose: run.PurposeImplementation, Status: run.StatusRunning, AgentRuntime: &agentruntime.Snapshot{Identity: agentruntime.Identity{RunID: "r", ProjectID: "p", FeatureAreaID: "area", TaskID: "task", ThreadID: "thread", TurnID: "turn"}, Availability: "AVAILABLE", WaitingState: "RUNNING", CurrentItem: &agentruntime.Item{Type: "commandExecution"}}}
	owner.FeatureAreaID = "area"
	now := time.Unix(700, 0)
	v := BuildTask(owner, []run.Run{r}, []taskgoal.Progress{{Title: "Preserve ownership", Status: taskgoal.StatusCompleted}}, now)
	want := []string{"REVIEW", "APPROVED", "IMPLEMENTATION", "VERIFICATION", "DONE"}
	for i, p := range v.Progress {
		if p.Phase != want[i] {
			t.Fatal(v.Progress)
		}
	}
	if v.Activity.Summary != "正在执行命令" || v.Activity.NativePlan != nil || len(v.Goals) != 1 || v.Goals[0].Status != "COMPLETED" {
		t.Fatal(v)
	}
	for _, kind := range []task.Type{task.TypeInspection} {
		owner.Type = kind
		v = BuildTask(owner, nil, nil, now)
		if len(v.Progress) != 1 || v.Progress[0].Phase != string(kind) {
			t.Fatal(v.Progress)
		}
	}
}

func TestHistoricalVerificationInfrastructureFailureSource(t *testing.T) {
	at := time.Unix(900, 0)
	owner := task.Task{ID: "historical", ProjectID: "p", FeatureAreaID: "area", Type: task.TypeDevelopment, Phase: task.PhaseVerifying, CurrentExecutionID: "execution", Status: task.StatusFailed}
	impl := run.Run{ID: "impl", TaskID: owner.ID, ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, Purpose: run.PurposeImplementation, Status: run.StatusSucceeded, CreatedAt: at}
	verify := run.Run{ID: "verify", TaskID: owner.ID, ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, Purpose: run.PurposeVerification, Status: run.StatusInfrastructureFailed, CreatedAt: at.Add(time.Second), Failure: &run.Failure{Kind: run.FailureInfrastructure, Code: "VERIFICATION_MUTATED_WORKTREE", Message: "managed worktree changed"}, Result: &run.Result{}}
	verify.Result.AgentClaim.Message = "验证通过; Task accepted"
	execution := executionproposal.Proposal{ID: "execution", TaskID: owner.ID, ProjectID: owner.ProjectID, Status: executionproposal.StatusConsumed, Outcome: executionproposal.OutcomeFailed, ImplementationRunID: impl.ID, VerificationRunID: verify.ID}
	for _, history := range [][]executionproposal.Proposal{{execution}} {
		v := BuildTaskWithHistory(owner, []run.Run{impl, verify}, nil, history, nil, at.Add(time.Minute))
		if v.OverallState != "FAILED" || !v.Terminal || v.ImplementationRun == nil || v.ImplementationRun.TransportStatus != "SUCCEEDED" || v.VerificationRun == nil || v.VerificationRun.TransportStatus != "INFRASTRUCTURE_FAILED" || v.VerificationRun.FailureCode != "VERIFICATION_MUTATED_WORKTREE" || v.VerificationReport == nil || v.VerificationReport.Availability != run.VerificationReportUnavailable || v.CurrentExecutionAcceptance.Status != "NOT_RECORDED" || len(v.CurrentExecutionAcceptance.AllowedActions) != 0 {
			t.Fatalf("legacy source lost: %+v", v)
		}
		text := RenderText(Projection{TaskView: &v})
		for _, fact := range []string{"Implementation Run transport", "SUCCEEDED", "INFRASTRUCTURE_FAILED", "VERIFICATION_MUTATED_WORKTREE", "UNAVAILABLE", "NOT_RECORDED"} {
			if !strings.Contains(text, fact) {
				t.Fatalf("missing %s: %s", fact, text)
			}
		}
	}
}
