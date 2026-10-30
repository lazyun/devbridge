package localmcp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexconfig"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// Reset is intentionally exercised through both transports: GPT creates the
// FeatureArea, Local MCP rotates it, and GPT performs the next rotation. Both
// registrations call the same explicit agent_context_reset product surface.
func TestFeatureAreaAgentContextResetAcrossGPTAndLocalMCP(t *testing.T) {
	fixture := newIntegrationFixture(t)
	gpt := connectGPT(t, fixture.gptURL)
	local := connectLocalMCP(t, fixture.libreURL)

	gptSession := callToolMap(t, fixture.ctx, gpt, "project_open", map[string]any{"projectId": fixture.projectID})["aiSessionId"].(string)
	localSession := callToolMap(t, fixture.ctx, local, "project_open", map[string]any{"projectId": fixture.projectID})["aiSessionId"].(string)
	created := callToolMap(t, fixture.ctx, gpt, "featureArea_create", map[string]any{"aiSessionId": gptSession, "title": "Reset context", "problemStatement": "Rotate the coding context explicitly."})
	featureAreaID := created["id"].(string)

	reset := callToolMap(t, fixture.ctx, local, "agent_context_reset", map[string]any{"aiSessionId": localSession, "featureAreaId": featureAreaID, "requestId": "reset-local-1"})
	if reset["featureAreaId"] != featureAreaID {
		t.Fatalf("local reset returned %+v", reset)
	}
	first, err := fixture.plane.AgentConversations.Current(fixture.ctx, featureAreaID)
	if err != nil || first.PredecessorID == "" || first.BackendThreadID != "" {
		t.Fatalf("first reset current=%+v err=%v", first, err)
	}
	// Replaying the same request is durable idempotency, not a third rotation.
	callToolMap(t, fixture.ctx, local, "agent_context_reset", map[string]any{"aiSessionId": localSession, "featureAreaId": featureAreaID, "requestId": "reset-local-1"})
	values, err := fixture.plane.AgentConversations.ListByFeatureArea(fixture.ctx, featureAreaID)
	if err != nil || len(values) != 2 || !containsConversation(values, first.ID, false) {
		t.Fatalf("reset replay lineage=%+v err=%v", values, err)
	}

	callToolMap(t, fixture.ctx, gpt, "agent_context_reset", map[string]any{"aiSessionId": gptSession, "featureAreaId": featureAreaID, "requestId": "reset-gpt-2"})
	current, err := fixture.plane.AgentConversations.Current(fixture.ctx, featureAreaID)
	if err != nil || current.PredecessorID != first.ID {
		t.Fatalf("GPT reset current=%+v err=%v", current, err)
	}
	values, err = fixture.plane.AgentConversations.ListByFeatureArea(fixture.ctx, featureAreaID)
	if err != nil || len(values) != 3 || !containsConversation(values, current.ID, false) || !containsConversation(values, first.ID, true) {
		t.Fatalf("cross-adapter lineage=%+v err=%v", values, err)
	}
	// A durable Review reservation blocks reset through the same external
	// surface, regardless of which adapter issued the reset request.
	head, err := fixture.plane.Repositories.CurrentHEAD(fixture.ctx, fixture.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	intentID := closureTaskReviewArgs(t, fixture, gptSession, featureAreaID)["taskId"].(string)
	reviewValue, err := fixture.plane.Reviews.Create(fixture.ctx, fixture.frozenCodingReview(t, productreview.Review{ReviewDepth: "DEEP", TaskID: intentID, ProjectID: fixture.projectID, FeatureAreaID: featureAreaID, CreatedByAISessionID: gptSession, RepositoryID: fixture.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, BaseCommit: head, Focus: productreview.Focus{Kind: productreview.FocusRepository, Summary: "Reset busy test"}, EngineeringGoal: "Reserve review", Instruction: "Read repository"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.NewRunStore(fixture.plane.DB).CreateProductScopeReviewAttempt(fixture.ctx, reviewValue.ID, "review-reset-busy", "reserve", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if text := callToolErrorText(t, fixture.ctx, gpt, "agent_context_reset", map[string]any{"aiSessionId": gptSession, "featureAreaId": featureAreaID, "requestId": "reset-busy"}); !strings.Contains(text, "FEATURE_AREA_BUSY") {
		t.Fatalf("busy reset error=%s", text)
	}

}

func containsConversation(values []agentconversation.Conversation, id string, ended bool) bool {
	for _, value := range values {
		if value.ID == id && (value.EndedAt != nil) == ended {
			return true
		}
	}
	return false
}
