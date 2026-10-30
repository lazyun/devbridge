package localmcp_test

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
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
func TestWebObservabilityCandidateRuntimeAcceptance(t *testing.T) {
	bin := os.Getenv("DEVBRIDGE_WEB_OBSERVABILITY_BIN")
	if bin == "" {
		t.Skip("candidate binary directory not supplied")
	}
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown=%v", unknown), func(t *testing.T) { webObservabilityCandidate(t, bin, unknown) })
	}
}

func webObservabilityCandidate(t *testing.T, bin string, unknown bool) {
	f := newIntegrationFixture(t)
	root := f.plane.DataPaths.Root
	backendDir := t.TempDir()
	backendPath := filepath.Join(backendDir, "codex-fixture")
	head, e := f.plane.Repositories.CurrentHEAD(f.ctx, f.repositoryID)
	if e != nil {
		t.Fatal(e)
	}
	source := `#!/usr/bin/env python3
import sys,json,uuid,time,os,threading
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
  thread=p.get('threadId') or thread;emit({'id':id,'result':{'thread':{'id':thread,'name':'Official observability fixture'},'cwd':p.get('cwd','')}})
 elif method=='turn/start':
  turn='turn-'+uuid.uuid4().hex
  emit({'id':id,'result':{'turn':{'id':turn,'status':'inProgress'}}})
  emit({'method':'turn/started','params':{'threadId':thread,'turn':{'id':turn,'status':'inProgress'}}})
  time.sleep(0.6)
  props=(p.get('outputSchema') or {}).get('properties',{})
  if 'workSpec' in props:message=json.dumps(proposal)
  else:
   steps=[{'step':x,'status':'completed' if i<2 else 'inProgress' if i==2 else 'pending'} for i,x in enumerate(['Inspect','Implement','Test','Review','Finish'])]
   emit({'method':'turn/plan/updated','params':{'threadId':thread,'turnId':turn,'plan':steps}})
   emit({'method':'item/started','params':{'threadId':thread,'turnId':turn,'item':{'id':'command','type':'commandExecution','status':'inProgress','command':'fixture PID '+str(os.getpid()),'cwd':os.getcwd(),'commandActions':[]}}})
   def permission():
    while not os.path.exists(os.environ['DEVBRIDGE_OBSERVABILITY_SIGNAL']):time.sleep(.02)
    emit({'id':'permission','method':'item/permissions/requestApproval','params':{'threadId':thread,'turnId':turn,'itemId':'command','cwd':os.getcwd(),'startedAtMs':int(time.time()*1000),'permissions':{'fileSystem':{'read':[os.getcwd()]}}}})
   threading.Thread(target=permission,daemon=True).start()
   continue
  counts={'inputTokens':584383,'cachedInputTokens':474624,'outputTokens':3111,'reasoningOutputTokens':1166,'totalTokens':587494}
  emit({'method':'thread/tokenUsage/updated','params':{'threadId':thread,'turnId':turn,'tokenUsage':{'total':counts,'last':counts,'modelContextWindow':258400}}})
  item={'id':'message-'+turn,'type':'agentMessage','text':message,'phase':'final_answer'}
  emit({'method':'item/completed','params':{'threadId':thread,'turnId':turn,'item':item}})
  emit({'method':'turn/completed','params':{'threadId':thread,'turn':{'id':turn,'status':'completed','items':[item]}}})
 elif method=='turn/interrupt':
  if os.environ['DEVBRIDGE_OBSERVABILITY_UNKNOWN']=='true':
   emit({'id':id,'error':{'code':-32000,'message':'fixture interrupt transport lost'}});sys.exit(2)
  emit({'id':id,'result':{}})
  emit({'method':'turn/completed','params':{'threadId':thread,'turn':{'id':turn,'status':'interrupted','items':[]}}})
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
		cmd.Env = append(os.Environ(), "DEVBRIDGE_OBSERVABILITY_UNKNOWN="+fmt.Sprint(unknown), "DEVBRIDGE_APPROVAL_FIXTURE_PROPOSAL="+reviewApprovalProposal(t), "DEVBRIDGE_APPROVAL_FIXTURE_BASE="+head, "DEVBRIDGE_OBSERVABILITY_SIGNAL="+filepath.Join(backendDir, "permission"))
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
	readCard := func() map[string]any {
		return callToolMap(t, f.ctx, gpt, "dev_bridge_card_get", map[string]any{"aiSessionId": sid, "featureAreaId": area})
	}
	reviewActive := readCard()

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
	reviewTerminal := readCard()
	approvalFact := pending["reviewApproval"].(map[string]any)
	wakeIdentity := map[string]any{"aiSessionId": sid, "projectId": f.projectID, "featureAreaId": area, "reviewId": pending["reviewId"], "decisionId": pending["decisionId"], "resultSha256": approvalFact["resultSha256"], "provenanceSha256": approvalFact["provenanceSha256"]}
	for _, want := range []string{"CLAIMED", "ALREADY_ATTEMPTED"} {
		claimed := callToolMap(t, f.ctx, gpt, "dev_bridge_review_wakeup_claim", wakeIdentity)
		if claimed["status"] != want {
			t.Fatalf("presentation wake claim=%v want=%s", claimed, want)
		}
	}
	exactReview := callToolMap(t, f.ctx, gpt, "review_get", map[string]any{"aiSessionId": sid, "reviewId": pending["reviewId"]})
	if exactReview["review"].(map[string]any)["id"] != pending["reviewId"] || exactReview["result"] == nil {
		t.Fatalf("exact durable Review read=%v", exactReview)
	}
	if current := waitState("REVIEW_APPROVAL_REQUIRED"); current["decisionId"] != pending["decisionId"] {
		t.Fatal("wake claim advanced Approval")
	}
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
	taskID := web["taskId"].(string)
	// The client goes idle: no GPT/Local/Web request follows APPROVE until the
	// candidate runtime itself has launched the first Implementation Run.
	idleDeadline := time.Now().Add(10 * time.Second)
	for {
		var running int
		e = read.QueryRow(`SELECT count(*) FROM execution_proposals p JOIN runs r ON r.id=p.run_id JOIN review_approval_materializations m ON m.task_id=p.task_id WHERE p.task_id=? AND p.task_definition_revision_id=m.initial_revision_id AND p.ai_session_id=m.ai_session_id AND r.purpose='IMPLEMENTATION' AND r.status='RUNNING'`, taskID).Scan(&running)
		if e != nil {
			t.Fatal(e)
		}
		if running == 1 {
			break
		}
		if time.Now().After(idleDeadline) {
			t.Fatal("idle client: Bridge did not start Implementation")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Log("NO_GPT_APPROVAL_TO_IMPLEMENTATION_PASS")
	implementationActive := readCard()
	replay := callToolMap(t, f.ctx, gpt, "review_approval_respond", request)
	if replay["taskId"] != taskID || replay["replay"] != true {
		t.Fatalf("cross-surface receipt=%+v", replay)
	}
	ready := waitState("IMPLEMENTATION_RUNNING")
	implementationObserved := readCard()
	snapshots, _ := json.Marshal(map[string]any{"approval": reviewTerminal, "running": implementationActive, "terminal": implementationObserved, "session": sid, "transitions": []any{[]any{reviewActive, reviewTerminal}}})
	snapshotPath := filepath.Join(t.TempDir(), "card-snapshots.json")
	if err := os.WriteFile(snapshotPath, snapshots, 0600); err != nil {
		t.Fatal(err)
	}
	dom := exec.Command("node", "--test", "../mcpserver/dev_bridge_candidate_test.js")
	dom.Env = append(os.Environ(), "DEVBRIDGE_CARD_SNAPSHOTS="+snapshotPath)
	if output, err := dom.CombinedOutput(); err != nil {
		t.Fatalf("candidate mounted Card: %v\n%s", err, output)
	}
	t.Log("REVIEW_AND_IMPLEMENTATION_CARD_LIVE_UPDATE_PASS; MOUNTED_CARD_REVIEW_WAKEUP_FIXTURE_PASS; no Approval mutation by wake; REAL_CHATGPT_WAKEUP_NOT_RUN")

	// Console rows use the same Core state, with no legacy confirmation label.
	response, e = http.Get(core + "/ui/api/project-workspace?project_id=" + f.projectID)
	if e != nil {
		t.Fatal(e)
	}
	var workspace map[string]any
	_ = json.NewDecoder(response.Body).Decode(&workspace)
	response.Body.Close()
	var reviewUsage map[string]any
	for _, item := range workspace["featureAreas"].([]any) {
		for _, raw := range item.(map[string]any)["reviews"].([]any) {
			row := raw.(map[string]any)
			if row["id"] == pending["reviewId"] {
				reviewUsage = row["usage"].(map[string]any)
			}
		}
	}
	if reviewUsage == nil || reviewUsage["status"] != "AVAILABLE" || reviewUsage["usage"].(map[string]any)["total_tokens"] != float64(587494) {
		t.Fatalf("candidate Review token: %+v", reviewUsage)
	}
	reviewMatched := false
	for _, item := range workspace["tasks"].([]any) {
		row := item.(map[string]any)
		work := row["work"].(map[string]any)
		if work["type"] == "REVIEW" && work["ownerId"] == pending["reviewId"] {
			latest := row["latest_run"].(map[string]any)
			a, _ := json.Marshal(latest["usage"])
			b, _ := json.Marshal(reviewUsage)
			if latest["id"] != work["runId"] || work["tokenAvailability"] != "AVAILABLE" || !bytes.Equal(a, b) {
				t.Fatalf("candidate Review/Task token mismatch: %+v", row)
			}
			reviewMatched = true
		}
	}
	if !reviewMatched {
		t.Fatal("exact Review Task missing")
	}
	t.Log("REVIEW_TOKEN_EXACT_RUN_PARITY_PASS total=587494 context=258400")
	found := false
	for _, item := range workspace["tasks"].([]any) {
		row := item.(map[string]any)
		if row["id"] == taskID {
			found = true
			if row["state"] != "IMPLEMENTATION_RUNNING" {
				t.Fatalf("Console second gate=%+v", row)
			}
		}
	}
	if !found {
		t.Fatal("approved Task absent from Console")
	}
	active := waitState("IMPLEMENTATION_RUNNING")
	executionID := ready["executionId"].(string)
	runID := active["runId"].(string)
	get := func(path string) map[string]any {
		t.Helper()
		response, e := http.Get(core + path)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		var value map[string]any
		if e = json.NewDecoder(response.Body).Decode(&value); e != nil || response.StatusCode != 200 {
			t.Fatalf("GET %s: %d %v %+v", path, response.StatusCode, e, value)
		}
		return value
	}
	var telemetry map[string]any
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		telemetry = callToolMap(t, f.ctx, gpt, "agent_runtime_state", map[string]any{"aiSessionId": sid, "runId": runID})
		if telemetry["latestPlan"] != nil && telemetry["currentItem"] != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	plan := telemetry["latestPlan"].(map[string]any)
	agent := telemetry["agent"].(map[string]any)
	if plan["completed"] != float64(2) || plan["total"] != float64(5) || agent["sessionName"] != "Official observability fixture" || agent["processLaunchId"] == "" {
		t.Fatalf("telemetry=%+v", telemetry)
	}
	pid := int(agent["pid"].(float64))
	command := telemetry["currentItem"].(map[string]any)["metadata"].(map[string]any)["command"].(string)
	if command != fmt.Sprintf("fixture PID %d", pid) {
		t.Fatalf("PID is not actual fixture: %s %+v", command, agent)
	}
	tasks := get("/ui/api/work-tabs/tasks?project_id=" + f.projectID)
	executions := get("/ui/api/work-tabs/executions?project_id=" + f.projectID)
	work := callToolMap(t, f.ctx, local, "task_work_list", map[string]any{"aiSessionId": sid})
	work["items"] = work["tasks"]
	contains := func(values map[string]any, id string) map[string]any {
		t.Helper()
		for _, v := range values["items"].([]any) {
			row := v.(map[string]any)
			if row["id"] == id {
				return row
			}
		}
		t.Fatalf("inventory missing %s", id)
		return nil
	}
	taskRow := contains(tasks, taskID)
	contains(work, taskID)
	contains(work, pending["reviewApproval"].(map[string]any)["reviewTaskId"].(string))
	branches := taskRow["development_branches"].([]any)
	if len(branches) != 1 || branches[0] != "refs/heads/main" || taskRow["work"].(map[string]any)["phase"] != "IMPLEMENTATION" {
		t.Fatalf("active exact branch Task %+v", taskRow)
	}
	execRow := contains(executions, executionID)
	if execRow["task_id"] != taskID || execRow["run_id"] != runID || execRow["cancellation_capability"].(map[string]any)["available"] != true {
		t.Fatalf("execution target %+v", execRow)
	}
	taskDetail := get("/ui/api/tasks/" + taskID + "?project_id=" + f.projectID)
	if taskDetail["id"] != taskID {
		t.Fatal("parent task changed")
	}
	card := callToolMap(t, f.ctx, gpt, "feature_area_presentation_get", map[string]any{"aiSessionId": sid, "featureAreaId": area})
	details := card["details"].(map[string]any)
	guidance := card["workflowGuidance"].(map[string]any)
	if card["currentState"] != "IMPLEMENTATION_RUNNING" || card["generation"] != guidance["generation"] || details["taskId"] != taskID || details["runId"] != runID || details["reviewId"] != pending["reviewId"] || card["sourceSequence"] != guidance["sourceSequence"] {
		t.Fatalf("mixed projection %+v", card)
	}
	webWorkspace := get("/ui/api/project-workspace?project_id=" + f.projectID)
	var areaRow map[string]any
	for _, value := range webWorkspace["featureAreas"].([]any) {
		row := value.(map[string]any)
		if row["id"] == area {
			areaRow = row
			break
		}
	}
	if areaRow == nil {
		t.Fatal("Web FeatureArea missing")
	}
	presentation := areaRow["presentation"].(map[string]any)
	operation := presentation["agentOperation"].(map[string]any)
	projectedDetails := presentation["details"].(map[string]any)
	if areaRow["state"] != presentation["currentState"] || areaRow["agent_operation_state"] != operation["status"] || areaRow["agent_conversation_id"] != operation["conversationId"] || areaRow["agent_operation_run_id"] != runID || projectedDetails["taskId"] != taskID || projectedDetails["runId"] != runID || presentation["generation"] != areaRow["workflowGuidance"].(map[string]any)["generation"] {
		t.Fatalf("Web mixed generations %+v", areaRow)
	}
	progress := card["progress"].([]any)
	if progress[0].(map[string]any)["status"] != "COMPLETE" || progress[1].(map[string]any)["status"] != "COMPLETE" {
		t.Fatalf("approved lineage progress %+v", progress)
	}
	// Controlled aging only of telemetry in this disposable candidate DataRoot.
	// No workflow/Run/claim table is modified; the real fixture remains connected.
	telemetryDB, e := sql.Open("sqlite", "file:"+filepath.Join(root, "state", "devbridge.db")+"?_pragma=busy_timeout(5000)")
	if e != nil {
		t.Fatal(e)
	}
	defer telemetryDB.Close()
	age := func() {
		t.Helper()
		_, e := telemetryDB.Exec(`UPDATE agent_runtime_states SET snapshot_json=json_set(snapshot_json,'$.lastActivityAt',?) WHERE run_id=?`, time.Now().Add(-43*time.Minute).UTC().Format(time.RFC3339Nano), runID)
		if e != nil {
			t.Fatal(e)
		}
	}
	age()
	stalled := callToolMap(t, f.ctx, gpt, "agent_runtime_state", map[string]any{"aiSessionId": sid, "runId": runID})
	if stalled["agentHealth"] != "POSSIBLY_STALLED" {
		t.Fatalf("stalled %+v", stalled)
	}
	var status string
	if e = read.QueryRow(`SELECT status FROM runs WHERE id=?`, runID).Scan(&status); e != nil || status != "RUNNING" {
		t.Fatalf("health mutated Run %s %v", status, e)
	}
	if e = os.WriteFile(filepath.Join(backendDir, "permission"), []byte("request"), 0600); e != nil {
		t.Fatal(e)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		telemetry = callToolMap(t, f.ctx, gpt, "agent_runtime_state", map[string]any{"aiSessionId": sid, "runId": runID})
		if telemetry["agentHealth"] == "WAITING" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	age()
	waiting := callToolMap(t, f.ctx, local, "agent_runtime_state", map[string]any{"aiSessionId": sid, "runId": runID})
	if waiting["agentHealth"] != "WAITING" || waiting["waitingState"] != "WAITING_FOR_PERMISSION" {
		t.Fatalf("waiting precedence %+v", waiting)
	}
	if marker := os.Getenv("DEVBRIDGE_WEB_OBSERVABILITY_UI_MARKER"); marker != "" && !unknown {
		info, _ := json.Marshal(map[string]string{"url": core + "/ui/", "projectId": f.projectID, "taskId": taskID, "executionId": executionID})
		if err := os.WriteFile(marker, info, 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(marker)
		until := time.Now().Add(2 * time.Minute)
		for time.Now().Before(until) {
			if _, err := os.Stat(marker + ".continue"); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	cancelBody, _ := json.Marshal(map[string]any{"runId": runID, "requestId": "web-observability-terminate"})
	response, e = http.Post(core+"/ui/api/executions/"+executionID+"/cancel?project_id="+f.projectID, "application/json", bytes.NewReader(cancelBody))
	if e != nil {
		t.Fatal(e)
	}
	var receipt map[string]any
	_ = json.NewDecoder(response.Body).Decode(&receipt)
	response.Body.Close()
	if response.StatusCode != 200 || receipt["runId"] != runID || receipt["executionId"] != executionID {
		t.Fatalf("canonical Web cancel %d %+v", response.StatusCode, receipt)
	}
	waitState("CANCELED")
	final := contains(get("/ui/api/work-tabs/executions?project_id="+f.projectID), executionID)
	wantTerminal := "CANCELED"
	if unknown {
		wantTerminal = "UNKNOWN_ORPHANED"
	}
	if final["state"] != wantTerminal || final["cancellation_capability"].(map[string]any)["available"] == true {
		t.Fatalf("terminal capability %+v", final)
	}
	t.Logf("WEB_OBSERVABILITY_CANDIDATE_PASS task=%s execution=%s run=%s PID=%d launch=%s plan=2/5 stalled=POSSIBLY_STALLED waiting=WAITING_FOR_PERMISSION terminal=%s generation=%s; real Core/GPT/Local processes, no paid model", taskID, executionID, runID, pid, agent["processLaunchId"], final["state"], card["generation"])
}
