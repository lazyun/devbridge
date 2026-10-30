package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/featurearea"
)

const selectFeatureAreaCreationReceipt = `SELECT ai_session_id,project_id,command,request_id,payload_sha256,contract_version,prepared_json,feature_area_id,COALESCE(task_id,''),COALESCE(review_id,''),COALESCE(run_id,''),review_admitted,COALESCE(failed_step,''),COALESCE(error_code,''),COALESCE(error_message,'') FROM feature_area_creation_receipts`

func scanFeatureAreaCreationReceipt(row featureAreaScanner) (featurearea.CreationReceipt, error) {
	var value featurearea.CreationReceipt
	var prepared string
	var admitted int
	if err := row.Scan(&value.AISessionID, &value.ProjectID, &value.Command, &value.RequestID, &value.PayloadSHA256, &value.ContractVersion, &prepared, &value.FeatureAreaID, &value.TaskID, &value.ReviewID, &value.RunID, &admitted, &value.FailedStep, &value.ErrorCode, &value.ErrorMessage); err != nil {
		return value, err
	}
	if !json.Valid([]byte(prepared)) {
		return value, errors.New("stored Feature Area creation receipt has invalid prepared JSON")
	}
	value.Prepared = json.RawMessage(append([]byte(nil), prepared...))
	value.ReviewAdmitted = admitted != 0
	return value, nil
}

func (s *FeatureAreaStore) ReplayCreation(ctx context.Context, sessionID, projectID, command, requestID, payloadSHA256 string) (featurearea.FeatureArea, featurearea.CreationReceipt, error) {
	receipt, err := scanFeatureAreaCreationReceipt(s.db.sql.QueryRowContext(ctx, selectFeatureAreaCreationReceipt+` WHERE ai_session_id=? AND project_id=? AND command=? AND request_id=?`, sessionID, projectID, command, requestID))
	if errors.Is(err, sql.ErrNoRows) {
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, featurearea.ErrNotFound
	}
	if err != nil {
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, err
	}
	if receipt.PayloadSHA256 != payloadSHA256 {
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, featurearea.ErrConflict
	}
	value, err := scanFeatureArea(s.db.sql.QueryRowContext(ctx, selectFeatureArea+` WHERE id=? AND project_id=?`, receipt.FeatureAreaID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, featurearea.ErrNotFound
	}
	return value, receipt, err
}

// CreateReceipted is the atomic root boundary: a created Feature Area can
// never become durable without its root request receipt.
func (s *FeatureAreaStore) CreateReceipted(ctx context.Context, value featurearea.FeatureArea, receipt featurearea.CreationReceipt) (featurearea.FeatureArea, featurearea.CreationReceipt, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, err
	}
	defer tx.Rollback()
	keyArgs := []any{receipt.AISessionID, receipt.ProjectID, receipt.Command, receipt.RequestID}
	existing, err := scanFeatureAreaCreationReceipt(tx.QueryRowContext(ctx, selectFeatureAreaCreationReceipt+` WHERE ai_session_id=? AND project_id=? AND command=? AND request_id=?`, keyArgs...))
	if err == nil {
		if existing.PayloadSHA256 != receipt.PayloadSHA256 {
			return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, featurearea.ErrConflict
		}
		area, getErr := scanFeatureArea(tx.QueryRowContext(ctx, selectFeatureArea+` WHERE id=? AND project_id=?`, existing.FeatureAreaID, receipt.ProjectID))
		if getErr != nil {
			return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, getErr
		}
		return area, existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, err
	}
	if receipt.FeatureAreaID != value.ID || value.ProjectID != receipt.ProjectID || value.CreatedByAISessionID != receipt.AISessionID {
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, featurearea.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO feature_areas(id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at,merged_commit,merged_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, value.ID, value.ProjectID, nullString(value.CreatedByAISessionID), value.Title, value.ProblemStatement, string(value.Status), formatTime(value.CreatedAt), formatTime(value.UpdatedAt), nullString(value.MergedCommit), nullableCommitTime(value.MergedAt)); err != nil {
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, conflict(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO feature_area_creation_receipts(ai_session_id,project_id,command,request_id,payload_sha256,contract_version,prepared_json,feature_area_id,task_id,review_id,run_id,review_admitted,failed_step,error_code,error_message) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, receipt.AISessionID, receipt.ProjectID, receipt.Command, receipt.RequestID, receipt.PayloadSHA256, receipt.ContractVersion, string(receipt.Prepared), receipt.FeatureAreaID, nullString(receipt.TaskID), nullString(receipt.ReviewID), nullString(receipt.RunID), boolInt(receipt.ReviewAdmitted), nullString(receipt.FailedStep), nullString(receipt.ErrorCode), nullString(receipt.ErrorMessage)); err != nil {
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, conflict(err)
	}
	payload, _ := event.Payload(map[string]any{"status": value.Status, "title": value.Title})
	if err := appendEvent(ctx, tx, event.Event{Type: event.FeatureAreaCreated, ProjectID: value.ProjectID, EntityType: "feature_area", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, err
	}
	return value, receipt, nil
}

func (s *FeatureAreaStore) UpdateCreationReceipt(ctx context.Context, receipt featurearea.CreationReceipt) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	previous, err := scanFeatureAreaCreationReceipt(tx.QueryRowContext(ctx, selectFeatureAreaCreationReceipt+` WHERE ai_session_id=? AND project_id=? AND command=? AND request_id=?`, receipt.AISessionID, receipt.ProjectID, receipt.Command, receipt.RequestID))
	if errors.Is(err, sql.ErrNoRows) {
		return featurearea.ErrNotFound
	}
	if err != nil {
		return err
	}
	if !sameCreationRoot(previous, receipt) || !monotonicCreationReceipt(previous, receipt) {
		return featurearea.ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE feature_area_creation_receipts SET task_id=?,review_id=?,run_id=?,review_admitted=?,failed_step=?,error_code=?,error_message=? WHERE ai_session_id=? AND project_id=? AND command=? AND request_id=?`, nullString(receipt.TaskID), nullString(receipt.ReviewID), nullString(receipt.RunID), boolInt(receipt.ReviewAdmitted), nullString(receipt.FailedStep), nullString(receipt.ErrorCode), nullString(receipt.ErrorMessage), receipt.AISessionID, receipt.ProjectID, receipt.Command, receipt.RequestID)
	if err != nil {
		return conflict(err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("update Feature Area creation receipt: %w", featurearea.ErrConflict)
	}
	return tx.Commit()
}

func sameCreationRoot(a, b featurearea.CreationReceipt) bool {
	return a.AISessionID == b.AISessionID && a.ProjectID == b.ProjectID && a.Command == b.Command && a.RequestID == b.RequestID && a.PayloadSHA256 == b.PayloadSHA256 && a.ContractVersion == b.ContractVersion && string(a.Prepared) == string(b.Prepared) && a.FeatureAreaID == b.FeatureAreaID
}

func monotonicCreationReceipt(old, next featurearea.CreationReceipt) bool {
	if (old.TaskID != "" && old.TaskID != next.TaskID) || (old.ReviewID != "" && old.ReviewID != next.ReviewID) || (old.RunID != "" && old.RunID != next.RunID) || (old.ReviewAdmitted && !next.ReviewAdmitted) {
		return false
	}
	// Failure is a recoverable observation rather than a business identity.
	// A later retry may clear it on progress or replace it with the current
	// blocked step; callers that need compare-and-merge must reread first.
	return true
}
