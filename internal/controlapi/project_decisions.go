package controlapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

// projectPendingDecisionsPartialError records non-authoritative detail reads
// that were skipped while assembling an otherwise usable project inventory.
// Its presence never invalidates the returned decisions; exact resolvers still
// perform their normal ownership and stale checks.
type projectPendingDecisionsPartialError struct {
	featureAreaReads int
	taskReads        int
	approvalRunReads int
}

func (e *projectPendingDecisionsPartialError) hasFailures() bool {
	return e.featureAreaReads != 0 || e.taskReads != 0 || e.approvalRunReads != 0
}

func (e *projectPendingDecisionsPartialError) Error() string {
	return fmt.Sprintf("Some pending-decision details could not be read (%d area, %d task, %d approval owner); refresh before resolving.", e.featureAreaReads, e.taskReads, e.approvalRunReads)
}

// ProjectPendingDecisionsForSession inventories the current Project's exact
// Core decision/action projections. It is intentionally a read-only adapter:
// it does not infer decisions from phase values and it does not create a new
// resolver or authorization path.
func (f *Facade) ProjectPendingDecisionsForSession(ctx context.Context, sessionID string) ([]presentation.PendingDecision, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	tasks, err := f.deps.Tasks.List(ctx)
	if err != nil {
		return nil, MapError(err)
	}
	out := make([]presentation.PendingDecision, 0)
	seen := make(map[string]struct{})
	partial := &projectPendingDecisionsPartialError{}
	byTask := make(map[string]task.Task, len(tasks))
	for _, value := range tasks {
		if value.ProjectID == projectID {
			byTask[value.ID] = value
		}
	}
	defaultModel := func(id string) string { return byTask[id].Model }
	runtimeOptions := func() *presentation.TaskOptions {
		selection := f.ExecutionAllowlist()
		return &presentation.TaskOptions{Models: selection.Models, ReasoningEfforts: selection.ReasoningEfforts, ModelConfigs: selection.Clone().ModelConfigs}
	}
	appendProjection := func(value presentation.Projection, owner, taskID, areaID string, includeDecision bool) {
		// Runtime approvals are collected from the scoped approval store below,
		// where their Run can establish the exact Task owner. Area projections
		// must not win de-duplication with an ownerless approximation.
		if includeDecision && value.PendingDecision != nil && (value.PendingDecision.Kind == "REVIEW_APPROVAL" || value.PendingDecision.Kind == "TASK_ACCEPTANCE") {
			key := value.PendingDecision.Kind + ":" + value.PendingDecision.ID + ":" + taskID + ":" + areaID
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				item := pendingFromProjection(value, owner, taskID, areaID)
				if item.Kind == "REVIEW_APPROVAL" || item.Kind == "TASK_ACCEPTANCE" {
					item.RuntimeOptions = runtimeOptions()
					item.DefaultModel = defaultModel(taskID)
				}
				out = append(out, item)
			}
		}
	}
	// Each Task read is the existing exact-task Core projection. This covers
	// pending Review Approval and Task Acceptance without borrowing the selected
	// workspace Task or manufacturing state from Task.phase.
	for _, value := range tasks {
		if value.ProjectID != projectID || value.FeatureAreaID == "" {
			continue
		}
		projection, readErr := f.TaskPresentationForSession(ctx, sessionID, value.FeatureAreaID, value.ID)
		if readErr != nil {
			partial.taskReads++
			continue
		}
		appendProjection(projection, "TASK", value.ID, value.FeatureAreaID, true)
	}
	// Runtime approvals can be associated with a Run that is not the current
	// selected Task projection. They remain runtime decisions and use the
	// existing approval resolver; they never alter Task workflow semantics.
	approvals := make([]approval.Approval, 0)
	for offset := 0; ; offset += MaxListLimit {
		page, listErr := f.ListApprovalsForSession(ctx, sessionID, ApprovalFilter{PendingOnly: true, ListOptions: ListOptions{Limit: MaxListLimit, Offset: offset}})
		if listErr != nil {
			return nil, listErr
		}
		approvals = append(approvals, page...)
		if len(page) < MaxListLimit {
			break
		}
	}
	for _, value := range approvals {
		key := "decision:RUNTIME_APPROVAL:" + value.ID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		item := runtimePendingDecision(value)
		if runValue, readErr := f.deps.Runs.Get(ctx, value.RunID); readErr == nil {
			if owner, ok := byTask[runValue.TaskID]; ok {
				item.TaskID, item.FeatureAreaID, item.Owner = owner.ID, owner.FeatureAreaID, "TASK"
			}
		} else {
			partial.approvalRunReads++
		}
		out = append(out, item)
	}
	// Codex App Server requests are runtime-owned decisions too. Keep their
	// official request identity/payload intact for runtime_request_respond; they
	// never become Task business decisions.
	runs, err := f.deps.Runs.List(ctx)
	if err != nil {
		return nil, MapError(err)
	}
	for _, value := range runs {
		if value.AgentRuntime == nil {
			continue
		}
		owner, ownedByTask := byTask[value.TaskID]
		if value.ProjectID != "" && value.ProjectID != projectID || value.ProjectID == "" && !ownedByTask {
			continue
		}
		for _, request := range value.AgentRuntime.Requests {
			if request.ProjectID != "" && request.ProjectID != projectID {
				continue
			}
			if !agentruntime.OpenRequest(request) {
				continue
			}
			requestKey := request.ID
			if requestKey == "" { // historical App Server observations predate runtimeRequestId.
				requestKey = string(request.RequestID) + ":" + request.ConnectionID + ":" + request.ItemID
			}
			key := "runtime-request:" + value.ID + ":" + requestKey
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			item := runtimeRequestPendingDecision(value, request, owner)
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i].UpdatedAt, out[j].UpdatedAt
		// Known updates remain newest first; unknown updates sort last with
		// the same exact identity tie-breaker, never a fabricated timestamp.
		if left == nil || right == nil {
			if left != nil || right != nil {
				return left != nil
			}
			return pendingKey(out[i]) < pendingKey(out[j])
		}
		if left.Equal(*right) {
			return pendingKey(out[i]) < pendingKey(out[j])
		}
		return left.After(*right)
	})
	if partial.hasFailures() {
		return out, partial
	}
	return out, nil
}

func pendingFromProjection(value presentation.Projection, owner, taskID, areaID string) presentation.PendingDecision {
	decision := *value.PendingDecision
	options := make([]presentation.Capability, 0, len(decision.AllowedActions))
	seen := make(map[string]bool)
	allowed := make(map[string]bool)
	for _, action := range decision.AllowedActions {
		allowed[action] = true
	}
	for _, cap := range value.Capabilities {
		if allowed[cap.Action] && cap.Target != nil && cap.Target.DecisionID == decision.ID && (cap.Target.TaskID == "" || cap.Target.TaskID == taskID) && (cap.Target.FeatureAreaID == "" || cap.Target.FeatureAreaID == areaID) {
			key := cap.Action + ":" + actionTargetKey(cap.Target, taskID, areaID)
			if !seen[key] {
				options = append(options, cap)
				seen[key] = true
			}
		}
	}
	state := value.CurrentState
	if state == "" {
		state = value.WorkflowStatus
	}
	return presentation.PendingDecision{Kind: decision.Kind, Owner: owner, TaskID: taskID, FeatureAreaID: areaID, SemanticState: state, Decision: &decision, Options: options, RequiredInput: requiredDecisionInput(decision.Kind), CreatedAt: decision.CreatedAt, UpdatedAt: presentation.OptionalDecisionTime(value.UpdatedAt)}
}

func runtimePendingDecision(value approval.Approval) presentation.PendingDecision {
	cancelEffect := "CANCEL_LEGACY_RUN"
	if value.WorkflowOwnedRun {
		cancelEffect = "DECLINE_ACTION"
	}
	decision := presentation.Decision{CreatedAt: presentation.OptionalDecisionTime(value.CreatedAt), Kind: "RUNTIME_APPROVAL", ID: "RUNTIME_APPROVAL:" + value.ID, SubjectID: value.ID, Summary: "Runtime approval required: " + string(value.Kind), Guarantee: "AUTHORITATIVE_ENTITY_STATE", CancelEffect: cancelEffect, AllowedActions: make([]string, 0, len(value.AllowedDecisions))}
	options := make([]presentation.Capability, 0, len(value.AllowedDecisions))
	for _, option := range value.AllowedDecisions {
		if option == approval.DecisionGrantRequestedPermissions {
			normalized, err := approval.NormalizeDecision(value, approval.Decision{Type: option})
			if err == nil {
				decision.Permissions = normalized.Permissions
			}
		}
		decision.AllowedActions = append(decision.AllowedActions, string(option))
		options = append(options, presentation.Capability{Action: string(option), DecisionOption: string(option), Available: true, Target: &presentation.ActionTarget{DecisionID: decision.ID, SubjectID: value.ID, RunID: value.RunID}})
	}
	request, truncated := approval.SafePresentationPayload(value.RequestPayload)
	return presentation.PendingDecision{Kind: decision.Kind, Owner: "RUNTIME", SemanticState: string(value.Status), Decision: &decision, Options: options, RuntimeApprovalRequest: request, RuntimeApprovalRequestTruncated: truncated, RequiredInput: requiredDecisionInput(decision.Kind), CreatedAt: presentation.OptionalDecisionTime(value.CreatedAt), UpdatedAt: presentation.OptionalDecisionTime(value.CreatedAt)}
}

// runtimeRequestPendingDecision preserves the exact durable user-decision
// request. Only a current CODEX_REQUEST in WAITING_FOR_USER with a complete
// identity is actionable; recorded and unknown requests stay in the projection
// as read-only evidence rather than being relabelled as waiting.
func runtimeRequestPendingDecision(value run.Run, request agentruntime.Request, owner task.Task) presentation.PendingDecision {
	var observation *agentruntime.RuntimeObservation
	turnStatus := ""
	if value.AgentRuntime != nil {
		observation = value.AgentRuntime.Observation
		turnStatus = value.AgentRuntime.TurnStatus
	}
	projection := agentruntime.ProjectRequest(request, observation, turnStatus)
	request.Projection = &projection
	copy := request
	taskID, areaID := value.TaskID, value.FeatureAreaID
	if owner.ID != "" {
		taskID, areaID = owner.ID, owner.FeatureAreaID
	}
	actionable := request.Kind == "CODEX_REQUEST" && projection.LifecycleState == "WAITING_FOR_USER" && agentruntime.CompleteDecisionIdentity(request.Identity) && len(projection.AvailableActions) != 0
	options := []presentation.Capability(nil)
	if actionable {
		options = []presentation.Capability{{Action: "SUBMIT_AGENT_USER_DECISION", Available: true, Target: &presentation.ActionTarget{TaskID: taskID, FeatureAreaID: areaID, RunID: value.ID, AgentConversationID: request.AgentConversationID, SubjectID: request.ID}}}
	}
	kind := request.Kind
	if kind == "" {
		kind = "RUNTIME_REQUEST" // retained only for historical rows.
	}
	return presentation.PendingDecision{Kind: kind, Owner: "RUNTIME", TaskID: taskID, FeatureAreaID: areaID, SemanticState: request.State, Options: options, RequiredInput: []string{"exact runtime request identity", "canonical user decision"}, RuntimeRequest: &copy, RuntimeRequestActionable: actionable, CreatedAt: presentation.OptionalDecisionTime(request.CreatedAt), UpdatedAt: presentation.OptionalDecisionTime(request.UpdatedAt)}
}

func requiredDecisionInput(kind string) []string {
	switch kind {
	case "TASK_ACCEPTANCE":
		return []string{"requestId", "exact taskId", "exact featureAreaId", "exact executionId", "exact taskDefinitionRevisionId"}
	case "REVIEW_APPROVAL":
		return []string{"requestId", "exact taskId", "exact decisionId"}
	case "RUNTIME_APPROVAL":
		return []string{"exact approvalId", "permissions when required"}
	}
	return []string{}
}

func requiredActionInput(action string) []string {
	if action == "CANCEL_EXECUTION" || action == "RECOVER" {
		return []string{"exact action target"}
	}
	return []string{}
}

func actionTargetKey(target *presentation.ActionTarget, taskID, areaID string) string {
	if target == nil {
		return taskID + ":" + areaID
	}
	encoded, _ := json.Marshal(struct {
		Target         presentation.ActionTarget
		TaskID, AreaID string
	}{*target, taskID, areaID})
	return string(encoded)
}

func pendingKey(value presentation.PendingDecision) string {
	if value.Decision != nil {
		return value.Decision.ID
	}
	return value.Kind + ":" + actionTargetKey(firstTarget(value.Options), value.TaskID, value.FeatureAreaID)
}

func firstTarget(options []presentation.Capability) *presentation.ActionTarget {
	if len(options) == 0 {
		return nil
	}
	return options[0].Target
}
