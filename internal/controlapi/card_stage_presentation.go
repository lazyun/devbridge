package controlapi

import (
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

// projectCardStages decorates the captured selected graph. It does not change
// guidance, legacy progress, capabilities or domain state. Plan telemetry is
// deliberately not an input to stage completion.
func projectCardStages(out *presentation.Projection, current CurrentWorkflow) {
	t := current.Task
	if t == nil || (t.Type != "" && t.Type != task.TypeDevelopment) || out.Details.TaskID != t.ID {
		return
	}
	view := &presentation.TaskStages{TaskID: t.ID, Title: t.Title, Status: string(t.Status), Phase: string(t.Phase), Approved: t.ReviewApprovalID != ""}
	if view.Title == "" {
		view.Title = t.WorkSpec.Goal
	}
	if view.Title == "" {
		view.Title = t.Goal
	}
	for _, phase := range []string{"IMPLEMENTATION", "VERIFICATION"} {
		view.Stages = append(view.Stages, presentation.PhaseProgress{Phase: phase, Status: "PENDING"})
	}
	activeRun := t.Work.RunID
	if e := current.Execution; e != nil && e.TaskID == t.ID && activeRun == "" {
		activeRun = e.VerificationRunID
		if activeRun == "" {
			activeRun = e.ImplementationRunID
		}
		if activeRun == "" {
			activeRun = e.RunID
		}
	}

	// A FeatureArea's current operation can belong to a different historical Task.
	// Its telemetry must not leak into this selected Task's stage summary.
	out.AgentRuntime = nil
	out.Details.RunID = ""
	for _, r := range current.Runs {
		if r.TaskID != t.ID || r.ProjectID != t.ProjectID || r.FeatureAreaID != t.FeatureAreaID {
			continue
		}
		index := -1

		if e := current.Execution; e != nil && e.TaskID == t.ID && r.ExecutionProposalID == e.ID {
			impl := e.ImplementationRunID
			if impl == "" {
				impl = e.RunID
			}
			if r.ID == impl && r.Purpose == run.PurposeImplementation {
				index = 0
			}
			if r.ID == e.VerificationRunID && r.Purpose == run.PurposeVerification {
				index = 1
			}
			if index >= 0 {
				view.Stages[index] = presentation.PhaseProgress{Phase: string(r.Purpose), Status: string(r.Status), SourceID: r.ID}
			}
		}
		if r.ID == activeRun {
			out.Details.RunID = r.ID
			if r.AgentRuntime != nil && r.AgentRuntime.RunID == r.ID {
				summary := r.AgentRuntime.Summary()
				out.AgentRuntime = &summary
			}
		}
	}
	if e := current.Execution; e != nil && e.TaskID == t.ID && e.Outcome == "SUCCEEDED" && e.VerificationSpec.Mode == task.VerificationModeNone {
		view.Stages[1].Status = "WAIVED"
	}
	out.TaskStages = view
	out.ProductProgress = presentation.TaskPhaseProgress(t.Phase, t.Status)
	ownsRecovery := false
	if op := current.AgentOperation; op != nil && op.Claim != nil {
		for _, r := range current.Runs {
			if r.ID == activeRun && r.ID == op.Claim.RunID && r.TaskID == t.ID && r.ProjectID == t.ProjectID && r.FeatureAreaID == t.FeatureAreaID {
				ownsRecovery = true
			}
		}
	}
	if out.Cancellation != nil && out.Cancellation.RunID == activeRun {
		ownsRecovery = true
	}
	if ownsRecovery {
		view.Recovery = out.Recovery
	}

}

// Task navigation follows the durable current Review pointer.
func selectTaskPresentationReview(current *CurrentWorkflow) {
	current.ActiveReview, current.CurrentReview, current.LatestReview = nil, nil, nil
	if current.Task == nil || current.Task.CurrentReviewID == "" {
		return
	}
	for _, value := range current.Reviews {
		if value.ID == current.Task.CurrentReviewID && value.TaskID == current.Task.ID && value.ProjectID == current.Task.ProjectID && value.FeatureAreaID == current.Task.FeatureAreaID {
			copy := value
			current.CurrentReview = &copy
			current.LatestReview = &copy
			if op := current.AgentOperation; op != nil && op.Claim != nil && op.Claim.OperationID == copy.ID && op.Claim.RunID == copy.RunID {
				current.ActiveReview = &copy
			}
			return
		}
	}
}
