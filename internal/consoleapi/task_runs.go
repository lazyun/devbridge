package consoleapi

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

type TaskRunPage struct {
	Items  []presentation.TaskRunDetail `json:"items"`
	Total  int                          `json:"total"`
	Limit  int                          `json:"limit"`
	Offset int                          `json:"offset"`
}

// ListTaskRuns is an adapter over durable Run owners, not another Run registry.
type TaskRunFilter struct {
	PageRequest
	Purpose run.Purpose
}

func (r *Reader) ListTaskRuns(ctx context.Context, projectID, taskID string, request PageRequest) (TaskRunPage, error) {
	return r.ListTaskRunsFiltered(ctx, projectID, taskID, TaskRunFilter{PageRequest: request})
}
func (r *Reader) ListTaskRunsFiltered(ctx context.Context, projectID, taskID string, filter TaskRunFilter) (TaskRunPage, error) {
	request := filter.PageRequest
	if filter.Purpose != "" && filter.Purpose != run.PurposeScopeReview && filter.Purpose != run.PurposeImplementation && filter.Purpose != run.PurposeVerification {
		return TaskRunPage{}, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Task Run purpose must be SCOPE_REVIEW, IMPLEMENTATION or VERIFICATION"}
	}

	if request.Limit == 0 {
		request.Limit = presentation.DefaultTaskRunLimit
	}
	limit, offset, err := r.projectPage(ctx, projectID, request)
	if err != nil {
		return TaskRunPage{}, err
	}
	t, err := r.plane.Tasks.Get(ctx, taskID)
	if err != nil || t.ProjectID != projectID || !semanticProductTask(t) {
		return TaskRunPage{}, notFound()
	}
	values, err := r.plane.Runs.ListByTask(ctx, t.ID)
	if err != nil {
		return TaskRunPage{}, controlapi.MapError(err)
	}
	executions, err := r.plane.ExecutionProposals.ListByTask(ctx, t.ID)
	if err != nil {
		return TaskRunPage{}, controlapi.MapError(err)
	}
	reviews, err := r.plane.Reviews.ListByFeatureArea(ctx, t.FeatureAreaID)
	if err != nil {
		return TaskRunPage{}, controlapi.MapError(err)
	}
	exact := make([]run.Run, 0, len(values))
	seen := map[string]bool{}
	for _, v := range values {
		if !seen[v.ID] && (filter.Purpose == "" || v.Purpose == filter.Purpose) && presentation.TaskRunOwned(t, v, executions, reviews) {
			exact = append(exact, v)
			seen[v.ID] = true
		}
	}
	sort.Slice(exact, func(i, j int) bool {
		if exact[i].CreatedAt.Equal(exact[j].CreatedAt) {
			return exact[i].ID < exact[j].ID
		}
		return exact[i].CreatedAt.Before(exact[j].CreatedAt)
	})
	out := TaskRunPage{Items: []presentation.TaskRunDetail{}, Total: len(exact), Limit: limit, Offset: offset}
	now := time.Now()
	for i := offset; i < len(exact) && i-offset < limit; i++ {
		d := presentation.TaskRunSummary(exact[i], now)
		presentation.AttachTaskRunConfig(&d, executions, reviews)
		out.Items = append(out.Items, d)
	}
	return out, nil
}

// requireTaskRun adds the explicit Task route fence to the Project diagnostic
// boundary. No identity falls back to an AgentExecution or current candidate.
func (r *Reader) requireTaskRun(ctx context.Context, projectID, taskID, runID string) error {
	if err := r.requireProject(ctx, projectID); err != nil {
		return err
	}
	if err := validID("task_id", taskID); err != nil {
		return err
	}
	t, err := r.plane.Tasks.Get(ctx, taskID)
	if err != nil || t.ProjectID != projectID {
		return notFound()
	}
	v, err := r.plane.Runs.Get(ctx, runID)
	if err != nil {
		return notFound()
	}
	var executions []executionproposal.Proposal
	var reviews []review.Review
	if v.ExecutionProposalID != "" {
		e, err := r.plane.ExecutionProposals.GetForProject(ctx, projectID, v.ExecutionProposalID)
		if err != nil {
			return notFound()
		}
		executions = append(executions, e)
	}
	if v.ReviewID != "" {
		review, err := r.plane.Reviews.Get(ctx, v.ReviewID)
		if err != nil {
			return notFound()
		}
		reviews = append(reviews, review)
	}
	if !presentation.TaskRunOwned(t, v, executions, reviews) {
		return &controlapi.Error{Code: controlapi.CodeNotFound, Message: "Run not found under the requested Task owner"}
	}
	return nil
}

func (r *Reader) GetRunForTask(ctx context.Context, projectID, taskID, runID string) (RunDetail, error) {
	if err := r.requireTaskRun(ctx, projectID, taskID, runID); err != nil {
		return RunDetail{}, err
	}
	return r.GetRun(ctx, projectID, runID)
}
func (r *Reader) GetRunPromptForTask(ctx context.Context, projectID, taskID, runID string) (RunPromptDiagnostic, error) {
	if err := r.requireTaskRun(ctx, projectID, taskID, runID); err != nil {
		return RunPromptDiagnostic{}, err
	}
	return r.GetRunPrompt(ctx, projectID, runID)
}

// Bound the read copy while preserving RawMessage objects (runtime forms must
// never regress to a JSON string or malformed truncated JSON).
func boundedRunRuntime(value run.Run) *agentruntime.Snapshot {
	if value.AgentRuntime == nil || value.AgentRuntime.RunID != value.ID {
		return nil
	}
	a := value.AgentRuntime
	if a.ProjectID != "" && a.ProjectID != value.ProjectID || a.TaskID != "" && a.TaskID != value.TaskID || a.FeatureAreaID != "" && a.FeatureAreaID != value.FeatureAreaID || a.ThreadID != "" && a.ThreadID != value.ThreadID || a.TurnID != "" && a.TurnID != value.TurnID {
		return nil
	}
	raw, err := json.Marshal(value.AgentRuntime)
	if err != nil {
		return nil
	}
	var object any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&object) != nil {
		return nil
	}
	redactSecrets(object)
	object = boundRuntimeValue(object)
	raw, err = json.Marshal(object)
	if err != nil {
		return nil
	}
	var result agentruntime.Snapshot
	if json.Unmarshal(raw, &result) != nil {
		return nil
	}
	for i := range result.Requests {
		result.Requests[i].Payload, _ = approval.SafePresentationPayload(result.Requests[i].Payload)
		if len(result.Requests[i].Resolution) > 0 {
			result.Requests[i].Resolution, _ = approval.SafePresentationPayload(result.Requests[i].Resolution)
		}
		for j := range result.Requests[i].AvailableResponses {
			result.Requests[i].AvailableResponses[j], _ = approval.SafePresentationPayload(result.Requests[i].AvailableResponses[j])
		}
	}
	for i := range result.Items {
		if len(result.Items[i].Metadata) > 0 {
			var cut bool
			result.Items[i].Metadata, cut = approval.SafePresentationPayload(result.Items[i].Metadata)
			result.Items[i].Truncated = result.Items[i].Truncated || cut
		}
	}
	if result.CurrentItem != nil && len(result.CurrentItem.Metadata) > 0 {
		var cut bool
		result.CurrentItem.Metadata, cut = approval.SafePresentationPayload(result.CurrentItem.Metadata)
		result.CurrentItem.Truncated = result.CurrentItem.Truncated || cut
	}
	return &result
}
func boundRuntimeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = boundRuntimeValue(item)
		}
		return v
	case []any:
		if len(v) > agentruntime.MaxItems {
			v = v[len(v)-agentruntime.MaxItems:]
		}
		for i := range v {
			v[i] = boundRuntimeValue(v[i])
		}
		return v
	case string:
		return summarizeOnly(v, agentruntime.MaxOutput)
	default:
		return v
	}
}

// TaskDefinitionDetail reads one immutable, already bounded authored revision.
// It has no update counterpart: successor specifications use existing decisions.
type TaskDefinitionDetail struct {
	ID            string          `json:"id"`
	TaskID        string          `json:"taskId"`
	Ordinal       int             `json:"ordinal"`
	Current       bool            `json:"current"`
	PayloadSHA256 string          `json:"payloadSha256"`
	CreatedAt     time.Time       `json:"createdAt"`
	Definition    task.Definition `json:"definition"`
}

func (r *Reader) GetTaskDefinitionRevision(ctx context.Context, projectID, taskID, revisionID string) (TaskDefinitionDetail, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return TaskDefinitionDetail{}, err
	}
	if err := validID("revision_id", revisionID); err != nil {
		return TaskDefinitionDetail{}, err
	}
	owner, err := r.plane.Tasks.Get(ctx, taskID)
	if err != nil || owner.ProjectID != projectID || !semanticProductTask(owner) {
		return TaskDefinitionDetail{}, notFound()
	}
	revision, err := r.plane.Tasks.GetDefinitionRevision(ctx, revisionID)
	if err != nil {
		return TaskDefinitionDetail{}, controlapi.MapError(err)
	}
	if revision.TaskID != owner.ID || revision.Definition.ProjectID != projectID || revision.Definition.FeatureAreaID != owner.FeatureAreaID {
		return TaskDefinitionDetail{}, notFound()
	}
	return TaskDefinitionDetail{ID: revision.ID, TaskID: revision.TaskID, Ordinal: revision.Ordinal, Current: revision.ID == owner.CurrentDefinitionRevisionID, PayloadSHA256: revision.PayloadSHA256, CreatedAt: revision.CreatedAt, Definition: revision.Definition}, nil
}
