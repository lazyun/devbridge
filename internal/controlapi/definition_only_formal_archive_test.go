package controlapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/formalapproval"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

type definitionFormalFixture struct {
	f                      *upperLifecycleFixture
	parent                 task.Task
	parentJSON, parentHash string
	first, prepared        c.Intent
	firstCommit, commit    c.CommitInput
}

// Both revisions come from the canonical read-only Review protocol and its
// evidence-backed report. SQL below only reads identities; it never fabricates
// an approval, a parent revision, or a successful lifecycle boundary.
func newDefinitionFormalFixture(t *testing.T) *definitionFormalFixture {
	t.Helper()
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	x := &definitionFormalFixture{f: f}
	x.first, x.firstCommit = prepareDefinitionFormalApproval(t, f, "formal-parent")
	approved, err := f.facade.CommitConfirmation(f.ctx, x.firstCommit)
	if err != nil || approved.State != "COMPLETED" {
		t.Fatalf("first definition approval: state=%s err=%v", approved.State, err)
	}
	x.first = approved
	x.parent, err = f.facade.GetTask(f.ctx, f.task)
	if err != nil || x.parent.CurrentExecutionID != "" || x.parent.Phase != task.PhaseImplementationReady || x.parent.CurrentDefinitionRevisionID == "" {
		t.Fatalf("real parent: task=%+v err=%v", x.parent, err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=?`, x.parent.CurrentDefinitionRevisionID).Scan(&x.parentJSON, &x.parentHash); err != nil {
		t.Fatal(err)
	}
	var parent task.Definition
	if err = json.Unmarshal([]byte(x.parentJSON), &parent); err != nil || parent.Version != 2 {
		t.Fatalf("parent definition version=%d err=%v", parent.Version, err)
	}
	startDefinitionFormalSupplemental(t, f, x.parent, "formal-supplemental")
	x.prepared, x.commit = prepareDefinitionFormalApproval(t, f, "formal-child")
	return x
}

func startDefinitionFormalSupplemental(t *testing.T, f *upperLifecycleFixture, current task.Task, request string) {
	t.Helper()
	start, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{
		AISessionID: f.session, RequestID: request, Purpose: c.Review, Action: "REQUEST_SUPPLEMENTAL_REVIEW",
		Review:   &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: workset.CloneTargets(f.targets), ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}, SupplementalPurpose: "Recheck the exact approved definition before creating its next revision", ExpectedTarget: &wp.TaskReviewRequestTarget{Phase: current.Phase, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, CurrentReviewID: current.CurrentReviewID, CurrentExecutionID: current.CurrentExecutionID}},
		Variants: []c.Variant{{ID: "upper", Summary: "Recheck both exact members", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read both same.txt contracts and propose the next exact definition", PrimaryMemberID: f.targets[0].MemberID}},
	})
	if err != nil {
		t.Fatalf("prepare supplemental Review: %v", err)
	}
	started, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, Action: start.Action, RequestID: request + "-commit", ContentHash: start.ContentHash, VariantID: "upper"})
	if err != nil || started.State != "COMPLETED" || len(started.Steps) != 2 || started.Steps[1].ReviewID == "" {
		t.Fatalf("supplemental Review: state=%s steps=%+v err=%v", started.State, started.Steps, err)
	}
	f.review, f.run = started.Steps[1].ReviewID, started.Steps[1].RunID
	f.access, err = f.facade.UpperReviewAccessForSession(f.ctx, f.session, f.review)
	if err != nil || f.access.State != "ACTIVE" {
		t.Fatalf("supplemental access=%+v err=%v", f.access, err)
	}
}

func prepareDefinitionFormalApproval(t *testing.T, f *upperLifecycleFixture, request string) (c.Intent, c.CommitInput) {
	t.Helper()
	targets, work, verification, _ := submitLifecycleApprovalReady(t, f, request+"-result")
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{
		AISessionID: f.session, RequestID: request + "-prepare", Purpose: c.Execution, Action: "APPROVE",
		Execution: &c.ExecutionBody{Action: "APPROVE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: current.CurrentExecutionID},
		Variants:  []c.Variant{{ID: "approve", Summary: "Approve the exact definition without starting implementation", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}},
	})
	if err != nil {
		t.Fatalf("prepare approval %s: %v", request, err)
	}
	return prepared, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: request + "-commit", ContentHash: prepared.ContentHash, VariantID: "approve"}
}

func TestDefinitionOnlyFormalArchiveWorkflowFixture(t *testing.T) {
	x := newDefinitionFormalFixture(t)
	current, err := x.f.facade.GetTask(x.f.ctx, x.f.task)
	if err != nil || current.CurrentDefinitionRevisionID != x.parent.CurrentDefinitionRevisionID || current.CurrentReviewID == x.parent.CurrentReviewID || current.Phase != task.PhaseReviewApprovalPending {
		t.Fatalf("supplemental approval did not retain its true parent: %+v %v", current, err)
	}
}

func definitionFormalStore(t *testing.T, f *upperLifecycleFixture) formalapproval.Store {
	t.Helper()
	store, ok := any(f.plane.Confirmations).(formalapproval.Store)
	if !ok {
		t.Fatal("production ConfirmationStore does not expose formal approval read/finalize")
	}
	return store
}

func definitionFormalIdentity(t *testing.T, f *upperLifecycleFixture, prepared c.Intent, request string) formalapproval.ReadIdentity {
	t.Helper()
	stored, err := f.plane.Confirmations.Get(f.ctx, f.session, prepared.IntentID)
	if err != nil || stored.Commit == nil {
		t.Fatalf("read normalized commit: %v", err)
	}
	submission, err := f.plane.Confirmations.ReadSubmission(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: request})
	if err != nil || !submission.Recorded || submission.PayloadSHA256 == "" {
		t.Fatalf("read original submission: %+v %v", submission, err)
	}
	return formalapproval.ReadIdentity{AISessionID: f.session, IntentID: prepared.IntentID, IntentVersion: prepared.Version, RequestID: request, ContentHash: prepared.ContentHash, TypedPayloadHash: formalapproval.HashCommit(*stored.Commit), SubmittedHash: submission.PayloadSHA256}
}

var definitionFormalBusinessTables = []string{
	"tasks", "task_definition_revisions", "task_goals", "feature_areas", "reviews", "review_result_materializations", "review_approval_decisions", "decision_resolution_receipts", "review_approval_receipt_context", "review_approval_materializations", "confirmation_approval_sources", "events",
	"formal_definition_approval_fences", "formal_definition_approval_business", "formal_definition_approval_receipts",
}

var definitionFormalRuntimeTables = []string{
	"execution_proposals", "execution_start_effects", "execution_proposal_admissions", "operator_task_execution_start_receipts", "runs", "run_repository_bindings", "runtime_scope_plans", "runtime_preparation_authorities", "runtime_preparation_attempts", "runtime_workspace_preparation_claims", "runtime_workspace_observations", "runtime_workset_bindings", "runtime_start_authorities", "runtime_preparation_receipts", "runtime_handoffs", "runtime_platform_qualifications",
}

func definitionFormalSnapshot(t *testing.T, f *upperLifecycleFixture, tables []string) string {
	t.Helper()
	all := map[string][][]any{}
	for _, table := range tables {
		rows, err := f.plane.DB.SQL().Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		all[table] = nil
		for rows.Next() {
			values, targets := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, value := range values {
				if raw, ok := value.([]byte); ok {
					values[i] = string(raw)
				}
			}
			all[table] = append(all[table], values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		sort.Slice(all[table], func(i, j int) bool {
			a, _ := json.Marshal(all[table][i])
			b, _ := json.Marshal(all[table][j])
			return string(a) < string(b)
		})
	}
	raw, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func definitionFormalAllTables() []string {
	all := append([]string{}, definitionFormalBusinessTables...)
	all = append(all, definitionFormalRuntimeTables...)
	return append(all, "confirmation_intents", "confirmation_commit_requests")
}

func assertDefinitionFormalSnapshot(t *testing.T, f *upperLifecycleFixture, tables []string, before string, label string) {
	t.Helper()
	if after := definitionFormalSnapshot(t, f, tables); after != before {
		t.Fatalf("%s changed durable facts:\n%s", label, definitionRuntimeSnapshotDiff(t, f, before, after))
	}
}

func assertDefinitionFormalReceipt(t *testing.T, x *definitionFormalFixture, identity formalapproval.ReadIdentity, receipt formalapproval.Receipt) {
	t.Helper()
	if err := identity.MatchesReceipt(receipt); err != nil {
		t.Fatalf("formal receipt validation: %v", err)
	}
	f, s := x.f, receipt.Snapshot
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil || current.Phase != task.PhaseImplementationReady || current.CurrentExecutionID != "" || current.CurrentDefinitionRevisionID == x.parent.CurrentDefinitionRevisionID {
		t.Fatalf("definition-only output: %+v %v", current, err)
	}
	var revisionJSON, revisionHash string
	if err = f.plane.DB.SQL().QueryRow(`SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=?`, current.CurrentDefinitionRevisionID).Scan(&revisionJSON, &revisionHash); err != nil {
		t.Fatal(err)
	}
	if s.Fence.ParentRevisionID != x.parent.CurrentDefinitionRevisionID || s.Fence.ParentRevisionHash != x.parentHash || string(s.Fence.ParentDefinitionJSON) != x.parentJSON || s.Fence.TaskID != f.task || s.Fence.ReviewID != f.review || s.Fence.DecisionID != wp.DecisionID(f.review) || s.Fence.DecisionVersion == "" || s.RevisionID != current.CurrentDefinitionRevisionID || s.RevisionHash != revisionHash || string(s.DefinitionJSON) != revisionJSON || s.ResultID != wp.DecisionID(f.review) {
		t.Fatalf("formal archive disagrees with real parent/output identities: fence=%+v result=%s revision=%s", s.Fence, s.ResultID, s.RevisionID)
	}
	if s.Core.TaskPhase != string(task.PhaseReviewApprovalPending) || s.Core.CurrentRevisionID != x.parent.CurrentDefinitionRevisionID || s.Core.CurrentExecutionID != "" || s.Core.DecisionState != "PENDING" || s.FrozenLineage != s.ResultLineage || s.FrozenLineage.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID {
		t.Fatalf("formal archive lost pre-approval facts or exact lineage: core=%+v frozen=%+v result=%+v", s.Core, s.FrozenLineage, s.ResultLineage)
	}
	if receipt.Role != formalapproval.CurrentReceiptRole || receipt.ProducerKind != "DEFINITION_ONLY_COMMIT" || receipt.TransitionID != "confirmation.commit" || receipt.BusinessOutcome != "COMMITTED" || receipt.ConsumptionDisposition != "CONSUMED" || s.Commit.Selection.RuntimeConfig != nil || s.Commit.BackendSelection != nil {
		t.Fatalf("definition-only receipt gained runtime authority: %+v", receipt)
	}
}

func TestDefinitionOnlyFormalArchiveSurvivesColdReplay(t *testing.T) {
	x := newDefinitionFormalFixture(t)
	f := x.f
	runtimeBefore := definitionFormalSnapshot(t, f, definitionFormalRuntimeTables)
	approved, err := f.facade.CommitConfirmation(f.ctx, x.commit)
	if err != nil || approved.State != "COMPLETED" {
		t.Fatalf("archive approval: state=%s err=%v", approved.State, err)
	}
	identity := definitionFormalIdentity(t, f, x.prepared, x.commit.RequestID)
	receipt, err := definitionFormalStore(t, f).ReadDefinitionApproval(f.ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	assertDefinitionFormalReceipt(t, x, identity, receipt)
	assertDefinitionFormalSnapshot(t, f, definitionFormalRuntimeTables, runtimeBefore, "NO_START formal approval")
	for _, cold := range []bool{false, true} {
		if cold {
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
		}
		before := definitionFormalSnapshot(t, f, definitionFormalAllTables())
		store := definitionFormalStore(t, f)
		for _, read := range []func(context.Context, formalapproval.ReadIdentity) (formalapproval.Receipt, error){store.ReadDefinitionApproval, store.FinalizeDefinitionApproval} {
			got, err := read(f.ctx, identity)
			if err != nil || !reflect.DeepEqual(got, receipt) {
				t.Fatalf("cold=%v exact archive replay: %v", cold, err)
			}
		}
		replay, err := f.facade.CommitConfirmation(f.ctx, x.commit)
		if err != nil || replay.State != "COMPLETED" || !reflect.DeepEqual(replay.Steps, approved.Steps) {
			t.Fatalf("cold=%v exact confirmation replay: state=%s err=%v", cold, replay.State, err)
		}
		assertDefinitionFormalSnapshot(t, f, definitionFormalAllTables(), before, fmt.Sprintf("cold=%v exact replay", cold))
	}
}

func TestDefinitionOnlyFormalArchiveRejectsChangedIdentityAndSQLMutation(t *testing.T) {
	x := newDefinitionFormalFixture(t)
	f := x.f
	if _, err := f.facade.CommitConfirmation(f.ctx, x.commit); err != nil {
		t.Fatal(err)
	}
	identity := definitionFormalIdentity(t, f, x.prepared, x.commit.RequestID)
	store := definitionFormalStore(t, f)
	before := definitionFormalSnapshot(t, f, definitionFormalAllTables())
	for _, field := range []string{"version", "intent", "content", "typed", "submitted"} {
		t.Run(field, func(t *testing.T) {
			changed := identity
			switch field {
			case "version":
				changed.IntentVersion++
			case "intent":
				changed.IntentID = x.first.IntentID
			case "content":
				changed.ContentHash = readIdentityStaleHash
			case "typed":
				changed.TypedPayloadHash = readIdentityStaleHash
			case "submitted":
				changed.SubmittedHash = readIdentityStaleHash
			}
			for _, read := range []func(context.Context, formalapproval.ReadIdentity) (formalapproval.Receipt, error){store.ReadDefinitionApproval, store.FinalizeDefinitionApproval} {
				if _, err := read(f.ctx, changed); !errors.Is(err, formalapproval.ErrConflict) {
					t.Fatalf("changed %s accepted or misclassified: %v", field, err)
				}
			}
			assertDefinitionFormalSnapshot(t, f, definitionFormalAllTables(), before, "conflicting "+field)
		})
	}
	for _, field := range []string{"request", "session"} {
		changed := identity
		if field == "request" {
			changed.RequestID = "different-original-approval-request"
		} else {
			changed.AISessionID = "different-original-approval-session"
		}
		for _, read := range []func(context.Context, formalapproval.ReadIdentity) (formalapproval.Receipt, error){store.ReadDefinitionApproval, store.FinalizeDefinitionApproval} {
			if _, err := read(f.ctx, changed); err == nil {
				t.Fatalf("different %s borrowed original formal receipt", field)
			}
		}
		assertDefinitionFormalSnapshot(t, f, definitionFormalAllTables(), before, "different "+field)
	}
	for _, table := range []string{"formal_definition_approval_fences", "formal_definition_approval_business", "formal_definition_approval_receipts"} {
		for _, statement := range []string{`UPDATE ` + table + ` SET ai_session_id=ai_session_id`, `DELETE FROM ` + table} {
			if _, err := f.plane.DB.SQL().Exec(statement); err == nil {
				t.Fatalf("immutable archive accepted %s", statement)
			}
			assertDefinitionFormalSnapshot(t, f, definitionFormalAllTables(), before, statement)
		}
	}
}

func TestDefinitionOnlyFirstAuthoredDraftHasOriginalFormalArchive(t *testing.T) {
	x := newDefinitionFormalFixture(t)
	f := x.f
	identity := definitionFormalIdentity(t, f, x.first, x.firstCommit.RequestID)
	store := definitionFormalStore(t, f)
	before := definitionFormalSnapshot(t, f, definitionFormalAllTables())
	receipt, err := store.ReadDefinitionApproval(f.ctx, identity)
	if err != nil || receipt.Snapshot.Fence.ParentSchema == nil || *receipt.Snapshot.Fence.ParentSchema != formalapproval.DraftParentSchema() {
		t.Fatalf("first authored draft lost formal parent: %+v %v", receipt, err)
	}
	var draft task.Definition
	if err = json.Unmarshal(receipt.Snapshot.Fence.ParentDefinitionJSON, &draft); err != nil || draft.Version != 1 || draft.ReviewApprovalID != "" || draft.WorkSpec.ImplementationMode != "" || draft.WorkSpec.CodexInstruction != "" {
		t.Fatalf("authored draft contains fabricated approval: %+v %v", draft, err)
	}
	if replay, err := store.FinalizeDefinitionApproval(f.ctx, identity); err != nil || !reflect.DeepEqual(receipt, replay) {
		t.Fatalf("first approval replay changed: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, definitionFormalAllTables(), before, "first approval historical read/finalize")
}

func TestDefinitionOnlyFormalArchiveFaultBoundaries(t *testing.T) {
	for _, stage := range []string{"business-basis", "formal-receipt", "completed-step"} {
		t.Run(stage, func(t *testing.T) {
			x := newDefinitionFormalFixture(t)
			f := x.f
			businessBefore := definitionFormalSnapshot(t, f, definitionFormalBusinessTables)
			runtimeBefore := definitionFormalSnapshot(t, f, definitionFormalRuntimeTables)
			var basesBefore, receiptsBefore int
			if err := f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM formal_definition_approval_business),(SELECT count(*) FROM formal_definition_approval_receipts)`).Scan(&basesBefore, &receiptsBefore); err != nil {
				t.Fatal(err)
			}
			fault := `CREATE TRIGGER formal_archive_fault BEFORE INSERT ON formal_definition_approval_business BEGIN SELECT RAISE(ABORT,'injected formal business fault'); END`
			if stage == "formal-receipt" {
				fault = `CREATE TRIGGER formal_archive_fault BEFORE INSERT ON formal_definition_approval_receipts BEGIN SELECT RAISE(ABORT,'injected formal receipt fault'); END`
			} else if stage == "completed-step" {
				fault = `CREATE TRIGGER formal_archive_fault BEFORE UPDATE OF intent_json ON confirmation_intents WHEN json_extract(NEW.intent_json,'$.action')='APPROVE' AND EXISTS(SELECT 1 FROM json_each(NEW.intent_json,'$.steps') step WHERE json_extract(step.value,'$.kind')='APPROVE' AND json_extract(step.value,'$.state')='COMPLETED') BEGIN SELECT RAISE(ABORT,'injected completed step receipt fault'); END`
			}
			if _, err := f.plane.DB.SQL().Exec(fault); err != nil {
				t.Fatal(err)
			}
			failed, err := f.facade.CommitConfirmation(f.ctx, x.commit)
			if err == nil {
				t.Fatalf("%s fault was not reached: %s", stage, failed.State)
			}
			t.Logf("%s production failure: %v", stage, err)
			if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER formal_archive_fault`); err != nil {
				t.Fatal(err)
			}
			var bases, receipts int
			if err := f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM formal_definition_approval_business),(SELECT count(*) FROM formal_definition_approval_receipts)`).Scan(&bases, &receipts); err != nil {
				t.Fatal(err)
			}
			if bases != basesBefore || receipts != receiptsBefore {
				t.Fatalf("%s atomic rollback leaked basis/receipt: %d/%d", stage, bases, receipts)
			}
			assertDefinitionFormalSnapshot(t, f, definitionFormalBusinessTables, businessBefore, "failed atomic approval")
			assertDefinitionFormalSnapshot(t, f, definitionFormalRuntimeTables, runtimeBefore, "failed NO_START archive")
			// Every failure rolls back business, receipt and completion together.
			// A cold read cannot manufacture a success from the consumed request.
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()

			// A failed business transaction leaves no durable success basis.
			// UNKNOWN therefore stays fenced instead of automatically approving
			// again merely because the injection has been removed.
			before := definitionFormalSnapshot(t, f, definitionFormalAllTables())
			identity := definitionFormalIdentity(t, f, x.prepared, x.commit.RequestID)
			store := definitionFormalStore(t, f)
			for _, read := range []func(context.Context, formalapproval.ReadIdentity) (formalapproval.Receipt, error){store.ReadDefinitionApproval, store.FinalizeDefinitionApproval} {
				if _, err := read(f.ctx, identity); !errors.Is(err, formalapproval.ErrMissing) {
					t.Fatalf("uncommitted business basis became a receipt: %v", err)
				}
			}
			for range 2 {
				replayed, err := f.facade.CommitConfirmation(f.ctx, x.commit)
				if err != nil || replayed.State != "UNKNOWN" {
					t.Fatalf("absent basis retry must stay UNKNOWN: state=%s err=%v", replayed.State, err)
				}
			}
			assertDefinitionFormalSnapshot(t, f, definitionFormalAllTables(), before, "unknown business retry")
			assertDefinitionFormalSnapshot(t, f, definitionFormalBusinessTables, businessBefore, "cold failed business transaction")
		})
	}
}

func definitionFormalEveryTable(t *testing.T, f *upperLifecycleFixture) []string {
	t.Helper()
	rows, err := f.plane.DB.SQL().Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return tables
}

func TestDefinitionOnlyFormalArchiveReplayIgnoresStaleCallbackFence(t *testing.T) {
	x := newDefinitionFormalFixture(t)
	f := x.f
	if _, err := f.facade.CommitConfirmation(f.ctx, x.commit); err != nil {
		t.Fatal(err)
	}
	identity := definitionFormalIdentity(t, f, x.prepared, x.commit.RequestID)
	store := definitionFormalStore(t, f)
	receipt, err := store.ReadDefinitionApproval(f.ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	// One retained connection makes total_changes an exact write observation,
	// including attempted writes later rolled back. The actual DB writer remains
	// current: this tests a stale callback fence, not a second simultaneous Core.
	f.plane.DB.SQL().SetMaxOpenConns(1)
	f.plane.DB.SQL().SetMaxIdleConns(1)
	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	var changesBefore int64
	if err := f.plane.DB.SQL().QueryRow(`SELECT total_changes()`).Scan(&changesBefore); err != nil {
		t.Fatal(err)
	}
	stale := runtimeepoch.WithFence(f.ctx, "retired-formal-approval-callback", "")
	for _, read := range []func(context.Context, formalapproval.ReadIdentity) (formalapproval.Receipt, error){store.ReadDefinitionApproval, store.FinalizeDefinitionApproval} {
		got, err := read(stale, identity)
		if err != nil || !reflect.DeepEqual(got, receipt) {
			t.Fatalf("stale callback lost exact historical receipt: %v", err)
		}
	}
	_, err = f.plane.Confirmations.Prepare(stale, f.project, c.PrepareInput{AISessionID: f.session, RequestID: "stale-callback-new-prepare", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: f.area, Title: "Must not be prepared", ProblemStatement: "Stale callback has no current write authority"}, Variants: []c.Variant{{ID: "create", Summary: "Must be fenced"}}})
	if !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("stale callback acquired new write authority: %v", err)
	}
	var changesAfter int64
	if err := f.plane.DB.SQL().QueryRow(`SELECT total_changes()`).Scan(&changesAfter); err != nil {
		t.Fatal(err)
	}
	if changesAfter != changesBefore {
		t.Fatalf("historical replay or fenced new write changed SQLite: %d -> %d", changesBefore, changesAfter)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "stale callback historical replay and denied fresh write")
}

func TestDefinitionOnlyFormalArchiveRejectsHistoricalPrefix(t *testing.T) {
	x := newDefinitionFormalFixture(t)
	f := x.f
	if _, err := f.facade.CommitConfirmation(f.ctx, x.commit); err != nil {
		t.Fatal(err)
	}
	identity := definitionFormalIdentity(t, f, x.prepared, x.commit.RequestID)
	// Simulate an old database prefix by removing only its final receipt while
	// retaining the original business archive. Production never has this port.
	var triggerName, triggerSQL string
	if err := f.plane.DB.SQL().QueryRow(`SELECT name,sql FROM sqlite_master WHERE type='trigger' AND tbl_name='formal_definition_approval_receipts' AND sql LIKE '%BEFORE DELETE%'`).Scan(&triggerName, &triggerSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER ` + triggerName); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.DB.SQL().Exec(`DELETE FROM formal_definition_approval_receipts WHERE ai_session_id=? AND request_id=?`, f.session, x.commit.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.DB.SQL().Exec(triggerSQL); err != nil {
		t.Fatal(err)
	}
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	before := definitionFormalSnapshot(t, f, definitionFormalAllTables())
	if _, err := definitionFormalStore(t, f).FinalizeDefinitionApproval(f.ctx, identity); !errors.Is(err, formalapproval.ErrCorrupt) {
		t.Fatalf("committed prefix was backfilled: %v", err)
	}
	if _, err := f.facade.CommitConfirmation(f.ctx, x.commit); err == nil {
		t.Fatal("prefix replay manufactured completion")
	}
	assertDefinitionFormalSnapshot(t, f, definitionFormalAllTables(), before, "cold prefix refusal")
}

func TestDefinitionOnlyFormalArchiveUnselectedDirectVariantCannotDisableArchive(t *testing.T) {
	x := newDefinitionFormalFixture(t)
	f := x.f
	if _, err := f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(x.prepared)); err != nil {
		t.Fatal(err)
	}
	input := x.prepared.Content
	input.RequestID, input.SubmittedHash = "formal-mixed-variants", ""
	input.SourceIntentID = ""
	coding := input.Variants[0]
	direct := coding
	direct.ID = "unselected-direct"
	direct.Summary = "An unselected but internally consistent Direct File alternative"
	direct.ImplementationMode = task.ImplementationDirectFile
	directWork := *coding.WorkSpec
	directWork.ImplementationMode = task.ImplementationDirectFile
	direct.WorkSpec = &directWork
	if err := task.ValidateDefinitionTargets(task.Definition{Version: 2, RepositoryTargets: direct.RepositoryTargets, PrimaryMemberID: direct.PrimaryMemberID, WorkSpec: *direct.WorkSpec, VerificationSpec: *direct.VerificationSpec}); err != nil {
		t.Fatalf("alternate must be a valid typed variant, not a malformed-input test: %v", err)
	}
	input.Variants = []c.Variant{coding, direct}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, input)
	if err != nil || len(prepared.Content.Variants) != 2 {
		t.Fatalf("prepare mixed legal variants: %v", err)
	}
	var fences int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM formal_definition_approval_fences WHERE intent_id=?`, prepared.IntentID).Scan(&fences); err != nil {
		t.Fatal(err)
	}
	if fences != 1 {
		t.Fatalf("unselected Direct File variant disabled formal fence for actual v2 Coding Review: fences=%d", fences)
	}
	x.prepared = prepared
	x.commit = c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "formal-mixed-commit", ContentHash: prepared.ContentHash, VariantID: coding.ID}
	runtimeBefore := definitionFormalSnapshot(t, f, definitionFormalRuntimeTables)
	approved, err := f.facade.CommitConfirmation(f.ctx, x.commit)
	if err != nil || approved.State != "COMPLETED" {
		t.Fatalf("approve actual Coding choice: state=%s err=%v", approved.State, err)
	}
	identity := definitionFormalIdentity(t, f, prepared, x.commit.RequestID)
	receipt, err := definitionFormalStore(t, f).ReadDefinitionApproval(f.ctx, identity)
	if err != nil {
		t.Fatalf("unselected alternative disabled business/final archive: %v", err)
	}
	assertDefinitionFormalReceipt(t, x, identity, receipt)
	if receipt.Snapshot.Commit.VariantID != coding.ID {
		t.Fatal("archive selected the unused alternative")
	}
	var basisCount, receiptCount int
	if err := f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM formal_definition_approval_business WHERE intent_id=?),(SELECT count(*) FROM formal_definition_approval_receipts WHERE ai_session_id=? AND request_id=?)`, prepared.IntentID, f.session, x.commit.RequestID).Scan(&basisCount, &receiptCount); err != nil || basisCount != 1 || receiptCount != 1 {
		t.Fatalf("mixed choice basis/receipt=%d/%d err=%v", basisCount, receiptCount, err)
	}
	assertDefinitionFormalSnapshot(t, f, definitionFormalRuntimeTables, runtimeBefore, "mixed variants definition-only approval")
}
