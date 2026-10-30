package controlapi

import (
	"github.com/lazyun/devbridge/internal/agentconversation"
	"strings"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// taskActionKind is local to the read adapter, not a command registry. Runtime
// requests and FeatureArea actions keep their own eligibility and entry points.
func taskActionKind(action string) bool {
	switch action {
	case "COMPLETE_TASK", "DEFER_TASK", "CONTINUE_TASK", "CONFIRM_IMPLEMENTATION", "CONFIRM_AND_START_IMPLEMENTATION", "REQUEST_TASK_REVIEW", "START_TASK_REVIEW", "APPROVE", "APPROVE_AND_START", "REVISE", "REJECT", "START_EXECUTION", "START_VERIFICATION", "ACCEPT", "REQUEST_CHANGES", "REQUEST_REVERIFICATION", "RETRY_IMPLEMENTATION", "CANCEL_EXECUTION", "RECOVER", "RETRY_TASK_COMMIT":
		return true
	}
	return false
}

// assembleTaskActions is the only assembler of current Task actions. It consumes
// completed, exact Core reads; it does not authorize commands or change phase.
// Call after every independent eligibility enrichment and safety blocker.
func assembleTaskActions(out *p.Projection, t task.Task) {
	v := out.TaskView
	if v == nil {
		return
	}
	defer func() {
		attachVerificationChoiceActions(out, t)
		v.CodexContinuation = p.CodexContinuationFor(*v)
	}()
	v.Actions, v.ActionsStatus, v.ActionsReadError = []p.Capability{}, "AVAILABLE", ""
	if v.ID != t.ID || out.Details.TaskID != t.ID || out.Details.ProjectID != t.ProjectID || out.Details.FeatureAreaID != t.FeatureAreaID {
		v.ActionsStatus, v.ActionsReadError = "UNAVAILABLE", "Task action ownership changed; refresh required."
	} else if t.Type == task.TypeDevelopment {
		if err := validateTaskPresentationGuidance(t, out.Guidance); err != nil {
			v.ActionsStatus, v.ActionsReadError = "UNAVAILABLE", err.Error()
		}
	}
	candidates := []p.Capability{}
	proposalError := ""
	if g := out.Guidance; g != nil && g.Approval != nil && g.Approval.Proposal != nil {
		if err := task.ValidateExecutableVerificationSpec(g.Approval.Proposal.VerificationSpec); err != nil {
			proposalError = "验证方案尚不可执行，请修订原审查方案：" + err.Error()
		}
	}
	compatible := []p.Capability{}
	for _, c := range out.Capabilities {
		// The same label may belong to a runtime decision; never consume that owner.
		runtime := out.PendingDecision != nil && out.PendingDecision.Kind == "RUNTIME_APPROVAL" && c.Target != nil && c.Target.DecisionID == out.PendingDecision.ID
		otherTask := c.Target != nil && c.Target.TaskID != "" && c.Target.TaskID != t.ID
		if !taskActionKind(c.Action) || runtime || otherTask {
			compatible = append(compatible, c)
			continue
		}
		candidates = append(candidates, c)
	}
	// Reuse the existing complete acceptance/verification constraints, including
	// runtime disposition. A phase or an allowedActions string alone is insufficient.
	d, a := out.PendingDecision, v.CurrentExecutionAcceptance
	if d != nil && d.Kind == "TASK_ACCEPTANCE" && a != nil && a.Receipt == nil && a.DecisionID == d.ID &&
		d.SubjectID == t.CurrentExecutionID && v.CurrentExecution != nil && v.CurrentExecution.ID == t.CurrentExecutionID &&
		v.CurrentExecution.TaskDefinitionRevisionID == t.CurrentDefinitionRevisionID && out.Guidance != nil &&
		out.Guidance.NextAction.Code == "RESOLVE_TASK_ACCEPTANCE" && out.Guidance.DecisionID == d.ID {
		for _, action := range a.AllowedActions {
			candidates = append(candidates, p.Capability{Action: action, Available: true, DecisionOption: action, Target: &p.ActionTarget{TaskID: t.ID, FeatureAreaID: t.FeatureAreaID, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, ExecutionID: t.CurrentExecutionID, SubjectID: t.CurrentExecutionID, DecisionID: d.ID}})
		}
	}
	type identity struct {
		Action           string
		HasTarget        bool
		Target           p.ActionTarget
		HasUpperVersion  bool
		UpperVersion     agentconversation.UpperVersion
		HasReviewRequest bool
		ReviewRequest    wp.TaskReviewRequestTarget
	}
	positions := map[identity]int{}
	for _, candidate := range candidates {
		// An implementation channel is an exact approved fact, never inferred
		// from the Review executor, instructions or an absent legacy field.
		if candidate.Action == "START_EXECUTION" && v.ImplementationMode == "DIRECT_FILE" {
			continue
		}
		if candidate.Action == "START_EXECUTION" && v.ImplementationMode == "UNKNOWN" {
			continue
		}
		c := candidate
		if inputs, declared := taskActionRequiredInputs(c.Action); declared {
			c.RequiredInputs = inputs
		}
		if c.Action == "START_EXECUTION" && v.CurrentExecution != nil && v.CurrentExecution.ID == t.CurrentExecutionID && v.CurrentExecution.TaskDefinitionRevisionID == t.CurrentDefinitionRevisionID {
			c.RequiredInputs = nil // This existing candidate already owns its frozen selection.
		}
		if c.Action == "APPROVE" && v.ImplementationMode == "CODING_AGENT" {
			c.RequiredInputs = []string{"nextRunRuntimeConfig"}
		}
		if c.Target != nil {
			target := *c.Target
			c.Target = &target // Never mutate the independent source projection.
			if target.ExpectedReviewRequest != nil {
				request := *target.ExpectedReviewRequest
				c.Target.ExpectedReviewRequest = &request
			}
			if target.ExpectedUpperVersion != nil {
				version := *target.ExpectedUpperVersion
				c.Target.ExpectedUpperVersion = &version
			}
			if target.TaskID == "" && target.FeatureAreaID == t.FeatureAreaID && exactTaskOperation(t, v, target) {
				c.Target.TaskID = t.ID
			}
			// These are immutable Task facts already validated against Core guidance.
			if (c.Action == "START_EXECUTION" || c.Action == "START_VERIFICATION") && target.TaskID == t.ID && target.TaskDefinitionRevisionID == "" {
				c.Target.TaskDefinitionRevisionID = t.CurrentDefinitionRevisionID
			}
			if (c.Action == "START_EXECUTION" || c.Action == "START_VERIFICATION") && target.TaskID == t.ID && v.ExecutionStartTarget != nil {
				c.Target.PrimaryMemberID = v.ExecutionStartTarget.PrimaryMemberID
				c.Target.ApprovedWorksetHash = v.ExecutionStartTarget.ApprovedWorksetHash
			}
		}
		reason := ""
		switch {
		case v.ActionsStatus != "AVAILABLE":
			reason = v.ActionsReadError
		case proposalError != "" && (c.Action == "APPROVE" || c.Action == "APPROVE_AND_START"):
			reason = proposalError
		case c.Action == "REQUEST_CHANGES" && v2CodingDefinition(t):
			reason = requestChangesRequiresReview + ": " + requestChangesRequiresReviewFailure().Message
		case out.LifecycleStatus == "RETIRED" || out.LifecycleStatus == "RETIRING" && !currentUpperCancellation(v, t, c):
			reason = "FeatureArea engineering authority is closed."
		case t.LegacyReadOnly || out.Guidance != nil && out.Guidance.Legacy || out.TaskStages != nil && out.TaskStages.ReadOnly:
			reason = "Historical or legacy Task actions are read only."
		case c.Available && !taskActionTargetCurrent(out, t, c):
			reason = "Task action target is stale or incomplete; refresh required."
		case c.Action != "RETRY_TASK_COMMIT" && v.Terminal:
			reason = "This Task is terminal."
		}
		if reason != "" {
			c.Available, c.Reason = false, reason
		}
		if !c.Available && c.Reason == "" {
			c.Reason = "Core does not currently permit this operation."
		}
		key := identity{Action: c.Action, HasTarget: c.Target != nil}
		if c.Target != nil {
			key.Target = *c.Target
			if c.Target.ExpectedReviewRequest != nil {
				key.HasReviewRequest, key.ReviewRequest = true, *c.Target.ExpectedReviewRequest
				key.Target.ExpectedReviewRequest = nil
			}
			if c.Target.ExpectedUpperVersion != nil {
				key.HasUpperVersion, key.UpperVersion = true, *c.Target.ExpectedUpperVersion
				key.Target.ExpectedUpperVersion = nil
			}
		}
		if index, ok := positions[key]; ok {
			// A conflicting negative observation is never overwritten by available=true.
			previous := &v.Actions[index]
			if !c.Available {
				previous.Available, previous.Reason = false, c.Reason
			}
			if previous.DecisionOption != c.DecisionOption {
				previous.Available, previous.Reason = false, "Conflicting Task action facts; refresh required."
			}
			continue
		}
		positions[key] = len(v.Actions)
		v.Actions = append(v.Actions, c)
	}
	// Compatibility outputs are views of the same result, never another button rule.
	out.Capabilities = append(compatible, v.Actions...)
	if d != nil && (d.Kind == "REVIEW_APPROVAL" || d.Kind == "TASK_ACCEPTANCE") {
		d.AllowedActions = []string{}
		for _, c := range v.Actions {
			if c.Available && c.Target != nil && c.Target.DecisionID == d.ID && c.Target.SubjectID == d.SubjectID {
				d.AllowedActions = append(d.AllowedActions, c.Action)
			}
		}
	}
}

func currentUpperCancellation(v *p.TaskView, t task.Task, c p.Capability) bool {
	r := v.CurrentReview
	return r != nil && r.ExecutorKind == review.ExecutorUpperLLM && r.ID == t.CurrentReviewID && r.Run != nil && exactUpperCancellation(c, t.FeatureAreaID, t.ID, r.ID, r.Run.ID)
}

// Exact eligibility comes from Core. This only prevents a generic retirement
// display guard from hiding the existing Upper authority-revocation command.
func exactUpperCancellation(c p.Capability, areaID, taskID, reviewID, runID string) bool {
	x := c.Target
	return c.Available && c.Action == "CANCEL_EXECUTION" && x != nil && x.FeatureAreaID == areaID && x.TaskID == taskID && x.OperationKind == "REVIEW" && x.OperationID == reviewID && x.RunID == runID && runID != "" && x.AgentConversationID == ""
}

func exactTaskOperation(t task.Task, v *p.TaskView, target p.ActionTarget) bool {
	switch target.OperationKind {
	case "TASK":
		if target.OperationID != t.ID {
			return false
		}
		if target.RunID == "" {
			return true
		}
		r := v.CurrentReview
		return r != nil && r.ID == t.CurrentReviewID && r.ExecutorKind == review.ExecutorUpperLLM && r.Run != nil && r.Run.ID == target.RunID && r.UpperAccess != nil && target.ExpectedUpperVersion.Valid() && r.UpperAccess.Target.RuntimeEpoch == target.ExpectedUpperVersion.GrantEpoch && r.UpperAccess.Target.GrantGeneration == target.ExpectedUpperVersion.GrantGeneration && (r.UpperAccess.State == "PREPARING" || r.UpperAccess.State == "RECOVERY_REQUIRED")
	case "REVIEW":
		return target.OperationID != "" && target.OperationID == t.CurrentReviewID && v.CurrentReview != nil && v.CurrentReview.ID == target.OperationID && v.CurrentReview.Run != nil && v.CurrentReview.Run.ID == target.RunID
	case "EXECUTION":
		return target.OperationID != "" && target.OperationID == t.CurrentExecutionID && v.CurrentExecution != nil && (target.RunID == v.CurrentExecution.ImplementationRunID || target.RunID == v.CurrentExecution.VerificationRunID)
	}
	return false
}

func taskActionTargetCurrent(out *p.Projection, t task.Task, c p.Capability) bool {
	x, v, g := c.Target, out.TaskView, out.Guidance
	if x == nil || x.TaskID != t.ID || x.FeatureAreaID != t.FeatureAreaID {
		return false
	}
	switch c.Action {
	case "RETRY_TASK_COMMIT":
		if out.Commit == nil || !out.Commit.CanRetry || out.Commit.RetryTaskID != t.ID {
			return false
		}
		if out.Commit.RetryOperation != nil {
			return x.RepositoryID == out.Commit.RetryOperation.RepositoryID && x.OperationID == out.Commit.RetryOperation.OperationID
		}
		return x.RepositoryID == "" && x.OperationID == ""
	case "CONFIRM_IMPLEMENTATION", "CONFIRM_AND_START_IMPLEMENTATION", "START_TASK_REVIEW", "START_EXECUTION", "START_VERIFICATION":
		if g == nil || g.NextAction.Code != c.Action || g.CurrentTask == nil || g.CurrentTask.ID != t.ID || g.CurrentTask.Status != string(task.StatusActive) || x.SubjectID != t.ID || x.ExecutionID != t.CurrentExecutionID {
			return false
		}
		return c.Action == "START_TASK_REVIEW" || x.TaskDefinitionRevisionID != "" && x.TaskDefinitionRevisionID == t.CurrentDefinitionRevisionID
	case "REQUEST_TASK_REVIEW":
		r := x.ExpectedReviewRequest
		return r != nil && x.SubjectID == t.ID && r.Phase == t.Phase && r.TaskDefinitionRevisionID == t.CurrentDefinitionRevisionID && r.CurrentReviewID == t.CurrentReviewID && r.CurrentExecutionID == t.CurrentExecutionID
	case "RETRY_IMPLEMENTATION":
		// This candidate was read from the canonical pure retry eligibility, not
		// inferred from phase, report text, or the generic acceptance menu.
		return v.CurrentExecution != nil && v.CurrentExecutionAcceptance != nil && v.CurrentExecutionAcceptance.Receipt == nil && x.ExecutionID == t.CurrentExecutionID && x.SubjectID == t.CurrentExecutionID && x.TaskDefinitionRevisionID != "" && x.TaskDefinitionRevisionID == t.CurrentDefinitionRevisionID && x.DecisionID == v.CurrentExecutionAcceptance.DecisionID
	case "APPROVE", "APPROVE_AND_START", "REVISE", "REJECT", "ACCEPT", "REQUEST_CHANGES", "REQUEST_REVERIFICATION":
		d := out.PendingDecision
		if d == nil || g == nil || x.DecisionID == "" || x.DecisionID != d.ID || x.DecisionID != g.DecisionID || x.SubjectID != d.SubjectID {
			return false
		}
		if d.Kind == "REVIEW_APPROVAL" {
			if c.Action != "APPROVE" && c.Action != "APPROVE_AND_START" && c.Action != "REVISE" && c.Action != "REJECT" {
				return false
			}
			a := g.Approval
			if a == nil || a.Status != "PENDING" || a.TaskID != t.ID || a.ReviewID != t.CurrentReviewID || a.DecisionID != d.ID || x.SubjectID != t.ID {
				return false
			}
			for _, option := range a.AllowedActions {
				if option == c.Action {
					return true
				}
			}
			return false
		}
		if d.Kind != "TASK_ACCEPTANCE" || v.CurrentExecutionAcceptance == nil || v.CurrentExecutionAcceptance.Receipt != nil || g.NextAction.Code != "RESOLVE_TASK_ACCEPTANCE" || x.ExecutionID != t.CurrentExecutionID || x.SubjectID != t.CurrentExecutionID || x.TaskDefinitionRevisionID == "" || x.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID {
			return false
		}
		for _, option := range v.CurrentExecutionAcceptance.AllowedActions {
			if option == c.Action {
				return true
			}
		}
		return false
	case "CANCEL_EXECUTION", "RECOVER":
		if x.OperationKind == "REVIEW" && v.CurrentReview != nil && v.CurrentReview.ExecutorKind == review.ExecutorUpperLLM && !x.ExpectedUpperVersion.Valid() {
			return false
		}
		if !exactTaskOperation(t, v, *x) {
			return false
		}
		if c.Action == "RECOVER" && (x.RunID == "" || x.OperationKind == "TASK" || x.OperationKind == "EXECUTION" && strings.TrimSpace(x.AgentConversationID) == "") {
			return false
		}
		return x.OperationKind != "EXECUTION" || x.ExecutionID == "" || x.ExecutionID == t.CurrentExecutionID
	}
	return false
}

// taskActionRequiredInputs names only user-supplied business payload. Exact
// target, decision identity and request id are projection/transport facts and
// are deliberately not represented as editable form fields. In particular,
// APPROVE does not gain a synthetic form merely because a
// UI submits a request; REQUEST_CHANGES requires a complete replacement
// definition as required by the canonical acceptance command.
func taskActionRequiredInputs(action string) ([]string, bool) {
	switch action {
	case "REQUEST_CHANGES":
		return []string{"nextWorkSpec", "nextVerificationSpec", "nextRunRuntimeConfig"}, true
	case "REQUEST_TASK_REVIEW":
		return []string{"reviewPurpose"}, true
	case "START_TASK_REVIEW":
		return []string{"executorKind", "reviewDepth", "reviewEngineeringGoal", "reviewInstruction", "reviewFocus", "model", "reasoningEffort", "codexFastMode"}, true
	case "APPROVE_AND_START":
		return []string{"nextRunRuntimeConfig"}, true
	case "START_EXECUTION":
		return []string{"nextRunRuntimeConfig", "backendSelection"}, true
	default:
		return nil, false
	}
}
