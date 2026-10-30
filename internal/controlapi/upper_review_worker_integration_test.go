package controlapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/review"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This exercises the production bootstrap and full shared MCP surface, not a
// synthetic worker or a separate queue. The Review and all members are already
// admitted by the normal confirmation/coordinator path in the real fixture.
func TestUpperReviewWorkerMultiMemberMCPRoundTrip(t *testing.T) {
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "worker-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "worker-test", Version: "1"}, nil)
	mcpserver.RegisterSurfaceBusinessTools(server, service)
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ss, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "worker-client", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]*sdkmcp.Tool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = tool
	}
	for _, name := range []string{"review_worker_claim", "review_worker_get", "review_result_submit", "file_read"} {
		if names[name] == nil {
			t.Fatalf("missing production tool %s", name)
		}
	}
	if names["review_worker_claim"].Annotations.ReadOnlyHint || !names["review_worker_get"].Annotations.ReadOnlyHint {
		t.Fatal("incorrect worker side-effect annotations")
	}
	input := review.WorkerClaimInput{UpperTarget: f.access.Target, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "dispatch-v2", WorkerID: "dot-label-not-identity"}}
	args := mcpObject(t, input)
	// Caller scope is adapter-owned and cannot be smuggled in through JSON.
	bad := mcpObject(t, input)
	bad["callerScope"] = "other-principal"
	if result, e := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "review_worker_claim", Arguments: bad}); e == nil && !result.IsError {
		t.Fatal("caller-supplied scope accepted")
	}
	var before int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	wire := callUpperMCP(t, ctx, cs, "review_worker_claim", args)
	var claim review.WorkerClaimResult
	raw, _ := json.Marshal(wire)
	if err = json.Unmarshal(raw, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.State != review.WorkerDispatchIntent || claim.Replay || claim.ExecutionPayload == nil || len(claim.ExecutionPayload.Access.RepositoryTargets) != 2 || claim.Receipt.UpperTarget != f.access.Target {
		t.Fatalf("claim=%+v", claim)
	}
	var instruction string
	if err = f.plane.DB.SQL().QueryRow(`SELECT codex_instruction FROM runs WHERE id=?`, f.run).Scan(&instruction); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(instruction))
	if claim.ExecutionPayload.Instruction != instruction || claim.Receipt.InstructionSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("frozen instruction changed at delivery")
	}
	payloadBytes, _ := json.Marshal(claim.ExecutionPayload)
	sum = sha256.Sum256(payloadBytes)
	if claim.Receipt.PayloadSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("payload receipt not exact")
	}
	// An identical retry is a status read, never executable delivery again.
	retry := callUpperMCP(t, ctx, cs, "review_worker_claim", args)
	if retry["state"] != review.WorkerOutcomeUnknown || retry["executionPayload"] != nil || !reflect.DeepEqual(retry["receipt"], wire["receipt"]) {
		t.Fatalf("unsafe redispatch=%+v", retry)
	}
	for _, target := range f.targets {
		callUpperMCP(t, ctx, cs, "file_read", map[string]any{"reviewTarget": mcpObject(t, f.access.Target), "repositoryId": target.RepositoryID, "memberId": target.MemberID, "memberVersion": target.MemberVersion, "path": "same.txt"})
	}
	report := f.findingsOnly("worker-report-v2", f.access.Target)
	if result, e := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "review_result_submit", Arguments: mcpObject(t, report)}); e == nil && !result.IsError {
		t.Fatal("claim omitted from enrolled v2 submission")
	}
	wrong := input.WorkerClaimIdentity
	wrong.WorkerID = "other-worker"
	report.WorkerClaim = &wrong
	if result, e := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "review_result_submit", Arguments: mcpObject(t, report)}); e == nil && !result.IsError {
		t.Fatal("wrong worker committed report")
	}
	report.WorkerClaim = &input.WorkerClaimIdentity
	receipt := callUpperMCP(t, ctx, cs, "review_result_submit", mcpObject(t, report))
	replay := callUpperMCP(t, ctx, cs, "review_result_submit", mcpObject(t, report))
	if !reflect.DeepEqual(receipt, replay) {
		t.Fatalf("completed receipt changed: %v %v", receipt, replay)
	}
	status := callUpperMCP(t, ctx, cs, "review_worker_get", args)
	if status["state"] != review.WorkerResultRecorded || status["executionPayload"] != nil || !reflect.DeepEqual(status["resultReceipt"], receipt) {
		t.Fatalf("worker status=%+v", status)
	}
	var after, reports int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM review_reports WHERE review_id=?`, f.review).Scan(&reports); err != nil {
		t.Fatal(err)
	}
	if before != after || reports != 1 {
		t.Fatalf("worker added admission or duplicate result: runs %d -> %d, reports %d", before, after, reports)
	}
	var taskStatus string
	if err = f.plane.DB.SQL().QueryRow(`SELECT status FROM tasks WHERE id=?`, f.task).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "ACTIVE" {
		t.Fatalf("engineering report became task acceptance: %s", taskStatus)
	}
}

func TestUpperReviewWorkerMultiMemberCancelAndScopeFence(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	in := review.WorkerClaimInput{UpperTarget: f.access.Target, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "dispatch-cancel", WorkerID: "worker"}}
	claim, err := f.facade.ClaimUpperReviewWorker(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	foreign := aisession.WithCallerScope(context.Background(), "foreign-principal")
	if _, err = f.facade.GetUpperReviewWorkerClaim(foreign, in); err == nil {
		t.Fatal("foreign scope read receipt")
	}
	receipt, err := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "REVIEW", OwnerID: f.review, RunID: f.run, RequestID: "cancel-worker", Reason: "withdraw", ExpectedUpperVersion: f.upperVersion()})
	if err != nil || receipt.State != "CANCELED" || receipt.BackendDisposition != "NO_BACKEND" {
		t.Fatalf("cancellation=%+v err=%v", receipt, err)
	}
	for _, target := range f.targets {
		if _, err = f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt")); err == nil {
			t.Fatal("read allowed after cancel")
		}
	}
	report := f.findingsOnly("late-worker-report", f.access.Target)
	report.WorkerClaim = &in.WorkerClaimIdentity
	if _, err = f.facade.SubmitReviewResult(f.ctx, report); err == nil {
		t.Fatal("late worker committed result")
	}
	got, err := f.facade.GetUpperReviewWorkerClaim(f.ctx, in)
	if err != nil || got.State != review.WorkerOutcomeUnknown || got.ExecutionPayload != nil || !reflect.DeepEqual(got.Receipt, claim.Receipt) {
		t.Fatalf("cancel incorrectly claimed execution stopped or replayed: %+v %v", got, err)
	}
}

func TestUpperReviewWorkerRecoveryRefusedBeforeEffects(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	in := review.WorkerClaimInput{UpperTarget: f.access.Target, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "dispatch-recover", WorkerID: "worker"}}
	claim, err := f.facade.ClaimUpperReviewWorker(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	f.plane = nil
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	expected := &agentconversation.UpperVersion{AuthorityEpoch: f.plane.DB.RuntimeEpoch(), GrantEpoch: in.RuntimeEpoch, GrantGeneration: in.GrantGeneration}
	_, err = f.facade.RecoverFeatureAreaAgentOperation(f.ctx, f.session, agentconversation.RecoverInput{ExpectedUpperVersion: expected, FeatureAreaID: f.area, RunID: f.run, OperationKind: agentconversation.Review, OperationID: f.review, RequestID: "recover-worker"})
	if err == nil {
		t.Fatal("enrolled worker acquired unusable new generation through recovery")
	}
	var epoch string
	var generation int64
	if err = f.plane.DB.SQL().QueryRow(`SELECT runtime_epoch,generation FROM review_grants WHERE review_id=?`, f.review).Scan(&epoch, &generation); err != nil {
		t.Fatal(err)
	}
	if epoch != in.RuntimeEpoch || generation != in.GrantGeneration {
		t.Fatalf("refused recovery mutated grant: %s/%d", epoch, generation)
	}
	next := in
	next.RequestID = "replacement-delivery"
	next.WorkerID = "replacement-worker"
	if _, err = f.facade.ClaimUpperReviewWorker(f.ctx, next); err == nil {
		t.Fatal("unknown delivery reassigned after restart")
	}
	report := f.findingsOnly("late-before-recovery", in.UpperTarget)
	report.WorkerClaim = &in.WorkerClaimIdentity
	if _, err = f.facade.SubmitReviewResult(f.ctx, report); err == nil {
		t.Fatal("late old-epoch worker report accepted")
	}
	got, err := f.facade.GetUpperReviewWorkerClaim(f.ctx, in)
	if err != nil || got.State != review.WorkerOutcomeUnknown || got.ExecutionPayload != nil || !reflect.DeepEqual(got.Receipt, claim.Receipt) {
		t.Fatalf("old readback=%+v %v", got, err)
	}
}

func TestUpperReviewWorkerResultCancelRaceHasOneTerminalWinner(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	in := review.WorkerClaimInput{UpperTarget: f.access.Target, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "dispatch-race", WorkerID: "worker"}}
	if _, err := f.facade.ClaimUpperReviewWorker(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	report := f.findingsOnly("worker-racing-report", in.UpperTarget)
	report.WorkerClaim = &in.WorkerClaimIdentity
	start := make(chan struct{})
	reportErr := make(chan error, 1)
	cancelErr := make(chan error, 1)
	go func() { <-start; _, err := f.facade.SubmitReviewResult(f.ctx, report); reportErr <- err }()
	go func() {
		<-start
		_, err := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "REVIEW", OwnerID: f.review, RunID: f.run, RequestID: "cancel-race-worker", Reason: "withdraw", ExpectedUpperVersion: f.upperVersion()})
		cancelErr <- err
	}()
	close(start)
	re, ce := <-reportErr, <-cancelErr
	if (re == nil) == (ce == nil) {
		t.Fatalf("expected one terminal winner report=%v cancel=%v", re, ce)
	}
	state, err := f.facade.GetUpperReviewWorkerClaim(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM review_reports WHERE review_id=?`, f.review).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if re == nil {
		if state.State != review.WorkerResultRecorded || rows != 1 {
			t.Fatalf("report winner=%+v rows=%d", state, rows)
		}
	} else {
		if state.State != review.WorkerOutcomeUnknown || rows != 0 {
			t.Fatalf("cancel winner=%+v rows=%d", state, rows)
		}
	}
}
