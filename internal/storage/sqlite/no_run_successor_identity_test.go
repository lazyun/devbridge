package sqlite

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func readyNoRunSuccessor(t *testing.T, f *reviewApprovalFixture, owner task.Task, predecessor executionproposal.Proposal, cancelID string) wp.PrepareSuccessorExecution {
	t.Helper()
	ctx := context.Background()
	if _, _, err := reserveNoRunFixture(ctx, f, owner.ID); err != nil {
		t.Fatal(err)
	}
	cancellation := executioncancel.Input{AISessionID: f.session, ProjectID: owner.ProjectID, ExecutionID: predecessor.ID, OwnerType: "EXECUTION", OwnerID: predecessor.ID, RequestID: cancelID, Reason: "cancel before Run"}
	r, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, cancellation, false)
	if err != nil || r.State != "CANCELED" || r.RunID != "" {
		t.Fatalf("cancellation %+v %v", r, err)
	}
	if _, err = NewExecutionCancellationStore(f.db).ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, owner.ID, predecessor.ID); err != nil {
		t.Fatal(err)
	}
	return wp.PrepareSuccessorExecution{RequestID: "successor-request", PayloadSHA256: strings.Repeat("b", 64), ProjectID: owner.ProjectID, TaskID: owner.ID, PredecessorExecutionID: predecessor.ID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: owner.CurrentDefinitionRevisionID, ActorAuthority: f.session}
}
func noRunCount(t *testing.T, f *reviewApprovalFixture, table string) int {
	t.Helper()
	var n int
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPrepareSuccessorExecutionRequestIdentity(t *testing.T) {
	f, owner, predecessor := qualifiedExecutionFixture(t, "-successor-identity")
	in := readyNoRunSuccessor(t, f, owner, predecessor, "cancel-identity")
	first, err := f.store.PrepareSuccessorExecution(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]int{}
	for _, table := range []string{"no_run_successor_command_receipts", "no_run_successor_effect_authorities", "execution_proposals", "runtime_scope_plans", "runtime_preparation_authorities"} {
		before[table] = noRunCount(t, f, table)
	}
	replay, err := f.store.PrepareSuccessorExecution(context.Background(), in)
	if err != nil || replay != first {
		t.Fatalf("replay=%+v %v", replay, err)
	}
	conflict := in
	conflict.PayloadSHA256 = strings.Repeat("c", 64)
	if _, err = f.store.PrepareSuccessorExecution(context.Background(), conflict); err == nil || !strings.Contains(err.Error(), "REQUEST_PAYLOAD_CONFLICT") {
		t.Fatalf("payload conflict: %v", err)
	}
	for table, count := range before {
		if got := noRunCount(t, f, table); got != count {
			t.Fatalf("%s changed after replay/conflict: %d -> %d", table, count, got)
		}
	}
	other := in
	other.RequestID = "successor-request-other"
	ack, err := f.store.PrepareSuccessorExecution(context.Background(), other)
	if err != nil || ack.Outcome != wp.NoRunSuccessorAlreadyExists || ack.SuccessorExecutionID != first.SuccessorExecutionID || ack.SuccessorEffectAuthorityID != first.SuccessorEffectAuthorityID {
		t.Fatalf("different request: %+v %v", ack, err)
	}
	for table, count := range before {
		want := count
		if table == "no_run_successor_command_receipts" {
			want++
		}
		if got := noRunCount(t, f, table); got != want {
			t.Fatalf("%s after ack %d want %d", table, got, want)
		}
	}
}

func TestNoRunSuccessorProjectionSeparateTransactionAndIdempotent(t *testing.T) {
	f, owner, predecessor := qualifiedExecutionFixture(t, "-successor-projection")
	ctx := context.Background()
	if _, _, err := reserveNoRunFixture(ctx, f, owner.ID); err != nil {
		t.Fatal(err)
	}
	cancellation := executioncancel.Input{AISessionID: f.session, ProjectID: owner.ProjectID, ExecutionID: predecessor.ID, OwnerType: "EXECUTION", OwnerID: predecessor.ID, RequestID: "cancel-projection", Reason: "cancel before Run"}
	receipt, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, cancellation, false)
	if err != nil || receipt.State != "CANCELED" || receipt.BackendDisposition != "NO_BACKEND" || receipt.BackendInterruptState != "NOT_SENT" || receipt.RecoveryRequired {
		t.Fatalf("cancellation %+v %v", receipt, err)
	}
	var beforePhase, beforeCurrent, beforeRevision, beforeUpdated string
	if err := f.db.sql.QueryRowContext(ctx, `SELECT phase,current_execution_id,current_definition_revision_id,updated_at FROM tasks WHERE id=?`, owner.ID).Scan(&beforePhase, &beforeCurrent, &beforeRevision, &beforeUpdated); err != nil {
		t.Fatal(err)
	}
	if beforePhase != string(task.PhaseImplementationReady) || beforeCurrent != predecessor.ID || beforeRevision != owner.CurrentDefinitionRevisionID {
		t.Fatalf("cancellation changed Task: %s %s %s", beforePhase, beforeCurrent, beforeRevision)
	}
	projector := NewExecutionCancellationStore(f.db)
	if phase, err := projector.ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, owner.ID, predecessor.ID); err != nil || phase != task.PhaseNoRunSuccessorRequired {
		t.Fatalf("projection %s %v", phase, err)
	}
	var phase, current, revision, updated string
	if err := f.db.sql.QueryRowContext(ctx, `SELECT phase,current_execution_id,current_definition_revision_id,updated_at FROM tasks WHERE id=?`, owner.ID).Scan(&phase, &current, &revision, &updated); err != nil {
		t.Fatal(err)
	}
	if phase != string(task.PhaseNoRunSuccessorRequired) || current != beforeCurrent || revision != beforeRevision || updated != beforeUpdated {
		t.Fatalf("projected Task: %s %s %s %s", phase, current, revision, updated)
	}
	if _, err := projector.ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, owner.ID, predecessor.ID); err != nil {
		t.Fatal(err)
	}
	var replayUpdated string
	if err := f.db.sql.QueryRowContext(ctx, `SELECT updated_at FROM tasks WHERE id=?`, owner.ID).Scan(&replayUpdated); err != nil || replayUpdated != updated {
		t.Fatalf("replay updated_at %s -> %s %v", updated, replayUpdated, err)
	}
}

func TestCrossLineageRequestIDConflictFromSuccessorRequired(t *testing.T) {
	f, ownerA, predecessorA := qualifiedExecutionFixture(t, "-cross-A")
	requestA := readyNoRunSuccessor(t, f, ownerA, predecessorA, "cancel-A")
	requestA.RequestID = "shared-cross-lineage-request"
	first, err := f.store.PrepareSuccessorExecution(context.Background(), requestA)
	if err != nil {
		t.Fatal(err)
	}
	// A second actual Task and predecessor are admitted in the same database.
	next := ownerA
	next.ID = "cross-lineage-task-B"
	next.Title = "Second Task"
	next.Goal = "Second Task"
	next.ProblemStatement = "Second Task"
	next.Status = task.StatusActive
	next.Phase = task.PhaseReviewRequired
	next.CurrentReviewID = ""
	next.CurrentExecutionID = ""
	next.CurrentDefinitionRevisionID = ""
	next.ConfirmedTaskPlanID = ""
	next.ReviewApprovalID = ""
	next.RepositoryTargets, next.PrimaryMemberID = nil, ""
	next.RepositoryID, next.BaseCommit, next.CodexConfigID = "owned-repo", strings.Repeat("a", 40), codexconfig.DefaultConfigID
	next.WorkSpec = task.WorkSpec{}
	next.VerificationSpec = task.VerificationSpec{}
	if err = NewTaskStore(f.db).Create(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	f.candidate.TaskID = next.ID
	f.candidate.WorkspaceID, f.candidate.RepositoryTargets, f.candidate.PrimaryMemberID = "", nil, ""
	f.candidate.RepositoryID, f.candidate.BaseCommit = next.RepositoryID, next.BaseCommit
	f.candidate.RepositoryProvenance = &review.RepositoryProvenance{RepositoryID: next.RepositoryID, Commit: next.BaseCommit, RepositoryHEAD: next.BaseCommit, BranchKind: review.ProvenanceBranch, BranchRef: "refs/heads/dev", InspectionMode: review.InspectionFeatureAreaWorkspace, CapturedAt: testNow}
	f.candidate.EngineeringGoal += " cross-B"
	review := f.start(t)
	f.complete(t, review, approvedResult(t))
	approved, err := f.store.Resolve(context.Background(), f.input(review, "APPROVE"))
	if err != nil {
		t.Fatal(err)
	}
	predecessorB, err := f.store.AuthorizeTaskExecution(context.Background(), f.session, approved.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	requestB := readyNoRunSuccessor(t, f, *approved.Task, predecessorB, "cancel-B")
	requestB.RequestID = requestA.RequestID
	if requestB.TaskID == requestA.TaskID || requestB.PredecessorExecutionID == requestA.PredecessorExecutionID {
		t.Fatal("not two lineages")
	}
	before := map[string]int{}
	for _, table := range []string{"no_run_successor_command_receipts", "no_run_successor_effect_authorities", "execution_proposals", "runtime_scope_plans", "runtime_preparation_authorities"} {
		before[table] = noRunCount(t, f, table)
	}
	if _, err = f.store.PrepareSuccessorExecution(context.Background(), requestB); err == nil || !strings.Contains(err.Error(), "REQUEST_IDENTITY_CONFLICT") {
		t.Fatalf("cross-lineage: %v", err)
	}
	for table, count := range before {
		if got := noRunCount(t, f, table); got != count {
			t.Fatalf("%s changed on conflict: %d -> %d", table, count, got)
		}
	}
	current, err := NewTaskStore(f.db).Get(context.Background(), requestB.TaskID)
	if err != nil || current.Phase != task.PhaseNoRunSuccessorRequired || current.CurrentExecutionID != requestB.PredecessorExecutionID {
		t.Fatalf("loser Task %+v %v", current, err)
	}
	replay, err := f.store.PrepareSuccessorExecution(context.Background(), requestA)
	if err != nil || replay != first {
		t.Fatalf("winner replay %+v %v", replay, err)
	}
}

func TestConcurrentSameSuccessorRequestCreatesOneEffect(t *testing.T) {
	f, owner, predecessor := qualifiedExecutionFixture(t, "-successor-race")
	in := readyNoRunSuccessor(t, f, owner, predecessor, "cancel-race")
	const calls = 4
	type result struct {
		r   wp.SuccessorCommandReceipt
		err error
	}
	out := make(chan result, calls)
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := 0; i < calls; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			r, err := f.store.PrepareSuccessorExecution(context.Background(), in)
			out <- result{r, err}
		}()
	}
	close(start)
	group.Wait()
	close(out)
	var first wp.SuccessorCommandReceipt
	for value := range out {
		if value.err != nil {
			t.Fatal(value.err)
		}
		if first.RequestID == "" {
			first = value.r
		} else if first != value.r {
			t.Fatalf("inconsistent receipts %+v %+v", first, value.r)
		}
	}
	if first.Outcome != wp.NoRunSuccessorCreated || noRunCount(t, f, "no_run_successor_command_receipts") != 1 || noRunCount(t, f, "no_run_successor_effect_authorities") != 1 {
		t.Fatal("duplicate successor authority")
	}
}

func TestConcurrentDifferentSuccessorRequestsCreateOneEffect(t *testing.T) {
	f, owner, predecessor := qualifiedExecutionFixture(t, "-successor-different-race")
	in := readyNoRunSuccessor(t, f, owner, predecessor, "cancel-different-race")
	const calls = 4
	type result struct {
		r   wp.SuccessorCommandReceipt
		err error
	}
	out := make(chan result, calls)
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := 0; i < calls; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			request := in
			request.RequestID = "different-race-" + string(rune('a'+index))
			<-start
			r, err := f.store.PrepareSuccessorExecution(context.Background(), request)
			out <- result{r, err}
		}(i)
	}
	close(start)
	group.Wait()
	close(out)
	created, acknowledged := 0, 0
	var successorID, authorityID string
	for value := range out {
		if value.err != nil {
			t.Fatal(value.err)
		}
		switch value.r.Outcome {
		case wp.NoRunSuccessorCreated:
			created++
		case wp.NoRunSuccessorAlreadyExists:
			acknowledged++
		default:
			t.Fatalf("unexpected receipt %+v", value.r)
		}
		if successorID == "" {
			successorID, authorityID = value.r.SuccessorExecutionID, value.r.SuccessorEffectAuthorityID
		} else if successorID != value.r.SuccessorExecutionID || authorityID != value.r.SuccessorEffectAuthorityID {
			t.Fatalf("different effects %+v", value.r)
		}
	}
	if created != 1 || acknowledged != calls-1 || noRunCount(t, f, "no_run_successor_command_receipts") != calls || noRunCount(t, f, "no_run_successor_effect_authorities") != 1 {
		t.Fatalf("outcomes created=%d acknowledged=%d", created, acknowledged)
	}
}

func TestNoRunSuccessorRequiredGuidance(t *testing.T) {
	f, owner, predecessor := qualifiedExecutionFixture(t, "-successor-guidance")
	in := readyNoRunSuccessor(t, f, owner, predecessor, "cancel-guidance")
	g, err := f.store.ReadTask(context.Background(), owner.ProjectID, owner.FeatureAreaID, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if g.CurrentState != wp.NoRunSuccessorState || g.NextAction.Code != wp.NoRunSuccessorAction || g.RequiredAction.Code != wp.NoRunSuccessorAction {
		t.Fatalf("guidance %+v", g)
	}
	for _, forbidden := range []string{"START_EXECUTION", "CREATE_PREPARATION_RUN", "RESOLVE_RECOVERY"} {
		for _, action := range g.AvailableActions {
			if action == forbidden {
				t.Fatalf("forbidden %s in %+v", forbidden, g.AvailableActions)
			}
		}
	}
	if _, err = f.store.PrepareSuccessorExecution(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	g, err = f.store.ReadTask(context.Background(), owner.ProjectID, owner.FeatureAreaID, owner.ID)
	if err != nil || g.CurrentState != "TASK_IMPLEMENTATION_PREPARING" {
		t.Fatalf("successor guidance %+v %v", g, err)
	}
}
