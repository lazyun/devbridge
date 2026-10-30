package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	c "github.com/lazyun/devbridge/internal/confirmation"
)

func (s *ConfirmationStore) ListSessionConfirmations(ctx context.Context, session string) ([]c.Intent, error) {
	return s.ListSessionConfirmationsForTask(ctx, session, "")
}

func (s *ConfirmationStore) ListSessionConfirmationsForTask(ctx context.Context, session, taskID string, requests ...string) ([]c.Intent, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = instructionCaller(ctx, tx, session); err != nil {
		return nil, err
	}
	requestID := ""
	if len(requests) > 0 {
		requestID = requests[0]
	}
	rows, err := tx.QueryContext(ctx, `WITH scoped AS (
 SELECT *,json_extract(intent_json,'$.state') AS state,
 (?<>'' AND (json_extract(intent_json,'$.content.execution.taskId')=? OR json_extract(intent_json,'$.content.review.taskId')=?)) AS focused,
 (json_extract(intent_json,'$.state') IN ('CONFIRMING','UNKNOWN','PARTIAL') OR (json_extract(intent_json,'$.state')='PENDING' AND julianday(json_extract(intent_json,'$.expiresAt'))>julianday('now'))) AS unresolved
 FROM confirmation_intents WHERE ai_session_id=?
 ) SELECT intent_json FROM scoped ORDER BY
 CASE WHEN ?<>'' AND json_extract(intent_json,'$.content.requestId')=? THEN 0 WHEN focused AND unresolved THEN 1 WHEN unresolved THEN 2 WHEN focused THEN 3 ELSE 4 END,
 CASE state WHEN 'CONFIRMING' THEN 0 WHEN 'PENDING' THEN 1 WHEN 'UNKNOWN' THEN 2 WHEN 'PARTIAL' THEN 2 ELSE 3 END,
 json_extract(intent_json,'$.createdAt') DESC,intent_id DESC LIMIT 21`, taskID, taskID, taskID, session, requestID, requestID)
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
