package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/project"
)

func sessionRequestHash(kind, session, projectID string) string {
	raw, _ := json.Marshal([]string{kind, session, projectID})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
func readSessionInitializationTx(ctx context.Context, tx *sql.Tx, scope, id string) (aisession.Initialization, error) {
	var result aisession.Initialization
	var bound, owner sql.NullString
	var created, updated string
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,caller_scope,created_at,updated_at FROM ai_sessions WHERE id=?`, id).Scan(&result.AISessionID, &bound, &owner, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return result, aisession.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if owner.Valid && owner.String != scope {
		return aisession.Initialization{}, aisession.ErrScopeMismatch
	}
	result.State = aisession.ProjectSelectionRequired
	if bound.Valid {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=?)`, bound.String).Scan(&exists); err != nil {
			return aisession.Initialization{}, err
		}
		if !exists {
			return aisession.Initialization{}, project.ErrNotFound
		}
		result.BoundProjectID = &bound.String
		result.State = aisession.ProjectBound
	}
	result.CreatedAt, err = parseTime(created)
	if err != nil {
		return aisession.Initialization{}, err
	}
	result.UpdatedAt, err = parseTime(updated)
	return result, err
}
func readSessionRequestTx(ctx context.Context, tx *sql.Tx, scope, requestID, kind, hash string) (aisession.RequestReceipt, error) {
	var result aisession.RequestReceipt
	var storedHash string
	var bound sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT request_id,operation_kind,payload_sha256,ai_session_id,project_id FROM ai_session_requests WHERE caller_scope=? AND request_id=?`, scope, requestID).Scan(&result.RequestID, &result.OperationKind, &storedHash, &result.AISessionID, &bound)
	if err != nil {
		return result, err
	}
	if result.OperationKind != kind || storedHash != hash {
		return aisession.RequestReceipt{}, aisession.ErrConflict
	}
	if bound.Valid {
		result.ProjectID = &bound.String
	}
	result.Replayed = true
	return result, nil
}
func (s *AISessionStore) ReadInitialization(ctx context.Context, scope, id string) (aisession.Initialization, error) {
	if scope == "" || !aisession.ValidRequestID(id) {
		return aisession.Initialization{}, aisession.ErrInvalidInitialization
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return aisession.Initialization{}, err
	}
	defer tx.Rollback()
	return readSessionInitializationTx(ctx, tx, scope, id)
}

// replaySessionRequest is pure: a receipt never recreates a missing Session.
func (s *AISessionStore) replaySessionRequest(ctx context.Context, scope, requestID, kind, hash string) (aisession.Initialization, aisession.RequestReceipt, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return aisession.Initialization{}, aisession.RequestReceipt{}, err
	}
	defer tx.Rollback()
	receipt, err := readSessionRequestTx(ctx, tx, scope, requestID, kind, hash)
	if err != nil {
		return aisession.Initialization{}, receipt, err
	}
	value, err := readSessionInitializationTx(ctx, tx, scope, receipt.AISessionID)
	if err == nil && kind == "BIND_PROJECT" && (value.BoundProjectID == nil || receipt.ProjectID == nil || *value.BoundProjectID != *receipt.ProjectID) {
		err = aisession.ErrConflict
	}
	return value, receipt, err
}
func (s *AISessionStore) InitializeSession(ctx context.Context, scope, requestID, id string, at time.Time) (aisession.Initialization, error) {
	if scope == "" || !aisession.ValidRequestID(requestID) || !aisession.ValidRequestID(id) {
		return aisession.Initialization{}, aisession.ErrInvalidInitialization
	}
	hash := sessionRequestHash("INITIALIZE", "", "")
	old, receipt, err := s.replaySessionRequest(ctx, scope, requestID, "INITIALIZE", hash)
	if err == nil {
		old.Receipt = &receipt
		return old, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return aisession.Initialization{}, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return aisession.Initialization{}, err
	}
	defer tx.Rollback()
	receipt, err = readSessionRequestTx(ctx, tx, scope, requestID, "INITIALIZE", hash)
	if err == nil {
		value, err := readSessionInitializationTx(ctx, tx, scope, receipt.AISessionID)
		value.Receipt = &receipt
		return value, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return aisession.Initialization{}, err
	}
	stamp := formatTime(at)
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_sessions(id,project_id,caller_scope,created_at,updated_at) VALUES(?,NULL,?,?,?)`, id, scope, stamp, stamp); err != nil {
		return aisession.Initialization{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_session_requests(caller_scope,request_id,operation_kind,payload_sha256,ai_session_id,project_id,created_at) VALUES(?,?,'INITIALIZE',?,?,NULL,?)`, scope, requestID, hash, id, stamp); err != nil {
		return aisession.Initialization{}, err
	}
	if err := tx.Commit(); err != nil {
		return aisession.Initialization{}, err
	}
	return aisession.Initialization{AISessionID: id, State: aisession.ProjectSelectionRequired, CreatedAt: at, UpdatedAt: at, Receipt: &aisession.RequestReceipt{RequestID: requestID, OperationKind: "INITIALIZE", AISessionID: id}}, nil
}
func boundInitialization(value aisession.Initialization) aisession.Session {
	return aisession.Session{ID: value.AISessionID, ProjectID: *value.BoundProjectID, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}
func (s *AISessionStore) BindSessionProject(ctx context.Context, scope, requestID, sessionID, projectID, selectionID string, at time.Time) (aisession.Session, aisession.RequestReceipt, bool, error) {
	if scope == "" || !aisession.ValidRequestID(requestID) || !aisession.ValidRequestID(sessionID) || !aisession.ValidRequestID(projectID) || !aisession.ValidRequestID(selectionID) {
		return aisession.Session{}, aisession.RequestReceipt{}, false, aisession.ErrInvalidInitialization
	}
	hash := sessionRequestHash("BIND_PROJECT", sessionID, projectID)
	old, receipt, err := s.replaySessionRequest(ctx, scope, requestID, "BIND_PROJECT", hash)
	if err == nil {
		return boundInitialization(old), receipt, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return aisession.Session{}, receipt, false, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return aisession.Session{}, receipt, false, err
	}
	defer tx.Rollback()
	receipt, err = readSessionRequestTx(ctx, tx, scope, requestID, "BIND_PROJECT", hash)
	if err == nil {
		value, err := readSessionInitializationTx(ctx, tx, scope, receipt.AISessionID)
		if err != nil {
			return aisession.Session{}, receipt, false, err
		}
		if value.BoundProjectID == nil || *value.BoundProjectID != projectID {
			return aisession.Session{}, receipt, false, aisession.ErrConflict
		}
		return boundInitialization(value), receipt, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return aisession.Session{}, receipt, false, err
	}
	value, err := readSessionInitializationTx(ctx, tx, scope, sessionID)
	if err != nil {
		return aisession.Session{}, receipt, false, err
	}
	if value.BoundProjectID != nil && *value.BoundProjectID != projectID {
		return aisession.Session{}, receipt, false, aisession.ErrConflict
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=?)`, projectID).Scan(&exists); err != nil {
		return aisession.Session{}, receipt, false, err
	}
	if !exists {
		return aisession.Session{}, receipt, false, project.ErrNotFound
	}
	created := value.BoundProjectID == nil
	stamp := formatTime(at)
	if created {
		// RECORDED selection has no Session FK yet. Bind the real Session first,
		// then consume the selection against the now-valid composite Session FK.
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_project_selections(id,project_id,status,created_at) VALUES(?,?,'RECORDED',?)`, selectionID, projectID, stamp); err != nil {
			return aisession.Session{}, receipt, false, err
		}
		changed, err := tx.ExecContext(ctx, `UPDATE ai_sessions SET project_id=?,updated_at=? WHERE id=? AND project_id IS NULL AND (caller_scope=? OR caller_scope IS NULL)`, projectID, stamp, sessionID, scope)
		if err != nil {
			return aisession.Session{}, receipt, false, err
		}
		n, err := changed.RowsAffected()
		if err != nil {
			return aisession.Session{}, receipt, false, err
		}
		if n != 1 {
			return aisession.Session{}, receipt, false, aisession.ErrConflict
		}
		changed, err = tx.ExecContext(ctx, `UPDATE ai_project_selections SET status='CONSUMED',ai_session_id=?,consumed_at=? WHERE id=? AND status='RECORDED'`, sessionID, stamp, selectionID)
		if err != nil {
			return aisession.Session{}, receipt, false, err
		}
		n, err = changed.RowsAffected()
		if err != nil {
			return aisession.Session{}, receipt, false, err
		}
		if n != 1 {
			return aisession.Session{}, receipt, false, aisession.ErrConflict
		}
		value.BoundProjectID = &projectID
		value.UpdatedAt = at
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ai_session_requests(caller_scope,request_id,operation_kind,payload_sha256,ai_session_id,project_id,created_at) VALUES(?,?,'BIND_PROJECT',?,?,?,?)`, scope, requestID, hash, sessionID, projectID, stamp); err != nil {
		return aisession.Session{}, receipt, false, err
	}
	if err := tx.Commit(); err != nil {
		return aisession.Session{}, receipt, false, err
	}
	receipt = aisession.RequestReceipt{RequestID: requestID, OperationKind: "BIND_PROJECT", AISessionID: sessionID, ProjectID: &projectID}
	return boundInitialization(value), receipt, created, nil
}
