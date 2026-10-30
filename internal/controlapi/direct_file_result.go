package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type fileImplementationResults interface {
	GetFileImplementationResult(context.Context, directfile.ImplementationResultReadInput) (directfile.ImplementationResult, error)
	SubmitFileImplementationResult(context.Context, directfile.ImplementationResultInput) (directfile.ImplementationResult, error)
	AcceptFileImplementationResult(context.Context, directfile.ImplementationAcceptInput) (directfile.ImplementationResult, error)
}

func (f *Facade) GetFileImplementationResult(ctx context.Context, in directfile.ImplementationResultReadInput) (directfile.ImplementationResult, error) {
	if err := f.fileResultSessionTask(ctx, in.AISessionID, in.TaskID); err != nil {
		return directfile.ImplementationResult{}, err
	}
	service, ok := f.deps.WorkflowProgression.(fileImplementationResults)
	if !ok {
		return directfile.ImplementationResult{}, MapError(directfile.Failure("FILE_RESULT_UNAVAILABLE", "file result reader unavailable"))
	}
	out, err := service.GetFileImplementationResult(ctx, in)
	if err != nil {
		return out, MapError(err)
	}
	return out, nil
}

func (f *Facade) SubmitFileImplementationResult(ctx context.Context, in directfile.ImplementationResultInput) (directfile.ImplementationResult, error) {
	if err := f.fileResultSessionTask(ctx, in.AISessionID, in.TaskID); err != nil {
		return directfile.ImplementationResult{}, err
	}
	service, ok := f.deps.WorkflowProgression.(fileImplementationResults)
	if !ok {
		return directfile.ImplementationResult{}, MapError(directfile.Failure("FILE_RESULT_UNAVAILABLE", "file result authority unavailable"))
	}
	out, err := service.SubmitFileImplementationResult(ctx, in)
	if err != nil {
		return out, MapError(err)
	}
	if out.ExecutionID != "" && out.VerificationStatus == "READY" {
		p, e := f.deps.ExecutionProposals.Get(ctx, in.AISessionID, out.ExecutionID)
		if e != nil {
			out.VerificationStartError = "CODEX_VERIFICATION_START_FAILED: " + MapError(e).Error()
			return out, nil
		}
		owner, e := f.deps.Tasks.Get(ctx, in.TaskID)
		if e != nil {
			out.VerificationStartError = "CODEX_VERIFICATION_START_FAILED: " + MapError(e).Error()
			return out, nil
		}
		started, e := f.StartTaskExecutionExpectedCommand(ctx, in.AISessionID, in.TaskID, "START_VERIFICATION", "file-verification-"+out.ID, wp.TaskExecutionExpectation{FeatureAreaID: owner.FeatureAreaID, TaskDefinitionRevisionID: in.TaskDefinitionRevisionID, ExecutionID: p.ID, Phase: task.PhaseVerificationReady, PrimaryMemberID: p.PrimaryMemberID, ApprovedWorksetHash: p.ApprovedWorksetHash})
		if e != nil {
			out.VerificationStartError = "CODEX_VERIFICATION_START_FAILED: " + MapError(e).Error()
		} else {
			out.VerificationRunID = started.Run.ID
			out.VerificationStatus = "RUNNING"
		}
	}
	return out, nil
}
func (f *Facade) AcceptFileImplementationResult(ctx context.Context, in directfile.ImplementationAcceptInput) (directfile.ImplementationResult, error) {
	if err := f.fileResultSessionTask(ctx, in.AISessionID, in.TaskID); err != nil {
		return directfile.ImplementationResult{}, err
	}
	service, ok := f.deps.WorkflowProgression.(fileImplementationResults)
	if !ok {
		return directfile.ImplementationResult{}, MapError(directfile.Failure("FILE_RESULT_UNAVAILABLE", "file result authority unavailable"))
	}
	out, err := service.AcceptFileImplementationResult(ctx, in)
	if err != nil {
		return out, MapError(err)
	}
	return out, nil
}

func (f *Facade) fileResultSessionTask(ctx context.Context, sessionID, taskID string) error {
	project, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return err
	}
	value, err := f.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if value.ProjectID != project {
		return notFound()
	}
	return nil
}
