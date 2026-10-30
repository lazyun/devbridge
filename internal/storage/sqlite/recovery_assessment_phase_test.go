package sqlite

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func TestRecoveryBlockingAssessmentDoesNotWriteTaskPhase(t *testing.T) {
	for _, scopeValid := range []bool{true, false} {
		name := "insufficient"
		if !scopeValid {
			name = "conflict-recovery-failed"
		}
		t.Run(name, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				beforeTask, err := NewTaskStore(f.db).Get(ctx, source.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				if beforeTask.Phase == task.PhaseRecoveryRequired {
					t.Fatal("fixture already in recovery; phase mutation would be hidden")
				}
				report := rs.Report{Version: rs.ReportVersion, SourceOperationKind: record.Spec.OperationKind, SourceOperationID: record.Spec.OperationID, SourceRunID: record.Spec.RunID, SourceTurnID: record.Spec.TurnID, OverallStatus: "UNKNOWN", Summary: "Source completion is unproven", CompletedGoals: []rs.GoalObservation{}, RemainingGoals: []rs.GoalObservation{}, RepositoryObservations: []rs.RepositoryObservation{}, VerificationObservations: []rs.VerificationObservation{}, UnresolvedIssues: []string{"Evidence incomplete"}, RecommendedNextAction: "NONE"}
				raw, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				record, err = store.SaveReport(ctx, record.ID, record.RuntimeEpoch, raw, []byte(`[]`))
				if err != nil {
					t.Fatal(err)
				}
				facts := rs.FactualObservation{Source: record.Spec.Source, SourcePurpose: string(source.Purpose), RecoveryRunID: child.ID, RecoveryTurnID: child.TurnID, ReportID: record.ID, ReportHash: record.ReportHash, Workset: *source.RuntimeWorkset, Before: []rs.WorktreeObservation{}, After: []rs.WorktreeObservation{}, SourceTurnTerminal: true, BackendQuiescent: true, ScopeValid: scopeValid, SourceResponseDisposition: record.SourceResponseDisposition, ObservedAt: time.Now().UTC()}
				assessment, err := rs.Assess(report, facts)
				if err != nil {
					t.Fatal(err)
				}
				want := "RECOVERY_INSUFFICIENT"
				if !scopeValid {
					want = "RECOVERY_FAILED"
				}
				if assessment.EngineeringDisposition != want {
					t.Fatalf("assessment=%+v", assessment)
				}
				assessmentBytes, _ := json.Marshal(assessment)
				factBytes, _ := json.Marshal(facts)
				for i := 0; i < 2; i++ {
					saved, err := store.SaveAssessment(ctx, record.ID, record.RuntimeEpoch, assessmentBytes, factBytes)
					if err != nil || saved.State != "ASSESSED" {
						t.Fatalf("save=%+v %v", saved, err)
					}
					currentTask, err := NewTaskStore(f.db).Get(ctx, source.TaskID)
					if err != nil || !reflect.DeepEqual(beforeTask, currentTask) {
						t.Fatalf("assessment changed Task: before=%+v after=%+v err=%v", beforeTask, currentTask, err)
					}
					currentSource, err := NewRunStore(f.db).Get(ctx, source.ID)
					if err != nil || !reflect.DeepEqual(source, currentSource) {
						t.Fatalf("assessment changed source Run: %v", err)
					}
					state, err := readRuntimeAuthority(ctx, f.db.sql, source.FeatureAreaID)
					if err != nil || !state.Busy {
						t.Fatalf("blocking assessment lost recovery fence: %+v %v", state, err)
					}
					projection := rs.ProjectProjection(rs.Projection{Required: true, State: saved.State, Consistency: assessment.Consistency, EngineeringDisposition: assessment.EngineeringDisposition, RecoveryRunID: child.ID, RecoveryTurnID: child.TurnID})
					for _, action := range projection.AvailableActions {
						if action == "RESOLVE_RECOVERY_STATUS" {
							t.Fatal("blocking assessment offers resolution")
						}
					}
				}
			})
		})
	}
}
