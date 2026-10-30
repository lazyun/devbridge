package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/task"
)

// Terminal delivery is historical observation, including abandonment. Its
// immutable receipt does not require permission to start another write.
func readCompletedTaskCommit(ctx context.Context, q definitionQuerier, project, taskID string) (task.Task, bool, error) {
	var outcome string
	err := q.QueryRowContext(ctx, `SELECT q.terminal_outcome FROM task_commit_receipts q JOIN tasks t ON t.id=q.task_id AND t.feature_area_id=q.feature_area_id WHERE t.id=? AND t.project_id=? AND q.completed_at IS NOT NULL`, taskID, project).Scan(&outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Task{}, false, nil
	}
	if err != nil {
		return task.Task{}, false, err
	}
	current, err := scanTask(q.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, taskID, project))
	if err != nil {
		return task.Task{}, true, err
	}
	if outcome == "ABANDONED" {
		return current, true, delivery.ErrAbandoned
	}
	return current, true, nil
}
