package localmcp_test

import (
	"github.com/lazyun/devbridge/internal/task"
	"testing"
)

// TaskPlan Card confirmation was removed. The durable Task Review gate is
// exercised through fresh transports, including exact cross-project fencing.
func TestTaskReviewGateCrossProjectIsolation(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	current, source := f.pendingReviewGate(t, sid)
	foreign, e := f.plane.Projects.Create(f.ctx, "Other project")
	if e != nil {
		t.Fatal(e)
	}
	other := callToolMap(t, f.ctx, connectLocalMCP(t, f.libreURL), "project_open", map[string]any{"projectId": foreign.ID})["aiSessionId"].(string)
	// GPT resolves this decision only through a purpose-confirmation Card. The
	// Local canonical decision endpoint still proves the foreign-session fence.
	callToolErrorText(t, f.ctx, connectLocalMCP(t, f.libreURL), "review_approval_respond", gateArgs(other, current, source.ID, "APPROVE", "cross-project"))
	unchanged, e := f.plane.Tasks.Get(f.ctx, current.ID)
	if e != nil || unchanged.Phase != task.PhaseReviewApprovalPending || unchanged.CurrentDefinitionRevisionID != current.CurrentDefinitionRevisionID || unchanged.ReviewApprovalID != "" || unchanged.CurrentExecutionID != "" {
		t.Fatalf("foreign mutation: %+v %v", unchanged, e)
	}
}
