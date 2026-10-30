package localmcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

type continuityBackend struct {
	*closureFactory
	ceilings chan codex.AuthorizationCeiling
}

func (b *continuityBackend) StartForOperation(ctx context.Context, c codexconfig.Config, _, _ string, ceiling codex.AuthorizationCeiling) (execution.CodexSession, error) {
	b.ceilings <- ceiling
	return b.Start(ctx, c)
}
func fixtureGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = path
	out, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("fixture Git %v: %s %v", args, out, e)
	}
	return strings.TrimSpace(string(out))
}
func TestFeatureAreaThreeTasksKeepWorkspaceAndExactAuthority(t *testing.T) {
	f := newIntegrationFixture(t)
	backend := &continuityBackend{closureFactory: &closureFactory{entered: make(chan *closureSession, 8)}, ceilings: make(chan codex.AuthorizationCeiling, 8)}
	f.plane.Coordinator.Codex = backend
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Continuous engineering work")
	// Preserve the selected development baseline for every semantic Task before
	// the fixture deliberately moves the source checkout away from it.
	seededTasks := make([]string, 3)
	initial := fixtureGit(t, f.repoPath, "rev-parse", "HEAD")
	// Source checkout moves away from the selected integration branch. First
	// workspace creation must still use its exact selected baseline.
	fixtureGit(t, f.repoPath, "checkout", "-qb", "source-checkout-only")
	if e := os.WriteFile(filepath.Join(f.repoPath, "checkout-only.txt"), []byte("not the integration baseline"), 0600); e != nil {
		t.Fatal(e)
	}
	fixtureGit(t, f.repoPath, "add", ".")
	fixtureGit(t, f.repoPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "source checkout advances")
	var workspaceID, workspacePath, firstTask string
	for i := 0; i < 3; i++ {
		scope := []string{"tracked.txt", "support/helper.go"}
		if i == 1 {
			scope = []string{"support/helper.go"}
		}
		intent := []string{"Implement the change", "Correct the helper only", "Commit current changes to the FeatureArea working branch"}[i]
		seededTasks[i] = f.seedTask(t, map[string]any{"aiSessionId": sid, "featureAreaId": area, "goal": intent}).ID
		proposal := review.ImplementationResult{ReviewDisposition: review.DispositionApprovalReady, Version: 1, Summary: intent, WorkSpec: task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: intent, Constraints: []string{"Do not merge or push"}, CodexInstruction: intent, ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopePaths, Paths: scope}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "authorized task paths", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "git diff --check", Required: true}}, AcceptanceCriteria: []string{"Previous work remains visible"}, CodexInstruction: "Verify this Task without expanding scope"}}

		raw, e := json.Marshal(proposal)
		if e != nil {
			t.Fatal(e)
		}
		backend.message = string(raw)
		// This continuity scenario begins at an already canonical Task. GPT's
		// retired direct task_create transport cannot be used to fabricate that
		// setup; creation admission is covered by the confirmation fixtures.
		created := map[string]any{"id": seededTasks[i]}
		args := closureReviewArgs(sid, area)
		config, err := f.plane.CodexConfigs.Get(f.ctx, codexconfig.DefaultConfigID)
		if err != nil {
			t.Fatal(err)
		}
		selection := config.Selection("localmcp-continuity-fixture")
		args["backend"] = map[string]any{"configId": selection.ConfigID, "configFingerprint": selection.ConfigFingerprint}
		args["taskId"], args["requestId"], args["reviewInstruction"] = created["id"], fmt.Sprintf("review-intent-%d", i), intent
		// This fixture exercises canonical workspace continuity. Local MCP owns
		// the retained direct review command; GPT continues to read the same
		// authoritative workspace and receipts below.
		started := callToolMap(t, f.ctx, local, "task_review_start", args)
		inspecting := awaitClosure(t, backend.closureFactory)
		if ceiling := <-backend.ceilings; ceiling != codex.AuthorizationReadOnly {
			t.Fatalf("Review ceiling=%s", ceiling)
		}
		v, e := f.plane.Reviews.Get(f.ctx, started["review"].(map[string]any)["id"].(string))
		if e != nil {
			t.Fatal(e)
		}
		ws, e := f.plane.Workspaces.GetByFeatureArea(f.ctx, area)
		if e != nil {
			t.Fatal(e)
		}
		tree, e := sqlite.NewWorktreeStore(f.plane.DB).Get(f.ctx, ws.WorktreeID)
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			workspaceID, workspacePath = ws.ID, tree.Path
		} else if ws.ID != workspaceID || tree.Path != workspacePath {
			t.Fatal("next Task replaced canonical workspace")
		}
		if ws.BaseCommit != initial || v.BaseCommit != initial {
			t.Fatalf("source checkout replaced development baseline: %+v %+v", ws, v)
		}
		inspecting.finish()
		gate := awaitApprovalState(t, f, sid, area, "REVIEW_APPROVAL_PENDING")
		if gate.TaskID != seededTasks[i] || gate.CurrentWorkspaceID != workspaceID || gate.CurrentTask == nil || gate.CurrentTask.ID != seededTasks[i] {
			t.Fatalf("post-review authority=%+v", gate)
		}
		current, e := f.plane.Tasks.Get(f.ctx, seededTasks[i])
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			firstTask = current.ID
		} else if current.ID == firstTask {
			t.Fatalf("later intent inherited previous Task authority: task=%s/%s", current.ID, firstTask)
		}
		callToolMap(t, f.ctx, local, "decision_respond", map[string]any{"kind": "REVIEW_APPROVAL", "aiSessionId": sid, "taskId": gate.TaskID, "featureAreaId": area, "reviewId": gate.ReviewID, "decisionId": gate.DecisionID, "option": "REJECT", "requestId": fmt.Sprintf("reject-review-%d", i)})
	}
	var count int
	if e := f.plane.DB.SQL().QueryRow(`SELECT COUNT(*) FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT'`, area).Scan(&count); e != nil || count != 3 {
		t.Fatalf("history=%d %v", count, e)
	}
	latest, e := f.plane.Reviews.ListByFeatureArea(f.ctx, area)
	if e != nil || len(latest) != 3 {
		t.Fatalf("review history=%d %v", len(latest), e)
	}
	for _, v := range latest {
		drift, e := f.plane.ReviewDrift.RepositoryDrift(f.ctx, v.ID, 20)
		if e != nil || !drift.Available || drift.RepositoryChanged {
			t.Fatalf("workspace evolution or source checkout mistaken for integration drift: %+v %v", drift, e)
		}
	}

	t.Log("Three semantic Tasks with owned Reviews; same physical workspace, narrowed authority, context reset, explicit commit and retire; no real Codex")
}

func surfaceCapabilityAvailable(envelope map[string]any, action string) bool {
	workflow, _ := envelope["workflow"].(map[string]any)
	capabilities, _ := workflow["capabilities"].([]any)
	for _, raw := range capabilities {
		c, _ := raw.(map[string]any)
		if c["action"] == action && c["available"] == true {
			return true
		}
	}
	return false
}
