package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestRejectedImplementationPreparationSettlementGuards(t *testing.T) {
	for _, name := range []string{"safe", "unknown_receipt", "accepted_receipt", "preparing_receipt", "receipt_write_abort", "nonterminal_run", "wrong_code", "wrong_stage", "runtime_boundary", "wrong_execution", "wrong_run", "stale_context", "retiring_area"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := openTestDB(t, t.TempDir())
			defer db.Close()
			current, session := readyExecutionTask(t, db, "settle-"+name)
			proposal, reserved := reserveTaskExecutionFull(t, db, session.ID, current.ID)
			store := NewExecutionProposalStore(db)
			if _, admitted, err := store.ClaimHandoff(ctx, proposal.ProjectID, reserved.ID, testNow); err != nil || !admitted {
				t.Fatalf("claim %t %v", admitted, err)
			}
			clock := testNow.Add(time.Second)
			runs := run.NewService(NewRunStore(db), nil, func() time.Time { clock = clock.Add(time.Second); return clock })
			if _, err := runs.Transition(ctx, reserved.ID, run.StatusPreparing); err != nil {
				t.Fatal(err)
			}
			if name == "runtime_boundary" {
				if _, err := NewAgentConversationStore(db).BeginRun(ctx, reserved.ID); err != nil {
					t.Fatal(err)
				}
			}
			code, stage := "RUNTIME_WORKSET_PREPARATION_FAILED", "exact repository workset"
			if name == "wrong_code" {
				code = "OTHER_PREPARATION_FAILURE"
			}
			if name == "wrong_stage" {
				stage = "other preparation"
			}
			if name != "nonterminal_run" {
				if _, err := runs.Fail(ctx, reserved.ID, run.Failure{Kind: run.FailureInfrastructure, Code: code, Stage: stage, Message: "unit-controlled never-entered preparation"}); err != nil {
					t.Fatal(err)
				}
			}
			state := executionproposal.HandoffRejected
			if name == "unknown_receipt" {
				state = executionproposal.HandoffUnknown
			}
			if name == "accepted_receipt" {
				state = executionproposal.HandoffAccepted
			}
			if name == "receipt_write_abort" {
				if _, err := db.sql.Exec(`CREATE TRIGGER reject_handoff_receipt BEFORE UPDATE ON runtime_handoffs BEGIN SELECT RAISE(ABORT,'test receipt write rollback'); END`); err != nil {
					t.Fatal(err)
				}
				if _, err := store.RecordHandoff(ctx, proposal.ProjectID, reserved.ID, state, "RUNTIME_WORKSET_PREPARATION_FAILED", clock); err == nil {
					t.Fatal("receipt fault did not fire")
				}
				if _, err := db.sql.Exec(`DROP TRIGGER reject_handoff_receipt`); err != nil {
					t.Fatal(err)
				}
			} else if name != "preparing_receipt" {
				if _, err := store.RecordHandoff(ctx, proposal.ProjectID, reserved.ID, state, "RUNTIME_WORKSET_PREPARATION_FAILED", clock); err != nil {
					t.Fatal(err)
				}
			}
			if name == "retiring_area" {
				if _, err := NewFeatureAreaStore(db).beginRetirement(ctx, featurearea.LifecycleMutation{ProjectID: proposal.ProjectID, FeatureAreaID: current.FeatureAreaID, AISessionID: session.ID, RequestID: "retire-before-settlement", PayloadSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("unit-retirement"))), Action: featurearea.ActionRetire, At: clock}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := store.Get(ctx, proposal.ID)
			if err != nil {
				t.Fatal(err)
			}
			var beforeClaims, beforeEvents int
			if err = db.sql.QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?`, reserved.ID).Scan(&beforeClaims); err != nil {
				t.Fatal(err)
			}
			if err = db.sql.QueryRow(`SELECT count(*) FROM events`).Scan(&beforeEvents); err != nil {
				t.Fatal(err)
			}
			executionID, runID := proposal.ID, reserved.ID
			if name == "wrong_execution" {
				executionID = "unrelated-execution"
			}
			if name == "wrong_run" {
				runID = "unrelated-run"
			}
			settleCtx := ctx
			if name == "stale_context" {
				settleCtx = runtimeepoch.WithFence(ctx, "runtime_stale_settlement", reserved.ID)
			}
			got, settled, err := store.SettleRejectedImplementationPreparation(settleCtx, executionID, runID, clock.Add(time.Second))
			if name == "stale_context" {
				if !errors.Is(err, runtimeepoch.ErrFenced) {
					t.Fatalf("stale authority error %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after, err := store.Get(ctx, proposal.ID)
			if err != nil {
				t.Fatal(err)
			}
			var afterClaims, afterEvents int
			if err = db.sql.QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?`, reserved.ID).Scan(&afterClaims); err != nil {
				t.Fatal(err)
			}
			if err = db.sql.QueryRow(`SELECT count(*) FROM events`).Scan(&afterEvents); err != nil {
				t.Fatal(err)
			}
			if name == "safe" {
				if !settled || got.Outcome != executionproposal.OutcomeFailed || after.Outcome != executionproposal.OutcomeFailed || afterClaims != 0 || beforeClaims != 1 || afterEvents != beforeEvents+1 {
					t.Fatalf("safe settlement got=%+v settled=%t claims=%d/%d events=%d/%d", got, settled, beforeClaims, afterClaims, beforeEvents, afterEvents)
				}
				if _, again, err := store.SettleRejectedImplementationPreparation(ctx, proposal.ID, reserved.ID, clock.Add(2*time.Second)); err != nil || again {
					t.Fatalf("duplicate settlement %t %v", again, err)
				}
			} else if settled || !reflect.DeepEqual(before, after) || beforeClaims != afterClaims || beforeEvents != afterEvents {
				t.Fatalf("guard %s changed authority settled=%t before=%+v after=%+v claims=%d/%d events=%d/%d", name, settled, before, after, beforeClaims, afterClaims, beforeEvents, afterEvents)
			}
		})
	}
}
