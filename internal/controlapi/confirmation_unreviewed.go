package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
)

func (f *Facade) validateUnreviewedImplementation(ctx context.Context, in *confirmation.PrepareInput, project string) error {
	b := in.Execution
	if b == nil || (b.Action != "CONFIRM_IMPLEMENTATION" && b.Action != "CONFIRM_AND_START_IMPLEMENTATION") || b.ExpectedTarget != nil || b.ReviewID != "" || b.DecisionID != "" || b.ExecutionID != "" || (b.Action == "CONFIRM_IMPLEMENTATION" && in.RecommendedRuntimeConfig != nil) {
		return invalid("unreviewed implementation requires an exact unapproved Task without Review, Decision, Execution or a preexisting start target; legacy definition-only confirmation cannot select runtime")
	}
	owner, err := f.deps.Tasks.Get(ctx, b.TaskID)
	if err != nil {
		return err
	}
	if owner.ProjectID != project || owner.FeatureAreaID != b.FeatureAreaID || owner.CurrentDefinitionRevisionID != b.TaskDefinitionRevisionID {
		return invalid("implementation confirmation target changed")
	}
	if owner.RepositoryID == "" {
		if b.RepositoryID == "" || len(b.BaseCommit) != 40 {
			return invalid("select an explicit Repository and full base commit in the implementation plan")
		}
		repository, err := f.deps.Repositories.Get(ctx, b.RepositoryID)
		if err != nil || repository.ProjectID != project {
			return notFound()
		}
	} else if (b.RepositoryID != "" && b.RepositoryID != owner.RepositoryID) || (b.BaseCommit != "" && b.BaseCommit != owner.BaseCommit) {
		return invalid("implementation confirmation cannot replace the Task's existing Repository baseline")
	}
	if owner.RepositoryID != "" {
		b.RepositoryID, b.BaseCommit = owner.RepositoryID, owner.BaseCommit
	}
	baseline, ok := f.deps.Tasks.(interface {
		ValidateImplementationBaseline(context.Context, string, string) error
	})
	if !ok {
		return invalid("implementation baseline validation unavailable")
	}
	if err := baseline.ValidateImplementationBaseline(ctx, b.RepositoryID, b.BaseCommit); err != nil {
		return err
	}
	reader, ok := f.deps.Tasks.(interface {
		CanConfirmUnreviewedImplementation(context.Context, string, string) (bool, error)
	})
	if !ok {
		return invalid("unreviewed implementation confirmation unavailable")
	}
	available, err := reader.CanConfirmUnreviewedImplementation(ctx, in.AISessionID, b.TaskID)
	if err != nil {
		return err
	}
	if !available {
		return invalid("Task has no current explicit Review-skip receipt awaiting implementation confirmation")
	}
	for _, variant := range in.Variants {
		if variant.ImplementationMode != task.ImplementationCodingAgent || variant.WorkSpec == nil || variant.WorkSpec.ImplementationMode != variant.ImplementationMode || variant.VerificationSpec == nil || variant.ExecutorKind != "" || len(variant.RepositoryTargets) != 0 || variant.ExecutionInstructions != nil {
			return invalid("complete single-repository Coding implementation and verification definitions required")
		}
		if err = task.ValidateWorkSpec(*variant.WorkSpec); err != nil {
			return err
		}
		if err = task.ValidateExecutableVerificationSpec(*variant.VerificationSpec); err != nil {
			return err
		}
	}
	return nil
}
