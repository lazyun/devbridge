package mcpserver

import (
	"context"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	bridgegit "github.com/lazyun/devbridge/internal/git"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const DevelopmentBranchCreateInputSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"repositoryId":{"type":"string","minLength":1,"maxLength":128},"developmentRef":{"type":"string","minLength":12,"maxLength":1024,"pattern":"^refs/heads/.+"},"baseCommit":{"type":"string","pattern":"^([0-9a-f]{40}|[0-9a-f]{64})$"}},"required":["aiSessionId","repositoryId","developmentRef","baseCommit"],"additionalProperties":false}`
const DevelopmentBranchCreateOutputSchema = `{"type":"object","properties":{"fullRef":{"type":"string"},"shortName":{"type":"string"},"headSha":{"type":"string"},"repositoryCheckout":{"type":"boolean"}},"required":["fullRef","shortName","headSha","repositoryCheckout"],"additionalProperties":false}`

type developmentBranchCreationService interface {
	CreateDevelopmentBranchForSession(context.Context, developmentcycle.CreateBranchInput) (bridgegit.LocalBranch, error)
}

func (s *StaticService) CreateDevelopmentBranchForSession(ctx context.Context, input developmentcycle.CreateBranchInput) (bridgegit.LocalBranch, error) {
	if s.control == nil {
		return bridgegit.LocalBranch{}, errDevelopmentContextUnavailable
	}
	return s.control.CreateDevelopmentBranchForSession(ctx, input)
}

func registerDevelopmentBranchCreate(server *sdkmcp.Server, service BridgeService) {
	addExternalTool(server, "development_branch_create", "Create Development Branch", "Create the user's exact local refs/heads branch at an immutable baseCommit in the session Project Repository. Atomic creation; an existing identical ref is returned and a different head conflicts. No request receipt, branch selection, checkout, or workflow start. Select the created branch separately.", DevelopmentBranchCreateInputSchema, DevelopmentBranchCreateOutputSchema, stateChangingAnnotations("Create Development Branch", false), func(ctx context.Context, _ *sdkmcp.CallToolRequest, input developmentcycle.CreateBranchInput) (*sdkmcp.CallToolResult, bridgegit.LocalBranch, error) {
		creator, ok := service.(developmentBranchCreationService)
		if !ok {
			return nil, bridgegit.LocalBranch{}, publicToolError(errDevelopmentContextUnavailable)
		}
		value, err := creator.CreateDevelopmentBranchForSession(ctx, input)
		return nil, value, publicToolError(err)
	})
}
