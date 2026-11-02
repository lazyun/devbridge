package controlapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// A distinct explicit cancellation of an already closed grant must either be
// refused before consumption or retain its own exact durable business receipt.
// The first cancellation's receipt is never proof for the second confirmation.
func TestDirectFileClosedCancellationExactRecoveryMCP(t *testing.T) {
	f, current, state, claim := fileWorkerHTTPFixture(t)
	local, closeLocal := pendingCancelHTTPClient(t, f, false)
	gpt, closeGPT := pendingCancelHTTPClient(t, f, true)
	evidence := os.Getenv("DEVBRIDGE_CLOSED_CANCEL_EVIDENCE_DIR")
	if evidence != "" {
		if err := os.MkdirAll(evidence, 0700); err != nil {
			t.Fatal(err)
		}
	}
	save := func(name string, value any) {
		t.Helper()
		if evidence == "" {
			return
		}
		b, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(evidence, name+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	seq := 0
	raw := func(s *sdkmcp.ClientSession, name string, args any) *sdkmcp.CallToolResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := s.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
		seq++
		save(fmt.Sprintf("%02d-%s", seq, name), map[string]any{"input": args, "output": r, "transportError": fmt.Sprint(err)})
		if err != nil {
			t.Fatalf("%s transport: %v", name, err)
		}
		return r
	}
	call := func(s *sdkmcp.ClientSession, name string, args any) map[string]any {
		t.Helper()
		r := raw(s, name, args)
		if r.IsError {
			t.Fatalf("%s: %+v", name, r)
		}
		return mcpObject(t, r.StructuredContent)
	}
	intent := func(v any) c.Intent {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var r c.Result
		if err = json.Unmarshal(b, &r); err != nil || r.Intent == nil {
			t.Fatalf("intent: %s %v", b, err)
		}
		return *r.Intent
	}
	prepare := func(tag string) c.Intent {
		t.Helper()
		in := c.PrepareInput{AISessionID: f.session, RequestID: "prepare-" + tag, Purpose: c.Execution, Action: "CANCEL_DIRECT_FILE", Execution: &c.ExecutionBody{Action: "CANCEL_DIRECT_FILE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: state.DecisionID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, Variants: []c.Variant{{ID: "cancel", Label: "Explicit isolated cancellation", Summary: "Cancel isolated file authority", ImplementationMode: current.WorkSpec.ImplementationMode, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}}
		return intent(call(gpt, "execution_prepare", in))
	}
	commit := func(p c.Intent, tag string) c.CommitInput {
		return c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "commit-" + tag, ContentHash: p.ContentHash, VariantID: "cancel", Selection: c.Selection{}}
	}
	read := func(p c.Intent, in c.CommitInput) c.ReadInput {
		return c.ReadInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, ContentHash: p.ContentHash, RequestID: in.RequestID}
	}
	subrequest := func(p c.Intent) string {
		return "confirmation_" + c.ValueHash([]string{p.IntentID, "CANCEL_DIRECT_FILE"})[:32]
	}
	call(local, "file_worker_claim", claim)
	call(local, "file_read", directInput(f, state, current.RepositoryTargets[0]))
	var root string
	if err := f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, state.WorkflowID, current.RepositoryTargets[0].MemberID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "same.txt")
	physical, err := snapshotTCPMutation(path)
	if err != nil {
		t.Fatal(err)
	}
	first := prepare("first")
	firstCommit := commit(first, "first")
	completed := intent(call(gpt, "confirmation_commit", firstCommit))
	if completed.State != "COMPLETED" {
		t.Fatalf("first cancellation not completed: %+v", completed)
	}
	second := prepare("already-closed")
	secondCommit := commit(second, "already-closed")
	trigger := `CREATE TRIGGER closed_cancel_completion_fault BEFORE UPDATE OF intent_json ON confirmation_intents WHEN NEW.intent_id='` + second.IntentID + `' AND json_extract(NEW.intent_json,'$.state')='COMPLETED' BEGIN SELECT RAISE(ABORT,'isolated already-closed completion loss'); END`
	if _, err = f.plane.DB.SQL().Exec(trigger); err != nil {
		t.Fatal(err)
	}
	failed := raw(gpt, "confirmation_commit", secondCommit)
	failure := mcpObject(t, failed.StructuredContent)["failure"].(map[string]any)
	if !failed.IsError || failure["code"] != "CONFIRMATION_FACTS_UNAVAILABLE" || failure["stage"] != "RECEIPT_READBACK" {
		t.Fatalf("wrong fault: %+v", failed)
	}
	var firstReceipts, secondReceipts int
	var grant, durable string
	count := func(p c.Intent) int {
		t.Helper()
		var n int
		if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_start_receipts WHERE ai_session_id=? AND request_id=? AND action='CANCEL_DIRECT_FILE' AND workflow_id=?`, f.session, subrequest(p), state.WorkflowID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	firstReceipts, secondReceipts = count(first), count(second)
	if err = f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_task_authorizations WHERE workflow_id=?`, state.WorkflowID).Scan(&grant); err != nil {
		t.Fatal(err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, second.IntentID).Scan(&durable); err != nil {
		t.Fatal(err)
	}
	save("pre-restart", map[string]any{"firstIntent": first.IntentID, "secondIntent": second.IntentID, "firstSubrequest": subrequest(first), "secondSubrequest": subrequest(second), "firstReceipts": firstReceipts, "secondReceipts": secondReceipts, "grant": grant, "secondDurable": json.RawMessage(durable), "trigger": trigger})
	if firstReceipts != 1 || grant != "CLOSED" {
		t.Fatalf("first cancellation facts changed: receipts=%d grant=%s", firstReceipts, grant)
	}
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER closed_cancel_completion_fault`); err != nil {
		t.Fatal(err)
	}
	closeLocal()
	closeGPT()
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	local, closeLocal = pendingCancelHTTPClient(t, f, false)
	gpt, closeGPT = pendingCancelHTTPClient(t, f, true)
	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	original := intent(call(gpt, "confirmation_get", read(first, firstCommit)))
	observed := intent(call(gpt, "confirmation_get", read(second, secondCommit)))
	worker := call(local, "file_worker_get", claim)
	workspace := call(gpt, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
	assertDefinitionFormalSnapshot(t, f, tables, before, "cold exact reads must not repair or redispatch")
	now, err := snapshotTCPMutation(path)
	if err != nil || !sameTCPMutation(physical, now) {
		t.Fatalf("file changed: %v", err)
	}
	save("cold-witness", map[string]any{"first": original, "second": observed, "worker": worker, "workspace": workspace, "allTablesUnchanged": true, "fileUnchanged": true, "secondReceipts": secondReceipts})
	t.Logf("already-closed second cancel: receipts first=%d second=%d; first=%s second=%s canResume=%v", firstReceipts, secondReceipts, original.State, observed.State, observed.CanResume)
	if original.State != "COMPLETED" {
		t.Fatalf("first historical result changed: %+v", original)
	}
	// Do not resend the uncertain commit or infer success from CLOSED alone.
	if secondReceipts != 1 || observed.State != "COMPLETED" || observed.CanResume {
		t.Fatalf("successful already-closed branch lost exact completion proof: receipts=%d state=%s canResume=%v", secondReceipts, observed.State, observed.CanResume)
	}
	if len(observed.Steps) != 1 || observed.Steps[0].RequestID != subrequest(second) || observed.Steps[0].WorkflowID != state.WorkflowID || observed.Steps[0].RevisionID != current.CurrentDefinitionRevisionID {
		t.Fatalf("cold completion borrowed another cancellation identity: %+v", observed.Steps)
	}
	node := workspace["result"].(map[string]any)["taskView"].(map[string]any)["actionNode"].(map[string]any)
	if node["blocking"] == true {
		t.Fatalf("proven completed cancellation still blocks the Task: %+v", node)
	}
	// Only after exact completion is proven may the original request be
	// replayed; it must read the receipt without inserting it again.
	replayed := intent(call(gpt, "confirmation_commit", secondCommit))
	if replayed.State != "COMPLETED" || !reflect.DeepEqual(replayed.Steps, observed.Steps) {
		t.Fatalf("completed replay changed exact result: %+v", replayed)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "proven completed replay must not create effects")
	if count(first) != 1 || count(second) != 1 {
		t.Fatal("historical cancellation replay duplicated receipts")
	}
	save("completed-replay", map[string]any{"intent": replayed, "allTablesUnchanged": true, "actionNode": node})
	closeLocal()
	closeGPT()
}
