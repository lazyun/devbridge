package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

// ListTaskConfirmations reads proposals as well as receipts. It is deliberately
// separate from ListProject: unsubmitted proposals must never block navigation.
func (s *ConfirmationStore) ListTaskConfirmations(ctx context.Context, session, project, task string) ([]c.Intent, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = instructionCaller(ctx, tx, session); err != nil {
		return nil, err
	}
	var owned bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN ai_sessions s ON s.project_id=t.project_id WHERE s.id=? AND t.project_id=? AND t.id=?)`, session, project, task).Scan(&owned); err != nil {
		return nil, err
	}
	if !owned {
		return nil, sql.ErrNoRows
	}
	rows, err := tx.QueryContext(ctx, `SELECT intent_json FROM confirmation_intents WHERE ai_session_id=? AND project_id=? AND
 (json_extract(intent_json,'$.content.execution.taskId')=? OR json_extract(intent_json,'$.content.review.taskId')=? OR EXISTS(SELECT 1 FROM json_each(json_extract(intent_json,'$.steps')) step WHERE json_extract(step.value,'$.taskId')=?))
 ORDER BY CASE WHEN json_extract(intent_json,'$.state')='CONFIRMING' THEN 0 WHEN json_extract(intent_json,'$.state')='PENDING' AND julianday(json_extract(intent_json,'$.expiresAt'))>julianday('now') THEN 1 WHEN json_extract(intent_json,'$.state') IN ('UNKNOWN','PARTIAL') THEN 2 ELSE 3 END,json_extract(intent_json,'$.createdAt') DESC,intent_id DESC LIMIT 21`, session, project, task, task, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []c.Intent{}
	for rows.Next() {
		var raw string
		var value c.Intent
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}
