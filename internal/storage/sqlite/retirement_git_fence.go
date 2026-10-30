package sqlite

import "context"

// Keep the read capability and command admission aligned with the existing
// feature_area_git_command_fence trigger. Released merge plans no longer own a
// reservation; UNKNOWN and incomplete Task deliveries still do.
const pendingRetirementGitSQL = `EXISTS(SELECT 1 FROM task_commit_receipts WHERE feature_area_id=? AND completed_at IS NULL)
 OR EXISTS(SELECT 1 FROM pending_feature_area_commits WHERE feature_area_id=? AND completed_at IS NULL)`

func retirementGitBlocked(ctx context.Context, q definitionQuerier, area string) (bool, error) {
	var blocked bool
	err := q.QueryRowContext(ctx, `SELECT `+pendingRetirementGitSQL, area, area).Scan(&blocked)
	return blocked, err
}

func (s *WorkflowProgressionStore) FeatureAreaRetirementBlocked(ctx context.Context, project, area string) (bool, error) {
	var blocked bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT `+pendingRetirementGitSQL+` FROM feature_areas WHERE id=? AND project_id=?`, area, area, area, project).Scan(&blocked)
	return blocked, err
}
