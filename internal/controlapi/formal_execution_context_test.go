package controlapi_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

func formalExecutionContextInput(t *testing.T, f *upperLifecycleFixture, id fe.ReadIdentity) (c.CommitInput, c.ExecutionBody) {
	t.Helper()
	intent, err := f.plane.Confirmations.Get(f.ctx, id.AISessionID, id.IntentID)
	if err != nil || intent.Commit == nil || intent.Content.Execution == nil {
		t.Fatalf("original confirmation: %+v %v", intent, err)
	}
	frozen := *intent.Content.Execution
	if frozen.PreparedTopologyRevision <= 0 || frozen.PreparedDevelopmentContextHash == "" {
		t.Fatalf("APPROVE_AND_START did not freeze Project context: %+v", frozen)
	}
	commit := *intent.Commit
	commit.SubmittedHash = id.SubmittedHash
	return commit, frozen
}

func interruptFormalExecutionContext(t *testing.T, f *upperLifecycleFixture, id fe.ReadIdentity, ref fe.PlatformReference, table string) {
	t.Helper()
	if _, err := f.plane.DB.SQL().Exec(`CREATE TRIGGER formal_context_fault BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT,'formal-context-fault'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitApproveAndStart(f.ctx, id, ref)
	if err == nil || !strings.Contains(err.Error(), "formal-context-fault") {
		t.Fatalf("expected genuine transaction interruption: %v", err)
	}
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER formal_context_fault`); err != nil {
		t.Fatal(err)
	}
}

func changeApproveStartContext(t *testing.T, f *upperLifecycleFixture, kind string) string {
	t.Helper()
	if kind == "topology" {
		f.appendTopologyMember(t, "formal-approve-start-extra-member")
		return "PROJECT_TOPOLOGY_CHANGED"
	}
	changeFormalDevelopmentContext(t, f)
	return "DEVELOPMENT_BRANCH_CONFIGURATION_CHANGED"
}

func TestFormalExecutionContextFenceRejectsT1AndT2Changes(t *testing.T) {
	for _, stage := range []string{"T1", "T2"} {
		for _, change := range []string{"topology", "development"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				f, id, ref := formalExecutionFixture(t)
				f.plane.Retirement.Close()
				commit, frozen := formalExecutionContextInput(t, f, id)
				if stage == "T2" {
					interruptFormalExecutionContext(t, f, id, ref, "formal_execution_t2")
				}
				code := changeApproveStartContext(t, f, change)
				store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
				tables := definitionFormalEveryTable(t, f)
				before := definitionFormalSnapshot(t, f, tables)
				_, err := store.CommitApproveAndStart(f.ctx, id, ref)
				assertFormalContextFailure(t, err, code)
				assertDefinitionFormalSnapshot(t, f, tables, before, "writer rejects changed context without any durable writes")
				if stage == "T2" {
					// The public facade records the failed attempt on its journal;
					// every other durable fact must retain the exact T1 prefix.
					businessTables := slices.DeleteFunc(slices.Clone(tables), func(table string) bool { return table == "confirmation_intents" })
					businessBefore := definitionFormalSnapshot(t, f, businessTables)
					retried, err := f.facade.CommitConfirmation(f.ctx, commit)
					assertFormalContextFailure(t, err, code)
					if retried.State != "PARTIAL" || len(retried.Steps) != 1 || retried.Steps[0].RevisionID == "" || retried.Steps[0].ExecutionID != "" {
						t.Fatalf("public retry lost its committed T1 prefix: %+v", retried)
					}
					assertDefinitionFormalSnapshot(t, f, businessTables, businessBefore, "public retry cannot issue an Execution, Plan or authority in changed context")
					progress, err := store.ReadApproveAndStartProgress(f.ctx, id)
					if err != nil || progress.T2 != nil || progress.NextTransaction != fe.T2Transaction {
						t.Fatalf("blocked T1 must remain readable: %+v %v", progress, err)
					}
					_, after := formalExecutionContextInput(t, f, id)
					if !reflect.DeepEqual(after, frozen) {
						t.Fatal("retry rewrote the original Prepare context")
					}
				}
			})
		}
	}
}

func TestFormalExecutionContextFenceT3AndFinalUseCommittedPlan(t *testing.T) {
	for _, change := range []string{"topology", "development"} {
		t.Run(change, func(t *testing.T) {
			f, id, ref := formalExecutionFixture(t)
			f.plane.Retirement.Close()
			commit, frozen := formalExecutionContextInput(t, f, id)
			interruptFormalExecutionContext(t, f, id, ref, "formal_execution_receipts")
			store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
			progress, err := store.ReadApproveAndStartProgress(f.ctx, id)
			if err != nil || progress.T2 == nil || progress.Plan == nil || progress.NextTransaction != fe.T3Transaction {
				t.Fatalf("expected committed T2: %+v %v", progress, err)
			}
			if progress.Plan.TopologyRevision != frozen.PreparedTopologyRevision || progress.Plan.DevelopmentContextHash != frozen.PreparedDevelopmentContextHash {
				t.Fatalf("T2 plan does not use frozen Prepare context: %+v", progress.Plan)
			}
			changeApproveStartContext(t, f, change)
			completed, err := f.facade.CommitConfirmation(f.ctx, commit)
			if err != nil || completed.State != "COMPLETED" {
				t.Fatalf("T3 may finish the committed Plan: state=%s error=%v", completed.State, err)
			}
			receipt, err := store.ReadApproveAndStart(f.ctx, id)
			if err != nil || !reflect.DeepEqual(receipt.Plan, *progress.Plan) || receipt.T1 != progress.T1 || receipt.T2 != *progress.T2 {
				t.Fatalf("T3 rebound its immutable prefix: %v", err)
			}
			tables := definitionFormalEveryTable(t, f)
			before := definitionFormalSnapshot(t, f, tables)
			replayed, err := store.CommitApproveAndStart(f.ctx, id, fe.PlatformReference{})
			if err != nil || !reflect.DeepEqual(receipt, replayed) {
				t.Fatalf("final receipt consulted changed Project context: %v", err)
			}
			completed, err = f.facade.CommitConfirmation(f.ctx, commit)
			if err != nil || completed.State != "COMPLETED" {
				t.Fatalf("public final replay: state=%s error=%v", completed.State, err)
			}
			assertDefinitionFormalSnapshot(t, f, tables, before, "final business and public replay must be zero-write")
		})
	}
}
