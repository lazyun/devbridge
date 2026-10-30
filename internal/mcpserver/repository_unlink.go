package mcpserver

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/controlapi"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type repositoryUnlinkToolService interface {
	PrepareRepositoryUnlink(context.Context, controlapi.RepositoryUnlinkPrepareInput) (controlapi.RepositoryUnlinkProjection, error)
	ReadRepositoryUnlink(context.Context, string, string) (controlapi.RepositoryUnlinkProjection, error)
	CommitRepositoryUnlink(context.Context, controlapi.RepositoryUnlinkCommitInput) (controlapi.RepositoryUnlinkProjection, error)
}

type repositoryUnlinkPrepareInput struct {
	RequestID    string `json:"requestId"`
	ProjectID    string `json:"projectId"`
	RepositoryID string `json:"repositoryId"`
}
type repositoryUnlinkCommitInput struct {
	RequestID                string `json:"requestId"`
	ProjectID                string `json:"projectId"`
	RepositoryID             string `json:"repositoryId"`
	ExpectedGeneration       int64  `json:"expectedRegistrationGeneration"`
	ExpectedTopologyRevision int64  `json:"expectedTopologyRevision"`
	Reason                   string `json:"reason"`
}

const repositoryUnlinkPrepareSchema = `{"type":"object","properties":{"requestId":{"type":"string","minLength":1,"maxLength":128},"projectId":{"type":"string","minLength":1,"maxLength":128},"repositoryId":{"type":"string","minLength":1,"maxLength":128}},"required":["requestId","projectId","repositoryId"],"additionalProperties":false}`
const repositoryUnlinkReadSchema = `{"type":"object","properties":{"projectId":{"type":"string","minLength":1,"maxLength":128},"repositoryId":{"type":"string","minLength":1,"maxLength":128}},"required":["projectId","repositoryId"],"additionalProperties":false}`
const repositoryUnlinkCommitSchema = `{"type":"object","properties":{"requestId":{"type":"string","minLength":1,"maxLength":128},"projectId":{"type":"string","minLength":1,"maxLength":128},"repositoryId":{"type":"string","minLength":1,"maxLength":128},"expectedRegistrationGeneration":{"type":"integer","minimum":1},"expectedTopologyRevision":{"type":"integer","minimum":1},"reason":{"type":"string","maxLength":4096}},"required":["requestId","projectId","repositoryId","expectedRegistrationGeneration","expectedTopologyRevision","reason"],"additionalProperties":false}`
const repositoryUnlinkOutputSchema = `{"type":"object","properties":{"repositoryId":{"type":"string"},"repositoryName":{"type":"string"},"repositoryPath":{"type":"string"},"registrationState":{"type":"string","enum":["ACTIVE","UNLINKED"]},"registrationGeneration":{"type":"integer","minimum":1},"unlinkedAt":{"type":"string"},"historyAvailable":{"type":"boolean"},"availableActions":{"type":"array","items":{"type":"string"}},"unlinkEligibility":{"type":"object"},"confirmation":{"type":"object"}},"required":["repositoryId","repositoryName","repositoryPath","registrationState","registrationGeneration","historyAvailable","availableActions","unlinkEligibility"],"additionalProperties":false}`

func registerRepositoryUnlinkTools(server *sdkmcp.Server, service BridgeService) {
	provider, ok := service.(repositoryUnlinkToolService)
	if !ok {
		return
	}
	addExternalTool(server, "repository_unlink_prepare", "Prepare repository unlink", "Purely read the exact Project Repository unlink eligibility, blockers and frozen registration/topology generations. It does not delete local files or managed worktrees, create a receipt, close a DevelopmentCycle, start a model, or alter work.", repositoryUnlinkPrepareSchema, repositoryUnlinkOutputSchema, toolAnnotations("Prepare repository unlink", true, true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in repositoryUnlinkPrepareInput) (*sdkmcp.CallToolResult, controlapi.RepositoryUnlinkProjection, error) {
		out, err := provider.PrepareRepositoryUnlink(ctx, controlapi.RepositoryUnlinkPrepareInput{RequestID: in.RequestID, ProjectID: in.ProjectID, RepositoryID: in.RepositoryID})
		return nil, out, publicToolError(err)
	})
	addExternalTool(server, "repository_unlink_read", "Read repository unlink", "Pure current-state read of an exact Project Repository identity, including retained UNLINKED history. This does not mutate Core.", repositoryUnlinkReadSchema, repositoryUnlinkOutputSchema, toolAnnotations("Read repository unlink", true, true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in repositoryUnlinkPrepareInput) (*sdkmcp.CallToolResult, controlapi.RepositoryUnlinkProjection, error) {
		out, err := provider.ReadRepositoryUnlink(ctx, in.ProjectID, in.RepositoryID)
		return nil, out, publicToolError(err)
	})
	addExternalTool(server, "repository_unlink_commit", "Commit repository unlink", "Commit only the exact prepared Project Repository unlink generations. Core rechecks all blockers under its writer transaction. It never accepts force, an outcome, blocker bypass, replacement targets, relink, transfer, deletion, or worktree cleanup.", repositoryUnlinkCommitSchema, repositoryUnlinkOutputSchema, stateChangingAnnotations("Commit repository unlink", true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in repositoryUnlinkCommitInput) (*sdkmcp.CallToolResult, controlapi.RepositoryUnlinkProjection, error) {
		out, err := provider.CommitRepositoryUnlink(ctx, controlapi.RepositoryUnlinkCommitInput{RequestID: in.RequestID, ProjectID: in.ProjectID, RepositoryID: in.RepositoryID, ExpectedGeneration: in.ExpectedGeneration, ExpectedTopologyRevision: in.ExpectedTopologyRevision, Reason: in.Reason})
		return nil, out, publicToolError(err)
	})
}

func (s *StaticService) PrepareRepositoryUnlink(ctx context.Context, in controlapi.RepositoryUnlinkPrepareInput) (controlapi.RepositoryUnlinkProjection, error) {
	if s.control == nil {
		return controlapi.RepositoryUnlinkProjection{}, errors.New("repository unlink unavailable")
	}
	return s.control.PrepareRepositoryUnlink(ctx, in)
}
func (s *StaticService) ReadRepositoryUnlink(ctx context.Context, projectID, repositoryID string) (controlapi.RepositoryUnlinkProjection, error) {
	if s.control == nil {
		return controlapi.RepositoryUnlinkProjection{}, errors.New("repository unlink unavailable")
	}
	return s.control.ReadRepositoryUnlink(ctx, projectID, repositoryID)
}
func (s *StaticService) CommitRepositoryUnlink(ctx context.Context, in controlapi.RepositoryUnlinkCommitInput) (controlapi.RepositoryUnlinkProjection, error) {
	if s.control == nil {
		return controlapi.RepositoryUnlinkProjection{}, errors.New("repository unlink unavailable")
	}
	return s.control.CommitRepositoryUnlink(ctx, in)
}
