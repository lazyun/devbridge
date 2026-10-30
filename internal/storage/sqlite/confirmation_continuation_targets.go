package sqlite

import (
	"context"
	"database/sql"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
	"reflect"
)

func applyConfirmedContinuationTargetsTx(ctx context.Context, tx *sql.Tx, current *task.Task, predecessor executionproposal.Proposal, in wp.TaskAcceptanceInput) error {
	if len(in.NextRepositoryTargets) == 0 && in.NextPrimaryMemberID == "" {
		if len(current.RepositoryTargets) > 0 {
			return executionproposal.ErrConflict
		}
		return nil
	}
	if in.Option != wp.AcceptanceRequestChanges || in.ConfirmationIntentID == "" || len(current.RepositoryTargets) == 0 {
		return executionproposal.ErrConflict
	}
	intent, err := confirmationRead(ctx, tx, in.AISessionID, in.ConfirmationIntentID)
	if err != nil {
		return err
	}
	b := intent.Content.Execution
	if intent.State != "CONFIRMING" || intent.Purpose != confirmation.Execution || intent.Action != "REQUEST_CHANGES" || intent.Commit == nil || b == nil || b.ExecutionID != predecessor.ID || b.TaskID != current.ID || b.FeatureAreaID != current.FeatureAreaID || b.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID {
		return executionproposal.ErrConflict
	}
	stepOK := false
	for _, step := range intent.Steps {
		if step.Kind == "RESOLVE_CONTINUATION" && step.RequestID == in.RequestID && step.State == "UNKNOWN" {
			stepOK = true
		}
	}
	if !stepOK {
		return executionproposal.ErrConflict
	}
	found := false
	for _, v := range intent.Content.Variants {
		if v.ID == intent.Commit.VariantID {
			found = reflect.DeepEqual(v.RepositoryTargets, in.NextRepositoryTargets) && v.PrimaryMemberID == in.NextPrimaryMemberID && reflect.DeepEqual(v.WorkSpec, in.NextWorkSpec) && reflect.DeepEqual(v.VerificationSpec, in.NextVerificationSpec)
		}
	}
	if !found {
		return executionproposal.ErrConflict
	}
	d := task.Definition{Version: 2, RepositoryTargets: in.NextRepositoryTargets, PrimaryMemberID: in.NextPrimaryMemberID, WorkSpec: *in.NextWorkSpec, VerificationSpec: *in.NextVerificationSpec}
	if err = task.ValidateDefinitionTargets(d); err != nil {
		return err
	}
	if err = task.ValidateExecutablePrimary(d); err != nil {
		return err
	}
	if len(current.RepositoryTargets) != len(in.NextRepositoryTargets) {
		return executionproposal.ErrConflict
	}
	frozen := map[string]workset.Target{}
	for _, v := range current.RepositoryTargets {
		frozen[v.MemberID] = v
	}
	for _, v := range in.NextRepositoryTargets {
		old, ok := frozen[v.MemberID]
		if !ok || old.RepositoryID != v.RepositoryID || old.MemberVersion != v.MemberVersion || old.OriginCommit != v.OriginCommit || old.OriginRef != v.OriginRef || old.DevelopmentCycleID != v.DevelopmentCycleID || old.ObservedCommit != v.ObservedCommit {
			return executionproposal.ErrConflict
		}
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members m JOIN workspaces w ON w.id=m.workspace_id WHERE m.id=? AND m.repository_id=? AND m.identity_version=? AND m.origin_commit=? AND w.project_id=? AND w.feature_area_id=?)`, v.MemberID, v.RepositoryID, v.MemberVersion, v.OriginCommit, current.ProjectID, current.FeatureAreaID).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return executionproposal.ErrConflict
		}
	}
	current.RepositoryTargets = workset.CloneTargets(in.NextRepositoryTargets)
	current.PrimaryMemberID = in.NextPrimaryMemberID
	return nil
}
