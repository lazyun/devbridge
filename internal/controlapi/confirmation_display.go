package controlapi

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/aisession"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/developmentcycle"
)

// ConfirmationDisplay decorates an already-authorized result with optional live
// names. No failure here changes admission, receipts or frozen intent content.
// Every read is by an exact ID; this path performs no inventory or Git scan.
func (f *Facade) ConfirmationDisplay(ctx context.Context, intent c.Intent) *c.DisplayMetadata {
	out := &c.DisplayMetadata{AISessionID: intent.AISessionID, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, ProjectID: intent.ProjectID, Status: "UNAVAILABLE", ObjectNameSource: "UNAVAILABLE"}
	defer func() {
		if out.ProjectName == "" && intent.ProjectID != "" {
			out.ProjectName = "Current Project"
			out.PresentationIssue = "PROJECT_NAME_UNAVAILABLE"
		}
		if out.TaskID != "" && out.TaskName == "" {
			out.TaskName = "Current Task"
			out.PresentationIssue = "TASK_NAME_UNAVAILABLE"
		}
	}()
	out.Related = confirmationRelatedPlaceholder(intent)
	if _, err := confirmationCallerReference(ctx); err != nil {
		confirmationRelatedUnavailable(out.Related, "CALLER_CONTEXT_UNAVAILABLE")
		return out
	}
	reader, ok := f.deps.AISessions.(interface {
		GetOwned(context.Context, string) (aisession.Session, error)
	})
	if !ok {
		confirmationRelatedUnavailable(out.Related, "SESSION_READ_UNAVAILABLE")
		return out
	}
	session, err := reader.GetOwned(ctx, intent.AISessionID)
	if err != nil || session.ID != intent.AISessionID {
		confirmationRelatedUnavailable(out.Related, "SESSION_READ_UNAVAILABLE")
		return out
	}
	project := session.ProjectID
	if intent.ProjectID != "" && project != intent.ProjectID {
		confirmationRelatedUnavailable(out.Related, "PROJECT_IDENTITY_MISMATCH")
		return out
	}
	if intent.ProjectID == "" && intent.Purpose != c.ProjectSetup {
		confirmationRelatedUnavailable(out.Related, "PROJECT_IDENTITY_MISMATCH")
		return out
	}
	out.Status = "AVAILABLE"
	name := func(value string) string {
		if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 1024 {
			out.Status = "PARTIAL"
			return ""
		}
		return value
	}
	if project != "" && f.deps.Projects != nil {
		value, e := f.deps.Projects.Get(ctx, project)
		if e == nil && value.ID == project {
			out.ProjectName = name(value.Name)
		} else {
			out.Status = "PARTIAL"
		}
	} else if project != "" {
		out.Status = "PARTIAL"
	}
	if project != "" && f.deps.Repositories != nil {
		if values, e := f.deps.Repositories.List(ctx); e == nil {
			for _, value := range values {
				if value.ProjectID == project && value.RegistrationState == "ACTIVE" {
					out.RepositoryCount++
				}
			}
		} else {
			out.Status, out.PresentationIssue = "PARTIAL", "REPOSITORY_COUNT_UNAVAILABLE"
		}
		if reader, ok := f.deps.Repositories.(interface {
			TopologyRevision(context.Context, string) (int64, error)
		}); ok {
			if revision, e := reader.TopologyRevision(ctx, project); e == nil {
				out.TopologyRevision = revision
			} else {
				out.Status, out.PresentationIssue = "PARTIAL", "TOPOLOGY_REVISION_UNAVAILABLE"
			}
		}
	}

	switch intent.Purpose {
	case c.ProjectSetup:
		// An old UNBOUND intent is not rewritten after binding. Only its exact
		// confirmed Project selection can provide post-confirmation environment data.
		if intent.Commit == nil {
			// Already-bound setup may show its exact immutable Project name;
			// an unbound setup does not borrow a later unrelated binding.
			if intent.ProjectID == "" {
				out.ProjectName = ""
			}
			return out
		}
		if intent.Commit.Selection.ProjectID != project || project == "" {
			out.ProjectName = ""
			return out
		}
		out.RepositoryID = intent.Commit.Selection.RepositoryID
	case c.FeatureArea:
		if body := intent.Content.FeatureArea; body != nil {
			out.FeatureAreaID = body.FeatureAreaID
			out.FeatureAreaName = name(body.Title)
			out.ObjectNameSource = "PREPARED_CONTENT"
			if body.InitialTask != nil {
				out.TaskName = name(body.InitialTask.Title)
				out.RepositoryID = body.InitialTask.RepositoryID
			}
		}
	case c.Task:
		if body := intent.Content.Task; body != nil {
			out.TaskName = name(body.Title)
			out.FeatureAreaID = body.FeatureAreaID
			out.RepositoryID = body.RepositoryID
			out.ObjectNameSource = "PREPARED_CONTENT"
		}
	case c.Review:
		if body := intent.Content.Review; body != nil {
			out.TaskID = body.TaskID
			out.FeatureAreaID = body.FeatureAreaID
			out.RepositoryID = body.RepositoryID
			out.ReviewID = body.ReviewID
			out.DecisionID = body.DecisionID
			if confirmationReviewDecision(intent.Action) {
				out.DecisionPurpose = "REVIEW_RESULT_RESOLUTION"
			}
		}
	case c.Execution:
		if body := intent.Content.Execution; body != nil {
			out.TaskID = body.TaskID
			out.FeatureAreaID = body.FeatureAreaID
		}
	}
	if out.TaskID != "" {
		if f.deps.Tasks == nil {
			out.Status = "PARTIAL"
		} else {
			value, e := f.deps.Tasks.Get(ctx, out.TaskID)
			if e == nil && value.ID == out.TaskID && value.ProjectID == project && value.FeatureAreaID == out.FeatureAreaID && (out.RepositoryID == "" || value.RepositoryID == out.RepositoryID) {
				out.TaskName = name(value.Title)
				// Project-scope and multi-repository confirmation intentionally has
				// no single Repository identity. A Task's legacy primary repository
				// is display context only when the frozen confirmation bound it.
				if out.RepositoryID != "" {
					out.RepositoryID = value.RepositoryID
				}
				out.ObjectNameSource = "CURRENT_AUTHORIZED_OBJECT"
			} else {
				out.Status = "PARTIAL"
			}
		}
	}
	if out.ReviewID != "" && f.deps.Reviews != nil {
		if value, e := f.deps.Reviews.Get(ctx, out.ReviewID); e == nil && value.ID == out.ReviewID && value.ProjectID == project && value.TaskID == out.TaskID {
			out.ReviewOrdinal = value.Ordinal
		} else {
			out.Status, out.PresentationIssue = "PARTIAL", "REVIEW_METADATA_UNAVAILABLE"
		}
	}
	if out.FeatureAreaID != "" {
		if f.deps.FeatureAreas == nil {
			out.Status = "PARTIAL"
		} else {
			value, e := f.deps.FeatureAreas.GetForSession(ctx, intent.AISessionID, out.FeatureAreaID)
			if e == nil && value.ID == out.FeatureAreaID && value.ProjectID == project {
				out.FeatureAreaName = name(value.Title)
			} else {
				out.Status = "PARTIAL"
			}
		}
	}
	if out.RepositoryID != "" {
		if f.deps.Repositories == nil {
			out.Status = "PARTIAL"
			return out
		}
		repo, e := f.deps.Repositories.Get(ctx, out.RepositoryID)
		if e != nil || repo.ID != out.RepositoryID || repo.ProjectID != project {
			out.Status = "PARTIAL"
			return out
		}
		out.RepositoryName = name(repo.Name)
		selectedReader, ok := f.deps.DevelopmentCycles.(interface {
			ReadSelected(context.Context, string, string) (developmentcycle.Cycle, error)
		})
		if !ok {
			out.Status = "PARTIAL"
			return out
		}
		selected, e := selectedReader.ReadSelected(ctx, project, out.RepositoryID)
		if e == nil && selected.ProjectID == project && selected.RepositoryID == out.RepositoryID && selected.SupersededAt == nil && strings.HasPrefix(selected.DevelopmentRef, "refs/heads/") {
			out.DevelopmentBranch = name(strings.TrimPrefix(selected.DevelopmentRef, "refs/heads/"))
		} else {
			out.Status = "PARTIAL"
		}
	}
	f.confirmationRelatedState(ctx, intent, project, out.Related)
	return out
}

func confirmationRelatedPlaceholder(intent c.Intent) *c.RelatedBusinessState {
	if intent.Purpose == c.Review && confirmationReviewDecision(intent.Action) && intent.Content.Review != nil && intent.Content.Review.ReviewID != "" {
		return &c.RelatedBusinessState{Availability: "UNAVAILABLE", Kind: "REVIEW", ID: intent.Content.Review.ReviewID, ReadFailureCode: "NOT_READ"}
	}
	if intent.Purpose == c.Execution && (confirmationAcceptanceDecision(intent.Action) || verificationTaskChoice(intent.Action)) && intent.Content.Execution != nil && intent.Content.Execution.ExecutionID != "" {
		return &c.RelatedBusinessState{Availability: "UNAVAILABLE", Kind: "EXECUTION", ID: intent.Content.Execution.ExecutionID, ReadFailureCode: "NOT_READ"}
	}
	return nil
}

func confirmationRelatedUnavailable(out *c.RelatedBusinessState, code string) {
	if out == nil {
		return
	}
	out.Availability = "UNAVAILABLE"
	out.ReadFailureCode = code
}

// confirmationRelatedState reads only the operation identity frozen in the
// decision intent. It never follows a Task's current Review/Execution pointer.
func (f *Facade) confirmationRelatedState(ctx context.Context, intent c.Intent, project string, out *c.RelatedBusinessState) {
	if out == nil {
		return
	}
	if out.Kind == "REVIEW" {
		body := intent.Content.Review
		if body == nil || f.deps.Reviews == nil {
			confirmationRelatedUnavailable(out, "RELATED_REVIEW_UNAVAILABLE")
			return
		}
		value, err := f.deps.Reviews.Get(ctx, body.ReviewID)
		if err != nil {
			confirmationRelatedUnavailable(out, "RELATED_REVIEW_UNAVAILABLE")
			return
		}
		if value.ID != body.ReviewID || value.ProjectID != project || value.TaskID != body.TaskID || value.FeatureAreaID != body.FeatureAreaID {
			confirmationRelatedUnavailable(out, "RELATED_REVIEW_IDENTITY_MISMATCH")
			return
		}
		out.Availability, out.ReadFailureCode, out.State, out.Outcome = "AVAILABLE", "", value.TerminalStatus, value.TerminalStatus
		if value.RunID == "" {
			return
		}
		if f.deps.Runs == nil {
			confirmationRelatedUnavailable(out, "RELATED_REVIEW_RUN_UNAVAILABLE")
			return
		}
		runValue, err := f.deps.Runs.Get(ctx, value.RunID)
		if err != nil || runValue.ID != value.RunID || runValue.ProjectID != project || runValue.TaskID != body.TaskID || runValue.FeatureAreaID != body.FeatureAreaID || runValue.ReviewID != value.ID {
			confirmationRelatedUnavailable(out, "RELATED_REVIEW_RUN_UNAVAILABLE")
			return
		}
		failureCode := ""
		if runValue.Failure != nil {
			failureCode = runValue.Failure.Code
		}
		out.ReviewRun = &c.RelatedRun{ID: runValue.ID, Status: string(runValue.Status), FailureCode: failureCode}
		return
	}

	body := intent.Content.Execution
	if body == nil || f.deps.ExecutionProposals == nil {
		confirmationRelatedUnavailable(out, "RELATED_EXECUTION_UNAVAILABLE")
		return
	}
	value, err := f.deps.ExecutionProposals.Get(ctx, intent.AISessionID, body.ExecutionID)
	if err != nil {
		confirmationRelatedUnavailable(out, "RELATED_EXECUTION_UNAVAILABLE")
		return
	}
	if value.ID != body.ExecutionID || value.ProjectID != project || value.TaskID != body.TaskID || value.TaskDefinitionRevisionID != body.TaskDefinitionRevisionID {
		confirmationRelatedUnavailable(out, "RELATED_EXECUTION_IDENTITY_MISMATCH")
		return
	}
	owner, err := f.deps.Tasks.Get(ctx, body.TaskID)
	if err != nil || owner.ID != body.TaskID || owner.ProjectID != project || owner.FeatureAreaID != body.FeatureAreaID {
		confirmationRelatedUnavailable(out, "RELATED_EXECUTION_TASK_IDENTITY_MISMATCH")
		return
	}
	out.Availability, out.ReadFailureCode, out.State, out.Outcome, out.FailureCode = "AVAILABLE", "", string(value.Status), string(value.Outcome), value.FailureCode
	if verificationTaskChoice(intent.Action) {
		if reader, ok := f.deps.WorkflowProgression.(taskVerificationDecisionsStore); ok {
			completion, e := reader.ReadTaskCompletion(ctx, project, body.TaskID)
			if e == nil && completion != nil && completion.Plan.ExecutionID == body.ExecutionID && completion.Plan.RevisionID == body.TaskDefinitionRevisionID {
				out.TaskDisposition = completion.State
				out.TaskDispositionSummary = map[string]string{"COMPLETED": "任务已完成并合入", "DELIVERING": "验收已确认，正在提交并合入", "BLOCKED": "合入受阻：" + completion.Error}[completion.State]
			} else if owner.CurrentExecutionID == body.ExecutionID && owner.CurrentDefinitionRevisionID == body.TaskDefinitionRevisionID {
				if hold, e := reader.ReadTaskHold(ctx, project, body.TaskID); e == nil && hold != nil {
					out.TaskDisposition, out.TaskDispositionSummary = "DEFERRED", "任务已暂存："+hold.Note
				} else if continued, e := reader.ReadTaskContinuation(ctx, project, body.TaskID); e == nil && continued {
					out.TaskDisposition, out.TaskDispositionSummary = "CONTINUATION_CONFIRMED", "已选择延续原任务；请在原任务准备后续工作。"
				}
			}
		}
	}
	readRun := func(id string, purpose string) (*c.RelatedRun, bool) {
		if id == "" {
			return nil, true
		}
		if f.deps.Runs == nil {
			return nil, false
		}
		runValue, err := f.deps.Runs.Get(ctx, id)
		if err != nil || runValue.ID != id || runValue.ProjectID != project || runValue.TaskID != body.TaskID || runValue.ExecutionProposalID != value.ID || string(runValue.Purpose) != purpose {
			return nil, false
		}
		failureCode := ""
		if runValue.Failure != nil {
			failureCode = runValue.Failure.Code
		}
		return &c.RelatedRun{ID: runValue.ID, Status: string(runValue.Status), FailureCode: failureCode}, true
	}
	var ok bool
	if out.ImplementationRun, ok = readRun(value.ImplementationRunID, "IMPLEMENTATION"); !ok {
		confirmationRelatedUnavailable(out, "RELATED_IMPLEMENTATION_RUN_UNAVAILABLE")
		return
	}
	if out.VerificationRun, ok = readRun(value.VerificationRunID, "VERIFICATION"); !ok {
		confirmationRelatedUnavailable(out, "RELATED_VERIFICATION_RUN_UNAVAILABLE")
	}
}
