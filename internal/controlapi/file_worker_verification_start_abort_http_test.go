package controlapi_test

// Actual Core transactions and GPT MCP; only a narrowly scoped SQLite trigger
// injects the fault. The callback and verification subprocess are TEST_ONLY,
// not a model/native-runtime qualification. Result recording and backend start
// are separate commits; observing a recorded result must not resend either.
import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/fileworker"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	sqlitedriver "modernc.org/sqlite"
)

var fileHandoffObserverID atomic.Uint64

func TestFileWorkerVerificationStartAbortRealHTTP(t *testing.T) {
	for _, fault := range []bool{false, true} {
		name := "healthy_auto_start"
		if fault {
			name = "committed_result_sql_abort_then_explicit_continue"
		}
		t.Run(name, func(t *testing.T) { testFileHandoffAbort(t, fault) })
	}
}

func testFileHandoffAbort(t *testing.T, fault bool) {
	// Register before opening fixture connections. This function is observation
	// only: it returns success, never injects an error, writes rows or commits.
	// The following RAISE(ABORT) is the actual SQLite failure; public error mapping
	// intentionally redacts its token, so a side-channel proves exact trigger entry.
	observer := fmt.Sprintf("test_file_handoff_observe_%d", fileHandoffObserverID.Add(1))
	hits := make(chan []driver.Value, 4)
	var hitCount atomic.Uint64
	var observedTarget atomic.Pointer[chan []driver.Value]
	observedTarget.Store(&hits)
	t.Cleanup(func() { observedTarget.Store(nil) })
	if err := sqlitedriver.RegisterScalarFunction(observer, 3, func(_ *sqlitedriver.FunctionContext, args []driver.Value) (driver.Value, error) {
		p := observedTarget.Load()
		if p == nil {
			return int64(1), nil
		}
		hitCount.Add(1)
		copyArgs := append([]driver.Value(nil), args...)
		select {
		case *p <- copyArgs:
		default:
		}
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	f, current, _, request := fileClientFixture(t, task.VerificationModeRun)
	ctx, session := compositionGPTClient(t, f)
	wire := fileClientTransport(t, session, request, current)
	const summary = "Actual file result remains recorded if verification start transaction fails"
	const notes = "Separate explicit RUN authorization; commands belong to Core verification, not callback"
	target := request.Claim.WorkerTarget
	expectedInput := directfile.ImplementationResultInput{WorkerClaim: &request.Claim.WorkerClaimIdentity, AISessionID: target.AISessionID, TaskID: target.TaskID, TaskDefinitionRevisionID: target.TaskDefinitionRevisionID, WorkflowID: target.WorkflowID, RequestID: request.ResultRequestID, Summary: summary, VerificationNotes: notes, ProtocolVersion: target.ProtocolVersion, RuntimeEpoch: target.RuntimeEpoch, GrantGeneration: target.GrantGeneration}
	raw, err := json.Marshal(expectedInput)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	resultID := "file_result_" + hex.EncodeToString(hash[:])[:32]
	autoRequest := "file-verification-" + resultID
	const token = "TEST_ONLY_FILE_VERIFICATION_START_ABORT"
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	if fault {
		ddl := fmt.Sprintf(`CREATE TRIGGER test_file_verification_start_abort BEFORE INSERT ON execution_start_effects WHEN NEW.task_id=%s AND NEW.actor_kind='AI_SESSION' AND NEW.actor_id=%s AND NEW.action='START_VERIFICATION' AND NEW.request_id=%s BEGIN SELECT %s(%s,NEW.task_id,NEW.request_id); SELECT RAISE(ABORT,'%s'); END`, quote(f.task), quote(f.session), quote(autoRequest), observer, quote(token), token)
		if _, err = f.plane.DB.SQL().Exec(ddl); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, e := f.plane.DB.SQL().Exec(`DROP TRIGGER IF EXISTS test_file_verification_start_abort`); e != nil {
				t.Error(e)
			}
		})
	}
	beforeTrees := upperClientTrees(t, f)
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if e := f.plane.DB.SQL().QueryRow(query, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	baselineRuns := count(`SELECT count(*) FROM runs WHERE task_id=?`, f.task)
	callbacks := 0
	client, err := fileworker.New(wire, request, func(callCtx context.Context, d fileworker.Delivery, files fileworker.Files) (fileworker.Completion, error) {
		callbacks++
		fileClientReplaceBoth(t, f, current, callCtx, d, files)
		return fileworker.Completion{Summary: summary, VerificationNotes: notes}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.Run(ctx)
	if err != nil || out.State != fileworker.ResultRecorded || out.ResultReceipt == nil || out.ResultInput == nil || callbacks != 1 || !reflect.DeepEqual(*out.ResultInput, expectedInput) {
		t.Fatalf("formal result lost: %+v err=%v callbacks=%d", out, err, callbacks)
	}
	receipt := out.ResultReceipt
	if receipt.ID != resultID || receipt.ExecutionID == "" || receipt.Accepted || len(out.Mutations) != 2 {
		t.Fatalf("wrong formal receipt: %+v", out)
	}
	for _, m := range out.Mutations {
		if m.Response == nil {
			t.Fatal("successful callback lacked mutation proof")
		}
	}
	fileClientSubmissionProof(t, f, request, out.ResultInput)
	for table, want := range map[string]int{"direct_file_worker_claims": 1, "direct_file_implementation_results": 1, "direct_file_verifications": 1, "execution_proposals": 1, "direct_file_operation_receipts": 2, "direct_file_implementation_acceptances": 0, "task_commit_receipts": 0} {
		if got := fileClientCount(t, f, table); got != want {
			t.Fatalf("%s=%d want%d", table, got, want)
		}
	}
	state, e := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if e != nil || state.Enabled || state.State != "CLOSED" {
		t.Fatalf("committed result left grant open: %+v %v", state, e)
	}
	frozenTables := []string{"direct_file_implementation_results", "direct_file_operation_receipts", "direct_file_worker_claims", "direct_file_verifications", "task_definition_revisions", "direct_file_verification_selections"}
	frozen := definitionFormalSnapshot(t, f, frozenTables)
	treesAfterWrite := upperClientTrees(t, f)
	for key, old := range beforeTrees {
		now := treesAfterWrite[key]
		if strings.HasPrefix(key, "source:") && now != old || now.HEAD != old.HEAD {
			t.Fatalf("unexpected source/HEAD change: %s", key)
		}
	}
	verificationRun := receipt.VerificationRunID
	if fault {
		select {
		case actual := <-hits:
			if !reflect.DeepEqual(actual, []driver.Value{token, f.task, autoRequest}) {
				t.Fatalf("trigger identity: %+v", actual)
			}
		default:
			t.Fatal("actual exact SQLite trigger was never reached")
		}
		if hitCount.Load() != 1 {
			t.Fatalf("fault trigger hits=%d want1", hitCount.Load())
		}
		if receipt.VerificationStatus != "READY" || receipt.VerificationRunID != "" || receipt.VerificationStartError != "CODEX_VERIFICATION_START_FAILED: CONFLICT: the request conflicts with durable control-plane state" {
			t.Fatalf("wrong committed/error reply: %+v", receipt)
		}
		if got := count(`SELECT count(*) FROM execution_start_effects WHERE task_id=? AND action='START_VERIFICATION'`, f.task); got != 0 {
			t.Fatalf("failed start left effect %d", got)
		}
		if got := count(`SELECT count(*) FROM runs WHERE task_id=?`, f.task); got != baselineRuns {
			t.Fatalf("failed start created Run %d vs%d", got, baselineRuns)
		}
		if got := fileClientCount(t, f, "runtime_handoffs"); got != 0 {
			t.Fatalf("failed start handed off %d", got)
		}
		if b, e := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG")); e == nil && len(b) != 0 || e != nil && !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("failed start reached protocol subprocess: %s %v", b, e)
		}
		owner, e := f.facade.GetTask(f.ctx, f.task)
		if e != nil || owner.Phase != task.PhaseVerificationReady || owner.Status != task.StatusActive || owner.CurrentExecutionID != receipt.ExecutionID {
			t.Fatalf("not READY after rollback: %+v %v", owner, e)
		}
		if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER test_file_verification_start_abort`); err != nil {
			t.Fatal(err)
		}
		stableTables := append(append([]string{}, frozenTables...), "tasks", "execution_proposals", "runs", "runtime_handoffs", "execution_start_effects", "run_repository_bindings", "feature_area_agent_claims")
		beforeObserve := definitionFormalSnapshot(t, f, stableTables)
		observed, e := client.Observe(ctx)
		if e != nil || observed.State != fileworker.ResultRecorded || observed.ResultReceipt == nil || observed.ResultReceipt.ExecutionID != receipt.ExecutionID || observed.ResultReceipt.ID != resultID || observed.ResultReceipt.VerificationStatus != "READY" || observed.ResultReceipt.VerificationRunID != "" || observed.ResultReceipt.Accepted {
			t.Fatalf("READY observe: %+v %v", observed, e)
		}
		if !reflect.DeepEqual(observed.ResultInput, out.ResultInput) || !reflect.DeepEqual(observed.ClaimReceipt, out.ClaimReceipt) || !reflect.DeepEqual(observed.ResultReceipt.Members, receipt.Members) {
			t.Fatal("Observe changed original result/member binding")
		}
		if beforeObserve != definitionFormalSnapshot(t, f, stableTables) || !reflect.DeepEqual(treesAfterWrite, upperClientTrees(t, f)) {
			t.Fatal("pure observation advanced handoff/state/files")
		}
		if wire.count("file_implementation_result_submit") != 1 || wire.count("file_worker_claim") != 1 || wire.count("file_worker_get") != 1 || callbacks != 1 {
			t.Fatal("observation resent work")
		}
		t.Logf("actual SQL abort token=%s exact request=%s; result=%s+CLOSED committed, READY/noRun/nohandoff, Observe pure; transient reply error=%s", token, autoRequest, resultID, receipt.VerificationStartError)
		// Read the official continuation target. Never construct an omitted target
		// from private DB values to bypass a presentation/API defect.
		workspace := callUpperMCP(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
		workspaceRaw, e := json.Marshal(workspace["result"])
		if e != nil {
			t.Fatal(e)
		}
		var presented struct {
			TaskView struct {
				ExecutionStartTarget *wp.TaskExecutionExpectation `json:"executionStartTarget"`
			} `json:"taskView"`
		}
		if e = json.Unmarshal(workspaceRaw, &presented); e != nil {
			t.Fatal(e)
		}
		if presented.TaskView.ExecutionStartTarget == nil {
			t.Fatalf("real workspace_get omitted READY file verification continuation target: %s", workspaceRaw)
		}
		continuation := *presented.TaskView.ExecutionStartTarget
		official, e := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
		if e != nil {
			t.Fatalf("recorded READY file result has no official verification continuation target: %v", e)
		}
		if !reflect.DeepEqual(official, continuation) || continuation.ExecutionID != receipt.ExecutionID || continuation.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID || continuation.FeatureAreaID != f.area || continuation.Phase != task.PhaseVerificationReady {
			t.Fatalf("wrong exact continuation target: %+v", continuation)
		}
		reply, e := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "verification_continue", Arguments: map[string]any{"aiSessionId": f.session, "taskId": f.task, "expectedTarget": continuation}})
		if e != nil || reply == nil || reply.IsError {
			t.Fatalf("explicit continuation failed: %+v %v", reply, e)
		}
		b, e := json.Marshal(reply.StructuredContent)
		if e != nil {
			t.Fatal(e)
		}
		var started mcpserver.SurfaceExecutionStartOutput
		if e = json.Unmarshal(b, &started); e != nil || !started.ReservationCommitted || started.Proposal.ID != receipt.ExecutionID || started.Run.ID == "" {
			t.Fatalf("wrong continuation receipt %s %v", b, e)
		}
		verificationRun = started.Run.ID
	} else {
		select {
		case h := <-hits:
			t.Fatalf("healthy start unexpectedly hit fault observer %+v", h)
		default:
		}
		if hitCount.Load() != 0 {
			t.Fatalf("healthy trigger hits=%d want0", hitCount.Load())
		}
		if receipt.VerificationStartError != "" || verificationRun == "" {
			t.Fatalf("healthy handoff %+v", receipt)
		}
	}
	deadline := time.Now().Add(25 * time.Second)
	var vr run.Run
	for time.Now().Before(deadline) {
		vr, err = f.facade.GetRunForSession(f.ctx, f.session, verificationRun)
		if err != nil {
			t.Fatal(err)
		}
		if vr.Status == run.StatusSucceeded || vr.Status == run.StatusFailed || vr.Status == run.StatusInfrastructureFailed {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if vr.Status == run.StatusSucceeded && vr.ResultProtocol == run.NaturalResultProtocol && vr.VerificationReport == nil {
		submitNaturalFixtureAnalysis(t, f, vr.ID, nil)
		vr, err = f.facade.GetRunForSession(f.ctx, f.session, vr.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if vr.Status != run.StatusSucceeded || vr.Purpose != run.PurposeVerification || vr.ExecutionProposalID != receipt.ExecutionID || vr.VerificationReport == nil || len(vr.VerificationReport.Checks) != 2 {
		t.Fatalf("verification not actually complete: %+v", vr)
	}
	seen := map[string]bool{}
	for _, check := range vr.VerificationReport.Checks {
		if check.Observed == nil || check.Observed.ExitCode == nil || *check.Observed.ExitCode != 0 || check.Observed.Command != "test -f same.txt" || check.Observed.Source == nil {
			t.Fatalf("missing observed command: %+v", check)
		}
		seen[check.Observed.Source.RepositoryID] = true
	}
	if len(seen) != 2 || !seen[f.repos["app"]] || !seen[f.repos["sdk"]] {
		t.Fatalf("verification member coverage %v", seen)
	}
	if got := count(`SELECT count(*) FROM runs WHERE execution_proposal_id=? AND purpose='VERIFICATION'`, receipt.ExecutionID); got != 1 {
		t.Fatalf("duplicate verification Run %d", got)
	}
	if got := count(`SELECT count(*) FROM runs WHERE task_id=? AND purpose='IMPLEMENTATION'`, f.task); got != 0 {
		t.Fatalf("invented implementation Run %d", got)
	}
	observed, e := client.Observe(ctx)
	if e != nil || observed.State != fileworker.ResultRecorded || observed.ResultReceipt == nil || observed.ResultReceipt.VerificationRunID != verificationRun || observed.ResultReceipt.VerificationStatus != "SUCCEEDED" || observed.ResultReceipt.ID != resultID || observed.ResultReceipt.ExecutionID != receipt.ExecutionID || observed.ResultReceipt.Accepted || !reflect.DeepEqual(observed.ResultReceipt.Members, receipt.Members) {
		t.Fatalf("final projection %+v %v", observed, e)
	}
	if frozen != definitionFormalSnapshot(t, f, frozenTables) || !reflect.DeepEqual(treesAfterWrite, upperClientTrees(t, f)) {
		t.Fatal("verification changed immutable file result or original file trees")
	}
	if wire.count("file_worker_claim") != 1 || wire.count("file_write") != 2 || wire.count("file_implementation_result_submit") != 1 || callbacks != 1 {
		t.Fatal("consumer resent work during continuation/observation")
	}
	wantHits := uint64(0)
	if fault {
		wantHits = 1
	}
	if hitCount.Load() != wantHits {
		t.Fatalf("unexpected later trigger hits=%d want%d", hitCount.Load(), wantHits)
	}
	fileClientNoAcceptance(t, f)
	if got := fileClientCount(t, f, "task_commit_receipts"); got != 0 {
		t.Fatalf("automatic delivery %d", got)
	}
	t.Logf("fault=%t original result=%s execution=%s; single real TEST_ONLY verification=%s, both member commands observed, no acceptance/delivery", fault, resultID, receipt.ExecutionID, verificationRun)
}
