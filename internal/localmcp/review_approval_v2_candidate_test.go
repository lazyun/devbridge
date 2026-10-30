package localmcp_test

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Real candidate Core/GPT/Local processes and the real app-server transport.
// Only Codex is deterministic; the formal runtime and live SQLite are never used.
func TestReviewApprovalV2CandidateRuntimeAcceptance(t *testing.T) {
	bin := os.Getenv("DEVBRIDGE_REVIEW_APPROVAL_V2_BIN")
	if bin == "" {
		t.Skip("candidate binary directory not supplied")
	}
	f := newIntegrationFixture(t)
	root := f.plane.DataPaths.Root
	backendDir := t.TempDir()
	backendPath := filepath.Join(backendDir, "codex-fixture")
	head, e := f.plane.Repositories.CurrentHEAD(f.ctx, f.repositoryID)
	if e != nil {
		t.Fatal(e)
	}
	source := `#!/usr/bin/env python3
import sys,json,uuid,time,os
if '--version' in sys.argv:
 print('codex-cli 0.149.0');sys.exit(0)
thread='thread-'+uuid.uuid4().hex
proposal=json.loads(os.environ['DEVBRIDGE_APPROVAL_FIXTURE_PROPOSAL'])
def emit(v):print(json.dumps(v),flush=True)
for line in sys.stdin:
 q=json.loads(line);method=q.get('method');id=q.get('id');p=q.get('params',{})
 if method=='initialize':emit({'id':id,'result':{'userAgent':'Review Approval v2 deterministic fixture'}})
 elif method=='initialized':pass
 elif method in ('thread/start','thread/resume'):
  thread=p.get('threadId') or thread;emit({'id':id,'result':{'thread':{'id':thread},'cwd':p.get('cwd','')}})
 elif method=='turn/start':
  turn='turn-'+uuid.uuid4().hex
  emit({'id':id,'result':{'turn':{'id':turn,'status':'inProgress'}}})
  emit({'method':'turn/started','params':{'threadId':thread,'turn':{'id':turn,'status':'inProgress'}}})
  time.sleep(0.6)
  props=(p.get('outputSchema') or {}).get('properties',{})
  if 'workSpec' in props:message=json.dumps(proposal)
  else:message='Deterministic fixture completed its approved implementation or verification turn.'
  item={'id':'message-'+turn,'type':'agentMessage','text':message,'phase':'final_answer'}
  emit({'method':'item/completed','params':{'threadId':thread,'turnId':turn,'item':item}})
  emit({'method':'turn/completed','params':{'threadId':thread,'turn':{'id':turn,'status':'completed','items':[item]}}})
 elif method=='turn/interrupt':emit({'id':id,'result':{}})
 elif id is not None:emit({'id':id,'result':{}})
`
	if e = os.WriteFile(backendPath, []byte(source), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = f.plane.DB.SQL().Exec(`UPDATE codex_configs SET codex_path=? WHERE id='codex_config_default'`, backendPath); e != nil {
		t.Fatal(e)
	}
	if e = f.plane.Close(); e != nil {
		t.Fatal(e)
	}
	launch := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(filepath.Join(bin, name), args...)
		cmd.Env = append(os.Environ(), "DEVBRIDGE_APPROVAL_FIXTURE_PROPOSAL="+reviewApprovalProposal(t), "DEVBRIDGE_APPROVAL_FIXTURE_BASE="+head)
		pipe, e := cmd.StdoutPipe()
		if e != nil {
			t.Fatal(e)
		}
		stderr, e := os.Create(filepath.Join(backendDir, name+".stderr"))
		if e != nil {
			t.Fatal(e)
		}
		cmd.Stderr = stderr
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		ready := make(chan string, 1)
		done := make(chan error, 1)
		go func() {
			scan := bufio.NewScanner(pipe)
			pattern := regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+`)
			for scan.Scan() {
				if u := pattern.FindString(scan.Text()); u != "" {
					select {
					case ready <- u:
					default:
					}
				}
			}
		}()
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() {
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			_ = stderr.Close()
		})
		select {
		case u := <-ready:
			return u
		case <-time.After(15 * time.Second):
			b, _ := os.ReadFile(filepath.Join(backendDir, name+".stderr"))
			t.Fatalf("candidate %s did not start: %s", name, b)
			return ""
		}
	}
	core := launch("devbridge", "-mode", "server", "-data-root", root, "-listen", "127.0.0.1:0", "-allowed-models", "gpt-5.6-terra", "-allowed-reasoning-efforts", "high")
	gptURL := launch("devbridge-mcp", "-data-root", root, "-core-url", core, "-listen", "127.0.0.1:0")
	localURL := launch("devbridge-local-mcp", "-data-root", root, "-core-url", core, "-listen", "127.0.0.1:0")
	gpt, local := connectGPT(t, gptURL), connectLocalMCP(t, localURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	created := callToolMap(t, f.ctx, gpt, "featureArea_create", map[string]any{"aiSessionId": sid, "title": "Hot-reload Codex runtime model configuration", "problemStatement": "Only future Runs should use new runtime model settings"})
	area := created["id"].(string)
	if created["currentState"] != "REVIEW_REQUIRED" {
		t.Fatalf("creation guidance=%+v", created)
	}
	if rejection := callToolErrorText(t, f.ctx, gpt, "task_create", validOwnershipTaskArgs(sid, area)); !strings.Contains(rejection, "START_FEATURE_AREA_REVIEW") {
		t.Fatal(rejection)
	}
	callToolMap(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sid, area))
	waitState := func(want string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			g := callToolMap(t, f.ctx, local, "workflow_guidance_get", map[string]any{"aiSessionId": sid, "featureAreaId": area})
			if g["currentState"] == want {
				return g
			}
			if time.Now().After(deadline) {
				db, _ := sql.Open("sqlite", "file:"+filepath.Join(root, "state", "devbridge.db")+"?mode=ro")
				var failure, detail string
				if db != nil {
					_ = db.QueryRow(`SELECT COALESCE(failure_code,''),COALESCE(failure_message,'') FROM execution_proposals WHERE id=?`, g["executionId"]).Scan(&failure, &detail)
					db.Close()
				}
				t.Fatalf("candidate want=%s got=%v failure=%s: %s", want, g["currentState"], failure, detail)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	pending := waitState("REVIEW_APPROVAL_REQUIRED")
	read, e := sql.Open("sqlite", "file:"+filepath.Join(root, "state", "devbridge.db")+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer read.Close()
	count := func(query string) int {
		t.Helper()
		var n int
		if e := read.QueryRow(query, area).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	if count(`SELECT count(*) FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT'`) != 0 {
		t.Fatal("candidate Task existed before Approval")
	}
	if count(`SELECT count(*) FROM tasks WHERE feature_area_id=? AND task_type='REVIEW'`) != 1 {
		t.Fatal("Review was not visible")
	}
	request := map[string]any{"aiSessionId": sid, "featureAreaId": area, "reviewId": pending["reviewId"], "decisionId": pending["decisionId"], "option": "APPROVE", "requestId": "candidate-user-intent"}
	// Web and GPT share exact receipt identity with Local, without new login.
	body, _ := json.Marshal(request)
	response, e := http.Post(core+"/ui/api/featureAreas/"+area+"/review-approval?project_id="+f.projectID, "application/json", bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	var web map[string]any
	_ = json.NewDecoder(response.Body).Decode(&web)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("Web Approval=%d %+v", response.StatusCode, web)
	}
	replay := callToolMap(t, f.ctx, gpt, "review_approval_respond", request)
	taskID := web["taskId"].(string)
	if replay["taskId"] != taskID || replay["replay"] != true {
		t.Fatalf("cross-surface receipt=%+v", replay)
	}
	ready := waitState("COMPLETE")
	// Console rows use the same Core state, with no legacy confirmation label.
	response, e = http.Get(core + "/ui/api/project-workspace?project_id=" + f.projectID)
	if e != nil {
		t.Fatal(e)
	}
	var workspace map[string]any
	_ = json.NewDecoder(response.Body).Decode(&workspace)
	response.Body.Close()
	found := false
	for _, item := range workspace["tasks"].([]any) {
		row := item.(map[string]any)
		if row["id"] == taskID {
			found = true
			if row["state"] != "COMPLETE" {
				t.Fatalf("Console second gate=%+v", row)
			}
		}
	}
	if !found {
		t.Fatal("approved Task absent from Console")
	}
	complete := waitState("COMPLETE")
	if count(`SELECT count(*) FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT'`) != 1 {
		t.Fatal("duplicate candidate Task")
	}
	var receipts int
	if e = read.QueryRow(`SELECT count(*) FROM decision_resolution_receipts WHERE project_id=?`, f.projectID).Scan(&receipts); e != nil || receipts != 1 {
		t.Fatalf("second receipt=%d err=%v", receipts, e)
	}
	replay = callToolMap(t, f.ctx, local, "review_approval_respond", request)
	if replay["currentState"] != "COMPLETE" || replay["taskId"] != taskID {
		t.Fatalf("stale candidate replay=%+v", replay)
	}
	// Exercise non-approval and retirement on the same real candidate processes.
	for _, option := range []string{"REJECT", "REVISE", "RETIRE", "APPROVE_THEN_RETIRE"} {
		area = callToolMap(t, f.ctx, gpt, "featureArea_create", map[string]any{"aiSessionId": sid, "title": "Review outcome " + option, "problemStatement": "Bounded model settings change"})["id"].(string)
		callToolMap(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sid, area))
		g := waitState("REVIEW_APPROVAL_REQUIRED")
		in := map[string]any{"aiSessionId": sid, "featureAreaId": area, "reviewId": g["reviewId"], "decisionId": g["decisionId"], "requestId": "candidate-" + option, "option": option}
		switch option {
		case "REJECT", "REVISE":
			resolved := callToolMap(t, f.ctx, gpt, "review_approval_respond", in)
			want := "WORKFLOW_REJECTED"
			if option == "REVISE" {
				want = "REVIEW_AGAIN"
			}
			if resolved["nextAction"].(map[string]any)["code"] != want || count(`SELECT count(*) FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT'`) != 0 {
				t.Fatalf("%s outcome=%+v", option, resolved)
			}
		default:
			in["option"] = "APPROVE"
			if option == "APPROVE_THEN_RETIRE" {
				callToolMap(t, f.ctx, gpt, "review_approval_respond", in)
			}
			callToolMap(t, f.ctx, local, "featureArea_retire", map[string]any{"aiSessionId": sid, "featureAreaId": area, "requestId": "candidate-retire-" + option})
			waitState("RETIRED")
			if rejection := callToolErrorText(t, f.ctx, gpt, "review_approval_respond", in); !strings.Contains(rejection, "WORKFLOW_CLOSED") {
				t.Fatal(rejection)
			}
			want := 0
			if option == "APPROVE_THEN_RETIRE" {
				want = 1
			}
			if count(`SELECT count(*) FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT'`) != want {
				t.Fatal("retirement revived or duplicated Task")
			}
		}
	}
	t.Logf("REAL_CODEX_NOT_RUN; real candidate Core/GPT/Local + Web Approval: Review=%v Task=%s Execution=%v %v", pending["reviewId"], taskID, ready["executionId"], complete["nextAction"])
}
