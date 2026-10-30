package sqlite

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/localoperator"
)

// OperatorFileAuthorityBlocked reveals only the admission blocker. It never
// exposes the Session, grant, workflow handle, scope, or workspace root.
func (s *WorkflowProgressionStore) OperatorFileAuthorityBlocked(ctx context.Context, project, area string) (bool, error) {
	if err := localoperator.RequireProject(ctx, project); err != nil {
		return false, err
	}
	var owns bool
	if err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_areas WHERE id=? AND project_id=?)`, area, project).Scan(&owns); err != nil {
		return false, err
	}
	if !owns {
		return false, featurearea.ErrNotFound
	}
	if !s.db.directFileOperations.TryRLock() {
		return true, nil
	}
	defer s.db.directFileOperations.RUnlock()
	err := directFileAdmissionTx(ctx, s.db.sql, s.db, area)
	var blocked *directfile.Error
	if errors.As(err, &blocked) && blocked.Code == directfile.CapabilityActive {
		return true, nil
	}
	return false, err
}
