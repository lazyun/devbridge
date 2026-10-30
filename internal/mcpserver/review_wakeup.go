package mcpserver

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"

	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Process-local presentation reservations coordinate separately mounted Cards.
// No SQLite, workflow state, business authority or claim of host delivery.
// Cards reserve only after non-message preflight and transport selection.
// A reservation is not a host delivery receipt; it is never rolled back.
// Never expire/evict a successful reservation: capacity exhaustion fails closed.
const reviewWakeupCapacity = 10000

type reviewWakeupIdentity struct {
	AISessionID   string `json:"aiSessionId"`
	ProjectID     string `json:"projectId"`
	FeatureAreaID string `json:"featureAreaId"`
	ReviewID      string `json:"reviewId"`
	// RunID is optional for older Review projections. When supplied it is part of
	// the exact event key and must match Core's current Review detail.
	RunID            string `json:"runId,omitempty"`
	ResultSHA256     string `json:"resultSha256"`
	ProvenanceSHA256 string `json:"provenanceSha256"`
	DecisionID       string `json:"decisionId"`
}
type reviewWakeupResult struct {
	Status string `json:"status"`
}
type reviewWakeupClaims struct {
	mu        sync.Mutex
	attempted map[reviewWakeupIdentity]struct{}
}

func (l *reviewWakeupClaims) reserve(in reviewWakeupIdentity) (reviewWakeupResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// RunID is checked against Core before reservation, but it is deliberately not
	// part of the legacy event key: an older Card without RunID and a newer Card
	// with the same verified Review must share one notification reservation.
	key := in
	key.RunID = ""
	if _, found := l.attempted[key]; found {
		return reviewWakeupResult{Status: "ALREADY_ATTEMPTED"}, nil
	}
	if len(l.attempted) >= reviewWakeupCapacity {
		return reviewWakeupResult{}, errors.New("presentation wakeup capacity reached; request the summary in normal chat")
	}
	l.attempted[key] = struct{}{}
	return reviewWakeupResult{Status: "CLAIMED"}, nil
}
func eligibleReviewWakeup(in reviewWakeupIdentity, p presentation.Projection) bool {
	g := p.Guidance
	if g == nil || g.WorkflowMode != "CURRENT" || p.WorkflowMode != "" && p.WorkflowMode != "CURRENT" || g.Legacy || g.RefreshRequired || g.TaskID == "" || g.TaskPhase != "REVIEW_APPROVAL_PENDING" || g.CurrentState != "REVIEW_APPROVAL_PENDING" || p.CurrentState != "REVIEW_APPROVAL_PENDING" {
		return false
	}
	if g.ProjectID != in.ProjectID || g.FeatureAreaID != in.FeatureAreaID || p.FeatureArea.ID != in.FeatureAreaID || p.LifecycleStatus == "RETIRING" || p.LifecycleStatus == "RETIRED" {
		return false
	}
	if p.Cancellation != nil || p.Recovery != nil && p.Recovery.Required || p.TaskStages != nil && p.TaskStages.ReadOnly {
		return false
	}
	a, r, d := g.Approval, g.CurrentWorkflowReview, p.PendingDecision
	if a == nil || r == nil || d == nil || a.TaskID != g.TaskID || r.Status != "SUCCEEDED" || r.ID != in.ReviewID || g.ReviewID != in.ReviewID || a.ReviewID != in.ReviewID || a.Status != "PENDING" || g.DecisionID != in.DecisionID || a.DecisionID != in.DecisionID || d.ID != in.DecisionID || d.SubjectID != g.TaskID || d.Kind != "REVIEW_APPROVAL" {
		return false
	}
	if p.CurrentWorkflowReview != nil && (p.CurrentWorkflowReview.ID != r.ID || p.CurrentWorkflowReview.Status != r.Status) || p.Details.ReviewID != "" && p.Details.ReviewID != r.ID {
		return false
	}
	if in.RunID != "" && p.Details.RunID != in.RunID {
		return false
	}
	for _, digest := range []string{in.ResultSHA256, in.ProvenanceSHA256} {
		b, err := hex.DecodeString(digest)
		if err != nil || len(b) != 32 {
			return false
		}
	}
	return a.ResultSHA256 == in.ResultSHA256 && a.ProvenanceSHA256 == in.ProvenanceSHA256
}
func registerReviewWakeupClaim(server *sdkmcp.Server, service BridgeService) {
	ledger := &reviewWakeupClaims{attempted: make(map[reviewWakeupIdentity]struct{})}
	schema := json.RawMessage(`{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"projectId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"reviewId":{"type":"string","minLength":1,"maxLength":128},"runId":{"type":"string","minLength":1,"maxLength":128},"decisionId":{"type":"string","minLength":1,"maxLength":256},"resultSha256":{"type":"string","pattern":"^[a-f0-9]{64}$"},"provenanceSha256":{"type":"string","pattern":"^[a-f0-9]{64}$"}},"required":["aiSessionId","projectId","featureAreaId","reviewId","decisionId","resultSha256","provenanceSha256"],"additionalProperties":false}`)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "dev_bridge_review_wakeup_claim", Title: "Reserve Review summary notification", Description: "App-only SILENT: reserve the one process-local Review wakeup message attempt only after the Card has completed non-message preflight and selected a host follow-up transport. ALREADY_ATTEMPTED does not prove host delivery. No Review, Decision, Task or other business state is changed or authorized. Explicit user retry is separate.", InputSchema: schema, Annotations: toolAnnotations("Reserve Review summary notification", true, true), Meta: sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}}}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in reviewWakeupIdentity) (*sdkmcp.CallToolResult, reviewWakeupResult, error) {
		reader, ok := service.(featureAreaPresentationReader)
		if !ok {
			return nil, reviewWakeupResult{}, publicToolError(errors.New("presentation unavailable"))
		}
		p, err := reader.FeatureAreaPresentationForSession(ctx, in.AISessionID, in.FeatureAreaID)
		if err != nil {
			return nil, reviewWakeupResult{}, publicToolError(err)
		}
		if !eligibleReviewWakeup(in, p) {
			return nil, reviewWakeupResult{}, publicToolError(errors.New("exact Review is no longer eligible for a pending approval summary"))
		}
		result, err := ledger.reserve(in)
		return &sdkmcp.CallToolResult{Meta: sdkmcp.Meta{"emission": "SILENT"}}, result, publicToolError(err)
	})
}
