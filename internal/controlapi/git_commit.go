package controlapi

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

type gitCommitStore interface {
	RetryTaskCommit(context.Context, string, string) (task.Task, error)
	CommitFeatureArea(context.Context, string, string, string) (featurearea.FeatureArea, error)
	CommitCapability(context.Context, string, string) (featurearea.CommitCapability, error)
}

// ReprepareAcceptedTaskCommit is an explicit new attempt after a durable
// before-effect rejection. It cannot turn UNKNOWN into fresh dispatch.
func (f *Facade) ReprepareAcceptedTaskCommit(ctx context.Context, sessionID string, in delivery.RetryInput) (delivery.RetryReceipt, error) {
	project, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return delivery.RetryReceipt{}, err
	}
	s, ok := f.deps.WorkflowProgression.(interface {
		ReprepareTaskCommit(context.Context, string, delivery.RetryInput) (delivery.RetryReceipt, error)
	})
	if !ok {
		return delivery.RetryReceipt{}, errors.New("Git retry authorization unavailable")
	}
	return s.ReprepareTaskCommit(ctx, project, in)
}

func (f *Facade) TaskDeliveryRetryCandidates(ctx context.Context, sessionID, taskID string) ([]delivery.RetryCandidate, error) {
	project, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	s, ok := f.deps.WorkflowProgression.(interface {
		TaskDeliveryRetryCandidates(context.Context, string, string) ([]delivery.RetryCandidate, error)
	})
	if !ok {
		return nil, errors.New("Git retry authorization unavailable")
	}
	return s.TaskDeliveryRetryCandidates(ctx, project, taskID)
}

func (f *Facade) CommitFeatureArea(ctx context.Context, sessionID, areaID, requestID string) (featurearea.FeatureArea, error) {
	project, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	s, ok := f.deps.WorkflowProgression.(gitCommitStore)
	if !ok {
		return featurearea.FeatureArea{}, errors.New("Git finalization unavailable")
	}
	return s.CommitFeatureArea(ctx, project, areaID, requestID)
}

// RetryAcceptedTaskCommit is recovery, never initial Task commit authorization.
func (f *Facade) RetryAcceptedTaskCommit(ctx context.Context, sessionID, taskID string) (task.Task, error) {
	project, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return task.Task{}, err
	}
	s, ok := f.deps.WorkflowProgression.(gitCommitStore)
	if !ok {
		return task.Task{}, errors.New("Git finalization unavailable")
	}
	current, err := s.RetryTaskCommit(ctx, project, taskID)
	if err != nil {
		return current, err
	}
	if completion, ok := f.deps.WorkflowProgression.(taskVerificationDecisionsStore); ok {
		state, e := completion.ReadTaskCompletion(ctx, project, taskID)
		if e != nil {
			return current, e
		}
		if state != nil && state.State != "COMPLETED" {
			return completion.FinishTaskCompletion(ctx, project, taskID)
		}
	}
	return current, nil
}
func (f *Facade) applyCommitPresentation(ctx context.Context, out *presentation.Projection, project, area string) error {
	s, ok := f.deps.WorkflowProgression.(gitCommitStore)
	if !ok {
		return nil
	}
	cap, err := s.CommitCapability(ctx, project, area)
	if err != nil {
		return err
	}
	if cap.DevelopmentBranch == "" && len(out.DevelopmentContext.Repositories) == 1 {
		cap.DevelopmentBranch = out.DevelopmentContext.Repositories[0].Branch
	}
	out.CommitStatus, out.MergedCommit, out.CanCommit, out.Commit = cap.CommitStatus, cap.MergedCommit, cap.CanCommit, &cap
	if reader, ok := f.deps.WorkflowProgression.(interface {
		CommitPresentationFacts(context.Context, string, string) (featurearea.CommitFacts, error)
	}); ok {
		facts, err := reader.CommitPresentationFacts(ctx, project, area)
		if err != nil {
			return err
		}
		presentation.ApplyCommitFacts(out, facts)
		cap = *out.Commit
	} else {
		presentation.ApplyCommitFacts(out, featurearea.CommitFacts{})
	}
	if cap.CanCommit {
		out.Capabilities = append(out.Capabilities, presentation.Capability{Action: "COMMIT_FEATURE_AREA", Available: true})
	}
	if cap.CanRecoverMerge {
		out.Capabilities = append(out.Capabilities, presentation.Capability{Action: "RECOVER_FEATURE_AREA_MERGE", Available: true})
	}
	if cap.MergeRetryOperation != nil {
		out.Capabilities = append(out.Capabilities, presentation.Capability{Action: "REPREPARE_FEATURE_AREA_MERGE", Available: true})
	}
	if cap.CanRetry {
		action := presentation.Capability{Action: "RETRY_TASK_COMMIT", Available: true, Target: &presentation.ActionTarget{FeatureAreaID: area, TaskID: cap.RetryTaskID}}
		if cap.RetryOperation != nil {
			action.Target.RepositoryID = cap.RetryOperation.RepositoryID
			action.Target.OperationID = cap.RetryOperation.OperationID
		}
		out.Capabilities = append(out.Capabilities, action)
	}
	return nil
}

func (f *Facade) ReprepareFeatureAreaMerge(ctx context.Context, session string, in delivery.MergeRetryInput) (delivery.MergeRetryReceipt, error) {
	project, err := f.sessionProject(ctx, session)
	if err != nil {
		return delivery.MergeRetryReceipt{}, err
	}
	s, ok := f.deps.WorkflowProgression.(interface {
		ReprepareFeatureAreaMerge(context.Context, string, delivery.MergeRetryInput) (delivery.MergeRetryReceipt, error)
	})
	if !ok {
		return delivery.MergeRetryReceipt{}, errors.New("Merge retry authorization unavailable")
	}
	return s.ReprepareFeatureAreaMerge(ctx, project, in)
}
func (f *Facade) FeatureAreaMergeRetryCandidates(ctx context.Context, session, area string) ([]delivery.MergeRetryCandidate, error) {
	project, err := f.sessionProject(ctx, session)
	if err != nil {
		return nil, err
	}
	s, ok := f.deps.WorkflowProgression.(interface {
		FeatureAreaMergeRetryCandidates(context.Context, string, string) ([]delivery.MergeRetryCandidate, error)
	})
	if !ok {
		return nil, errors.New("Merge retry authorization unavailable")
	}
	return s.FeatureAreaMergeRetryCandidates(ctx, project, area)
}
