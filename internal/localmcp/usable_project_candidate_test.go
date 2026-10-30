package localmcp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexlaunch"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This opt-in gate uses only the current public HTTP/MCP APIs of three packaged
// processes. All Git repositories, configuration and durable state are disposable.
// The installed native App Server receives deterministic loopback Responses;
// implementation edits and verification commands execute in the native runtime.
// Platform authority comes only from the same packaged Core's real collector.
func TestUsableProjectCandidatePublicLifecycle(t *testing.T) {
	bin := os.Getenv("DEVBRIDGE_USABLE_PROJECT_BIN")
	if bin == "" {
		t.Skip("set DEVBRIDGE_USABLE_PROJECT_BIN to the three candidate binaries")
	}
	native := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if native == "" {
		t.Fatal("DEVBRIDGE_PLATFORM_NATIVE_BINARY must explicitly select the installed native Codex binary")
	}
	root := t.TempDir()
	if parent := os.Getenv("DEVBRIDGE_USABLE_PROJECT_EVIDENCE_DIR"); parent != "" {
		var err error
		root, err = os.MkdirTemp(parent, "public-lifecycle-")
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("isolated evidence: %s", root)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	selection := filepath.Join(root, "codex-selection.json")
	if err := codexlaunch.Write(selection, codexlaunch.Selection{Configured: native, Executable: native}); err != nil {
		t.Fatal(err)
	}
	wire := filepath.Join(root, "native-provider.jsonl")
	provider := usableNativeResponses(t, root, wire)
	nativeHome := filepath.Join(root, "codex-home")
	if err := os.MkdirAll(nativeHome, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("openai_base_url=%q\nmodel_provider=\"offline_gate\"\n[model_providers.offline_gate]\nname=\"Offline packaged acceptance\"\nbase_url=%q\nwire_api=\"responses\"\nrequires_openai_auth=false\nrequest_max_retries=0\nstream_max_retries=0\n[features]\nresponses_websockets=false\nresponses_websockets_v2=false\nplugins=false\nremote_plugin=false\n[analytics]\nenabled=false\n[feedback]\nenabled=false\n", provider.URL+"/v1", provider.URL+"/v1")
	if err := os.WriteFile(filepath.Join(nativeHome, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	env := []string{codexlaunch.SelectionEnv + "=" + selection, "DEVBRIDGE_CODEX_APP_SERVER_CONTINUITY=0", "CODEX_HOME=" + nativeHome, "HOME=" + nativeHome, "TMPDIR=" + root, "OPENAI_BASE_URL=" + provider.URL + "/v1", "OPENAI_API_KEY=", "CODEX_API_KEY=", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "OTEL_SDK_DISABLED=true"}
	data := filepath.Join(root, "data")
	launch := func() (string, *sdkmcp.ClientSession, *sdkmcp.ClientSession, func()) {
		core, stopCore := usableLaunch(t, bin, root, env, "devbridge", "-mode", "server", "-data-root", data, "-listen", "127.0.0.1:0", "-allowed-models", "gpt-5.6-terra", "-allowed-reasoning-efforts", "high")
		gptURL, stopGPT := usableLaunch(t, bin, root, env, "devbridge-mcp", "-data-root", data, "-core-url", core, "-listen", "127.0.0.1:0")
		localURL, stopLocal := usableLaunch(t, bin, root, env, "devbridge-local-mcp", "-data-root", data, "-core-url", core, "-listen", "127.0.0.1:0")
		gpt, local := usableConnect(t, ctx, gptURL), usableConnect(t, ctx, localURL)
		var once sync.Once
		stop := func() { once.Do(func() { _ = gpt.Close(); _ = local.Close(); stopLocal(); stopGPT(); stopCore() }) }
		t.Cleanup(stop)
		return core, gpt, local, stop
	}
	core, gpt, local, stop := launch()
	paths, heads, repos := map[string]string{}, map[string]string{}, map[string]string{}
	inputs := []map[string]any{}
	for _, name := range []string{"app", "sdk", "backend"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		usableGit(t, path, "init", "-b", name+"-development")
		usableGit(t, path, "config", "user.name", "Usable Project Fixture")
		usableGit(t, path, "config", "user.email", "fixture@example.invalid")
		if err := os.WriteFile(filepath.Join(path, "same.txt"), []byte(name+" source\n"), 0600); err != nil {
			t.Fatal(err)
		}
		usableGit(t, path, "add", "same.txt")
		usableGit(t, path, "commit", "-m", "initial")
		paths[name], heads[name] = path, usableGit(t, path, "rev-parse", "HEAD")
		inputs = append(inputs, map[string]any{"path": path, "alias": name})
	}
	registered := usableHTTP(t, ctx, http.MethodPost, core+"/v1/projects", map[string]any{"request_id": "register-project", "name": "Packaged usable project", "repositories": inputs})
	project := usableString(t, registered, "project_id")
	for _, raw := range registered["repositories"].([]any) {
		r := raw.(map[string]any)
		repos[r["name"].(string)] = r["id"].(string)
	}
	if len(repos) != 3 {
		t.Fatalf("registered repositories=%v", registered)
	}
	gptID := usableString(t, usableCall(t, ctx, gpt, "session_initialize", map[string]any{"requestId": "initialize-gpt"}), "aiSessionId")
	localID := usableString(t, usableCall(t, ctx, local, "session_initialize", map[string]any{"requestId": "initialize-local"}), "aiSessionId")
	usableCall(t, ctx, local, "project_open", map[string]any{"aiSessionId": localID, "projectId": project, "requestId": "bind-local"})
	for _, name := range []string{"app", "sdk", "backend"} {
		usableCall(t, ctx, local, "development_branch_select", map[string]any{"aiSessionId": localID, "repositoryId": repos[name], "developmentRef": "refs/heads/" + name + "-development", "requestId": "select-" + name})
	}
	receipts := map[string]c.Intent{}
	variant := func(id, summary string) c.Variant { return c.Variant{ID: id, Label: summary, Summary: summary} }
	complete := func(in c.PrepareInput, selection c.Selection) (c.Intent, c.CommitInput) {
		t.Helper()
		in.AISessionID = gptID
		tool := map[c.Purpose]string{c.ProjectSetup: "project_setup_prepare", c.FeatureArea: "feature_area_prepare", c.Task: "task_prepare", c.Review: "review_prepare", c.Execution: "execution_prepare"}[in.Purpose]
		args := usableMap(t, in)
		// Public Review plans contain authored selection only; InitialScope is
		// an internal derived field and must not be sent on the closed wire.
		for _, key := range []string{"task", "review"} {
			if body, ok := args[key].(map[string]any); ok {
				body["reviewPlan"] = map[string]any{"mode": "REPOSITORY"}
			}
		}
		if in.Action == "CREATE_TASK" {
			delete(args["task"].(map[string]any), "reviewPlan")
		}
		if in.Review != nil {
			body := args["review"].(map[string]any)
			body["repositoryId"] = ""
			if targets, ok := body["repositoryTargets"].([]any); ok {
				for _, raw := range targets {
					target := raw.(map[string]any)
					if target["memberId"] == "" {
						delete(target, "memberId")
						delete(target, "memberVersion")
					}
				}
			}
		}
		if in.Action == "MERGE_FEATURE_AREA" {
			delete(args["featureArea"].(map[string]any), "title")
			delete(args["featureArea"].(map[string]any), "problemStatement")
		}
		prepared := usableIntent(t, usableCall(t, ctx, gpt, tool, args))
		input := c.CommitInput{AISessionID: gptID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "commit-" + in.RequestID, ContentHash: prepared.ContentHash, VariantID: in.Variants[0].ID, Selection: selection}
		result := usableIntent(t, usableCall(t, ctx, gpt, "confirmation_commit", input))
		if result.State != "COMPLETED" {
			t.Fatalf("%s not completed: %+v", in.Action, result)
		}
		receipts[input.RequestID] = result
		return result, input
	}
	complete(c.PrepareInput{RequestID: "bind-gpt", Purpose: c.ProjectSetup, Action: "SELECT_PROJECT", ProjectSetup: &c.ProjectBody{Summary: "Open the disposable project"}, Variants: []c.Variant{variant("select", "Select project")}}, c.Selection{ProjectID: project})
	areaResult, _ := complete(c.PrepareInput{RequestID: "create-area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Update both contracts", ProblemStatement: "Deliver app and sdk together"}, Variants: []c.Variant{variant("create", "Create area")}}, c.Selection{})
	area := areaResult.Steps[0].FeatureAreaID
	taskResult, _ := complete(c.PrepareInput{RequestID: "create-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: area, Title: "Update exact members", ProblemStatement: "Change app and sdk same.txt", Constraints: []string{"Do not change backend"}, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{variant("create", "Create Task")}}, c.Selection{})
	taskID := taskResult.Steps[0].TaskID
	targets := []workset.Target{}
	for _, name := range []string{"app", "sdk"} {
		targets = append(targets, workset.Target{RepositoryID: repos[name], OriginCommit: heads[name], OriginRef: "refs/heads/" + name + "-development", Read: true, Write: workset.WriteScope{Kind: "NONE"}})
	}
	upper := variant("upper", "Review the two exact members")
	upper.ExecutorKind = "UPPER_LLM"
	upper.ReviewInstruction = "Read both same.txt files and propose their exact update"
	reviewResult, _ := complete(c.PrepareInput{RequestID: "start-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: taskID, FeatureAreaID: area, RepositoryTargets: targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{upper}}, c.Selection{})
	reviewID := reviewResult.Steps[0].ReviewID
	var access review.UpperAccess
	usableDecode(t, usableCall(t, ctx, gpt, "review_access_get", map[string]any{"aiSessionId": gptID, "reviewId": reviewID}), &access)
	if access.State != "ACTIVE" || access.ProtocolVersion != 2 || len(access.RepositoryTargets) != 2 {
		t.Fatalf("Review access=%+v", access)
	}
	targets = workset.CloneTargets(access.RepositoryTargets)
	evidence := []review.EvidenceRef{}
	for i, target := range targets {
		read := usableCall(t, ctx, gpt, "file_read", map[string]any{"reviewTarget": access.Target, "repositoryId": target.RepositoryID, "memberId": target.MemberID, "memberVersion": target.MemberVersion, "path": "same.txt"})
		evidence = append(evidence, review.EvidenceRef{RepositoryID: target.RepositoryID, MemberID: target.MemberID, EvidenceID: usableString(t, read, "evidenceId"), Path: "same.txt", Digest: usableString(t, read, "digest")})
		targets[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update exact app and sdk contracts", Constraints: []string{"Preserve non-target backend"}, CodexInstruction: "Write coded app followed by a newline to app same.txt and coded sdk followed by a newline to sdk same.txt. Do not commit.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "Both exact members", Checks: []task.VerificationCheck{}, AcceptanceCriteria: []string{"Both contracts have their intended content"}, CodexInstruction: "Execute both exact test commands without editing files."}
	for _, name := range []string{"app", "sdk"} {
		verification.Checks = append(verification.Checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: fmt.Sprintf(`test "$(cat same.txt)" = "coded %s"`, name), Required: true, RepositoryID: repos[name], RepositoryIDs: []string{repos[name]}, WorkingDirectory: "."})
	}
	usableCall(t, ctx, gpt, "review_result_submit", review.SubmitInput{UpperTarget: access.Target, RequestID: "submit-review", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, Summary: "Update both exact contracts", Disposition: review.DispositionApprovalReady, Coverage: []string{"app/same.txt", "sdk/same.txt"}, Findings: []review.Finding{}, Limitations: []string{"Disposable fixture"}, RecommendedNextSteps: []string{"Approve definition then explicitly start"}, EvidenceRefs: evidence, WorkSpec: &work, VerificationSpec: &verification}})
	approve := variant("approve", "Approve definition without starting")
	approve.ImplementationMode = task.ImplementationCodingAgent
	approve.RepositoryTargets = targets
	approve.PrimaryMemberID = targets[0].MemberID
	approve.WorkSpec = &work
	approve.VerificationSpec = &verification
	beforeApproval := usableCall(t, ctx, local, "workspace_get", map[string]any{"aiSessionId": localID, "taskId": taskID, "featureAreaId": area, "include": []string{"TASK"}})
	parentRevision := usableString(t, beforeApproval["taskSources"].(map[string]any), "currentDefinitionRevisionId")
	approval, approvalInput := complete(c.PrepareInput{RequestID: "approve-no-start", Purpose: c.Execution, Action: "APPROVE", Execution: &c.ExecutionBody{Action: "APPROVE", TaskID: taskID, FeatureAreaID: area, ReviewID: reviewID, DecisionID: "REVIEW_APPROVAL:" + reviewID, TaskDefinitionRevisionID: parentRevision}, Variants: []c.Variant{approve}}, c.Selection{})
	if len(approval.Steps) != 1 {
		t.Fatalf("NO_START approval steps=%+v", approval.Steps)
	}
	wsArgs := map[string]any{"aiSessionId": localID, "taskId": taskID, "featureAreaId": area, "include": []string{"TASK", "RUNTIME_CONFIG"}}
	ready := usableCall(t, ctx, local, "workspace_get", wsArgs)
	current := ready["task"].(map[string]any)
	if current["phase"] != "IMPLEMENTATION_READY" || current["currentExecutionId"] != nil {
		t.Fatalf("NO_START Task=%v", current)
	}
	if raw, _ := os.ReadFile(wire); len(raw) != 0 {
		t.Fatalf("NO_START launched App Server: %s", raw)
	}
	options := ready["runtimeOptions"].(map[string]any)
	choices := options["backendCandidates"].([]any)
	if len(choices) != 1 {
		t.Fatalf("scoped backend choices=%v", choices)
	}
	backendChoice := choices[0].(map[string]any)
	// Qualification is an explicit operator action under the same DataRoot
	// writer lock. The same Core binary collects and later consumes its identity.
	stop()
	qualification := exec.CommandContext(ctx, filepath.Join(bin, "devbridge"), "-mode", "qualify-runtime", "-data-root", data, "-qualification-backend", usableString(t, backendChoice, "configId"), "-qualification-request", "packaged-gate-native-qualification")
	qualification.Env = append(os.Environ(), env...)
	qualificationOutput, qualifyErr := qualification.CombinedOutput()
	if err := os.WriteFile(filepath.Join(root, "qualification.json"), qualificationOutput, 0600); err != nil {
		t.Fatal(err)
	}
	if qualifyErr != nil {
		t.Fatalf("explicit native qualification: %v %s", qualifyErr, qualificationOutput)
	}
	core, gpt, local, stop = launch()
	_ = core
	expected := ready["taskView"].(map[string]any)["executionStartTarget"]
	if expected == nil {
		t.Fatalf("workspace has no copyable taskView.executionStartTarget: %v", ready)
	}
	startInput := map[string]any{"aiSessionId": localID, "taskId": taskID, "requestId": "explicit-local-start", "expectedTarget": expected, "runtimeConfig": map[string]any{"model": "gpt-5.6-terra", "reasoningEffort": "high", "codexFastMode": false}, "backend": map[string]any{"configId": backendChoice["configId"], "configFingerprint": backendChoice["configFingerprint"]}}
	started := usableCall(t, ctx, local, "execution_start", startInput)
	proposal := started["Proposal"].(map[string]any)
	initialRun := started["Run"].(map[string]any)
	if usableString(t, proposal, "ID") == "" || usableString(t, initialRun, "ID") == "" {
		t.Fatal("start omitted durable identities")
	}
	runtime := proposal["RuntimeConfig"].(map[string]any)
	selectedRuntime := startInput["runtimeConfig"].(map[string]any)
	for _, key := range []string{"model", "reasoningEffort", "codexFastMode"} {
		if runtime[key] != selectedRuntime[key] {
			t.Fatalf("start ignored explicit runtime %s: %v", key, runtime)
		}
	}
	selectedBackend := proposal["BackendSelection"].(map[string]any)
	for _, key := range []string{"configId", "configFingerprint"} {
		if selectedBackend[key] != backendChoice[key] {
			t.Fatalf("start ignored explicit backend %s: %v", key, selectedBackend)
		}
	}
	t.Logf("explicit start Execution %s / Run %s", proposal["ID"], initialRun["ID"])
	deadline := time.Now().Add(30 * time.Second)
	var acceptedTask map[string]any
	for {
		acceptedTask = usableCall(t, ctx, local, "workspace_get", wsArgs)
		view := acceptedTask["taskView"].(map[string]any)
		if view["phase"] == "ACCEPTANCE_PENDING" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("implementation/verification did not finish: %s", usableJSON(acceptedTask))
		}
		time.Sleep(25 * time.Millisecond)
	}
	view := acceptedTask["taskView"].(map[string]any)
	for _, key := range []string{"implementationRun", "verificationRun"} {
		r := view[key].(map[string]any)
		if r["transportStatus"] != "SUCCEEDED" {
			t.Fatalf("%s=%v", key, r)
		}
	}
	checks := view["verificationReport"].(map[string]any)["checks"].([]any)
	if len(checks) != 2 {
		t.Fatalf("Verification checks=%v", checks)
	}
	for _, raw := range checks {
		check := raw.(map[string]any)
		observed := check["observed"].(map[string]any)
		if observed["status"] != "PASSED" || observed["exitCode"] != float64(0) {
			t.Fatalf("Verification lacked real successful command evidence: %v", check)
		}
	}
	current = acceptedTask["task"].(map[string]any)
	executionID := usableString(t, current, "currentExecutionId")
	sources := acceptedTask["taskSources"].(map[string]any)
	revisionID := usableString(t, sources, "currentDefinitionRevisionId")
	_, acceptInput := complete(c.PrepareInput{RequestID: "accept-task", Purpose: c.Execution, Action: "ACCEPT", Execution: &c.ExecutionBody{Action: "ACCEPT", TaskID: taskID, FeatureAreaID: area, ExecutionID: executionID, TaskDefinitionRevisionID: revisionID, DecisionID: "TASK_ACCEPTANCE:" + executionID}, Variants: []c.Variant{variant("accept", "Accept verified result")}}, c.Selection{})
	committed := usableCall(t, ctx, local, "workspace_get", wsArgs)
	commitView := committed["taskView"].(map[string]any)
	receipt := commitView["details"].(map[string]any)["commitReceipt"].(map[string]any)
	delivery := receipt["delivery"].(map[string]any)
	members := delivery["members"].([]any)
	if commitView["status"] != "SUCCEEDED" || receipt["pending"] != false || delivery["state"] != "COMPLETED" || len(members) != 2 {
		t.Fatalf("Task acceptance/commit: status=%v receipt=%s", commitView["status"], usableJSON(receipt))
	}
	taskCommits := map[string]string{}
	for _, raw := range members {
		member := raw.(map[string]any)
		if member["state"] != "COMPLETED" {
			t.Fatalf("unfinished Task member: %v", member)
		}
		taskCommits[usableString(t, member, "repositoryId")] = usableString(t, member, "commit")
	}
	_, mergeInput := complete(c.PrepareInput{RequestID: "merge-area", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: area}, Variants: []c.Variant{variant("merge", "Merge accepted contracts")}}, c.Selection{})
	finalHeads := map[string]string{}
	for _, name := range []string{"app", "sdk", "backend"} {
		finalHeads[name] = usableGit(t, paths[name], "rev-parse", "HEAD")
		if name != "backend" {
			commit := taskCommits[repos[name]]
			if commit == "" {
				t.Fatalf("missing Task commit for %s", name)
			}
			usableGit(t, paths[name], "merge-base", "--is-ancestor", commit, "HEAD")
			if n := usableGit(t, paths[name], "rev-list", "--count", heads[name]+".."+commit); n != "1" {
				t.Fatalf("Task commit count for %s=%s", name, n)
			}
		}
		want := name + " source"
		if name != "backend" {
			want = "coded " + name
		}
		if got := usableGit(t, paths[name], "show", "HEAD:same.txt"); got != want {
			t.Fatalf("%s final content=%q", name, got)
		}
		if got := usableGit(t, paths[name], "status", "--porcelain"); got != "" {
			t.Fatalf("%s dirty after merge: %s", name, got)
		}
	}
	if finalHeads["backend"] != heads["backend"] {
		t.Fatal("unselected backend changed")
	}
	wireBefore, err := os.ReadFile(wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(wireBefore), []byte{'\n'}) {
		var entry struct {
			Model     string `json:"model"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Model != "gpt-5.6-terra" || entry.Reasoning.Effort != "high" {
			t.Fatalf("wrong explicit native provider runtime: %s", line)
		}
	}
	if count := bytes.Count(wireBefore, []byte{'\n'}); count != 5 {
		t.Fatalf("wanted exactly five native provider requests for two turns, got %d: %s", count, wireBefore)
	}
	stop()
	_, gpt, local, stop = launch()
	defer stop()
	// Completed requests remain exact historical receipts after process restart.
	for _, input := range []c.CommitInput{approvalInput, acceptInput, mergeInput} {
		replayed := usableIntent(t, usableCall(t, ctx, gpt, "confirmation_commit", input))
		original := receipts[input.RequestID]
		if replayed.State != "COMPLETED" || replayed.IntentID != original.IntentID || replayed.ContentHash != original.ContentHash || usableJSON(replayed.Steps) != usableJSON(original.Steps) {
			t.Fatalf("cold replay changed original %s receipt: before=%s after=%s", input.Action, usableJSON(original.Steps), usableJSON(replayed.Steps))
		}
	}
	replayed := usableCall(t, ctx, local, "execution_start", startInput)
	for _, key := range []string{"TaskDefinitionRevisionID", "RuntimeConfig", "BackendSelection", "PrimaryMemberID", "ApprovedWorksetHash"} {
		if usableJSON(started["Proposal"].(map[string]any)[key]) != usableJSON(replayed["Proposal"].(map[string]any)[key]) {
			t.Fatalf("cold replay changed frozen Execution %s", key)
		}
	}
	for _, key := range []string{"Proposal", "Run"} {
		original := started[key].(map[string]any)
		restored := replayed[key].(map[string]any)
		if original["ID"] != restored["ID"] {
			t.Fatalf("cold start replay changed %s identity: %v / %v", key, original, restored)
		}
	}
	changed := usableMap(t, startInput)
	changed["runtimeConfig"].(map[string]any)["reasoningEffort"] = "low"
	rejection, rejectErr := local.CallTool(ctx, &sdkmcp.CallToolParams{Name: "execution_start", Arguments: changed})
	if rejectErr != nil {
		t.Fatalf("changed request did not return a business rejection: %v", rejectErr)
	}
	if !rejection.IsError {
		t.Fatalf("changed original start payload was accepted: %s", usableJSON(rejection))
	}
	cold := usableCall(t, ctx, local, "workspace_get", wsArgs)
	coldSources := cold["taskSources"].(map[string]any)
	if coldSources["currentDefinitionRevisionId"] != revisionID || coldSources["currentExecutionId"] != executionID {
		t.Fatalf("cold replay changed Task pointers: %v", coldSources)
	}
	beforeRuns := commitView["details"].(map[string]any)["runs"].([]any)
	afterDetails := cold["taskView"].(map[string]any)["details"].(map[string]any)
	afterRuns := afterDetails["runs"].([]any)
	ids := func(runs []any) map[string]string {
		out := map[string]string{}
		for _, raw := range runs {
			r := raw.(map[string]any)
			out[r["id"].(string)] = r["purpose"].(string)
		}
		return out
	}
	if len(beforeRuns) != 3 || afterDetails["runsTotal"] != float64(3) || afterDetails["runsTruncated"] != false || usableJSON(ids(beforeRuns)) != usableJSON(ids(afterRuns)) {
		t.Fatalf("cold replay changed Review/Implementation/Verification identities: before=%v after=%v", ids(beforeRuns), ids(afterRuns))
	}
	for name, head := range finalHeads {
		if got := usableGit(t, paths[name], "rev-parse", "HEAD"); got != head {
			t.Fatalf("cold replay duplicated Git effect for %s", name)
		}
	}
	if after, _ := os.ReadFile(wire); !bytes.Equal(after, wireBefore) {
		t.Fatal("cold replay launched another runtime")
	}
	t.Logf("PASS Project %s: Task %s, Review %s, Execution %s; NO_START, two successful runtime turns, ACCEPT, Task commit, FeatureArea merge, cold exact replay", project, taskID, reviewID, executionID)
}

func usableMap(t *testing.T, v any) map[string]any {
	t.Helper()
	var out map[string]any
	usableDecode(t, v, &out)
	return out
}
func usableDecode(t *testing.T, v any, out any) {
	t.Helper()
	b, e := json.Marshal(v)
	if e == nil {
		e = json.Unmarshal(b, out)
	}
	if e != nil {
		t.Fatal(e)
	}
}
func usableJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func usableString(t *testing.T, v map[string]any, key string) string {
	t.Helper()
	s, ok := v[key].(string)
	if !ok || s == "" {
		t.Fatalf("missing %s: %s", key, usableJSON(v))
	}
	return s
}
func usableIntent(t *testing.T, v map[string]any) c.Intent {
	t.Helper()
	var out c.Intent
	usableDecode(t, v["intent"], &out)
	if out.IntentID == "" {
		t.Fatalf("missing intent: %s", usableJSON(v))
	}
	return out
}
func usableCall(t *testing.T, ctx context.Context, s *sdkmcp.ClientSession, name string, args any) map[string]any {
	t.Helper()
	result, err := s.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || result.IsError {
		t.Fatalf("public %s: err=%v result=%s", name, err, usableJSON(result))
	}
	value := usableMap(t, result.StructuredContent)
	if result, ok := value["result"].(map[string]any); ok {
		return result
	}
	return value
}
func usableConnect(t *testing.T, ctx context.Context, url string) *sdkmcp.ClientSession {
	t.Helper()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "usable-project-public-gate", Version: "1"}, nil)
	s, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: url + "/mcp", HTTPClient: &http.Client{Timeout: 20 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func usableHTTP(t *testing.T, ctx context.Context, method, url string, input any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(input)
	req, e := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	var out map[string]any
	if e = json.NewDecoder(resp.Body).Decode(&out); e != nil {
		t.Fatal(e)
	}
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: %d %v", method, url, resp.StatusCode, out)
	}
	return out
}
func usableGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = path
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func usableLaunch(t *testing.T, bin, root string, env []string, name string, args ...string) (string, func()) {
	t.Helper()
	cmd := exec.Command(filepath.Join(bin, name), args...)
	cmd.Env = append(os.Environ(), env...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.OpenFile(filepath.Join(root, name+".stderr"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready, done := make(chan string, 1), make(chan error, 1)
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
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			_ = stderr.Close()
		})
	}
	t.Cleanup(stop)
	select {
	case u := <-ready:
		return u, stop
	case <-time.After(20 * time.Second):
		b, _ := os.ReadFile(filepath.Join(root, name+".stderr"))
		t.Fatalf("%s startup timed out: %s", name, b)
		return "", stop
	}
}

func usableNativeResponses(t *testing.T, root, wire string) *httptest.Server {
	t.Helper()
	if err := os.WriteFile(wire, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	step := 0
	roots := map[string]string{}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		defer r.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			http.Error(w, "read request", 400)
			return
		}
		var body struct {
			Model     string `json:"model"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
			Input []struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if json.Unmarshal(raw, &body) != nil || body.Model != "gpt-5.6-terra" || body.Reasoning.Effort != "high" {
			t.Errorf("native request changed selected runtime")
			http.Error(w, "runtime", 400)
			return
		}
		log, err := os.OpenFile(wire, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Error(err)
			http.Error(w, "log", 500)
			return
		}
		_, err = log.Write(append(raw, '\n'))
		_ = log.Close()
		if err != nil {
			t.Error(err)
			http.Error(w, "log", 500)
			return
		}
		step++
		if step == 1 {
			for _, item := range body.Input {
				if item.Role != "user" {
					continue
				}
				for _, content := range item.Content {
					if !strings.Contains(content.Text, "<environment_context>") {
						continue
					}
					block := regexp.MustCompile(`(?s)<workspace_roots>(.*?)</workspace_roots>`).FindStringSubmatch(content.Text)
					if len(block) != 2 {
						continue
					}
					for _, match := range regexp.MustCompile(`<root>(.*?)</root>`).FindAllStringSubmatch(block[1], -1) {
						path := html.UnescapeString(match[1])
						rel, e := filepath.Rel(filepath.Join(root, "data", "worktrees"), path)
						if e != nil || rel == "." || strings.HasPrefix(rel, "..") {
							t.Errorf("native root escaped disposable worktrees: %s", path)
							http.Error(w, "root", 400)
							return
						}
						data, e := os.ReadFile(filepath.Join(path, "same.txt"))
						if e != nil {
							t.Error(e)
							http.Error(w, "member", 500)
							return
						}
						name := strings.TrimSuffix(strings.TrimSpace(string(data)), " source")
						if name != "app" && name != "sdk" {
							t.Errorf("unexpected native member: %q", data)
							http.Error(w, "member", 400)
							return
						}
						if roots[name] != "" && roots[name] != path {
							t.Errorf("ambiguous native member %s", name)
							http.Error(w, "member", 400)
							return
						}
						roots[name] = path
					}
				}
			}
		}
		if len(roots) != 2 || roots["app"] == roots["sdk"] {
			t.Errorf("native exact roots unavailable: %v", roots)
			http.Error(w, "roots", 400)
			return
		}
		var item map[string]any
		switch step {
		case 1, 3, 4:
			command, dir := `test "$(cat same.txt)" = "coded app"`, roots["app"]
			if step == 1 {
				command = "printf 'coded app\\n' > " + quote(filepath.Join(roots["app"], "same.txt")) + " && printf 'coded sdk\\n' > " + quote(filepath.Join(roots["sdk"], "same.txt"))
			}
			if step == 4 {
				command = `test "$(cat same.txt)" = "coded sdk"`
				dir = roots["sdk"]
			}
			args, _ := json.Marshal(map[string]any{"cmd": command, "workdir": dir, "max_output_tokens": 1000})
			item = map[string]any{"type": "function_call", "call_id": fmt.Sprintf("usable-native-%d", step), "name": "exec_command", "arguments": string(args)}
		case 2, 5:
			message := `{"resultDisposition":"COMPLETED","summary":"Updated both exact contracts"}`
			if step == 5 {
				message = `{"version":2,"summary":"Both exact checks passed","checks":[{"index":0,"status":"PASSED","summary":"app contract checked","evidence":"native command exited 0","reason":""},{"index":1,"status":"PASSED","summary":"sdk contract checked","evidence":"native command exited 0","reason":""}],"acceptanceCriteria":[{"index":0,"status":"PASSED","summary":"Both contracts verified","evidence":"Two successful native commands","reason":""}]}`
			}
			item = map[string]any{"id": fmt.Sprintf("usable-result-%d", step), "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": message}}}
		default:
			t.Errorf("unexpected extra provider request %d", step)
			http.Error(w, "extra request", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		id := fmt.Sprintf("usable-response-%d", step)
		for _, event := range []map[string]any{{"type": "response.created", "response": map[string]any{"id": id}}, {"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": id, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}} {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
		}
	}))
	t.Cleanup(server.Close)
	return server
}
