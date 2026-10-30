package sqlite

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/execution"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
)

func TestNaturalRecoveryCompletionKeepsSourceAuthorityUntilAnalysisAndResolution(t *testing.T) {
	runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
		boundary, err := NewAgentConversationStore(f.db).Boundary(ctx, source.ID)
		if err != nil {
			t.Fatal(err)
		}
		const raw = "诊断已执行结束；原任务完成情况仍需 GPT 分析并经用户确认。"
		after := json.RawMessage(`[]`)
		captured, _ := json.Marshal(run.ExecutionEvidence{Commands: []run.ObservedCommand{}, RecoveryAfter: after})
		native, err := store.SaveNaturalExecution(ctx, record.ID, record.RuntimeEpoch, []byte(raw), after, captured)
		if err != nil {
			t.Fatal(err)
		}
		if native.State != "RUNNING" || !native.AnalysisRequired || len(native.Report) > 0 || len(native.Assessment) > 0 || native.SourceResponseDisposition != record.SourceResponseDisposition {
			t.Fatalf("native completion inferred assessment: %+v", native)
		}
		current, err := NewRunStore(f.db).Get(ctx, child.ID)
		if err != nil || current.Status != run.StatusSucceeded || current.Result.AgentClaim.Message != raw {
			t.Fatalf("native child %+v %v", current, err)
		}
		ctx = aisession.WithCallerScope(ctx, "agent-fixture-recovery-child-canonical")
		service := &runanalysis.Service{Store: NewRunAnalysisStore(f.db), Analyze: execution.AnalyzeRunReport}
		evidence, err := service.Get(ctx, f.session.ID, child.ID)
		if err != nil || evidence.Kind != "RECOVERY" || evidence.State != "REQUIRED" {
			t.Fatalf("analysis source %+v %v", evidence, err)
		}
		report := rs.Report{Version: rs.ReportVersion, SourceOperationKind: record.Spec.OperationKind, SourceOperationID: record.Spec.OperationID, SourceRunID: source.ID, SourceTurnID: source.TurnID, OverallStatus: "UNKNOWN", Summary: "GPT requires more evidence before recovering the original owner", CompletedGoals: []rs.GoalObservation{}, RemainingGoals: []rs.GoalObservation{}, RepositoryObservations: []rs.RepositoryObservation{}, VerificationObservations: []rs.VerificationObservation{}, UnresolvedIssues: []string{"Original completion remains unproven"}, RecommendedNextAction: "REQUEST_USER"}
		formal, _ := json.Marshal(report)
		receipt, err := service.Submit(ctx, runanalysis.Input{AISessionID: f.session.ID, RunID: child.ID, RequestID: "recovery-gpt-analysis", SourceHash: evidence.SourceHash, Report: formal})
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Kind != "RECOVERY" {
			t.Fatalf("receipt %+v", receipt)
		}
		saved, err := store.Read(ctx, record.ID)
		if err != nil || saved.State != "REPORT_READY" || len(saved.Assessment) > 0 || len(saved.Resolution) > 0 {
			t.Fatalf("analysis resolved owner: %+v %v", saved, err)
		}
		afterBoundary, err := NewAgentConversationStore(f.db).Boundary(ctx, source.ID)
		if err != nil || !reflect.DeepEqual(boundary, afterBoundary) {
			t.Fatalf("source UNKNOWN boundary changed %+v %v", afterBoundary, err)
		}
		updated, err := NewRunStore(f.db).Get(ctx, source.ID)
		if err != nil || updated.Status != source.Status || updated.ThreadID != source.ThreadID || updated.TurnID != source.TurnID {
			t.Fatalf("source changed %+v %v", updated, err)
		}
		var claims int
		if err = f.db.sql.QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?`, source.ID).Scan(&claims); err != nil || claims != 1 {
			t.Fatalf("analysis released source claim: %d %v", claims, err)
		}
	})
}
