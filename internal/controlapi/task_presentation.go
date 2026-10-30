package controlapi

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// A missing or inconsistent observation is unavailable, not historical. Both
// full and compact Task projections must retain the same exact Core ownership.
func validateTaskPresentationGuidance(t task.Task, g *wp.Guidance) error {
	if g == nil || g.RefreshRequired || (g.WorkflowMode != "CURRENT" && !g.Legacy) {
		return errors.New("Task workflow guidance unavailable; refresh required")
	}
	if g.ProjectID != t.ProjectID || g.FeatureAreaID != t.FeatureAreaID || g.TaskID != t.ID || g.TaskPhase != t.Phase || g.ReviewID != t.CurrentReviewID || g.ExecutionID != t.CurrentExecutionID || g.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID {
		return errors.New("Task current pointers changed after guidance; refresh required")
	}
	if t.Status == task.StatusActive && g.CurrentTask == nil || g.CurrentTask != nil && (g.CurrentTask.ID != t.ID || g.CurrentTask.Status != string(t.Status) || g.CurrentTask.Phase != t.Phase) {
		return errors.New("Task lifecycle changed after guidance; refresh required")
	}
	if a := g.Approval; a != nil && (a.TaskID != t.ID || a.ReviewID != t.CurrentReviewID || a.DecisionID == "" || a.DecisionID != g.DecisionID) {
		return errors.New("Task Review approval changed after guidance; refresh required")
	}
	return nil
}

func (f *Facade) taskView(ctx context.Context, t task.Task) (presentation.TaskView, error) {
	var values []run.Run
	var err error
	if reader, ok := f.deps.Runs.(interface {
		ListByTask(context.Context, string) ([]run.Run, error)
	}); ok {
		values, err = reader.ListByTask(ctx, t.ID)
	} else {
		values, err = f.deps.Runs.List(ctx)
	}
	if err != nil {
		return presentation.TaskView{}, err
	}
	var goals []taskgoal.Progress
	if f.deps.ExecutionGoals != nil && t.Work.ExecutionProposalID != "" {
		goals, err = f.deps.ExecutionGoals.ListForExecution(ctx, t.Work.ExecutionProposalID)
		if err != nil {
			return presentation.TaskView{}, err
		}
	}
	var executions []executionproposal.Proposal
	if reader, ok := f.deps.ExecutionProposals.(interface {
		ListByTask(context.Context, string) ([]executionproposal.Proposal, error)
	}); ok {
		executions, err = reader.ListByTask(ctx, t.ID)
		if err != nil {
			return presentation.TaskView{}, err
		}
	}
	var receipts []decisionresolution.Receipt
	if reader, ok := f.deps.DecisionResolutions.(interface {
		ListTaskAcceptance(context.Context, string) ([]decisionresolution.Receipt, error)
	}); ok {
		receipts, err = reader.ListTaskAcceptance(ctx, t.ID)
		if err != nil {
			return presentation.TaskView{}, err
		}
	}
	var reviews []review.Review
	if f.deps.Reviews != nil && t.Type == task.TypeDevelopment {
		reviews, err = f.deps.Reviews.ListByFeatureArea(ctx, t.FeatureAreaID)
		if err != nil {
			return presentation.TaskView{}, err
		}
	}
	if t.Type == task.TypeDevelopment {
		exact := make([]run.Run, 0, len(values))
		for _, value := range values {
			if presentation.TaskRunOwned(t, value, executions, reviews) {
				exact = append(exact, value)
			}
		}
		values = exact
	}
	v := presentation.BuildTaskWithHistory(t, values, goals, executions, receipts, time.Now())
	if f.deps.RecoveryStatus != nil {
		for _, candidate := range values {
			if candidate.OperationID != t.CurrentExecutionID && candidate.ReviewID != t.CurrentReviewID {
				continue
			}
			record, e := f.deps.RecoveryStatus.LatestForSource(ctx, candidate.ID)
			if e == nil {
				projection := f.recoveryStatusResult(ctx, record).Projection
				v.Activity.RecoveryStatus = &projection
				break
			}
			if v.Activity.RecoveryStatus == nil {
				projection := recoveryStatusRequired(candidate)
				if projection.Required {
					v.Activity.RecoveryStatus = &projection
				}
			}
		}
	}
	if t.CurrentDefinitionRevisionID != "" {
		if reader, ok := f.deps.Tasks.(interface {
			GetDefinitionRevision(context.Context, string) (task.DefinitionRevision, error)
		}); ok {
			revision, e := reader.GetDefinitionRevision(ctx, t.CurrentDefinitionRevisionID)
			if e != nil {
				return presentation.TaskView{}, e
			}
			if revision.TaskID != t.ID || revision.Definition.ProjectID != t.ProjectID || revision.Definition.FeatureAreaID != t.FeatureAreaID {
				return presentation.TaskView{}, notFound()
			}
			work, verification := task.CloneWorkSpec(revision.Definition.WorkSpec), task.CloneVerificationSpec(revision.Definition.VerificationSpec)
			v.CurrentWorkSpec, v.CurrentVerificationSpec = &work, &verification
			v.ImplementationMode = taskImplementationMode(string(work.ImplementationMode))
			v.ExecutionStartTarget = taskExecutionStartTarget(revision.Definition.ApplyTo(t), executions)
		}
	}
	presentation.AttachTaskReviews(&v, t, reviews, values)
	presentation.ApplyTaskRunRecovery(&v)
	v.NextStep = presentation.TaskNextStepFor(v)
	if f.operatorProject != "" {
		// Stage/label/result facts were derived above; the Session's file target is private.
		if v.CurrentReview != nil {
			v.CurrentReview.UpperAccess = nil
		}
		for i := range v.ReviewHistory {
			v.ReviewHistory[i].UpperAccess = nil
		}
	}
	for i := range v.Details.Runs {
		presentation.AttachTaskRunConfig(&v.Details.Runs[i], executions, reviews)
	}
	v.Details.DefinitionRevisions = []presentation.TaskDefinitionRevisionSummary{}
	v.Details.DefinitionRevisionsStatus = "UNAVAILABLE"
	if reader, ok := f.deps.Tasks.(interface {
		GetDefinitionRevision(context.Context, string) (task.DefinitionRevision, error)
	}); ok {
		v.Details.DefinitionRevisionsStatus = "AVAILABLE"
		ids := map[string]bool{}
		if t.CurrentDefinitionRevisionID != "" {
			ids[t.CurrentDefinitionRevisionID] = true
		}
		for _, e := range executions {
			if e.TaskID == t.ID && e.ProjectID == t.ProjectID && e.TaskDefinitionRevisionID != "" {
				ids[e.TaskDefinitionRevisionID] = true
			}
		}
		for id := range ids {
			revision, e := reader.GetDefinitionRevision(ctx, id)
			if e != nil {
				v.Details.DefinitionRevisionsStatus = "PARTIAL"
				continue
			} // Missing historical metadata does not discard the current Task snapshot.
			if revision.TaskID != t.ID || revision.Definition.ProjectID != t.ProjectID || revision.Definition.FeatureAreaID != t.FeatureAreaID {
				v.Details.DefinitionRevisionsStatus = "PARTIAL"
				continue
			}
			v.Details.DefinitionRevisions = append(v.Details.DefinitionRevisions, presentation.TaskDefinitionRevisionSummary{InstructionSource: revision.InstructionSource, ID: id, Ordinal: revision.Ordinal, Current: id == t.CurrentDefinitionRevisionID, PayloadSHA256: revision.PayloadSHA256, CreatedAt: revision.CreatedAt})
		}
		sort.Slice(v.Details.DefinitionRevisions, func(i, j int) bool {
			a, b := v.Details.DefinitionRevisions[i], v.Details.DefinitionRevisions[j]
			if a.Ordinal == b.Ordinal {
				return a.ID < b.ID
			}
			return a.Ordinal < b.Ordinal
		})
	}

	if err := f.attachVerificationDecisions(ctx, &v, t); err != nil {
		return v, err
	}
	presentation.ApplyVerificationDecisions(&v)
	return v, nil
}

func taskImplementationMode(mode string) string {
	switch mode {
	case "CODING_AGENT", "DIRECT_FILE":
		return mode
	}
	return "UNKNOWN"
}

func (f *Facade) attachTaskView(ctx context.Context, out *presentation.Projection, t task.Task) error {
	v, err := f.taskView(ctx, t)
	if err != nil {
		return err
	}
	if out.TaskStages != nil {
		v.ReadOnly = v.ReadOnly || out.TaskStages.ReadOnly
	}
	if out.PendingDecision != nil && out.PendingDecision.Kind == "TASK_ACCEPTANCE" && v.CurrentExecutionAcceptance != nil {
		if !v.Terminal && t.Phase != task.PhaseAcceptancePending && out.Guidance != nil && out.Guidance.NextAction.Code == "RESOLVE_TASK_ACCEPTANCE" {
			v.CurrentExecutionAcceptance.AllowedActions = append([]string(nil), v.RuntimeDispositionActions...)
			v.CurrentExecutionAcceptance.Status = "WAITING_DISPOSITION"
		}
		out.PendingDecision.AllowedActions = append([]string(nil), v.CurrentExecutionAcceptance.AllowedActions...)
	}
	if out.Guidance != nil && out.Guidance.Approval != nil && out.Guidance.Approval.Proposal != nil && out.Guidance.Approval.Status == "PENDING" && out.Guidance.Approval.TaskID == t.ID && out.Guidance.Approval.ReviewID == t.CurrentReviewID {
		v.ImplementationMode = taskImplementationMode(string(out.Guidance.Approval.Proposal.WorkSpec.ImplementationMode))
	}
	v.CurrentAction = presentation.CurrentAction(*out)
	v.Recovery = out.Recovery
	if r := v.CurrentReview; r != nil && r.TaskID == v.ID && r.ExecutorKind == review.ExecutorUpperLLM && out.Recovery != nil && out.Recovery.Reason == "GPT_REVIEW_AUTHORIZATION_REQUIRED" {
		r.Stage, r.StageLabel, r.NextActor = "AUTHORIZATION_REQUIRED", "GPT 审查待重新授权", "USER"
		r.Activity = &presentation.ReviewActivity{ExecutionMethod: "GPT 审查", Stage: r.Stage, Summary: out.Recovery.Summary}
		for i := range v.ReviewHistory {
			if v.ReviewHistory[i].ID == r.ID {
				v.ReviewHistory[i] = *r
			}
		}
	}
	// Pending decisions are separate durable facts. They must not overwrite an
	// exact Run's activity/failure with a generic next-step summary.
	if decision := out.PendingDecision; decision != nil {
		if decision.Kind == "TASK_ACCEPTANCE" && v.CurrentExecution != nil && v.CurrentExecution.ID == decision.SubjectID {
			execution := v.CurrentExecution
			if v.VerificationRun != nil && execution.VerificationRunID != "" && v.VerificationRun.ID == execution.VerificationRunID {
				decision.Summary = "Read this Execution's actual Verification result and current acceptance eligibility before the user decides."
			} else if v.ImplementationRun != nil && execution.ImplementationRunID != "" && v.ImplementationRun.ID == execution.ImplementationRunID && execution.VerificationRunID == "" {
				decision.Summary = "Read this Execution's Implementation result and current successor eligibility. No Verification Run has been recorded for this Execution."
			}
		}
	} else if !v.Terminal && out.Recovery != nil && out.Recovery.Required {
		v.Activity = presentation.TaskActivity{RecoveryStatus: v.Activity.RecoveryStatus, Status: out.Recovery.Status, Summary: "当前阶段受阻，等待恢复。"}
		if v.CurrentExecution != nil && (v.CurrentExecution.FailureCode == "bridge_shutdown_interrupted" || v.CurrentExecution.FailureCode == "bridge_restart_interrupted") {
			v.Activity.Summary = "已中断，等待恢复。"
		}
	} else if !v.Terminal && len(out.Blockers) > 0 {
		v.Activity = presentation.TaskActivity{RecoveryStatus: v.Activity.RecoveryStatus, Status: string(t.Phase), Summary: strings.Join(out.Blockers, "；")}
	}
	presentation.ApplyTaskRunRecovery(&v)
	v.NextStep = presentation.TaskNextStepFor(v)
	if out.PendingDecision != nil {
		v.NextStep = nil
	}
	out.AgentRuntime = nil
	if v.Activity.Runtime != nil {
		summary := v.Activity.Runtime.Summary()
		out.AgentRuntime = &summary
	}
	out.ProductProgress = v.Progress
	presentation.ApplyVerificationDecisions(&v)
	out.TaskView = &v
	out.NextStep = v.NextStep
	return nil
}

// Non-development Tasks use the same product projection, including project
// inspections, with no synthetic FeatureArea or borrowed workflow decisions.
func (f *Facade) simpleTaskPresentation(ctx context.Context, sessionID, areaID string, t task.Task) (presentation.Projection, error) {
	if t.FeatureAreaID != areaID {
		return presentation.Projection{}, notFound()
	}
	development, err := f.DevelopmentContextPresentationForSession(ctx, sessionID)
	if err != nil {
		return presentation.Projection{}, err
	}
	out := presentation.Projection{SchemaVersion: "1", DevelopmentContext: development, Details: presentation.Details{View: "TASK_DETAILS", ProjectID: t.ProjectID, FeatureAreaID: areaID, TaskID: t.ID}, Capabilities: []presentation.Capability{}, Blockers: []string{}, UpdatedAt: t.UpdatedAt}
	if areaID != "" {
		area, e := f.GetFeatureAreaForSession(ctx, sessionID, areaID)
		if e != nil {
			return out, e
		}
		out.FeatureArea = presentation.Identity{ID: area.ID, Name: area.Title}
		out.LifecycleStatus = string(area.Status)
		// Review decisions remain exact and authoritative. History never borrows a
		// later Review's actions; terminal Review actions stay on the area overview.
	}
	if t.Work.ExecutionID != "" {
		cap, cancel, e := f.ExecutionCancellationPresentationForSession(ctx, sessionID, t.Work.ExecutionID)
		if e != nil {
			return out, e
		}
		out.Capabilities = append(out.Capabilities, cap)
		out.Cancellation = cancel
	}
	err = f.attachTaskView(ctx, &out, t)
	if err == nil {
		assembleTaskActions(&out, t)
		err = f.attachTaskActionNode(ctx, sessionID, &out)
	}
	return out, err
}
