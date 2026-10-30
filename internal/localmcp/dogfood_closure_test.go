package localmcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"

	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"

	"github.com/lazyun/devbridge/internal/execution"

	"github.com/lazyun/devbridge/internal/run"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Each backend turn is held after durable dispatch, exercising real supervisor,
// worktree, conversation, SQLite claim, and MCP result-delivery wiring.
type closureFactory struct {
	entered  chan *closureSession
	message  string
	commands []execution.ObservedCommand
}
type closureSession struct {
	*stubCodexSession
	factory *closureFactory
	release chan struct{}
	once    sync.Once
}

func (f *closureFactory) Start(context.Context, codexconfig.Config) (execution.CodexSession, error) {
	return &closureSession{stubCodexSession: &stubCodexSession{id: fmt.Sprint(stubCodexSequence.Add(1))}, factory: f, release: make(chan struct{})}, nil
}
func (s *closureSession) finish()      { s.once.Do(func() { close(s.release) }) }
func (s *closureSession) Close() error { s.finish(); return nil }
func (s *closureSession) StartTurnWithStarted(ctx context.Context, thread, prompt string, started func(string) error) (execution.CodexTurnResult, error) {
	result, err := s.stubCodexSession.StartTurn(ctx, thread, prompt)
	if err != nil {
		return result, err
	}
	if err = started(result.TurnID); err != nil {
		return result, err
	}
	select {
	case s.factory.entered <- s:
	case <-ctx.Done():
		return result, ctx.Err()
	}
	select {
	case <-s.release:
		result.Message = s.factory.message
		result.Commands = s.factory.commands
		return result, nil
	case <-ctx.Done():
		return result, ctx.Err()
	}
}
func awaitClosure(t *testing.T, f *closureFactory) *closureSession {
	t.Helper()
	select {
	case s := <-f.entered:
		t.Cleanup(s.finish)
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("backend did not enter turn")
		return nil
	}
}
func closureArea(t *testing.T, f *integrationFixture, c *sdkmcp.ClientSession, sid, title string) string {
	return callToolMap(t, f.ctx, c, "featureArea_create", map[string]any{"aiSessionId": sid, "title": title, "problemStatement": title})["id"].(string)
}
func closureReviewArgs(sid, area string) map[string]any {
	return map[string]any{"aiSessionId": sid, "featureAreaId": area, "requestId": "review-start-" + area, "reviewDepth": "DEEP", "reviewInstruction": "Inspect read-only and report the result for this exact Review.", "reviewPlan": map[string]any{"mode": "REPOSITORY"}, "model": "gpt-5.6-terra", "reasoningEffort": "high"}
}

func TestDogfoodReviewAdmissionRejectsBusyWithoutPhantomWorkflowAndReplaysExactly(t *testing.T) {
	f := newIntegrationFixture(t)
	backend := &closureFactory{entered: make(chan *closureSession, 2), message: "Admission closure review complete."}
	f.plane.Coordinator.Codex = backend
	gpt := connectGPT(t, f.gptURL)
	local := connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	areaA := closureArea(t, f, gpt, sid, "Admission A")
	areaB := closureArea(t, f, gpt, sid, "Admission B")

	acceptedArgs := closureTaskReviewArgs(t, f, sid, areaA)
	missingDepth := maps.Clone(acceptedArgs)
	delete(missingDepth, "reviewDepth")
	if text := callToolErrorText(t, f.ctx, local, "task_review_start", missingDepth); !strings.Contains(strings.ToLower(text), "reviewdepth") && !strings.Contains(strings.ToLower(text), "invalid") {
		t.Fatalf("missing depth accepted: %s", text)
	}
	missingRequest := maps.Clone(acceptedArgs)
	delete(missingRequest, "requestId")
	if text := callToolErrorText(t, f.ctx, local, "task_review_start", missingRequest); !strings.Contains(strings.ToLower(text), "requestid") && !strings.Contains(strings.ToLower(text), "invalid") {
		t.Fatalf("discovered required requestId was not enforced: %q", text)
	}
	unexpected := maps.Clone(acceptedArgs)
	unexpected["clientOnlyField"] = true
	// ChatGPT's purpose-confirmation adapter deliberately maps the accepted
	// intent fields and has no direct task_review_start schema. Exercise the
	// retained Local canonical endpoint for the closed-schema assertion.
	if text := callToolErrorText(t, f.ctx, local, "task_review_start", unexpected); !strings.Contains(strings.ToLower(text), "additional") && !strings.Contains(strings.ToLower(text), "invalid") {
		t.Fatalf("closed discovery schema accepted an additional field: %q", text)
	}
	if reviews, err := f.plane.Reviews.ListByFeatureArea(f.ctx, areaA); err != nil || len(reviews) != 0 {
		t.Fatalf("schema rejection created Review history: %+v err=%v", reviews, err)
	}
	accepted := callToolMap(t, f.ctx, gpt, "task_review_start", acceptedArgs)
	activeBackend := awaitClosure(t, backend)
	acceptedReview := accepted["review"].(map[string]any)
	reviewID := acceptedReview["id"].(string)
	stored, err := f.plane.Reviews.Get(f.ctx, reviewID)
	// The GPT confirmation commit owns the durable start receipt. The original
	// fixture request is presentation input, not the direct retired command's
	// receipt identity.
	if err != nil || stored.RunID == "" || stored.StartRequestID == "" {
		t.Fatalf("accepted Review identity=%+v err=%v", stored, err)
	}

	// Retrying the accepted command returns the original Review/Run and does
	// not hand a second turn to the backend.
	replayed := callToolMap(t, f.ctx, gpt, "task_review_start", acceptedArgs)
	if replayed["review"].(map[string]any)["id"] != reviewID {
		t.Fatalf("accepted replay created another Review: %+v", replayed)
	}
	select {
	case unexpected := <-backend.entered:
		unexpected.finish()
		t.Fatal("accepted replay dispatched a second backend turn")
	default:
	}

	changedDepth := maps.Clone(acceptedArgs)
	changedDepth["reviewDepth"] = "FAST"
	if text := callToolErrorText(t, f.ctx, gpt, "task_review_start", changedDepth); !strings.Contains(strings.ToUpper(text), "CONFLICT") {
		t.Fatalf("depth absent from replay hash: %s", text)
	}
	shallow := maps.Clone(acceptedArgs)
	shallow["requestId"] = "too-shallow-zero-write"
	shallow["reviewDepth"] = "FAST"
	// The confirmation flow rejects this before constructing a direct Review
	// command. Its depth gate is authoritative and must not create a second
	// Review or reinterpret the existing DEEP Review as an accepted FAST one.
	shallowText := callToolErrorText(t, f.ctx, gpt, "task_review_start", shallow)
	for _, fact := range []string{"REVIEW_DEPTH_TOO_SHALLOW", "requires DEEP review"} {
		if !strings.Contains(shallowText, fact) {
			t.Fatalf("missing depth domain fact %s: %s", fact, shallowText)
		}
	}
	busyArgs := closureTaskReviewArgs(t, f, sid, areaA)
	busyArgs["requestId"] = "review-start-busy-a"
	busyArgs["reviewInstruction"] = "A second request must be rejected before Review creation."
	busyText := callToolErrorText(t, f.ctx, gpt, "task_review_start", busyArgs)
	// A GPT confirmation may reject a busy Task during immutable intent
	// preparation, before it reaches Local's direct FEATURE_AREA_BUSY command.
	// The retained store/read checks below prove that this early rejection did
	// not create a Review, Run, or replacement claim.
	if !strings.Contains(busyText, "INVALID_ARGUMENT") || !strings.Contains(busyText, "Task is not awaiting a new Review") || strings.Contains(strings.ToLower(busyText), "recover") || strings.Contains(strings.ToLower(busyText), "restart") {
		t.Fatalf("busy admission guidance/correlation=%q", busyText)
	}
	if retryText := callToolErrorText(t, f.ctx, gpt, "task_review_start", busyArgs); retryText != busyText {
		t.Fatalf("busy command replay changed result:\nfirst=%q\nretry=%q", busyText, retryText)
	}
	conflicting := maps.Clone(busyArgs)
	conflicting["reviewInstruction"] = "The same request id cannot name a different payload."
	if text := callToolErrorText(t, f.ctx, gpt, "task_review_start", conflicting); !strings.Contains(text, "INVALID_ARGUMENT") || !strings.Contains(text, "Task is not awaiting a new Review") {
		t.Fatalf("request payload conflict=%q", text)
	}

	reviews, err := f.plane.Reviews.ListByFeatureArea(f.ctx, areaA)
	if err != nil || len(reviews) != 1 || reviews[0].ID != reviewID {
		t.Fatalf("busy request polluted Review history: %+v err=%v", reviews, err)
	}
	var runCount, receiptCount int
	if err = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM runs WHERE feature_area_id=? AND operation_kind='REVIEW'`, areaA).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM feature_area_review_start_requests WHERE feature_area_id=?`, areaA).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	// The confirmation preparation gate rejected the busy request before it
	// could write a Review-start receipt.
	if runCount != 1 || receiptCount != 1 {
		t.Fatalf("busy request polluted Run history: runs=%d receipts=%d", runCount, receiptCount)
	}

	workspace := callToolMap(t, f.ctx, gpt, "workspace_get", map[string]any{"aiSessionId": sid, "featureAreaId": areaA, "taskId": acceptedArgs["taskId"]})
	result := workspace["result"].(map[string]any)
	operation := result["selectedOperation"].(map[string]any)
	taskValue := result["selectedTask"].(map[string]any)
	if workspace["workflow"].(map[string]any)["state"] != "REVIEWING" || operation["operationId"] != reviewID || operation["runId"] != stored.RunID || taskValue["id"] != acceptedArgs["taskId"] || taskValue["phase"] != "REVIEWING" {
		t.Fatalf("busy projection replaced active Review: %#v", workspace)
	}
	authoritativeTask, taskErr := f.plane.Tasks.Get(f.ctx, acceptedArgs["taskId"].(string))
	if taskErr != nil || authoritativeTask.CurrentReviewID != reviewID {
		t.Fatalf("current Review pointer changed: %+v %v", authoritativeTask, taskErr)
	}
	ownerState, e := f.plane.AgentConversations.State(f.ctx, areaA)
	if e != nil || ownerState.Status != "BUSY" || ownerState.Claim == nil || ownerState.Claim.OperationID != reviewID || ownerState.Claim.RunID != stored.RunID {
		t.Fatalf("canonical active claim changed: %+v %v", ownerState, e)
	}

	// A different FeatureArea remains independently admissible.
	other := callToolMap(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sid, areaB))
	otherBackend := awaitClosure(t, backend)
	if other["review"].(map[string]any)["id"] == reviewID {
		t.Fatalf("different FeatureArea reused Review identity: %+v", other)
	}

	activeBackend.finish()
	otherBackend.finish()
	waitForReviewSucceeded(t, f, reviewID)
	completion := callToolMap(t, f.ctx, gpt, "ui_workflow_state_get", map[string]any{"aiSessionId": sid, "reviewId": reviewID, "runId": stored.RunID})
	continuation := completion["continuation"].(map[string]any)
	message := continuation["message"].(string)
	if continuation["reviewId"] != reviewID || continuation["runId"] != stored.RunID || continuation["featureAreaId"] != areaA || continuation["requestId"] != stored.StartRequestID || continuation["status"] != "SUCCEEDED" || continuation["completedTransition"] != true || !strings.Contains(message, "existing Task phase") || strings.Contains(message, "request or authorize another Review") {
		t.Fatalf("completion lost immutable identity or induced a repeat: %+v", continuation)
	}
}

func TestDogfoodCrossFeatureAreaReviewsAndOriginalCompletionCorrelation(t *testing.T) {
	f := newIntegrationFixture(t)
	backend := &closureFactory{entered: make(chan *closureSession, 2), message: "Read-only review complete."}
	f.plane.Coordinator.Codex = backend
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	a := closureArea(t, f, gpt, sid, "A")
	b := closureArea(t, f, gpt, sid, "B")
	callToolMap(t, f.ctx, gpt, "featureArea_activate", map[string]any{"aiSessionId": sid, "featureAreaId": a, "requestId": "activate-a"})
	f.seedTask(t, validOwnershipTaskArgs(sid, a))
	start := make(chan struct{})
	type reviewStartResult struct {
		value map[string]any
		err   error
	}
	results := make(chan reviewStartResult, 2)
	for i, c := range []*sdkmcp.ClientSession{gpt, local} {
		go func(i int, c *sdkmcp.ClientSession) {
			<-start
			// GPT uses purpose confirmation while Local retains the canonical
			// command. This shared test adapter drives both current paths.
			result, err := reviewStartFixture(t, f.ctx, c, closureTaskReviewArgs(t, f, sid, []string{a, b}[i]))
			results <- reviewStartResult{value: result, err: err}
		}(i, c)
	}
	close(start)
	reviewIDs := map[string]string{}
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		r := result.value
		review := r["review"].(map[string]any)
		areaID, _ := review["featureAreaId"].(string)
		if areaID == "" {
			if area, ok := review["featureArea"].(map[string]any); ok {
				areaID, _ = area["id"].(string)
			}
		}
		if areaID == "" {
			t.Fatalf("review omitted FeatureArea identity: %+v", review)
		}
		reviewIDs[areaID] = review["id"].(string)
	}
	left, right := awaitClosure(t, backend), awaitClosure(t, backend)
	for _, area := range []string{a, b} {
		v, e := f.plane.Reviews.Get(f.ctx, reviewIDs[area])
		if e != nil {
			t.Fatal(e)
		}
		r, e := f.plane.Runs.Get(f.ctx, v.RunID)
		if e != nil || r.Status != run.StatusRunning {
			t.Fatalf("both must be RUNNING: %+v %v", r, e)
		}
	}
	leftWorkspace, e := f.plane.Workspaces.GetByFeatureArea(f.ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	rightWorkspace, e := f.plane.Workspaces.GetByFeatureArea(f.ctx, b)
	if e != nil {
		t.Fatal(e)
	}
	if leftWorkspace.ID == rightWorkspace.ID || leftWorkspace.WorktreeID == rightWorkspace.WorktreeID {
		t.Fatal("concurrent FeatureAreas shared a canonical workspace")
	}
	c := closureArea(t, f, gpt, sid, "C selected while A runs")
	callToolMap(t, f.ctx, gpt, "workspace_get", map[string]any{"aiSessionId": sid, "featureAreaId": c})
	left.finish()
	right.finish()
	for _, area := range []string{a, b} {
		waitForReviewSucceeded(t, f, reviewIDs[area])
	}
	aReview, _ := f.plane.Reviews.Get(f.ctx, reviewIDs[a])
	delivery := callToolMap(t, f.ctx, gpt, "ui_workflow_state_get", map[string]any{"aiSessionId": sid, "reviewId": aReview.ID, "runId": aReview.RunID})
	continuation := delivery["continuation"].(map[string]any)
	if delivery["reviewId"] != aReview.ID || continuation["featureAreaId"] != a || continuation["reviewId"] != aReview.ID || continuation["runId"] != aReview.RunID || continuation["attempt"] != true {
		t.Fatalf("wrong completion owner: %+v", delivery)
	}
	for _, client := range []*sdkmcp.ClientSession{gpt, local} {
		read := callToolMap(t, f.ctx, client, "operation_get", map[string]any{"aiSessionId": sid, "target": map[string]any{"kind": "REVIEW", "operationId": aReview.ID, "runId": aReview.RunID}})
		if read["context"].(map[string]any)["featureAreaId"] != a {
			t.Fatalf("wrong result: %+v", read)
		}
	}
	if reviews, e := f.plane.Reviews.ListByFeatureArea(f.ctx, c); e != nil || len(reviews) != 0 {
		t.Fatalf("selection polluted C: %+v %v", reviews, e)
	}
	reopened := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID, "aiSessionId": sid})
	if reopened["aiSessionId"] != sid {
		t.Fatal("project reopen replaced the Session")
	}
	opened := callToolMap(t, f.ctx, gpt, "workspace_get", map[string]any{"aiSessionId": sid})
	if opened["result"].(map[string]any)["selectedFeatureArea"] != nil {
		t.Fatalf("unscoped workspace read selected FeatureArea: %#v", opened)
	}
}

func mustClosureRuns(t *testing.T, f *integrationFixture) []run.Run {
	t.Helper()
	r, e := f.plane.Runs.List(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

// Optional local audit: run against a consistent copy of the real dogfood DB.
// The original DB and its worktrees are never opened for mutation.

func TestDogfoodBoundedRealReadOnlyReview(t *testing.T) {
	if os.Getenv("DEVBRIDGE_REAL_REVIEW_SMOKE") != "1" {
		t.Skip("opt-in real Codex read-only Review smoke")
	}
	f := newIntegrationFixture(t)
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Bounded read-only Review smoke")
	args := closureTaskReviewArgs(t, f, sid, area)
	args["model"] = "gpt-5.6-sol"
	args["reasoningEffort"] = "low"
	args["reviewInstruction"] = "Read-only smoke. Read tracked.txt only and report its single line. Do not modify files, run tests, use network tools, or implement anything. Stop after that one observation."
	out := callToolMap(t, f.ctx, gpt, "task_review_start", args)
	id := out["review"].(map[string]any)["id"].(string)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		v, e := f.plane.Reviews.Get(f.ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		r, e := f.plane.Runs.Get(f.ctx, v.RunID)
		if e != nil {
			t.Fatal(e)
		}
		switch r.Status {
		case run.StatusSucceeded:
			proof, err := f.plane.Evidence.Get(f.ctx, r.ID)
			if err != nil || proof.BaseCommit == "" || len(proof.ChangedFiles) != 0 {
				t.Fatalf("read-only durable evidence: %+v %v", proof, err)
			}
			if tasks, err := f.plane.Tasks.List(f.ctx); err != nil || len(tasks) != 0 {
				t.Fatalf("read-only smoke created Task: %+v %v", tasks, err)
			}
			t.Logf("real Review %s Run %s SUCCEEDED with zero changed files; durable base=%s", id, r.ID, proof.BaseCommit)
			return
		case run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled:
			t.Fatalf("real Review terminal: %s failure=%+v", r.Status, r.Failure)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("real read-only Review exceeded 90-second bound")
}

// The fixture explicitly creates intent before any Review request; retries reuse
// that durable Task, including after lifecycle closure.
func closureTaskReviewArgs(t *testing.T, f *integrationFixture, sid, area string) map[string]any {
	t.Helper()
	f.intentMu.Lock()
	defer f.intentMu.Unlock()
	if f.intentIDs == nil {
		f.intentIDs = map[string]string{}
	}
	id := f.intentIDs[area]
	if id == "" {
		// This helper only needs a canonical Task fixture to reserve a Review.
		// Calling the retired GPT task_create RPC bypassed the Session request
		// context and now correctly fails caller-scope admission. Use the same
		// canonical Task store fixture used by the execution scenarios instead.
		id = f.seedTask(t, map[string]any{"aiSessionId": sid, "featureAreaId": area, "goal": "Inspect and improve this capability"}).ID
		f.intentIDs[area] = id
	}
	args := closureReviewArgs(sid, area)
	args["taskId"] = id
	args["repositoryId"] = f.repositoryID
	config, err := f.plane.CodexConfigs.Get(f.ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	selection := config.Selection("localmcp-dogfood-fixture")
	args["backend"] = map[string]any{"configId": selection.ConfigID, "configFingerprint": selection.ConfigFingerprint}
	return args
}

func (s *closureSession) StartTurnWithSchemaAndStarted(ctx context.Context, thread, prompt string, schema json.RawMessage, started func(string) error) (execution.CodexTurnResult, error) {
	result, err := s.StartTurnWithStarted(ctx, thread, prompt, started)
	if string(schema) == execution.ImplementationReportJSONSchema && err == nil {
		raw, _ := json.Marshal(execution.ImplementationReport{ResultDisposition: "COMPLETED", Summary: result.Message})
		result.Message = string(raw)
	}
	return result, err
}
