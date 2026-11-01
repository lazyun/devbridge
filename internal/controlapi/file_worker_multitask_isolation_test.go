package controlapi_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

// Two admitted Tasks share one actual Core/SQLite instance and source repositories.
// Admission uses typed production services; claims and mutations use actual Local
// MCP HTTP/SDK sessions. Deterministic callers, explicit NONE waiver, no model.
func TestFileWorkerRealCoreMultiTaskIsolation(t *testing.T) {
	a := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	ac := approveDirectOnFixture(t, a, "isolation-a")
	// Clone only fixture references, not a database or source repository. B obtains
	// a genuine separate FeatureArea through public services. Keep the same
	// business Session so requestId isolation cannot accidentally be Session-only.
	// A second real Session is reserved for cross-Session negative calls.
	copyB := *a
	b := &copyB
	ref := aiprojectselection.ClientBindingReference{CallerScope: "data-instance-local"}
	initialized, err := a.plane.AIContext.InitializeSession(a.ctx, ref, "isolation-b-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	otherSession := initialized.AISessionID
	if _, err = a.plane.AIContext.BindProject(a.ctx, ref, otherSession, a.project, "isolation-b-bind"); err != nil {
		t.Fatal(err)
	}
	commit := func(in c.PrepareInput) c.Intent {
		t.Helper()
		in.AISessionID = b.session
		p, e := b.facade.PrepareConfirmation(b.ctx, in)
		if e != nil {
			t.Fatal(e)
		}
		got, e := b.facade.CommitConfirmation(b.ctx, c.CommitInput{AISessionID: b.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "commit-" + in.RequestID, ContentHash: p.ContentHash, VariantID: in.Variants[0].ID})
		if e != nil || got.State != "COMPLETED" {
			t.Fatalf("B admission %s: %+v %v", in.Action, got, e)
		}
		return got
	}
	area := commit(c.PrepareInput{RequestID: "isolation-b-area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Independent B", ProblemStatement: "Isolate shared source repository"}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	b.area = area.Steps[0].FeatureAreaID
	created := commit(c.PrepareInput{RequestID: "isolation-b-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: b.area, Title: "Task B", ProblemStatement: "Independent file changes"}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	b.task = created.Steps[0].TaskID
	b.targets = nil
	for _, name := range []string{"app", "sdk"} {
		b.targets = append(b.targets, workset.Target{RepositoryID: a.repos[name], OriginCommit: a.heads[name], OriginRef: "refs/heads/" + name + "-development", Read: true, Write: workset.WriteScope{Kind: "NONE"}})
	}
	started := commit(c.PrepareInput{RequestID: "isolation-b-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: b.task, FeatureAreaID: b.area, RepositoryTargets: b.targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "review", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt from each member"}}})
	b.review, b.run = started.Steps[0].ReviewID, started.Steps[0].RunID
	b.access, err = b.facade.UpperReviewAccessForSession(b.ctx, b.session, b.review)
	if err != nil || b.access.State != "ACTIVE" {
		t.Fatalf("B access %+v %v", b.access, err)
	}
	frozen, err := b.plane.Reviews.Get(b.ctx, b.review)
	if err != nil {
		t.Fatal(err)
	}
	b.targets = workset.CloneTargets(frozen.RepositoryTargets)
	bc := approveDirectOnFixture(t, b, "isolation-b")
	fixtures := []*upperLifecycleFixture{a, b}
	currents := []task.Task{ac, bc}
	states := make([]directfile.State, 2)
	claims := make([]directfile.WorkerClaimInput, 2)
	for i, f := range fixtures {
		got, e := directConfirmationTagged(t, f, currents[i], "START_DIRECT_FILE", fmt.Sprintf("isolation-%d", i), directfile.State{})
		if e != nil || got.State != "COMPLETED" {
			t.Fatalf("start %d %+v %v", i, got, e)
		}
		states[i], e = f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
		if e != nil || states[i].State != "ACTIVE" {
			t.Fatalf("state %d %+v %v", i, states[i], e)
		}
		s := states[i]
		claims[i] = directfile.WorkerClaimInput{WorkerTarget: directfile.WorkerTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: s.TaskDefinitionRevisionID, DecisionID: s.DecisionID, WorkflowID: s.WorkflowID, ProtocolVersion: 2, RuntimeEpoch: s.RuntimeEpoch, GrantGeneration: s.GrantGeneration}, WorkerClaimIdentity: directfile.WorkerClaimIdentity{RequestID: fmt.Sprintf("dispatch-%d", i), WorkerID: fmt.Sprintf("worker-%d", i)}}
	}
	if a.plane != b.plane || a.session != b.session || otherSession == a.session || a.area == b.area || a.task == b.task || states[0].WorkflowID == states[1].WorkflowID {
		t.Fatal("fixture is not two independent Tasks in one Core")
	}
	ctx, sa, _ := workerHTTPClient(t, a)
	_, sb, _ := workerHTTPClient(t, b)
	call := func(i int, name string, args map[string]any) map[string]any {
		t.Helper()
		if i == 0 {
			return callUpperMCP(t, ctx, sa, name, args)
		}
		return callUpperMCP(t, ctx, sb, name, args)
	}
	reject := func(i int, name string, args map[string]any) {
		t.Helper()
		if i == 0 {
			wantFileWorkerMCPError(t, ctx, sa, name, args)
		} else {
			wantFileWorkerMCPError(t, ctx, sb, name, args)
		}
	}
	for i := range fixtures {
		got := call(i, "file_worker_claim", mcpObject(t, claims[i]))
		if got["result"].(map[string]any)["state"] != directfile.WorkerDispatchIntent {
			t.Fatalf("claim %d: %+v", i, got)
		}
	}
	// All managed members are observed, including untouched SDK members.
	paths := make([]string, 0, 4)
	for i, f := range fixtures {
		for _, target := range currents[i].RepositoryTargets {
			var root string
			if e := f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND repository_id=?`, states[i].WorkflowID, target.RepositoryID).Scan(&root); e != nil {
				t.Fatal(e)
			}
			paths = append(paths, filepath.Join(root, "same.txt"))
		}
	}
	if paths[0] == paths[2] || currents[0].RepositoryTargets[0].RepositoryID != currents[1].RepositoryTargets[0].RepositoryID {
		t.Fatal("expected shared source repository with distinct managed roots")
	}
	beforeTrees := []map[string]upperClientTree{upperClientTrees(t, a), upperClientTrees(t, b)}
	writes := make([]map[string]any, 2)
	results := make([]any, 2)
	for i, f := range fixtures {
		args := mcpObject(t, directInput(f, states[i], currents[i].RepositoryTargets[0]))
		before := call(i, "file_read", args)["result"].(map[string]any)
		args["action"], args["protocolVersion"], args["requestId"], args["expectedDigest"] = "REPLACE", float64(2), "same-write", before["digest"]
		args["content"], args["workerClaim"] = fmt.Sprintf("isolated Task %d\n", i), mcpObject(t, claims[i].WorkerClaimIdentity)
		writes[i] = args
		results[i] = call(i, "file_write", args)["result"]
	}
	if reflect.DeepEqual(results[0], results[1]) {
		t.Fatal("different Task mutations reused a result")
	}
	ledger := func() string {
		t.Helper()
		out := map[string][][]any{}
		for _, table := range []string{"direct_file_task_authorizations", "direct_file_task_members", "direct_file_worker_claims", "direct_file_operation_receipts", "direct_file_implementation_results"} {
			rows, e := a.plane.DB.SQL().Query("SELECT * FROM " + table + " ORDER BY rowid")
			if e != nil {
				t.Fatal(e)
			}
			cols, e := rows.Columns()
			if e != nil {
				t.Fatal(e)
			}
			for rows.Next() {
				v := make([]any, len(cols))
				p := make([]any, len(cols))
				for i := range v {
					p[i] = &v[i]
				}
				if e = rows.Scan(p...); e != nil {
					t.Fatal(e)
				}
				out[table] = append(out[table], v)
			}
			if e = rows.Err(); e != nil {
				t.Fatal(e)
			}
			rows.Close()
		}
		raw, e := json.Marshal(out)
		if e != nil {
			t.Fatal(e)
		}
		return string(raw)
	}
	baseline := ledger()
	snapshots := make([]tcpMutationSnapshot, len(paths))
	for i, p := range paths {
		snapshots[i], err = snapshotTCPMutation(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Fresh IDs cannot conceal an unauthorized attempt behind an existing receipt;
	// the same-request variants also exercise the completed replay lookup gate.
	for i := range fixtures {
		other := 1 - i
		for _, fresh := range []bool{false, true} {
			for _, kind := range []string{"worker", "member", "session"} {
				bad := mcpObject(t, writes[i])
				if fresh {
					bad["requestId"] = "forbidden-" + kind
					// A fresh attempt must otherwise be valid: the original pre-write
					// digest is stale and would mask a missing identity fence.
					bad["expectedDigest"] = results[i].(map[string]any)["digest"]
					bad["content"] = "unauthorized overwrite\n"
				}
				switch kind {
				case "worker":
					bad["workerClaim"] = mcpObject(t, claims[other].WorkerClaimIdentity)
				case "member":
					bad["memberId"] = currents[other].RepositoryTargets[0].MemberID
				case "session":
					bad["aiSessionId"] = otherSession
				}
				reject(i, "file_write", bad)
				if ledger() != baseline {
					t.Fatalf("%s fresh=%t changed durable state", kind, fresh)
				}
			}
		}
	}
	for i := range fixtures {
		bad := mcpObject(t, claims[i])
		bad["taskId"] = fixtures[1-i].task
		reject(i, "file_worker_get", bad)
		replay := call(i, "file_write", writes[i])["result"]
		if !reflect.DeepEqual(results[i], replay) {
			t.Fatalf("Task %d exact replay changed result", i)
		}
		var count int
		if e := a.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id='same-write' AND state='COMPLETED'`, states[i].WorkflowID).Scan(&count); e != nil || count != 1 {
			t.Fatalf("Task %d receipt count=%d err=%v", i, count, e)
		}
	}
	if ledger() != baseline {
		t.Fatal("replay or rejected reads changed durable state")
	}
	for i, p := range paths {
		after, e := snapshotTCPMutation(p)
		if e != nil || !sameTCPMutation(snapshots[i], after) {
			t.Fatalf("negative calls/replay changed physical member %d: %v", i, e)
		}
	}
	for i := range fixtures {
		raw, e := os.ReadFile(paths[i*2])
		if e != nil || string(raw) != fmt.Sprintf("isolated Task %d\n", i) {
			t.Fatalf("Task %d wrong bytes: %q %v", i, raw, e)
		}
	}
	for i, f := range fixtures {
		for key, now := range upperClientTrees(t, f) {
			old := beforeTrees[i][key]
			if (key != "managed:"+currents[i].RepositoryTargets[0].MemberID && now != old) || now.HEAD != old.HEAD {
				t.Fatalf("source, untouched member or HEAD changed: Task %d %s", i, key)
			}
		}
	}
	for table, want := range map[string]int{"direct_file_worker_claims": 2, "direct_file_operation_receipts": 2, "direct_file_implementation_results": 0, "direct_file_implementation_acceptances": 0, "execution_proposals": 0} {
		var got int
		if e := a.plane.DB.SQL().QueryRow("SELECT count(*) FROM " + table).Scan(&got); e != nil || got != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, got, want, e)
		}
	}
	t.Log("one Core, same business Session, two FeatureAreas/Tasks, same repositories: same-write has two distinct workflow receipts; 12 mixed-identity writes rejected; exact replay preserves both physical effects")
}
