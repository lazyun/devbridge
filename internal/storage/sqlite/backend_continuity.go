package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/run"
)

func validateFeatureAreaBackendContinuityTx(ctx context.Context, tx *sql.Tx, featureAreaID string, selected *codexconfig.BackendSelection) error {
	if selected == nil || selected.ConfigID == "" || selected.ConfigFingerprint == "" {
		return agentconversation.ErrUnavailable
	}
	var conversationID, projectID, threadID, sourceRunID, sourceThreadID string
	var encoded sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,project_id,COALESCE(backend_thread_id,''),backend_selection_json,COALESCE(backend_selection_source_run_id,''),COALESCE(backend_selection_source_thread_id,'') FROM agent_conversations WHERE feature_area_id=? AND ended_at IS NULL`, featureAreaID).Scan(&conversationID, &projectID, &threadID, &encoded, &sourceRunID, &sourceThreadID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && threadID == "" {
		return nil
	}
	if err != nil {
		return err
	}
	if !encoded.Valid || sourceRunID == "" || sourceThreadID == "" {
		return agentconversation.ErrUnavailable
	}
	if sourceThreadID != threadID {
		return agentconversation.ErrConflict
	}
	var sourceMatches bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN agent_operation_boundaries b ON b.run_id=r.id WHERE r.id=? AND r.project_id=? AND r.feature_area_id=? AND r.thread_id=? AND b.agent_conversation_id=?)`, sourceRunID, projectID, featureAreaID, threadID, conversationID).Scan(&sourceMatches); err != nil {
		return err
	}
	if !sourceMatches {
		return agentconversation.ErrConflict
	}
	var bound codexconfig.BackendSelection
	if err := json.Unmarshal([]byte(encoded.String), &bound); err != nil {
		return err
	}
	if bound.ConfigID == "" || bound.ConfigFingerprint == "" || bound.ConfigID != selected.ConfigID || bound.ConfigFingerprint != selected.ConfigFingerprint {
		return agentconversation.ErrConflict
	}
	return nil
}

func agentRunBackendSelectionTx(ctx context.Context, tx *sql.Tx, value run.Run) (*codexconfig.BackendSelection, error) {
	var encoded sql.NullString
	switch value.OperationKind {
	case run.OperationReview:
		var executor string
		if err := tx.QueryRowContext(ctx, `SELECT executor_kind,backend_selection_json FROM reviews WHERE id=? AND project_id=? AND feature_area_id=?`, value.OperationID, value.ProjectID, value.FeatureAreaID).Scan(&executor, &encoded); err != nil {
			return nil, err
		}
		if executor != "CODING_AGENT" {
			return nil, nil
		}
	case run.OperationExecution:
		if err := tx.QueryRowContext(ctx, `SELECT backend_selection_json FROM execution_proposals WHERE id=? AND project_id=? AND task_id=?`, value.OperationID, value.ProjectID, value.TaskID).Scan(&encoded); err != nil {
			return nil, err
		}
	default:
		return nil, agentconversation.ErrUnavailable
	}
	if !encoded.Valid {
		return nil, agentconversation.ErrUnavailable
	}
	var selected codexconfig.BackendSelection
	if err := json.Unmarshal([]byte(encoded.String), &selected); err != nil {
		return nil, err
	}
	return &selected, nil
}

func validateAgentRunBackendContinuityTx(ctx context.Context, tx *sql.Tx, value run.Run) error {
	if err := validateRunAgentCapabilitiesTx(ctx, tx, value); err != nil {
		return err
	}
	selected, err := agentRunBackendSelectionTx(ctx, tx, value)
	if err != nil || selected == nil {
		return err
	}
	return validateFeatureAreaBackendContinuityTx(ctx, tx, value.FeatureAreaID, selected)
}
