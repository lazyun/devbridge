package sqlite

import (
	"context"
	"database/sql"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/localoperator"
)

// Authorization precedes both immutable receipt reads and fresh writes. Owner
// lookup intentionally has no current-lifecycle predicate: replay is history.
func cancellationActorTx(ctx context.Context, tx *sql.Tx, in executioncancel.Input) error {
	if in.OperatorPrincipalID == "" {
		if _, ok := localoperator.FromContext(ctx); ok {
			return localoperator.ErrAccessDenied
		}
		var project string
		if err := tx.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, in.AISessionID).Scan(&project); err != nil {
			return err
		}
		if project != in.ProjectID {
			return executioncancel.ErrProjectMismatch
		}
		return nil
	}
	if in.AISessionID != "" {
		return localoperator.ErrAccessDenied
	}
	a, ok := localoperator.FromContext(ctx)
	if !ok || a.PrincipalID() != in.OperatorPrincipalID {
		return localoperator.ErrAccessDenied
	}
	var project, task string
	var err error
	switch in.OwnerType {
	case "REVIEW":
		err = tx.QueryRowContext(ctx, `SELECT project_id,task_id FROM reviews WHERE id=?`, in.OwnerID).Scan(&project, &task)
	case "EXECUTION":
		err = tx.QueryRowContext(ctx, `SELECT project_id,task_id FROM execution_proposals WHERE id=?`, in.OwnerID).Scan(&project, &task)
	case "TASK":
		err = tx.QueryRowContext(ctx, `SELECT project_id,id FROM tasks WHERE id=?`, in.OwnerID).Scan(&project, &task)
	default:
		return executioncancel.ErrInvalidArgument
	}
	if err != nil {
		return err
	}
	if project != in.ProjectID {
		return executioncancel.ErrProjectMismatch
	}
	return a.Check(project, task, "CANCEL_EXECUTION")
}

func recoveryActorTx(ctx context.Context, tx *sql.Tx, in agentconversation.RecoverInput) error {
	a, operator := localoperator.FromContext(ctx)
	if in.OperatorPrincipalID == "" {
		if operator {
			return localoperator.ErrAccessDenied
		}
		return nil // The unchanged Session facade owns AI caller authorization.
	}
	if !operator || a.PrincipalID() != in.OperatorPrincipalID {
		return localoperator.ErrAccessDenied
	}
	var project, task string
	err := tx.QueryRowContext(ctx, `SELECT r.project_id,r.task_id FROM runs r JOIN tasks t ON t.id=r.task_id AND t.project_id=r.project_id JOIN feature_areas f ON f.id=r.feature_area_id AND f.project_id=r.project_id WHERE r.id=? AND r.feature_area_id=? AND r.operation_kind=? AND r.operation_id=? AND t.feature_area_id=f.id`, in.RunID, in.FeatureAreaID, in.OperationKind, in.OperationID).Scan(&project, &task)
	if err != nil {
		return err
	}
	if project != in.ProjectID {
		return agentconversation.ErrConflict
	}
	return a.Check(project, task, "RECOVER")
}
