package controlapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Both adapters are production HTTP handlers. Only bootstrap admission and the
// explicit confirmation choice are trusted isolated TEST fixtures, not users.
func pendingCancelHTTPClient(t *testing.T, f *upperLifecycleFixture, gpt bool) (*sdkmcp.ClientSession, func()) {
	t.Helper()
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "pending-cancel-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler
	var path string
	if gpt {
		a, e := mcpserver.New(mcpserver.Config{}, service)
		if e != nil {
			t.Fatal(e)
		}
		handler = a.Handler()
		path = mcpserver.MCPPath
	} else {
		a, e := localmcp.New(localmcp.Config{}, service)
		if e != nil {
			t.Fatal(e)
		}
		handler = a.Handler()
		path = localmcp.MCPPath
	}
	server := httptest.NewServer(handler)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "isolated-pending-cancel", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: server.URL + path, HTTPClient: &http.Client{Timeout: 10 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	var once sync.Once
	close := func() { once.Do(func() { session.Close(); server.Close() }) }
	t.Cleanup(close)
	return session, close
}

func TestFileWorkerPendingMutationBlocksHTTPCancellationAcrossRestart(t *testing.T) {
	f, current, state, claim := fileWorkerHTTPFixture(t)
	initialAgentExecutions := fileClientCount(t, f, "agent_executions")
	local, closeLocal := pendingCancelHTTPClient(t, f, false)
	gpt, closeGPT := pendingCancelHTTPClient(t, f, true)
	evidence := os.Getenv("DEVBRIDGE_CANCEL_PENDING_EVIDENCE_DIR")
	if evidence != "" {
		if e := os.MkdirAll(evidence, 0700); e != nil {
			t.Fatal(e)
		}
		var e error
		evidence, e = os.MkdirTemp(evidence, "run-")
		if e != nil {
			t.Fatal(e)
		}
		t.Log("evidence:", evidence)
	}
	var seq atomic.Int64
	type response struct {
		value *sdkmcp.CallToolResult
		err   error
	}
	rawCall := func(s *sdkmcp.ClientSession, name string, args any) response {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, e := s.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
		if evidence != "" {
			b, _ := json.MarshalIndent(map[string]any{"tool": name, "input": args, "output": r, "transportError": fmt.Sprint(e)}, "", "  ")
			if we := os.WriteFile(filepath.Join(evidence, fmt.Sprintf("%03d-%s.json", seq.Add(1), name)), b, 0600); we != nil {
				return response{r, we}
			}
		}
		return response{r, e}
	}
	decode := func(v any, out any) {
		t.Helper()
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, out); e != nil {
			t.Fatal(e)
		}
	}
	checked := func(r response) map[string]any {
		t.Helper()
		if r.err != nil || r.value == nil || r.value.IsError {
			t.Fatalf("MCP failed: %+v %v", r.value, r.err)
		}
		return mcpObject(t, r.value.StructuredContent)
	}
	call := func(s *sdkmcp.ClientSession, name string, args any) map[string]any {
		t.Helper()
		return checked(rawCall(s, name, args))
	}
	intent := func(v any) c.Intent {
		t.Helper()
		var r c.Result
		decode(v, &r)
		if r.Intent == nil {
			t.Fatalf("missing intent: %+v", v)
		}
		return *r.Intent
	}
	originalClaim := call(local, "file_worker_claim", claim)["result"].(map[string]any)
	args := mcpObject(t, directInput(f, state, current.RepositoryTargets[0]))
	read := call(local, "file_read", args)["result"].(map[string]any)
	args["protocolVersion"], args["action"], args["requestId"], args["expectedDigest"] = 2, "REPLACE", "prime-implementing", read["digest"]
	args["workerClaim"], args["content"] = mcpObject(t, claim.WorkerClaimIdentity), "prime implementing\n"
	prime := call(local, "file_write", args)["result"].(map[string]any)
	var err error
	current, err = f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	prepare := func(request string) c.Intent {
		return intent(call(gpt, "execution_prepare", c.PrepareInput{AISessionID: f.session, RequestID: request, Purpose: c.Execution, Action: "CANCEL_DIRECT_FILE", Execution: &c.ExecutionBody{Action: "CANCEL_DIRECT_FILE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: state.DecisionID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, Variants: []c.Variant{{ID: "exact", Label: "Explicit fixture cancellation", Summary: "Cancel isolated pending-write fixture", ImplementationMode: current.WorkSpec.ImplementationMode, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}}))
	}
	p := prepare("prepare-pending-cancel")
	commit := c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "commit-pending-cancel", ContentHash: p.ContentHash, VariantID: "exact", Selection: c.Selection{}}
	args["requestId"], args["expectedDigest"], args["content"] = "pending-effect", prime["digest"], "durable effect before injected failure\n"
	var root string
	if err = f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, state.WorkflowID, current.RepositoryTargets[0].MemberID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "same.txt")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var effects atomic.Int64
	var barrierTimedOut atomic.Bool
	f.plane.DirectFiles.SetAfterFileEffectHookForTest(func() error {
		effects.Add(1)
		close(entered)
		select {
		case <-release:
			return errors.New("injected after durable effect before receipt completion")
		case <-time.After(10 * time.Second):
			barrierTimedOut.Store(true)
			return errors.New("TEST BARRIER TIMED OUT")
		}
	})
	writeDone, cancelDone := make(chan response, 1), make(chan response, 1)
	var wg sync.WaitGroup
	defer func() {
		unblock()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("MCP goroutines failed to join")
		}
	}()
	wg.Add(1)
	go func() { defer wg.Done(); writeDone <- rawCall(local, "file_write", args) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("write did not reach durable-effect barrier")
	}
	physical, err := snapshotTCPMutation(path)
	if err != nil {
		t.Fatal(err)
	}
	wg.Add(1)
	go func() { defer wg.Done(); cancelDone <- rawCall(gpt, "confirmation_commit", commit) }()
	deadline := time.Now().Add(5 * time.Second)
	var overlap c.Intent
	var outcome, submittedJSON, submittedHash string
	for {
		var raw string
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		e := f.plane.DB.SQL().QueryRowContext(ctx, `SELECT intent_json FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, p.IntentID, f.session).Scan(&raw)
		if e == nil {
			e = json.Unmarshal([]byte(raw), &overlap)
		}
		if e == nil {
			e = f.plane.DB.SQL().QueryRowContext(ctx, `SELECT outcome,request_json,submitted_sha256 FROM confirmation_commit_requests WHERE intent_id=? AND ai_session_id=? AND request_id=?`, p.IntentID, f.session, commit.RequestID).Scan(&outcome, &submittedJSON, &submittedHash)
		}
		cancel()
		if e == nil && outcome == "CONSUMED" && overlap.State == "CONFIRMING" && len(overlap.Steps) == 1 && overlap.Steps[0].State == "UNKNOWN" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no durable cancellation overlap: %+v %s %v", overlap, outcome, e)
		}
		time.Sleep(5 * time.Millisecond)
	}
	var storedCommit c.CommitInput
	decode(json.RawMessage(submittedJSON), &storedCommit)
	expectedHash := c.ValueHash(commit)
	if submittedHash != expectedHash || !reflect.DeepEqual(storedCommit, commit) {
		t.Fatalf("wrong durable submission: %s %s", submittedJSON, submittedHash)
	}
	if overlap.Commit == nil || !reflect.DeepEqual(*overlap.Commit, commit) {
		t.Fatalf("wrong consumed commit: %+v", overlap.Commit)
	}
	assertRows := func(wantGrant, wantReceipt string) {
		t.Helper()
		var grant, receipt string
		var results int
		if e := f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_task_authorizations WHERE workflow_id=?`, state.WorkflowID).Scan(&grant); e != nil {
			t.Fatal(e)
		}
		if e := f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, state.WorkflowID, "pending-effect").Scan(&receipt); e != nil {
			t.Fatal(e)
		}
		if e := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_implementation_results WHERE workflow_id=?`, state.WorkflowID).Scan(&results); e != nil {
			t.Fatal(e)
		}
		if grant != wantGrant || receipt != wantReceipt || results != 0 {
			t.Fatalf("grant=%s receipt=%s results=%d", grant, receipt, results)
		}
	}
	assertRows("ACTIVE", "PENDING")
	if evidence != "" {
		b, _ := json.MarshalIndent(map[string]any{"intent": overlap, "submissionOutcome": outcome, "submittedRequest": storedCommit, "submittedHash": submittedHash, "grant": "ACTIVE", "receipt": "PENDING", "results": 0}, "", "  ")
		if e := os.WriteFile(filepath.Join(evidence, "durable-overlap.json"), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	select {
	case r := <-cancelDone:
		t.Fatalf("cancel returned before release: %+v", r)
	default:
	}
	unblock()
	await := func(ch <-chan response) response {
		t.Helper()
		select {
		case r := <-ch:
			return r
		case <-time.After(10 * time.Second):
			t.Fatal("MCP completion timed out")
			return response{}
		}
	}
	failedWrite := await(writeDone)
	if failedWrite.err != nil || failedWrite.value == nil || !failedWrite.value.IsError {
		t.Fatalf("expected explicit write business failure: %+v", failedWrite)
	}
	cancelResponse := await(cancelDone)
	assertRows("ACTIVE", "PENDING")
	if cancelResponse.err != nil || cancelResponse.value == nil || !cancelResponse.value.IsError {
		t.Fatalf("expected cancellation business failure: %+v", cancelResponse)
	}
	if barrierTimedOut.Load() {
		t.Fatal("hook barrier timed out; not a successful ordering observation")
	}
	cancelled := intent(cancelResponse.value.StructuredContent)
	b, _ := json.Marshal(cancelled)
	if cancelled.State != "REJECTED" || cancelled.CanResume || len(cancelled.Steps) != 1 || cancelled.Steps[0].State != "REJECTED" || cancelled.Steps[0].Effect != "NOT_STARTED" || cancelled.Steps[0].Code != string(directfile.CancellationMutationUnresolved) || !strings.Contains(string(b), "unresolved file mutation receipt") {
		t.Fatalf("cancel did not explain unresolved mutation: %s", b)
	}
	if cancelled.Submission == nil || cancelled.Submission.Consumed == nil || !*cancelled.Submission.Consumed || cancelled.Submission.RedispatchSameIntent || !cancelled.Submission.Recorded || cancelled.Submission.Outcome != c.Consumed || cancelled.Submission.ConsumptionDisposition != "CONSUMED" {
		t.Fatalf("rejection lost consumed/no-redispatch truth: %+v", cancelled.Submission)
	}
	assertRows("ACTIVE", "PENDING")
	t.Logf("durable overlap proved; injected write failed; cancellation state=%s canResume=%v", cancelled.State, cancelled.CanResume)

	// This is an explicit historical replay of a known CONSUMED/REJECTED
	// receipt, never a retry of an UNKNOWN business result.
	frozenReplay := definitionFormalSnapshot(t, f, []string{"direct_file_task_authorizations", "direct_file_start_receipts", "direct_file_operation_receipts", "direct_file_worker_claims"})
	replayResponse := rawCall(gpt, "confirmation_commit", commit)
	if replayResponse.err != nil || replayResponse.value == nil {
		t.Fatalf("replay transport failure: %+v", replayResponse)
	}
	replay := intent(replayResponse.value.StructuredContent)
	replayPhysical, e := snapshotTCPMutation(path)
	if replay.State != "REJECTED" || !reflect.DeepEqual(replay.Steps, cancelled.Steps) || !reflect.DeepEqual(frozenReplay, definitionFormalSnapshot(t, f, []string{"direct_file_task_authorizations", "direct_file_start_receipts", "direct_file_operation_receipts", "direct_file_worker_claims"})) || e != nil || !sameTCPMutation(physical, replayPhysical) || effects.Load() != 1 {
		t.Fatal("known rejection replay changed business facts")
	}
	var rejectedReceipts int
	if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_start_receipts WHERE ai_session_id=? AND request_id=?`, f.session, cancelled.Steps[0].RequestID).Scan(&rejectedReceipts); e != nil || rejectedReceipts != 0 {
		t.Fatalf("rejected cancellation left business receipt: %d %v", rejectedReceipts, e)
	}
	// A separately confirmed cancellation still cannot bypass unresolved IO.
	stillPending := prepare("prepare-second-cancel-before-proof")
	pendingCommit := c.CommitInput{AISessionID: f.session, IntentID: stillPending.IntentID, Version: stillPending.Version, Purpose: stillPending.Purpose, Action: stillPending.Action, RequestID: "second-cancel-before-proof", ContentHash: stillPending.ContentHash, VariantID: "exact", Selection: c.Selection{}}
	secondResponse := rawCall(gpt, "confirmation_commit", pendingCommit)
	if secondResponse.err != nil || secondResponse.value == nil || !secondResponse.value.IsError {
		t.Fatalf("pending IO bypassed by new cancellation: %+v", secondResponse)
	}
	secondRejected := intent(secondResponse.value.StructuredContent)
	if secondRejected.State != "REJECTED" || len(secondRejected.Steps) != 1 || secondRejected.Steps[0].Effect != "NOT_STARTED" || secondRejected.Steps[0].Code != string(directfile.CancellationMutationUnresolved) {
		t.Fatalf("second cancellation did not fail closed: %+v", secondRejected)
	}
	assertRows("ACTIVE", "PENDING")
	closeLocal()
	closeGPT()
	reopenDirectDeliveryFixture(t, f)
	local, closeLocal = pendingCancelHTTPClient(t, f, false)
	gpt, closeGPT = pendingCancelHTTPClient(t, f, true)
	readInput := c.ReadInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, ContentHash: p.ContentHash, RequestID: commit.RequestID}
	oldCancel := intent(call(gpt, "confirmation_get", readInput))
	if oldCancel.State != cancelled.State || oldCancel.CanResume {
		t.Fatalf("restart changed unresolved confirmation: %+v", oldCancel)
	}
	assertOld := func() {
		t.Helper()
		got := call(local, "file_worker_get", claim)["result"].(map[string]any)
		if got["state"] != directfile.WorkerOutcomeUnknown || got["executionPayload"] != nil || got["resultReceipt"] != nil || !reflect.DeepEqual(got["receipt"], originalClaim["receipt"]) {
			t.Fatalf("old worker redispatched: %+v", got)
		}
	}
	assertOld()
	grantSnapshot := func() string {
		t.Helper()
		var s string
		if e := f.plane.DB.SQL().QueryRow(`SELECT json_array(state,runtime_epoch,generation,operation_count,started_at) FROM direct_file_task_authorizations WHERE workflow_id=?`, state.WorkflowID).Scan(&s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	frozen := grantSnapshot()
	f.plane.DirectFiles.SetAfterFileEffectHookForTest(func() error { effects.Add(1); return errors.New("FORBIDDEN redispatch after restart") })
	recovered := call(local, "file_write", args)["result"]
	if recovered == nil || effects.Load() != 1 {
		t.Fatal("journal proof redispatched physical mutation")
	}
	now, err := snapshotTCPMutation(path)
	if err != nil || !sameTCPMutation(physical, now) || grantSnapshot() != frozen {
		t.Fatalf("proof changed file or refreshed grant: %v", err)
	}
	assertRows("RECOVERY_REQUIRED", "COMPLETED")
	if evidence != "" {
		b, _ := json.MarshalIndent(map[string]any{"effects": effects.Load(), "beforeDigest": fmt.Sprintf("%x", physical.Digest), "afterDigest": fmt.Sprintf("%x", now.Digest), "beforeMtime": physical.Info.ModTime(), "afterMtime": now.Info.ModTime(), "sameFile": os.SameFile(physical.Info, now.Info), "beforeGrant": frozen, "afterGrant": grantSnapshot()}, "", "  ")
		if e := os.WriteFile(filepath.Join(evidence, "cold-journal-proof.json"), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	// Public prepare provides a distinct explicit choice; never replay the old
	// consumed rejected commit or invent a resume capability for its outcome.
	next := prepare("prepare-new-explicit-cancel-after-proof")
	if next.IntentID == p.IntentID {
		t.Fatal("new explicit cancellation reused old consumed intent")
	}
	nextCommit := c.CommitInput{AISessionID: f.session, IntentID: next.IntentID, Version: next.Version, Purpose: next.Purpose, Action: next.Action, RequestID: "new-explicit-cancel-after-proof", ContentHash: next.ContentHash, VariantID: "exact", Selection: c.Selection{}}
	closed := intent(call(gpt, "confirmation_commit", nextCommit))
	if closed.State != "COMPLETED" {
		t.Fatalf("new explicit cancellation: %+v", closed)
	}
	assertRows("CLOSED", "COMPLETED")
	assertOld()
	oldAfter := intent(call(gpt, "confirmation_get", readInput))
	if oldAfter.State != oldCancel.State || !reflect.DeepEqual(oldAfter.Steps, oldCancel.Steps) {
		t.Fatal("new cancellation rewrote old outcome")
	}

	if n := fileClientCount(t, f, "agent_executions"); n != initialAgentExecutions {
		t.Fatalf("created agent execution: before=%d after=%d", initialAgentExecutions, n)
	}
	finalPhysical, e := snapshotTCPMutation(path)
	if e != nil || !sameTCPMutation(physical, finalPhysical) || effects.Load() != 1 {
		t.Fatal("final cancellation changed physical evidence")
	}
	for _, table := range []string{"execution_proposals", "direct_file_implementation_results", "direct_file_implementation_acceptances", "task_commit_receipts"} {
		if n := fileClientCount(t, f, table); n != 0 {
			t.Fatalf("unexpected %s rows=%d", table, n)
		}
	}
	closeLocal()
	closeGPT()
	t.Log("cold exact-payload journal proof completed without file effect or grant refresh; new explicit cancellation closed grant, old worker UNKNOWN with no payload/result")
}
