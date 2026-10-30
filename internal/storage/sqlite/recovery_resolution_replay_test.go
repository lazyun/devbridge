package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

func TestRecoveryResolutionExactReplaySurvivesRestart(t *testing.T) {
	for _, disposition := range []string{"RECOVERED_PARTIAL", "RECOVERED_COMPLETE"} {
		for _, cold := range []bool{false, true} {
			name := disposition + "/warm"
			if cold {
				name = disposition + "/cold"
			}
			t.Run(name, func(t *testing.T) {
				runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
					_, in := prepareDiagnosticResolutionTest(t, f, ctx, record, source, disposition)
					stale := in
					stale.RuntimeEpoch += "-stale"
					stale.PayloadHash = stale.ComputePayloadHash()
					beforeResolution, err := store.Read(ctx, record.ID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = store.Resolve(ctx, stale); !errors.Is(err, rs.ErrStale) {
						t.Fatalf("uncommitted resolution bypassed live epoch: %v", err)
					}
					afterRejection, err := store.Read(ctx, record.ID)
					if err != nil || !reflect.DeepEqual(beforeResolution, afterRejection) {
						t.Fatalf("stale resolution mutated record: %v", err)
					}
					resolved, err := store.Resolve(ctx, in)
					if err != nil {
						t.Fatal(err)
					}
					if cold {
						root := f.db.stateRoot
						if err = f.db.Close(); err != nil {
							t.Fatal(err)
						}
						reopened := openTestDB(t, root)
						defer reopened.Close()
						store = NewRecoveryStatusStore(reopened)
						if reopened.RuntimeEpoch() == in.RuntimeEpoch {
							t.Fatal("restart kept old epoch")
						}
					}
					beforeSource, err := NewRunStore(store.db).Get(ctx, source.ID)
					if err != nil {
						t.Fatal(err)
					}
					beforeTask, err := NewTaskStore(store.db).Get(ctx, source.TaskID)
					if err != nil {
						t.Fatal(err)
					}
					// A replay must not even update the record or revisit a materializer.
					if _, err = store.db.sql.ExecContext(ctx, `CREATE TRIGGER reject_recovery_replay_update BEFORE UPDATE ON recovery_status_records BEGIN SELECT RAISE(ABORT,'replay attempted write'); END`); err != nil {
						t.Fatal(err)
					}
					replay, err := store.Resolve(ctx, in)
					if err != nil || !reflect.DeepEqual(replay, resolved) {
						t.Fatalf("exact historical replay changed result: err=%v before=%+v after=%+v", err, resolved, replay)
					}
					for _, field := range []string{"request", "actor", "epoch", "assessment", "report", "purpose"} {
						changed := in
						switch field {
						case "request":
							changed.RequestID += "-other"
						case "actor":
							changed.ActorID += "-other"
						case "epoch":
							changed.RuntimeEpoch = store.db.RuntimeEpoch() + "-other"
						case "assessment":
							changed.AssessmentHash += "-other"
						case "report":
							changed.ReportHash += "-other"
						case "purpose":
							changed.Purpose = &rs.PurposeProposal{}
						}
						changed.PayloadHash = changed.ComputePayloadHash()
						if _, err = store.Resolve(ctx, changed); !errors.Is(err, rs.ErrConflict) {
							t.Fatalf("changed %s accepted or misclassified: %v", field, err)
						}
					}
					afterSource, err := NewRunStore(store.db).Get(ctx, source.ID)
					if err != nil || !reflect.DeepEqual(beforeSource, afterSource) {
						t.Fatalf("source changed: %v", err)
					}
					afterTask, err := NewTaskStore(store.db).Get(ctx, source.TaskID)
					if err != nil || !reflect.DeepEqual(beforeTask, afterTask) {
						t.Fatalf("task changed: %v", err)
					}
				})
			})
		}
	}
}
