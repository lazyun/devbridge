package controlapi_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func nodeChoiceInput(f *upperLifecycleFixture, owner task.Task, execution, session, request, action string) c.PrepareInput {
	note := ""
	if action == "DEFER_TASK" {
		note = "保存当前结果，稍后继续"
	}
	return c.PrepareInput{AISessionID: session, RequestID: request, Purpose: c.Execution, Action: action, Execution: &c.ExecutionBody{Action: action, TaskID: f.task, FeatureAreaID: f.area, ExecutionID: execution, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(execution), Note: note}, Variants: []c.Variant{{ID: "choice", Summary: action}}}
}

func nodeCommit(i c.Intent, request string) c.CommitInput {
	return c.CommitInput{AISessionID: i.AISessionID, IntentID: i.IntentID, Version: i.Version, Purpose: i.Purpose, Action: i.Action, RequestID: request, ContentHash: i.ContentHash, VariantID: i.Content.Variants[0].ID}
}
func nodeRead(i c.Intent) c.ReadInput {
	return c.ReadInput{AISessionID: i.AISessionID, IntentID: i.IntentID, Version: i.Version, Purpose: i.Purpose, ContentHash: i.ContentHash}
}
func requireNodeFailure(t *testing.T, err error, code string) {
	t.Helper()
	var f *c.Failure
	if !errors.As(err, &f) || f.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestTaskActionNodeCrossSessionConcurrentPrepare(t *testing.T) {
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, execution string) bool {
		owner := awaitAcceptanceRecoveryFixture(t, f, execution)
		otherCtx := aisession.WithCallerScope(context.Background(), "node-other-session")
		ref := aiprojectselection.ClientBindingReference{CallerScope: "node-other-session"}
		other, err := f.plane.AIContext.InitializeSession(otherCtx, ref, "node-init", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.plane.AIContext.BindProject(otherCtx, ref, other.AISessionID, f.project, "node-bind"); err != nil {
			t.Fatal(err)
		}
		contexts := []context.Context{f.ctx, otherCtx}
		sessions := []string{f.session, other.AISessionID}
		var intents [2]c.Intent
		var errs [2]error
		start := make(chan struct{})
		var wg sync.WaitGroup
		for n := range 2 {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				<-start
				intents[n], errs[n] = f.facade.PrepareConfirmation(contexts[n], nodeChoiceInput(f, owner, execution, sessions[n], "node-concurrent", "CONTINUE_TASK"))
			}(n)
		}
		close(start)
		wg.Wait()
		winner := 0
		if errs[0] != nil {
			winner = 1
		}
		loser := 1 - winner
		if errs[winner] != nil {
			t.Fatalf("no winner: %v / %v", errs[0], errs[1])
		}
		requireNodeFailure(t, errs[loser], "TASK_ACTION_PENDING")
		store := sqlite.NewConfirmationStore(f.plane.DB)
		a, err := store.ReadTaskActionNode(contexts[winner], f.project, f.task, sessions[winner])
		if err != nil {
			t.Fatal(err)
		}
		b, err := store.ReadTaskActionNode(contexts[loser], f.project, f.task, sessions[loser])
		if err != nil {
			t.Fatal(err)
		}
		if a.ID != b.ID || !a.Blocking || !b.Blocking || !a.Owned || b.Owned || a.IntentID != intents[winner].IntentID || b.IntentID != "" {
			t.Fatalf("node/privacy mismatch: %+v / %+v", a, b)
		}
		view, err := f.facade.TaskPresentationForSession(contexts[loser], sessions[loser], f.area, f.task)
		if err != nil {
			t.Fatal(err)
		}
		if view.TaskView.ActionNode == nil || view.TaskView.ActionNode.ID != a.ID {
			t.Fatal("shared projection missing")
		}
		for _, choice := range view.TaskView.VerificationDecisions.Choices {
			if choice.Available {
				t.Fatalf("foreign active choice: %+v", choice)
			}
		}
		return false
	})
}

func TestTaskActionNodeUnknownBlocksOtherWritersAndSurvivesRestart(t *testing.T) {
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, execution string) bool {
		owner := awaitAcceptanceRecoveryFixture(t, f, execution)
		i, err := f.facade.PrepareConfirmation(f.ctx, nodeChoiceInput(f, owner, execution, f.session, "node-unknown", "CONTINUE_TASK"))
		if err != nil {
			t.Fatal(err)
		}
		input := nodeCommit(i, "node-unknown-commit")
		input.SubmittedHash = c.ValueHash(input)
		step := c.Step{Kind: "RECORD_TASK_CHOICE", RequestID: "confirmation_" + c.ValueHash([]string{i.IntentID, "RECORD_TASK_CHOICE"})[:32], State: "UNKNOWN"}
		store := sqlite.NewConfirmationStore(f.plane.DB)
		if _, fresh, e := store.BeginCommit(f.ctx, input, []c.Step{step}); e != nil || !fresh {
			t.Fatalf("begin fresh=%t err=%v", fresh, e)
		}
		if _, err = store.RecordStep(f.ctx, f.session, i.IntentID, i.Version, step, "UNKNOWN"); err != nil {
			t.Fatal(err)
		}
		for _, cold := range []bool{false, true} {
			if cold {
				reopenDirectDeliveryFixture(t, f)
				f.plane.Retirement.Close()
			}
			_, err = f.facade.PrepareConfirmation(f.ctx, nodeChoiceInput(f, owner, execution, f.session, "node-other-prepare", "DEFER_TASK"))
			requireNodeFailure(t, err, "TASK_ACTION_PENDING")
			ctx := operatorActionContext(t, f.project, f.task, "node-operator", "DEFER_TASK")
			_, err = f.facade.ChangeTaskHoldForOperator(ctx, f.project, task.HoldInput{TaskID: f.task, FeatureAreaID: f.area, ExecutionID: execution, RevisionID: owner.CurrentDefinitionRevisionID, Action: "DEFER_TASK", RequestID: "node-operator-defer", Note: "保存"})
			requireNodeFailure(t, err, "TASK_ACTION_PENDING")
			original, e := f.facade.ReadConfirmation(f.ctx, nodeRead(i))
			if e != nil || original.State != "UNKNOWN" || original.CanResume {
				t.Fatalf("unknown changed: %+v %v", original, e)
			}
			out, e := f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(i))
			if e != nil || out.State != "UNKNOWN" {
				t.Fatalf("consumed proposal discarded: %+v %v", out, e)
			}
		}
		var n int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_hold_receipts WHERE task_id=?`, f.task).Scan(&n); err != nil || n != 0 {
			t.Fatalf("unknown leaked effects=%d %v", n, err)
		}
		return false
	})
}

func TestTaskActionNodeDiscardFencesOldCardsAndOperatorGeneration(t *testing.T) {
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, execution string) bool {
		owner := awaitAcceptanceRecoveryFixture(t, f, execution)
		store := sqlite.NewConfirmationStore(f.plane.DB)
		before, err := store.ReadTaskActionNode(f.ctx, f.project, f.task, f.session)
		if err != nil {
			t.Fatal(err)
		}
		old, err := f.facade.PrepareConfirmation(f.ctx, nodeChoiceInput(f, owner, execution, f.session, "node-old", "CONTINUE_TASK"))
		if err != nil {
			t.Fatal(err)
		}
		ctx := operatorActionContext(t, f.project, f.task, "node-webui", "DEFER_TASK")
		hold := task.HoldInput{TaskID: f.task, FeatureAreaID: f.area, ExecutionID: execution, RevisionID: owner.CurrentDefinitionRevisionID, Action: "DEFER_TASK", RequestID: "node-defer", Note: "保存"}
		_, err = f.facade.ChangeTaskHoldForOperator(ctx, f.project, hold)
		requireNodeFailure(t, err, "TASK_ACTION_PENDING")
		if out, e := f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(old)); e != nil || out.State != "SUPERSEDED" {
			t.Fatalf("discard: %+v %v", out, e)
		}
		stale := taskaction.WithExpectation(ctx, taskaction.Expectation{TaskID: f.task, Generation: before.Generation})
		_, err = f.facade.ChangeTaskHoldForOperator(stale, f.project, hold)
		requireNodeFailure(t, err, "TASK_ACTION_STALE")
		if _, err = f.facade.ChangeTaskHoldForOperator(ctx, f.project, hold); err != nil {
			t.Fatal(err)
		}
		stalePrepare := nodeChoiceInput(f, owner, execution, f.session, "node-late-gpt-prepare", "CONTINUE_TASK")
		stalePrepare.TaskActionGeneration = before.Generation
		_, err = f.facade.PrepareConfirmation(f.ctx, stalePrepare)
		requireNodeFailure(t, err, "TASK_ACTION_STALE")
		if _, err = f.facade.CommitConfirmation(f.ctx, nodeCommit(old, "late-old-card")); err == nil {
			t.Fatal("old card consumed")
		}
		i, err := f.facade.PrepareConfirmation(f.ctx, nodeChoiceInput(f, owner, execution, f.session, "node-current", "CONTINUE_TASK"))
		if err != nil {
			t.Fatal(err)
		}
		in := nodeCommit(i, "node-current-commit")
		out, err := f.facade.CommitConfirmation(f.ctx, in)
		if err != nil || out.State != "COMPLETED" {
			t.Fatalf("current commit: %+v %v", out, err)
		}
		if _, err = f.facade.CommitConfirmation(f.ctx, in); err != nil {
			t.Fatal("immutable replay blocked", err)
		}
		h, err := f.plane.WorkflowProgression.ReadTaskHold(f.ctx, f.project, f.task)
		if err != nil || h != nil {
			t.Fatalf("hold not cleared: %+v %v", h, err)
		}
		return false
	})
}

func TestTaskActionNodeReviewReplacementCannotConsumeOldSelection(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	submitLifecycleApprovalReady(t, f, "node-review-ready")
	old, oldCommit := prepareLifecycleReviewDecision(t, f, "REVISE", "node-review-old")
	_, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "node-review-other", Purpose: c.Review, Action: "REJECT", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review)}, Variants: []c.Variant{{ID: "reject", Summary: "Reject"}}})
	requireNodeFailure(t, err, "TASK_ACTION_PENDING")
	if _, err = f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(old)); err != nil {
		t.Fatal(err)
	}
	newer, _ := prepareLifecycleReviewDecision(t, f, "REJECT", "node-review-new")
	if _, err = f.facade.CommitConfirmation(f.ctx, oldCommit); err == nil {
		t.Fatal("retired review choice consumed")
	}
	var selected string
	if err = f.plane.DB.SQL().QueryRow(`SELECT intent_id FROM confirmation_current_decisions WHERE ai_session_id=? AND decision_id=?`, f.session, wp.DecisionID(f.review)).Scan(&selected); err != nil || selected != newer.IntentID {
		t.Fatalf("selector changed: %s %v", selected, err)
	}
}

func rewindProjectAuthorizationV135(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	rewindAgentPolicyV136(t, f)
	// Rewind the later authorization schema along with its migration marker.
	// These fixtures have no authorization history to preserve.
	for _, table := range []string{"project_execution_authorization_revisions", "project_execution_authorizations"} {
		var count int
		if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("cannot rewind nonempty authorization fixture %s: %d %v", table, count, err)
		}
		if _, err := f.plane.DB.SQL().Exec(`DROP TABLE ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.plane.DB.SQL().Exec(`DELETE FROM schema_migrations WHERE version=135`); err != nil {
		t.Fatal(err)
	}
}

func rebuildTaskActionV133(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	rows, err := f.plane.DB.SQL().Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE 'task_action_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "task_action_") {
			t.Fatal(name)
		}
		if _, err = f.plane.DB.SQL().Exec(fmt.Sprintf(`DROP TRIGGER "%s"`, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"task_action_releases", "task_action_conflict_resolutions", "task_action_confirmation_bindings", "task_action_heads"} {
		if _, err = f.plane.DB.SQL().Exec(`DROP TABLE ` + table); err != nil {
			t.Fatal(err)
		}
	}
	rewindProjectAuthorizationV135(t, f)
	if _, err = f.plane.DB.SQL().Exec(`DELETE FROM schema_migrations WHERE version BETWEEN 133 AND 135`); err != nil {
		t.Fatal(err)
	}
	var through133 []sqlite.Migration
	for _, migration := range sqlite.DefaultMigrations() {
		if migration.Version <= 133 {
			through133 = append(through133, migration)
		}
	}
	if err := sqlite.ApplyMigrations(f.ctx, f.plane.DB.SQL(), through133); err != nil {
		t.Fatal(err)
	}
}

// This historical fixture uses only native runs. Rewind the complete later
// additive schema together with its marker so the production monotonic
// migration runner can replay v133/v134 normally. Never discard cooperative
// authority or edit production migration behavior just to manufacture history.
func rewindAgentPolicyV136(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	tx, err := f.plane.DB.SQL().BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// A historical fixture has no worker delivery. Rewind the complete later
	// additive table together with its marker, never drop a live dispatch fact.
	var fileWorkers int
	if err = tx.QueryRow(`SELECT count(*) FROM direct_file_worker_claims`).Scan(&fileWorkers); err != nil || fileWorkers != 0 {
		t.Fatalf("cannot rewind file worker dispatch authority: %d %v", fileWorkers, err)
	}
	if _, err = tx.Exec(`DROP TABLE direct_file_worker_claims`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM schema_migrations WHERE version=138`); err != nil {
		t.Fatal(err)
	}
	var workers int
	if err = tx.QueryRow(`SELECT count(*) FROM upper_review_worker_claims`).Scan(&workers); err != nil || workers != 0 {
		t.Fatalf("cannot rewind worker dispatch authority: %d %v", workers, err)
	}
	if _, err = tx.Exec(`DROP TABLE upper_review_worker_claims`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM schema_migrations WHERE version=137`); err != nil {
		t.Fatal(err)
	}
	var explicit int
	if err = tx.QueryRow(`SELECT count(*) FROM runtime_scope_plans WHERE agent_id<>'' OR execution_policy<>'' OR agent_config_fingerprint<>''`).Scan(&explicit); err != nil || explicit != 0 {
		t.Fatalf("cannot rewind explicit agent-policy authority: %d %v", explicit, err)
	}
	for _, column := range []string{"agent_config_fingerprint", "execution_policy", "agent_id"} {
		if _, err = tx.Exec(`ALTER TABLE runtime_scope_plans DROP COLUMN ` + column); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(`DELETE FROM schema_migrations WHERE version=136`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskActionNodeV133PreservesLegacyConflictAndExplicitSelection(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	submitLifecycleApprovalReady(t, f, "node-migration-ready")
	first, _ := prepareLifecycleReviewDecision(t, f, "REVISE", "node-legacy-first")
	// Reconstruct the pre-v133 absence of a shared reservation. This metadata
	// fault is confined to the test; both proposals use real production prepare.
	if _, err := f.plane.DB.SQL().Exec(`UPDATE task_action_heads SET owner_intent_id=NULL WHERE task_id=?`, f.task); err != nil {
		t.Fatal(err)
	}
	second, _ := prepareLifecycleReviewDecision(t, f, "REJECT", "node-legacy-second")
	var beforeFirst, beforeSecond string
	for i, id := range []string{first.IntentID, second.IntentID} {
		var raw string
		if err := f.plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			beforeFirst = raw
		} else {
			beforeSecond = raw
		}
	}
	rebuildTaskActionV133(t, f)
	for range 2 {
		if err := sqlite.ApplyMigrations(f.ctx, f.plane.DB.SQL(), sqlite.DefaultMigrations()); err != nil {
			t.Fatal(err)
		}
	}
	var afterFirst, afterSecond string
	if err := f.plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, first.IntentID).Scan(&afterFirst); err != nil {
		t.Fatal(err)
	}
	if err := f.plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, second.IntentID).Scan(&afterSecond); err != nil {
		t.Fatal(err)
	}
	if beforeFirst != afterFirst || beforeSecond != afterSecond {
		t.Fatal("migration rewrote original confirmations")
	}
	node, err := sqlite.NewConfirmationStore(f.plane.DB).ReadTaskActionNode(f.ctx, f.project, f.task, f.session)
	if err != nil || node.State != "CONFLICT" || len(node.Candidates) != 2 {
		t.Fatalf("conflict lost: %+v %v", node, err)
	}
	selection := taskaction.ConflictSelection{AISessionID: f.session, TaskID: f.task, Generation: node.Generation, IntentID: first.IntentID, Version: first.Version, Purpose: string(first.Purpose), ContentHash: first.ContentHash}
	for range 2 {
		if _, err = f.facade.ResolveTaskActionConflict(f.ctx, selection); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.facade.CommitConfirmation(f.ctx, nodeCommit(second, "node-late-legacy")); err == nil {
		t.Fatal("retired legacy sibling consumed")
	}
	out, err := f.facade.CommitConfirmation(f.ctx, nodeCommit(first, "node-selected-commit"))
	if err != nil || out.State != "COMPLETED" {
		t.Fatalf("selected original cannot continue: %+v %v", out, err)
	}
}

func TestTaskActionNodeDiscardRacesConsumptionWithoutLosingTheWinner(t *testing.T) {
	for attempt := range 3 {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, execution string) bool {
				owner := awaitAcceptanceRecoveryFixture(t, f, execution)
				i, err := f.facade.PrepareConfirmation(f.ctx, nodeChoiceInput(f, owner, execution, f.session, "node-race", "CONTINUE_TASK"))
				if err != nil {
					t.Fatal(err)
				}
				start := make(chan struct{})
				var wg sync.WaitGroup
				var discarded c.Intent
				var discardErr, commitErr error
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					discarded, discardErr = f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(i))
				}()
				go func() {
					defer wg.Done()
					<-start
					_, commitErr = f.facade.CommitConfirmation(f.ctx, nodeCommit(i, "node-race-commit"))
				}()
				close(start)
				wg.Wait()
				final, err := sqlite.NewConfirmationStore(f.plane.DB).Get(f.ctx, f.session, i.IntentID)
				if err != nil {
					t.Fatal(err)
				}
				var effects int
				if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_hold_receipts WHERE task_id=?`, f.task).Scan(&effects); err != nil {
					t.Fatal(err)
				}
				if final.Commit == nil {
					if final.State != "SUPERSEDED" || discardErr != nil || commitErr == nil || effects != 0 {
						t.Fatalf("discard winner lost: state=%s discard=%v commit=%v effects=%d", final.State, discardErr, commitErr, effects)
					}
				} else {
					if final.State != "COMPLETED" || commitErr != nil || effects != 1 {
						t.Fatalf("consumption winner lost: state=%s commit=%v effects=%d", final.State, commitErr, effects)
					}
					if discardErr == nil && discarded.Commit == nil {
						t.Fatal("discard erased consumed parent")
					}
				}
				return false
			})
		})
	}
}

func TestTaskActionNodeOperatorRacesGPTPrepareAtObservedGeneration(t *testing.T) {
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, execution string) bool {
		owner := awaitAcceptanceRecoveryFixture(t, f, execution)
		node, err := sqlite.NewConfirmationStore(f.plane.DB).ReadTaskActionNode(f.ctx, f.project, f.task, f.session)
		if err != nil {
			t.Fatal(err)
		}
		in := nodeChoiceInput(f, owner, execution, f.session, "node-operator-race", "CONTINUE_TASK")
		in.TaskActionGeneration = node.Generation
		ctx := taskaction.WithExpectation(operatorActionContext(t, f.project, f.task, "node-race-webui", "DEFER_TASK"), taskaction.Expectation{TaskID: f.task, Generation: node.Generation})
		hold := task.HoldInput{TaskID: f.task, FeatureAreaID: f.area, ExecutionID: execution, RevisionID: owner.CurrentDefinitionRevisionID, Action: "DEFER_TASK", RequestID: "node-operator-race-defer", Note: "保存"}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var prepared c.Intent
		var prepareErr, operatorErr error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; prepared, prepareErr = f.facade.PrepareConfirmation(f.ctx, in) }()
		go func() {
			defer wg.Done()
			<-start
			_, operatorErr = f.facade.ChangeTaskHoldForOperator(ctx, f.project, hold)
		}()
		close(start)
		wg.Wait()
		if (prepareErr == nil) == (operatorErr == nil) {
			t.Fatalf("expected one admission winner: prepare=%v operator=%v", prepareErr, operatorErr)
		}
		var effects int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_hold_receipts WHERE task_id=?`, f.task).Scan(&effects); err != nil {
			t.Fatal(err)
		}
		if prepareErr == nil {
			requireNodeFailure(t, operatorErr, "TASK_ACTION_PENDING")
			if effects != 0 || prepared.IntentID == "" {
				t.Fatal("losing operator wrote a business effect")
			}
		} else {
			requireNodeFailure(t, prepareErr, "TASK_ACTION_STALE")
			if effects != 1 {
				t.Fatalf("operator effects=%d", effects)
			}
		}
		return false
	})
}

func TestTaskActionReconciliationFencesStaleProposalsButRetainsUnknownEffects(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		t.Run(fmt.Sprintf("consumed-%t", consumed), func(t *testing.T) {
			runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, execution string) bool {
				owner := awaitAcceptanceRecoveryFixture(t, f, execution)
				first, err := f.facade.PrepareConfirmation(f.ctx, nodeChoiceInput(f, owner, execution, f.session, "stale-original", "CONTINUE_TASK"))
				if err != nil {
					t.Fatal(err)
				}
				store := sqlite.NewConfirmationStore(f.plane.DB)
				if consumed {
					in := nodeCommit(first, "stale-original-commit")
					in.SubmittedHash = c.ValueHash(in)
					step := c.Step{Kind: "RECORD_TASK_CHOICE", RequestID: "confirmation_" + c.ValueHash([]string{first.IntentID, "RECORD_TASK_CHOICE"})[:32], State: "UNKNOWN"}
					if _, fresh, e := store.BeginCommit(f.ctx, in, []c.Step{step}); e != nil || !fresh {
						t.Fatalf("begin %t %v", fresh, e)
					}
					if _, e := store.RecordStep(f.ctx, f.session, first.IntentID, first.Version, step, "UNKNOWN"); e != nil {
						t.Fatal(e)
					}
				}
				// Model the pre-v133 absence of one shared reservation, then a real
				// workflow fact advancing the generation after both old proposals.
				if _, err = f.plane.DB.SQL().Exec(`UPDATE task_action_heads SET owner_intent_id=NULL WHERE task_id=?`, f.task); err != nil {
					t.Fatal(err)
				}
				second, err := f.facade.PrepareConfirmation(f.ctx, nodeChoiceInput(f, owner, execution, f.session, "stale-sibling", "DEFER_TASK"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.plane.DB.SQL().Exec(`UPDATE task_action_heads SET owner_intent_id=NULL,conflict=1,generation=generation+1 WHERE task_id=?`, f.task); err != nil {
					t.Fatal(err)
				}
				original := definitionFormalSnapshot(t, f, []string{"confirmation_intents"})
				n, err := store.ReadTaskActionNode(f.ctx, f.project, f.task, f.session)
				if err != nil {
					t.Fatal(err)
				}
				if consumed {
					if !n.Blocking || n.IntentID != first.IntentID || n.State != "UNKNOWN" {
						t.Fatalf("unknown lost: %+v", n)
					}
				} else if n.Blocking {
					t.Fatalf("stale proposals still block: %+v", n)
				}
				rewindProjectAuthorizationV135(t, f)
				if _, err = f.plane.DB.SQL().Exec(`DELETE FROM schema_migrations WHERE version=134`); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if err = sqlite.ApplyMigrations(f.ctx, f.plane.DB.SQL(), sqlite.DefaultMigrations()); err != nil {
						t.Fatal(err)
					}
				}
				assertDefinitionFormalSnapshot(t, f, []string{"confirmation_intents"}, original, "stale generation migration")
				_, err = f.facade.CommitConfirmation(f.ctx, nodeCommit(second, "late-stale-sibling"))
				requireNodeFailure(t, err, "TASK_ACTION_STALE")
				var released int
				if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_action_releases WHERE intent_id=?`, first.IntentID).Scan(&released); err != nil {
					t.Fatal(err)
				}
				if consumed {
					if released != 0 {
						t.Fatal("unknown consumed effect released")
					}
					_, err = f.facade.PrepareConfirmation(f.ctx, nodeChoiceInput(f, owner, execution, f.session, "after-history", "CONTINUE_TASK"))
					requireNodeFailure(t, err, "TASK_ACTION_PENDING")
				} else {
					if released != 1 {
						t.Fatal("stale original lacks proof receipt")
					}
					if _, err = f.facade.PrepareConfirmation(f.ctx, nodeChoiceInput(f, owner, execution, f.session, "after-history", "CONTINUE_TASK")); err != nil {
						t.Fatalf("fresh proposal blocked: %v", err)
					}
				}
				return false
			})
		})
	}
}
