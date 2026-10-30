package localmcp_test

import (
	"bufio"
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

// Opt-in packaged acceptance: only candidate binaries supplied by the caller,
// a temporary DataRoot/repository and ephemeral ports. No installed Codex runs.
func TestUnifiedCandidateRuntimeAcceptance(t *testing.T) {
	runCandidateWorkAcceptance(t, os.Getenv("DEVBRIDGE_UNIFIED_CANDIDATE_BIN"), false)
}
func TestRetirementCandidateRuntimeAcceptance(t *testing.T) {
	runCandidateWorkAcceptance(t, os.Getenv("DEVBRIDGE_RETIRE_CANDIDATE_BIN"), true)
}
func runCandidateWorkAcceptance(t *testing.T, bin string, retirement bool) {
	if bin == "" {
		t.Skip("candidate binary directory not supplied")
	}
	f := newIntegrationFixture(t)
	root := f.plane.DataPaths.Root
	fixture := filepath.Join(t.TempDir(), "app-server-fixture")
	mode := filepath.Join(filepath.Dir(fixture), "mode")
	source := `#!/usr/bin/env python3
import sys,json,uuid,os,time
if '--version' in sys.argv:
 print('codex-cli 0.149.0');sys.exit(0)
thread='fixture-thread-'+uuid.uuid4().hex
turn='fixture-turn-'+uuid.uuid4().hex
def emit(v):
 print(json.dumps(v),flush=True)
for line in sys.stdin:
 q=json.loads(line);method=q.get('method');id=q.get('id')
 if method=='initialize':emit({'id':id,'result':{'userAgent':'isolated unified fixture'}})
 elif method=='initialized':pass
 elif method in ('thread/start','thread/resume'):
  thread=q.get('params',{}).get('threadId') or thread
  emit({'id':id,'result':{'thread':{'id':thread},'cwd':q.get('params',{}).get('cwd','')}})
 elif method=='turn/start':
  emit({'id':id,'result':{'turn':{'id':turn,'status':'inProgress'}}})
  emit({'method':'turn/started','params':{'threadId':thread,'turn':{'id':turn,'status':'inProgress'}}})
  if 'workSpec' in (q.get('params',{}).get('outputSchema') or {}).get('properties',{}) and open(os.environ['DEVBRIDGE_UNIFIED_FIXTURE_MODE']).read()=='approve':
   item={'id':'message','type':'agentMessage','text':os.environ['DEVBRIDGE_APPROVAL_FIXTURE_PROPOSAL'],'phase':'final_answer'}
   emit({'method':'item/completed','params':{'threadId':thread,'turnId':turn,'item':item}})
   emit({'method':'turn/completed','params':{'threadId':thread,'turn':{'id':turn,'status':'completed','items':[item]}}})
 elif method=='turn/interrupt':
  time.sleep(0.25)
  if open(os.environ['DEVBRIDGE_UNIFIED_FIXTURE_MODE']).read()=='unknown':sys.exit(0)
  emit({'id':id,'result':{}})
  emit({'method':'turn/completed','params':{'threadId':thread,'turn':{'id':turn,'status':'interrupted'}}})
 elif id is not None:emit({'id':id,'result':{}})
`
	if err := os.WriteFile(fixture, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	// Seed only the disposable fixture before the candidate opens it.
	if _, err := f.plane.DB.SQL().Exec(`UPDATE codex_configs SET codex_path=? WHERE id='codex_config_default'`, fixture); err != nil {
		t.Fatal(err)
	}
	if err := f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	launch := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(filepath.Join(bin, name), args...)
		cmd.Env = append(os.Environ(), "DEVBRIDGE_UNIFIED_FIXTURE_MODE="+mode, "DEVBRIDGE_APPROVAL_FIXTURE_PROPOSAL="+reviewApprovalProposal(t))
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := os.Create(filepath.Join(filepath.Dir(fixture), name+".stderr"))
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = stderr
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		ready := make(chan string, 1)
		done := make(chan error, 1)
		go func() {
			scanner := bufio.NewScanner(pipe)
			pattern := regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+`)
			for scanner.Scan() {
				if u := pattern.FindString(scanner.Text()); u != "" {
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
			t.Fatalf("candidate %s did not become ready", name)
			return ""
		}
	}
	core := launch("devbridge", "-mode", "server", "-data-root", root, "-listen", "127.0.0.1:0", "-allowed-models", "gpt-5.6-terra", "-allowed-reasoning-efforts", "high")
	gptURL := launch("devbridge-mcp", "-data-root", root, "-core-url", core, "-listen", "127.0.0.1:0")
	localURL := launch("devbridge-local-mcp", "-data-root", root, "-core-url", core, "-listen", "127.0.0.1:0")
	gpt, local := connectGPT(t, gptURL), connectLocalMCP(t, localURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	read, err := sql.Open("sqlite", "file:"+filepath.Join(root, "state", "devbridge.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	waitGuidance := func(want string, area string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			g := callToolMap(t, f.ctx, gpt, "workflow_guidance_get", map[string]any{"aiSessionId": sid, "featureAreaId": area})
			if g["currentState"] == want {
				return g
			}
			if time.Now().After(deadline) {
				t.Fatalf("want %s got %+v", want, g)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if retirement {
		area := callToolMap(t, f.ctx, gpt, "featureArea_create", map[string]any{"aiSessionId": sid, "title": "Idle discovery closure", "problemStatement": "Retire DISCOVERING once"})["id"].(string)
		out := callToolMap(t, f.ctx, gpt, "featureArea_retire", map[string]any{"aiSessionId": sid, "featureAreaId": area, "requestId": "idle-retire"})
		if out["status"] != "RETIRING" {
			t.Fatalf("idle intent=%+v", out)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			var status string
			if err = read.QueryRow(`SELECT status FROM feature_areas WHERE id=?`, area).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status == "RETIRED" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("idle retirement stalled")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	for _, kind := range []string{"REVIEW", "IMPLEMENTATION"} {
		for _, unknown := range []bool{false, true} {
			value := "known"
			if unknown {
				value = "unknown"
			}
			if err = os.WriteFile(mode, []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
			area := callToolMap(t, f.ctx, gpt, "featureArea_create", map[string]any{"aiSessionId": sid, "title": kind + " " + value, "problemStatement": "Isolated packaged cancellation acceptance"})["id"].(string)
			var owner, taskID, runID string
			if kind == "REVIEW" {
				out := callToolMap(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sid, area))
				owner = out["review"].(map[string]any)["id"].(string)
				if err = read.QueryRow(`SELECT task_id,linked_run_id FROM reviews WHERE id=?`, owner).Scan(&taskID, &runID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = os.WriteFile(mode, []byte("approve"), 0600); err != nil {
					t.Fatal(err)
				}
				callToolMap(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sid, area))
				pending := waitGuidance("REVIEW_APPROVAL_REQUIRED", area)
				if err = os.WriteFile(mode, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
				out := callToolMap(t, f.ctx, gpt, "review_approval_respond", map[string]any{"aiSessionId": sid, "featureAreaId": area, "reviewId": pending["reviewId"], "decisionId": pending["decisionId"], "option": "APPROVE", "requestId": "approve-" + area})
				taskID = out["taskId"].(string)
				active := waitGuidance("IMPLEMENTATION_RUNNING", area)
				owner, runID = active["executionId"].(string), active["runId"].(string)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				var status, turn string
				if err = read.QueryRow(`SELECT status,COALESCE(turn_id,'') FROM runs WHERE id=?`, runID).Scan(&status, &turn); err != nil {
					t.Fatal(err)
				}
				if status == "RUNNING" && turn != "" {
					break
				}
				if time.Now().After(deadline) || strings.Contains(status, "FAILED") {
					var code, detail string
					_ = read.QueryRow(`SELECT COALESCE(failure_code,''),COALESCE(failure_message,'') FROM runs WHERE id=?`, runID).Scan(&code, &detail)
					t.Fatalf("candidate launch=%s turn=%s failure=%s %s", status, turn, code, detail)
				}
				time.Sleep(10 * time.Millisecond)
			}
			inventory := callToolMap(t, f.ctx, local, "task_work_list", map[string]any{"aiSessionId": sid})
			found := false
			for _, item := range inventory["tasks"].([]any) {
				v := item.(map[string]any)
				if v["id"] == taskID {
					work := v["work"].(map[string]any)
					found = work["runId"] == runID && work["phase"] == kind && work["status"] == "RUNNING"
				}
			}
			if !found {
				t.Fatalf("packaged Task not immediately visible: %+v", inventory)
			}
			ownerType := kind
			if kind == "IMPLEMENTATION" {
				ownerType = "EXECUTION"
			}
			input := map[string]any{"aiSessionId": sid, "ownerType": ownerType, "ownerId": owner, "runId": runID, "requestId": "cancel-" + owner}
			var receipt map[string]any
			if retirement {
				out := callToolMap(t, f.ctx, gpt, "featureArea_retire", map[string]any{"aiSessionId": sid, "featureAreaId": area, "requestId": "retire-" + area})
				if out["status"] != "RETIRING" {
					t.Fatalf("retirement intent=%+v", out)
				}
				response, e := http.Get(core + "/ui/api/project-workspace?project_id=" + f.projectID)
				if e != nil {
					t.Fatal(e)
				}
				var view map[string]any
				e = json.NewDecoder(response.Body).Decode(&view)
				response.Body.Close()
				if e != nil {
					t.Fatal(e)
				}
				visible := false
				for _, item := range view["featureAreas"].([]any) {
					v := item.(map[string]any)
					if v["id"] == area {
						visible = v["state"] == "FEATURE_AREA_RETIRING" && v["retirement"] != nil && v["can_retire"] == false
					}
				}
				if !visible {
					t.Fatalf("Web closing projection missing: %+v", view)
				}
				if text := callToolErrorText(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sid, area)); text == "" {
					t.Fatal("new Review admitted after retire")
				}
				deadline = time.Now().Add(10 * time.Second)
				for {
					var status string
					if err = read.QueryRow(`SELECT status FROM feature_areas WHERE id=?`, area).Scan(&status); err != nil {
						t.Fatal(err)
					}
					if status == "RETIRED" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("candidate retirement stalled: %s", status)
					}
					time.Sleep(10 * time.Millisecond)
				}
				var disposition, quiescence string
				if err = read.QueryRow(`SELECT c.backend_disposition,CASE WHEN b.quiesced=1 AND b.advance_state<>'UNKNOWN' THEN 'QUIESCED' ELSE 'UNKNOWN' END FROM execution_cancellations c JOIN agent_operation_boundaries b ON b.run_id=c.run_id WHERE c.run_id=? ORDER BY c.requested_at DESC LIMIT 1`, runID).Scan(&disposition, &quiescence); err != nil {
					t.Fatal(err)
				}
				receipt = map[string]any{"backendDisposition": disposition, "quiescenceState": quiescence}
			} else {
				callToolMap(t, f.ctx, gpt, "operation_cancel", input)
				deadline = time.Now().Add(10 * time.Second)
				for {
					receipt = callToolMap(t, f.ctx, local, "operation_cancel", input)
					if receipt["state"] == "CANCELED" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("candidate cancellation=%+v", receipt)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			want, quiescence := "TERMINATED", "QUIESCED"
			if unknown {
				want, quiescence = "UNKNOWN_ORPHANED", "UNKNOWN"
			}
			if receipt["backendDisposition"] != want || receipt["quiescenceState"] != quiescence {
				t.Fatalf("candidate boundary=%+v", receipt)
			}
			var taskStatus string
			var claims int
			if err = read.QueryRow(`SELECT status,(SELECT COUNT(*) FROM feature_area_agent_claims WHERE run_id=?) FROM tasks WHERE id=?`, runID, taskID).Scan(&taskStatus, &claims); err != nil || taskStatus != "CANCELED" || claims != 0 {
				t.Fatalf("candidate terminal=%s claim=%d %v", taskStatus, claims, err)
			}
			if unknown {
				var exact bool
				if err = read.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_orphan_dispositions o JOIN workspaces w ON w.id=o.workspace_id JOIN agent_conversations a ON a.id=o.conversation_id JOIN agent_operation_boundaries b ON b.run_id=o.run_id WHERE o.run_id=? AND w.runtime_quarantined=1 AND a.ended_at IS NOT NULL AND b.advance_state='UNKNOWN' AND b.quiesced=0)`, runID).Scan(&exact); err != nil || !exact {
					t.Fatalf("candidate orphan isolation=%v %v", exact, err)
				}
			}
			response, e := http.Get(core + "/ui/api/project-workspace?project_id=" + f.projectID)
			if e != nil {
				t.Fatal(e)
			}
			var web map[string]any
			e = json.NewDecoder(response.Body).Decode(&web)
			response.Body.Close()
			if e != nil || response.StatusCode != 200 {
				t.Fatalf("candidate Web inventory=%+v %v", web, e)
			}
			if retirement {
				found := false
				for _, item := range web["featureAreas"].([]any) {
					v := item.(map[string]any)
					found = found || (v["id"] == area && v["state"] == "RETIRED")
				}
				if !found {
					t.Fatalf("Web closed history missing: %+v", web)
				}
			}
			t.Logf("candidate %s %s: visible Task=%s Run=%s; %s; claim released", kind, value, taskID, runID, want)
		}
	}
	t.Log("REAL_CODEX_UNKNOWN_NOT_RUN; packaged protocol fixtures completed; late callback fencing covered by the production-supervisor adapter matrix")
}
