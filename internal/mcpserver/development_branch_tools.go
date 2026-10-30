package mcpserver

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/developmentcycle"
)

const DevelopmentBranchListDescription = "List exact selectable local branches and current durable development context for every registered Repository in the session Project. Read-only; checkout is factual and does not select a development branch."
const DevelopmentBranchSelectDescription = "Record the development branch explicitly chosen by the user. Requires an exact refs/heads/ ref and stable requestId. Replays return the original cycle; changed payloads conflict. Reselection preserves history. This changes Dev Bridge metadata only and never performs Git checkout or Git mutation."
const DevelopmentCycleOutputSchema = `{"type":"object","properties":{"id":{"type":"string"},"projectId":{"type":"string"},"repositoryId":{"type":"string"},"developmentRef":{"type":"string"},"selectedHeadSha":{"type":"string"},"createdAt":{"type":"string"},"requestId":{"type":"string"},"endReason":{"type":"string"},"selectedByAiSessionId":{"type":"string"},"supersededAt":{"anyOf":[{"type":"null"},{"type":"string"}]}},"required":["id","projectId","repositoryId","developmentRef","selectedHeadSha","createdAt","requestId","supersededAt"],"additionalProperties":false}`
const DevelopmentContextOutputSchema = `{"type":"object","properties":{"projectId":{"type":"string"},"developmentBranchSelectionRequired":{"type":"boolean"},"repositories":{"type":"array","items":{"type":"object","properties":{"repositoryId":{"type":"string"},"repositoryName":{"type":"string"},"developmentRef":{"type":"string"},"branchShortName":{"type":"string"},"selectedHeadSha":{"type":"string"},"currentHeadSha":{"type":"string"},"developmentRefStatus":{"type":"string"},"statusCode":{"type":"string"},"repositoryCheckoutRef":{"type":"string"},"developmentCycle":{"anyOf":[{"type":"null"},` + DevelopmentCycleOutputSchema + `]},"developmentBranchSelectionRequired":{"type":"boolean"},"refChanged":{"anyOf":[{"type":"null"},{"type":"boolean"}]},"repositoryCheckoutMatchesDevelopmentRef":{"anyOf":[{"type":"null"},{"type":"boolean"}]},"repositoryCheckoutDetached":{"type":"boolean"},"branches":{"type":"array","items":{"type":"object","properties":{"fullRef":{"type":"string"},"shortName":{"type":"string"},"headSha":{"type":"string"},"repositoryCheckout":{"type":"boolean"}},"required":["fullRef","shortName","headSha","repositoryCheckout"],"additionalProperties":false}}},"required":["repositoryId","repositoryName","developmentRef","branchShortName","selectedHeadSha","currentHeadSha","developmentRefStatus","repositoryCheckoutRef","developmentCycle","developmentBranchSelectionRequired","refChanged","repositoryCheckoutMatchesDevelopmentRef","repositoryCheckoutDetached","branches"],"additionalProperties":false}},"availableActions":{"type":"array","items":{"type":"string"}}},"required":["projectId","developmentBranchSelectionRequired","repositories","availableActions"],"additionalProperties":false}`
const DevelopmentBranchSelectInputSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"repositoryId":{"type":"string","minLength":1,"maxLength":128},"developmentRef":{"type":"string","minLength":1,"maxLength":1024,"pattern":"^refs/heads/.+"},"requestId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","repositoryId","developmentRef","requestId"],"additionalProperties":false}`
const DevelopmentBranchListInputSchema = sessionInputSchema

type DevelopmentBranchListInput struct {
	AISessionID string `json:"aiSessionId"`
}
type DevelopmentBranchSelectInput struct {
	AISessionID    string `json:"aiSessionId"`
	RepositoryID   string `json:"repositoryId"`
	DevelopmentRef string `json:"developmentRef"`
	RequestID      string `json:"requestId"`
}

type developmentBranchService interface {
	DevelopmentContextForSession(context.Context, string) (developmentcycle.Context, error)
	SelectDevelopmentBranchForSession(context.Context, developmentcycle.SelectInput) (developmentcycle.Cycle, error)
}

var errDevelopmentContextUnavailable = errors.New("development context unavailable")

func (s *StaticService) DevelopmentContextForSession(ctx context.Context, sessionID string) (developmentcycle.Context, error) {
	if s.control == nil {
		return developmentcycle.Context{}, errDevelopmentContextUnavailable
	}
	return s.control.DevelopmentContextForSession(ctx, sessionID)
}
func (s *StaticService) SelectDevelopmentBranchForSession(ctx context.Context, input developmentcycle.SelectInput) (developmentcycle.Cycle, error) {
	if s.control == nil {
		return developmentcycle.Cycle{}, errDevelopmentContextUnavailable
	}
	return s.control.SelectDevelopmentBranchForSession(ctx, input)
}

func DevelopmentBranchListCore(ctx context.Context, service BridgeService, input DevelopmentBranchListInput) (developmentcycle.Context, error) {
	provider, ok := service.(developmentBranchService)
	if !ok {
		return developmentcycle.Context{}, publicToolError(errDevelopmentContextUnavailable)
	}
	value, err := provider.DevelopmentContextForSession(ctx, input.AISessionID)
	return value, publicToolError(err)
}
func DevelopmentBranchSelectCore(ctx context.Context, service BridgeService, input DevelopmentBranchSelectInput) (developmentcycle.Cycle, error) {
	provider, ok := service.(developmentBranchService)
	if !ok {
		return developmentcycle.Cycle{}, publicToolError(errDevelopmentContextUnavailable)
	}
	value, err := provider.SelectDevelopmentBranchForSession(ctx, developmentcycle.SelectInput{AISessionID: input.AISessionID, RepositoryID: input.RepositoryID, DevelopmentRef: input.DevelopmentRef, RequestID: input.RequestID})
	return value, publicToolError(err)
}

// Project opening wraps workflow focus, while retaining the same development context port.
func (s projectOpenProjectionService) DevelopmentContextForSession(ctx context.Context, sessionID string) (developmentcycle.Context, error) {
	if provider, ok := s.BridgeService.(developmentBranchService); ok {
		return provider.DevelopmentContextForSession(ctx, sessionID)
	}
	return developmentcycle.Context{}, errDevelopmentContextUnavailable
}
func (s projectOpenProjectionService) SelectDevelopmentBranchForSession(ctx context.Context, input developmentcycle.SelectInput) (developmentcycle.Cycle, error) {
	if provider, ok := s.BridgeService.(developmentBranchService); ok {
		return provider.SelectDevelopmentBranchForSession(ctx, input)
	}
	return developmentcycle.Cycle{}, errDevelopmentContextUnavailable
}
