package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/worktree"
)

const (
	ReviewRepositorySearchTool = "repository_search"
	ReviewRepositoryReadTool   = "repository_read"
	ReviewRepositoryListTool   = "repository_list"
	interactionBindingWait     = 2 * time.Second
	interactionBindingPoll     = 20 * time.Millisecond
)

type CodexInteractionContext struct {
	ThreadID, TurnID, RunID, ReviewID, ProjectID, FeatureAreaID, TaskID, RepositoryID, RuntimeEpoch string
	Purpose                                                                                         run.Purpose
}

type reviewInteractionRunLookup interface {
	GetByInteraction(context.Context, string, string) (run.Run, error)
}
type reviewProductRuntime interface {
	Get(context.Context, string) (review.Review, error)
	RecordPrompt(context.Context, string, string, time.Time) (review.Review, error)
	RecordAccess(context.Context, string, string, review.AccessOutcome, string) error
}
type reviewRepositoryLookup interface {
	Get(context.Context, string) (repository.Repository, error)
}
type reviewWorktreeLookup interface {
	Get(context.Context, string) (worktree.Worktree, error)
}
type repositoryInspector interface {
	Search(context.Context, string, string, []string, int) (reviewaccess.SearchResult, error)
	Read(context.Context, string, string, int, int) (reviewaccess.ReadResult, error)
	List(context.Context, string, string, int) (reviewaccess.ListResult, error)
}

type ReviewCapabilityDispatcher struct {
	Grants interface {
		Get(context.Context, string) (reviewgrant.Grant, error)
	}
	Runs          reviewInteractionRunLookup
	Reviews       reviewProductRuntime
	Repositories  reviewRepositoryLookup
	Worktrees     reviewWorktreeLookup
	Inspector     repositoryInspector
	Conversations agentconversation.Runtime
}

func ReviewDynamicToolSpecs() []codex.DynamicToolSpec {
	return []codex.DynamicToolSpec{
		{Type: "function", Name: ReviewRepositorySearchTool, Description: "Search tracked files in the authorized Repository for a fixed string without modifying files.", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":4096},"paths":{"type":"array","maxItems":64,"items":{"type":"string","maxLength":4096}},"limit":{"type":"integer","minimum":1,"maximum":100}},"required":["query"],"additionalProperties":false}`)},
		{Type: "function", Name: ReviewRepositoryReadTool, Description: "Read a bounded line range from one file in the authorized Repository using a repository-relative path.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":4096},"startLine":{"type":"integer","minimum":1},"endLine":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`)},
		{Type: "function", Name: ReviewRepositoryListTool, Description: "List a bounded directory in the authorized Repository using a repository-relative path.", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","maxLength":4096},"limit":{"type":"integer","minimum":1,"maximum":200}},"additionalProperties":false}`)},
	}
}

func (d *ReviewCapabilityDispatcher) Handle(ctx context.Context, call codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
	operation := call.Tool
	interaction, root, err := d.resolve(ctx, call.ThreadID, call.TurnID)
	if err != nil {
		code, message := "OPERATION_CONTEXT_UNAVAILABLE", "The authorized Repository context is unavailable for this interaction."
		if errors.Is(err, errRepositoryWrongOperation) {
			code, message = "INVALID_STATE_OPERATION", "Repository Host Tools are not legal for this operation."
		}
		observability.Info(ctx, "execution", "codex.repository.rejected", "Codex Repository capability was rejected", map[string]any{"threadId": call.ThreadID, "turnId": call.TurnID, "errorCode": code})
		return dynamicToolResult(false, map[string]any{"error": map[string]string{"code": code, "message": message}}), nil
	}
	ctx = runtimeepoch.WithHostToolFence(ctx, interaction.RuntimeEpoch, interaction.RunID)
	logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: interaction.ProjectID, FeatureAreaID: interaction.FeatureAreaID, ReviewID: interaction.ReviewID, TaskID: interaction.TaskID, RunID: interaction.RunID})
	data, accessErr := d.dispatch(ctx, root, call)
	outcome, code := "SUCCESS", ""
	if accessErr != nil {
		outcome, code = "FAILURE", "REPOSITORY_CONTEXT_UNAVAILABLE"
		var violation *reviewaccess.Violation
		if errors.As(accessErr, &violation) {
			outcome, code = "FORBIDDEN", string(violation.Code)
		}
	}
	var recordErr error

	reviewCode := code
	if reviewCode == "REPOSITORY_CONTEXT_UNAVAILABLE" {
		reviewCode = "REVIEW_CONTEXT_UNAVAILABLE"
	}
	recordErr = d.Reviews.RecordAccess(context.WithoutCancel(ctx), interaction.ReviewID, operation, review.AccessOutcome(outcome), reviewCode)

	if recordErr != nil {
		accessErr = errors.Join(accessErr, fmt.Errorf("record authoritative Repository access evidence: %w", recordErr))
		outcome, code = "FAILURE", "REPOSITORY_EVIDENCE_UNAVAILABLE"
	}
	component, eventPrefix, label := "review", "review", "Feature Area Review"

	observability.Info(logCtx, component, "codex."+eventPrefix+".repository."+strings.TrimPrefix(operation, "repository_"), "Codex "+label+" Repository capability completed", map[string]any{"threadId": interaction.ThreadID, "turnId": interaction.TurnID, "outcome": outcome, "errorCode": code})
	observability.Info(logCtx, component, eventPrefix+".evidence.observed", "authoritative "+label+" access evidence recorded", map[string]any{"operation": operation, "outcome": outcome, "errorCode": code})
	if accessErr != nil {
		message := "The requested Repository operation was unavailable."
		if outcome == "FORBIDDEN" {
			message = "The requested path is outside the authorized Repository boundary."
		}
		return dynamicToolResult(false, map[string]any{"error": map[string]string{"code": code, "message": message}}), nil
	}
	return dynamicToolResult(true, data), nil
}

var errRepositoryWrongOperation = errors.New("Repository Host Tool is not legal for this operation")

func (d *ReviewCapabilityDispatcher) resolve(ctx context.Context, threadID, turnID string) (CodexInteractionContext, string, error) {
	if d == nil || d.Runs == nil || d.Repositories == nil || d.Worktrees == nil || d.Inspector == nil || d.Conversations == nil {
		return CodexInteractionContext{}, "", errors.New("Repository capability dependencies are incomplete")
	}
	runValue, err := d.awaitInteraction(ctx, threadID, turnID)
	if err != nil || runValue.Status != run.StatusRunning {
		return CodexInteractionContext{}, "", errors.New("Codex interaction is not an active read-only product operation")
	}
	if d.Conversations != nil {
		claim, claimErr := d.Conversations.AuthorizeRun(ctx, runValue.ID)
		if claimErr != nil || claim.RunID != runValue.ID || claim.FeatureAreaID != runValue.FeatureAreaID || string(claim.Kind) != string(runValue.OperationKind) || claim.OperationID != runValue.OperationID || claim.RuntimeEpoch != runValue.RuntimeEpoch {
			return CodexInteractionContext{}, "", errors.New("Codex interaction does not own the active Feature Area operation")
		}
		expected := agentconversation.Review

		if claim.Kind != expected {
			return CodexInteractionContext{}, "", errRepositoryWrongOperation
		}
	}
	if runValue.Purpose != run.PurposeScopeReview || runValue.ReviewID == "" {
		return CodexInteractionContext{}, "", errRepositoryWrongOperation
	}
	projectID, featureAreaID, repositoryID := "", "", ""

	if d.Reviews == nil {
		return CodexInteractionContext{}, "", errors.New("Feature Area Review capability is unavailable")
	}
	reviewValue, reviewErr := d.Reviews.Get(ctx, runValue.ReviewID)
	if reviewErr != nil || reviewValue.TaskID == "" || runValue.TaskID != reviewValue.TaskID || reviewValue.RunID != runValue.ID || runValue.OperationKind != run.OperationReview || runValue.OperationID != reviewValue.ID {
		return CodexInteractionContext{}, "", errors.New("Feature Area Review linkage is unavailable")
	}
	projectID, featureAreaID, repositoryID = reviewValue.ProjectID, reviewValue.FeatureAreaID, reviewValue.RepositoryID
	if d.Grants == nil {
		return CodexInteractionContext{}, "", errors.New("Review grant unavailable")
	}
	grant, grantErr := d.Grants.Get(ctx, reviewValue.ID)
	if grantErr != nil || grant.ReviewID != reviewValue.ID || grant.ProjectID != projectID || grant.FeatureAreaID != featureAreaID || grant.RepositoryID != repositoryID || grant.Mode != reviewgrant.ModeRepository || grant.InitialScope.Kind != run.ReviewScopeRepository || len(grant.InitialScope.Paths) != 0 {
		return CodexInteractionContext{}, "", errors.New("Review grant ownership mismatch")
	}

	if projectID == "" || featureAreaID == "" || runValue.ProjectID != projectID || runValue.FeatureAreaID != featureAreaID {
		return CodexInteractionContext{}, "", errors.New("operation Project and FeatureArea ownership mismatch")
	}
	repositoryValue, err := d.Repositories.Get(ctx, repositoryID)
	if err != nil || repositoryValue.ProjectID != projectID {
		return CodexInteractionContext{}, "", errors.New("Repository mapping is unavailable")
	}
	worktreeValue, err := d.Worktrees.Get(ctx, runValue.WorktreeID)
	// A FeatureArea Workspace deliberately reuses its Worktree across Runs. The
	// Worktree's RunID therefore identifies the allocating Run, while the
	// current Run's durable WorktreeID is the authoritative attachment.
	if err != nil || worktreeValue.ID != runValue.WorktreeID || worktreeValue.RepositoryID != repositoryValue.ID || worktreeValue.Status != worktree.StatusReady {
		return CodexInteractionContext{}, "", errors.New("Worktree mapping is unavailable")
	}
	return CodexInteractionContext{ThreadID: threadID, TurnID: turnID, RunID: runValue.ID, RuntimeEpoch: runValue.RuntimeEpoch, ReviewID: runValue.ReviewID, ProjectID: projectID, FeatureAreaID: featureAreaID, TaskID: runValue.TaskID, RepositoryID: repositoryValue.ID, Purpose: runValue.Purpose}, worktreeValue.Path, nil
}

// awaitInteraction closes the small App Server ordering window in which an
// item/tool/call can arrive immediately after turn/start returns but just
// before the coordinator commits the trusted Thread/Turn pair. It never falls
// back to Thread-only identity or accepts a model-supplied Bridge handle.
func (d *ReviewCapabilityDispatcher) awaitInteraction(ctx context.Context, threadID, turnID string) (run.Run, error) {
	timer := time.NewTimer(interactionBindingWait)
	defer timer.Stop()
	ticker := time.NewTicker(interactionBindingPoll)
	defer ticker.Stop()
	for {
		value, err := d.Runs.GetByInteraction(ctx, threadID, turnID)
		if err == nil {
			return value, nil
		}
		select {
		case <-ctx.Done():
			return run.Run{}, ctx.Err()
		case <-timer.C:
			return run.Run{}, err
		case <-ticker.C:
		}
	}
}

func (d *ReviewCapabilityDispatcher) dispatch(ctx context.Context, root string, call codex.DynamicToolCallParams) (any, error) {
	switch call.Tool {
	case ReviewRepositorySearchTool:
		var input struct {
			Query string   `json:"query"`
			Paths []string `json:"paths"`
			Limit int      `json:"limit"`
		}
		if err := decodeClosedArguments(call.Arguments, &input); err != nil {
			return nil, err
		}
		return d.Inspector.Search(ctx, root, input.Query, input.Paths, input.Limit)
	case ReviewRepositoryReadTool:
		var input struct {
			Path      string `json:"path"`
			StartLine int    `json:"startLine"`
			EndLine   int    `json:"endLine"`
		}
		if err := decodeClosedArguments(call.Arguments, &input); err != nil {
			return nil, err
		}
		return d.Inspector.Read(ctx, root, input.Path, input.StartLine, input.EndLine)
	case ReviewRepositoryListTool:
		var input struct {
			Path  string `json:"path"`
			Limit int    `json:"limit"`
		}
		if err := decodeClosedArguments(call.Arguments, &input); err != nil {
			return nil, err
		}
		return d.Inspector.List(ctx, root, input.Path, input.Limit)
	default:
		return nil, errors.New("unknown Review repository capability")
	}
}

func decodeClosedArguments(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid Review repository arguments")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid Review repository arguments")
	}
	return nil
}

func dynamicToolResult(success bool, value any) codex.DynamicToolCallResponse {
	encoded, err := json.Marshal(value)
	if err != nil {
		success, encoded = false, []byte(`{"error":{"code":"REVIEW_CONTEXT_UNAVAILABLE","message":"The Review repository result is unavailable."}}`)
	}
	return codex.DynamicToolCallResponse{Success: success, ContentItems: []codex.DynamicToolCallOutputContentItem{{Type: "inputText", Text: string(encoded)}}}
}
