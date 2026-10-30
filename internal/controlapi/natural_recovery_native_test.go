package controlapi_test

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
)

func TestNaturalRecoveryNativeWaitsForExplicitGPTAnalysis(t *testing.T) {
	runRecoveryStatusOffline(t, "complete-old-unknown", func(f *upperLifecycleFixture, _ controlplane.Config, cmd controlapi.RecoveryStatusCommand, calls *atomic.Int64) bool {
		if _, err := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); err != nil {
			t.Fatal(err)
		}
		native := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool { return r.AnalysisRequired || r.State == "FAILED" })
		if !native.AnalysisRequired || len(native.Report) > 0 || len(native.Assessment) > 0 {
			t.Fatalf("native output became engineering analysis: %+v", native)
		}
		child, err := f.plane.Runs.Get(f.ctx, native.RecoveryRunID)
		if err != nil || child.Status != run.StatusSucceeded || child.ResultProtocol != run.NaturalResultProtocol {
			t.Fatalf("child %+v %v", child, err)
		}
		source, err := f.facade.GetRunAnalysis(f.ctx, f.session, child.ID)
		if err != nil || source.State != "REQUIRED" || source.Kind != "RECOVERY" {
			t.Fatalf("source %+v %v", source, err)
		}
		report := recoverystatus.Report{Version: recoverystatus.ReportVersion, SourceOperationKind: cmd.OperationKind, SourceOperationID: cmd.OperationID, SourceRunID: cmd.RunID, SourceTurnID: cmd.TurnID, OverallStatus: "UNKNOWN", Summary: "GPT requires more engineering evidence; native completion alone is insufficient", CompletedGoals: []recoverystatus.GoalObservation{}, RemainingGoals: []recoverystatus.GoalObservation{}, RepositoryObservations: []recoverystatus.RepositoryObservation{}, VerificationObservations: []recoverystatus.VerificationObservation{}, UnresolvedIssues: []string{"No user recovery resolution has been granted"}, RecommendedNextAction: "REQUEST_USER"}
		raw, _ := json.Marshal(report)
		if _, err = f.facade.SubmitRunAnalysis(f.ctx, runanalysis.Input{AISessionID: f.session, RunID: child.ID, RequestID: "gpt-recovery-native-analysis", SourceHash: source.SourceHash, Report: raw}); err != nil {
			t.Fatal(err)
		}
		assessed := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool { return r.State == "ASSESSED" || r.State == "FAILED" })
		if assessed.State != "ASSESSED" || len(assessed.Resolution) > 0 || assessed.SourceResponseDisposition != native.SourceResponseDisposition || calls.Load() != 4 {
			t.Fatalf("analysis changed runtime or resolution: %+v calls=%d", assessed, calls.Load())
		}
		original, err := f.plane.Runs.Get(f.ctx, cmd.RunID)
		if err != nil || original.Status == run.StatusSucceeded {
			t.Fatalf("analysis resolved original owner: %+v %v", original, err)
		}
		return false
	})
}
