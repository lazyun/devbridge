package controlapi_test

// Actual isolated worktree bytes, SQLite admission and GPT HTTP. The external
// edit is a TEST_ONLY drift injection, never a new worker write authorization.
import (
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/fileworker"
	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	sqlitedriver "modernc.org/sqlite"
)

func TestFileVerificationSnapshotDriftRealHTTP(t *testing.T) {
	// Exact observer records entry; only SQLite RAISE(ABORT) causes setup failure.
	var hits atomic.Uint64
	var active atomic.Bool
	active.Store(true)
	t.Cleanup(func() { active.Store(false) })
	observer := fmt.Sprintf("test_file_drift_observe_%d", fileHandoffObserverID.Add(1))
	if e := sqlitedriver.RegisterScalarFunction(observer, 0, func(_ *sqlitedriver.FunctionContext, _ []driver.Value) (driver.Value, error) {
		if active.Load() {
			hits.Add(1)
		}
		return int64(1), nil
	}); e != nil {
		t.Fatal(e)
	}
	f, current, _, request := fileClientFixture(t, task.VerificationModeRun)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	session, closeHTTP := pendingCancelHTTPClient(t, f, true)
	defer closeHTTP()
	wire := fileClientTransport(t, session, request, current)
	const summary = "Record exact FILE snapshot before independent verification drift"
	target := request.Claim.WorkerTarget
	input := directfile.ImplementationResultInput{WorkerClaim: &request.Claim.WorkerClaimIdentity, AISessionID: target.AISessionID, TaskID: target.TaskID, TaskDefinitionRevisionID: target.TaskDefinitionRevisionID, WorkflowID: target.WorkflowID, RequestID: request.ResultRequestID, Summary: summary, ProtocolVersion: target.ProtocolVersion, RuntimeEpoch: target.RuntimeEpoch, GrantGeneration: target.GrantGeneration}
	raw, e := json.Marshal(input)
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(raw)
	expectedID := "file_result_" + hex.EncodeToString(digest[:])[:32]
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	ddl := fmt.Sprintf(`CREATE TRIGGER test_file_drift_ready BEFORE INSERT ON execution_start_effects WHEN NEW.task_id=%s AND NEW.actor_kind='AI_SESSION' AND NEW.actor_id=%s AND NEW.action='START_VERIFICATION' AND NEW.request_id=%s BEGIN SELECT %s(); SELECT RAISE(ABORT,'TEST_ONLY_FILE_DRIFT_READY'); END`, quote(f.task), quote(f.session), quote("file-verification-"+expectedID), observer)
	if _, e = f.plane.DB.SQL().Exec(ddl); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := f.plane.DB.SQL().Exec(`DROP TRIGGER IF EXISTS test_file_drift_ready`); e != nil {
			t.Error(e)
		}
	})
	// Save actual pre-callback bytes for the distinct revert-to-baseline control.
	baselineBytes := map[string][]byte{}
	rows, e := f.plane.DB.SQL().Query(`SELECT member_id,canonical_path FROM direct_file_task_members WHERE workflow_id=?`, request.Claim.WorkflowID)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var id, root string
		if e = rows.Scan(&id, &root); e != nil {
			rows.Close()
			t.Fatal(e)
		}
		b, err := os.ReadFile(filepath.Join(root, "same.txt"))
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		baselineBytes[id] = b
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		t.Fatal(e)
	}
	rows.Close()

	callbacks := 0
	client, e := fileworker.New(wire, request, func(c context.Context, d fileworker.Delivery, files fileworker.Files) (fileworker.Completion, error) {
		callbacks++
		fileClientReplaceBoth(t, f, current, c, d, files)
		return fileworker.Completion{Summary: summary}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	out, e := client.Run(ctx)
	if e != nil || out.State != fileworker.ResultRecorded || out.ResultReceipt == nil || out.ResultInput == nil || !reflect.DeepEqual(*out.ResultInput, input) || hits.Load() != 1 {
		t.Fatalf("READY setup failed: %+v error=%v hits=%d", out, e, hits.Load())
	}
	original := *out.ResultReceipt
	if original.ID != expectedID || original.ExecutionID == "" || original.VerificationStatus != "READY" || original.VerificationRunID != "" || original.VerificationStartError != "CODEX_VERIFICATION_START_FAILED: CONFLICT: the request conflicts with durable control-plane state" {
		t.Fatalf("unexpected setup receipt %+v", original)
	}
	if _, e = f.plane.DB.SQL().Exec(`DROP TRIGGER test_file_drift_ready`); e != nil {
		t.Fatal(e)
	}
	fileClientSubmissionProof(t, f, request, out.ResultInput)
	workspace := callUpperMCP(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
	raw, e = json.Marshal(workspace["result"])
	if e != nil {
		t.Fatal(e)
	}
	var view struct {
		TaskView struct {
			ExecutionStartTarget *wp.TaskExecutionExpectation `json:"executionStartTarget"`
		} `json:"taskView"`
	}
	if e = json.Unmarshal(raw, &view); e != nil || view.TaskView.ExecutionStartTarget == nil {
		t.Fatalf("official target missing: %s %v", raw, e)
	}
	continuation := *view.TaskView.ExecutionStartTarget
	if continuation.ExecutionID != original.ExecutionID || continuation.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID || continuation.Phase != task.PhaseVerificationReady {
		t.Fatalf("wrong target %+v", continuation)
	}
	args := map[string]any{"aiSessionId": f.session, "taskId": f.task, "expectedTarget": continuation}
	tables := []string{"tasks", "execution_proposals", "runs", "runtime_handoffs", "execution_start_effects", "feature_area_agent_claims", "run_repository_bindings", "direct_file_task_authorizations", "direct_file_task_members", "direct_file_worker_claims", "direct_file_operation_receipts", "direct_file_implementation_results", "direct_file_verifications", "direct_file_verification_selections", "task_definition_revisions", "direct_file_implementation_acceptances", "task_commit_receipts", "runtime_selection_sources", "runtime_selection_bindings", "runtime_start_authorities"}
	immutableTables := []string{"direct_file_task_authorizations", "direct_file_task_members", "direct_file_worker_claims", "direct_file_operation_receipts", "direct_file_implementation_results", "direct_file_verifications", "direct_file_verification_selections", "task_definition_revisions", "confirmation_intents"}
	immutable := definitionFormalSnapshot(t, f, immutableTables)
	frozen := definitionFormalSnapshot(t, f, tables)
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if e := f.plane.DB.SQL().QueryRow(q, args...).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	noStart := func() {
		t.Helper()
		if count(`SELECT count(*) FROM runs WHERE execution_proposal_id=?`, original.ExecutionID) != 0 || count(`SELECT count(*) FROM execution_start_effects WHERE task_id=? AND action='START_VERIFICATION'`, f.task) != 0 || fileClientCount(t, f, "runtime_handoffs") != 0 {
			t.Fatal("unexpected reserved Run/start effect/handoff")
		}
		if b, e := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG")); e == nil && len(b) != 0 || e != nil && !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("unexpected protocol dispatch %s %v", b, e)
		}
	}
	noStart()
	member := original.Members[0]
	if member.State != "COMPLETED" || !reflect.DeepEqual(member.ChangedPaths, []string{"same.txt"}) {
		t.Fatalf("expected actual completed replacement %+v", member)
	}
	var root string
	if e = f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, request.Claim.WorkflowID, member.MemberID).Scan(&root); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "same.txt")
	oldBytes, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	gitClient := bridgegit.NewClient(bridgegit.Config{})
	oldSnapshot, e := gitClient.Snapshot(ctx, root)
	if e != nil || oldSnapshot != member.EndSnapshot {
		t.Fatalf("original snapshot=%s recorded=%s %v", oldSnapshot, member.EndSnapshot, e)
	}
	trees := upperClientTrees(t, f)
	if len(baselineBytes[member.MemberID]) == 0 || reflect.DeepEqual(baselineBytes[member.MemberID], oldBytes) {
		t.Fatal("missing distinct pre-callback member bytes")
	}
	for _, tc := range []struct {
		name    string
		bytes   []byte
		failure string
	}{
		{"new-post-result-bytes", append(append([]byte{}, oldBytes...), []byte("TEST_ONLY external modification after result commit\n")...), "FILE_OPERATION_FAILED: final file bytes differ from durable mutation proof"},
		{"revert-to-pre-callback", baselineBytes[member.MemberID], "FILE_RESULT_CONFLICT: exact file result identity, authorization or lifecycle changed"},
	} {

		if e = os.WriteFile(path, tc.bytes, info.Mode().Perm()); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			if e := os.WriteFile(path, oldBytes, info.Mode().Perm()); e != nil {
				t.Error(e)
			}
		})
		actualBytes, e := os.ReadFile(path)
		if e != nil || !reflect.DeepEqual(actualBytes, tc.bytes) {
			t.Fatalf("actual drift bytes: %v", e)
		}
		changedSnapshot, e := gitClient.Snapshot(ctx, root)
		if e != nil || changedSnapshot == oldSnapshot || sha256.Sum256(actualBytes) == sha256.Sum256(oldBytes) {
			t.Fatalf("drift injection not real %s %s %v", oldSnapshot, changedSnapshot, e)
		}
		t.Logf("case="+tc.name+" actual isolated member %s drift: snapshot %s -> %s, bytes SHA256 %x -> %x", member.MemberID, oldSnapshot, changedSnapshot, sha256.Sum256(oldBytes), sha256.Sum256(actualBytes))
		if tc.name == "revert-to-pre-callback" && changedSnapshot != member.StartSnapshot {
			t.Fatalf("baseline did not recover original start snapshot: %s vs%s", changedSnapshot, member.StartSnapshot)
		}
		driftTrees := upperClientTrees(t, f)
		for key, before := range trees {
			after := driftTrees[key]
			if after.HEAD != before.HEAD || key != "managed:"+member.MemberID && after != before {
				t.Fatalf("drift changed other member/source/HEAD: %s", key)
			}
		}
		denied, e := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "verification_continue", Arguments: args})
		if e != nil || denied == nil || !denied.IsError {
			t.Fatalf("drift was not refused: %+v %v", denied, e)
		}
		text := ""
		for _, content := range denied.Content {
			if v, ok := content.(*sdkmcp.TextContent); ok {
				text += v.Text
			}
		}
		if text != tc.failure {
			t.Fatalf("unexpected precise drift failure: %q; result=%+v", text, denied)
		}
		if frozen != definitionFormalSnapshot(t, f, tables) {
			t.Fatal("drift refusal changed durable authority/results")
		}
		noStart()
		exact := callUpperMCP(t, ctx, session, "file_implementation_result_get", map[string]any{"aiSessionId": f.session, "taskId": f.task, "taskDefinitionRevisionId": current.CurrentDefinitionRevisionID, "workflowId": request.Claim.WorkflowID})
		raw, e = json.Marshal(exact["result"])
		if e != nil {
			t.Fatal(e)
		}
		var persisted directfile.ImplementationResult
		if e = json.Unmarshal(raw, &persisted); e != nil || persisted.ID != original.ID || persisted.VerificationStatus != "READY" || !reflect.DeepEqual(persisted.Members, original.Members) || persisted.Accepted {
			t.Fatalf("drift rewrote frozen result: %s %v", raw, e)
		}
		if frozen != definitionFormalSnapshot(t, f, tables) {
			t.Fatal("result read mutated durable state")
		}
		if !reflect.DeepEqual(driftTrees, upperClientTrees(t, f)) {
			t.Fatal("refusal or read changed injected member trees")
		}
		t.Logf("exact refusal %q; no Run/handoff/effect; result_get retains original endSnapshot", text)
	}
	// Restore only the injected bytes, never rows, authority or configuration.
	if e = os.WriteFile(path, oldBytes, info.Mode().Perm()); e != nil {
		t.Fatal(e)
	}
	restored, e := gitClient.Snapshot(ctx, root)
	if e != nil || restored != oldSnapshot || !reflect.DeepEqual(trees, upperClientTrees(t, f)) {
		t.Fatalf("exact restore failed %s %v", restored, e)
	}
	reply, e := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "verification_continue", Arguments: args})
	if e != nil || reply == nil || reply.IsError {
		t.Fatalf("same target after restore failed %+v %v", reply, e)
	}
	raw, e = json.Marshal(reply.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	var start mcpserver.SurfaceExecutionStartOutput
	if e = json.Unmarshal(raw, &start); e != nil || !start.ReservationCommitted || start.Proposal.ID != original.ExecutionID || start.Run.ID == "" {
		t.Fatalf("wrong restored reservation %s %v", raw, e)
	}
	var verified run.Run
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		verified, e = f.facade.GetRunForSession(f.ctx, f.session, start.Run.ID)
		if e != nil {
			t.Fatal(e)
		}
		if verified.Status == run.StatusSucceeded || verified.Status == run.StatusFailed || verified.Status == run.StatusInfrastructureFailed {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if verified.Status == run.StatusSucceeded && verified.ResultProtocol == run.NaturalResultProtocol && verified.VerificationReport == nil {
		submitNaturalFixtureAnalysis(t, f, verified.ID, nil)
		verified, e = f.facade.GetRunForSession(f.ctx, f.session, verified.ID)
		if e != nil {
			t.Fatal(e)
		}
	}
	if verified.Status != run.StatusSucceeded || verified.Purpose != run.PurposeVerification || verified.VerificationReport == nil || len(verified.VerificationReport.Checks) != 2 {
		t.Fatalf("not real verified %+v", verified)
	}
	seen := map[string]bool{}
	for _, check := range verified.VerificationReport.Checks {
		if check.Observed == nil || check.Observed.ExitCode == nil || *check.Observed.ExitCode != 0 || check.Observed.Command != "test -f same.txt" || check.Observed.Source == nil {
			t.Fatalf("missing actual command %+v", check)
		}
		seen[check.Observed.Source.RepositoryID] = true
	}
	if len(seen) != 2 || !seen[f.repos["app"]] || !seen[f.repos["sdk"]] || count(`SELECT count(*) FROM runs WHERE execution_proposal_id=?`, original.ExecutionID) != 1 {
		t.Fatal("wrong verification workset or Run count")
	}
	if callbacks != 1 || wire.count("file_write") != 2 || wire.count("file_implementation_result_submit") != 1 || hits.Load() != 1 || !reflect.DeepEqual(trees, upperClientTrees(t, f)) {
		t.Fatal("continuation repeated or changed file work")
	}
	if immutable != definitionFormalSnapshot(t, f, immutableTables) {
		t.Fatal("positive continuation changed original result/authority/approval")
	}
	fileClientNoAcceptance(t, f)
	if fileClientCount(t, f, "task_commit_receipts") != 0 {
		t.Fatal("implicit delivery")
	}
	t.Logf("restore-only positive: same execution=%s sole TEST_ONLY verification=%s, both real member commands, no new approval/submission/acceptance", original.ExecutionID, verified.ID)
}
