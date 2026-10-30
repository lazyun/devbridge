package sqlite

import (
	"context"
	"github.com/lazyun/devbridge/internal/run"
)

func (s *RunStore) ListProjectPage(ctx context.Context, filter run.ProjectPage) ([]run.Run, error) {
	query := selectRun + ` WHERE (r.project_id=? OR (COALESCE(r.project_id,'')='' AND EXISTS(SELECT 1 FROM tasks t WHERE t.id=r.task_id AND t.project_id=?)))`
	args := []any{filter.ProjectID, filter.ProjectID}
	if filter.FeatureAreaID != "" {
		query += ` AND r.feature_area_id=?`
		args = append(args, filter.FeatureAreaID)
	}
	if filter.Status != "" {
		query += ` AND r.status=?`
		args = append(args, string(filter.Status))
	}
	switch filter.Scope {
	case "current":
		query += ` AND r.status IN ('CREATED','PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING')`
	case "history":
		query += ` AND r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')`
	}
	query += ` ORDER BY r.created_at DESC,r.id LIMIT ? OFFSET ?`
	args = append(args, filter.Limit, filter.Offset)
	rows, err := s.db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []run.Run{}
	for rows.Next() {
		value, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
