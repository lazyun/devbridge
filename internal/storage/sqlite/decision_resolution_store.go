package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/decisionresolution"
)

type DecisionResolutionStore struct{ db *DB }

func NewDecisionResolutionStore(db *DB) *DecisionResolutionStore {
	return &DecisionResolutionStore{db: db}
}

const selectDecisionReceipt = `SELECT decision_id,project_id,decision_kind,subject_kind,subject_id,option,request_id,result_kind,result_id,resolved_at,COALESCE(actor_kind,''),COALESCE(actor_id,'') FROM decision_resolution_receipts`

func scanDecisionReceipt(row rowScanner) (decisionresolution.Receipt, error) {
	var value decisionresolution.Receipt
	var resolved string
	err := row.Scan(&value.DecisionID, &value.ProjectID, &value.Kind, &value.SubjectKind, &value.SubjectID, &value.Option, &value.RequestID, &value.ResultKind, &value.ResultID, &resolved, &value.ActorKind, &value.ActorID)
	if err != nil {
		return value, err
	}
	value.ResolvedAt, err = parseTime(resolved)
	return value, err
}

func (s *DecisionResolutionStore) GetByDecision(ctx context.Context, decisionID string) (decisionresolution.Receipt, error) {
	value, err := scanDecisionReceipt(s.db.sql.QueryRowContext(ctx, selectDecisionReceipt+` WHERE decision_id=?`, decisionID))
	if errors.Is(err, sql.ErrNoRows) {
		return value, decisionresolution.ErrNotFound
	}
	if err != nil {
		return value, fmt.Errorf("get Decision resolution: %w", err)
	}
	return value, nil
}

func (s *DecisionResolutionStore) GetByRequest(ctx context.Context, projectID, requestID string) (decisionresolution.Receipt, error) {
	value, err := scanDecisionReceipt(s.db.sql.QueryRowContext(ctx, selectDecisionReceipt+` WHERE project_id=? AND request_id=?`, projectID, requestID))
	if errors.Is(err, sql.ErrNoRows) {
		return value, decisionresolution.ErrNotFound
	}
	if err != nil {
		return value, fmt.Errorf("get Decision request receipt: %w", err)
	}
	return value, nil
}

func (s *DecisionResolutionStore) ListTaskAcceptance(ctx context.Context, taskID string) ([]decisionresolution.Receipt, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectDecisionReceipt+` WHERE decision_kind='TASK_ACCEPTANCE' AND subject_id IN (SELECT id FROM execution_proposals WHERE task_id=?) ORDER BY resolved_at,decision_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []decisionresolution.Receipt
	for rows.Next() {
		value, scanErr := scanDecisionReceipt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// preflightDecisionResolutionTx serializes request-level and entity-level
// idempotency before the authoritative workflow transition is touched.
func preflightDecisionResolutionTx(ctx context.Context, tx *sql.Tx, request decisionresolution.Request, projectID string) (decisionresolution.Receipt, bool, error) {
	if err := decisionresolution.ValidateRequest(request); err != nil {
		return decisionresolution.Receipt{}, false, err
	}
	byRequest, err := scanDecisionReceipt(tx.QueryRowContext(ctx, selectDecisionReceipt+` WHERE project_id=? AND request_id=?`, projectID, request.RequestID))
	if err == nil {
		// Pre-v62 receipts have no actor evidence. Preserve their original
		// immutable replay/convergence behavior without relabeling them as the
		// current caller. Every newly sourced receipt compares actor identity.
		actorCompatible := byRequest.ActorKind == "" && byRequest.ActorID == "" || byRequest.ActorKind == request.ActorKind && byRequest.ActorID == request.ActorID
		exact := byRequest.DecisionID == request.DecisionID && byRequest.Kind == request.Kind && byRequest.SubjectKind == request.SubjectKind && byRequest.SubjectID == request.SubjectID && byRequest.Option == request.Option && actorCompatible
		if attempt, ok := decisionresolution.FromContext(ctx); ok {
			attempt.Record(byRequest, exact, false, !exact)
		}
		return byRequest, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return decisionresolution.Receipt{}, false, err
	}
	byDecision, err := scanDecisionReceipt(tx.QueryRowContext(ctx, selectDecisionReceipt+` WHERE decision_id=?`, request.DecisionID))
	if err == nil {
		actorCompatible := byDecision.ActorKind == "" && byDecision.ActorID == "" || byDecision.ActorKind == request.ActorKind && byDecision.ActorID == request.ActorID
		same := byDecision.Kind == request.Kind && byDecision.SubjectKind == request.SubjectKind && byDecision.SubjectID == request.SubjectID && byDecision.Option == request.Option && actorCompatible
		if attempt, ok := decisionresolution.FromContext(ctx); ok {
			attempt.Record(byDecision, false, same, !same)
		}
		return byDecision, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return decisionresolution.Receipt{}, false, nil
	}
	return decisionresolution.Receipt{}, false, err
}

func insertDecisionResolutionTx(ctx context.Context, tx *sql.Tx, request decisionresolution.Request, projectID, resultKind, resultID string, now time.Time) (decisionresolution.Receipt, error) {
	receipt := decisionresolution.Receipt{DecisionID: request.DecisionID, ProjectID: projectID, Kind: request.Kind, SubjectKind: request.SubjectKind, SubjectID: request.SubjectID, Option: request.Option, RequestID: request.RequestID, ResultKind: resultKind, ResultID: resultID, ResolvedAt: now.UTC(), ActorKind: request.ActorKind, ActorID: request.ActorID}
	_, err := tx.ExecContext(ctx, `INSERT INTO decision_resolution_receipts(decision_id,project_id,decision_kind,subject_kind,subject_id,option,request_id,result_kind,result_id,resolved_at,actor_kind,actor_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.DecisionID, receipt.ProjectID, receipt.Kind, receipt.SubjectKind, receipt.SubjectID, receipt.Option, receipt.RequestID, receipt.ResultKind, receipt.ResultID, formatTime(receipt.ResolvedAt), receipt.ActorKind, receipt.ActorID)
	if err != nil {
		return decisionresolution.Receipt{}, conflict(err)
	}
	if attempt, ok := decisionresolution.FromContext(ctx); ok {
		attempt.Record(receipt, false, false, false)
	}
	return receipt, nil
}
