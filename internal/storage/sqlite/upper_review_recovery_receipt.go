package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/agentconversation"
)

func upperRecoveryPayload(in agentconversation.RecoverInput) ([]byte, error) {
	// Project is a trusted adapter boundary, not a caller-supplied authority.
	// Explicit fields keep payload identity independent of JSON property order.
	return json.Marshal(struct {
		ProjectID            string                          `json:"projectId"`
		FeatureAreaID        string                          `json:"featureAreaId"`
		RunID                string                          `json:"runId"`
		OperationKind        agentconversation.OperationKind `json:"operationKind"`
		OperationID          string                          `json:"operationId"`
		AgentConversationID  string                          `json:"agentConversationId"`
		RequestID            string                          `json:"requestId"`
		ExpectedUpperVersion *agentconversation.UpperVersion `json:"expectedUpperVersion"`
	}{in.ProjectID, in.FeatureAreaID, in.RunID, in.OperationKind, in.OperationID, in.AgentConversationID, in.RequestID, in.ExpectedUpperVersion})
}

// recoveryReplayTx executes only a receipt query. A completed Upper recovery
// returns the original recorded PREPARING result, never the current grant or
// another Prepare call. Legacy records remain unmodified and unversioned.
func recoveryReplayTx(ctx context.Context, tx *sql.Tx, in agentconversation.RecoverInput) (agentconversation.RecoveryResult, bool, error) {
	empty := agentconversation.RecoveryResult{}
	var runID, kind, owner, oldID, nextID, projectID string
	var payload, result sql.NullString
	var operator string
	err := tx.QueryRowContext(ctx, `SELECT r.run_id,r.operation_kind,r.operation_id,COALESCE(r.old_conversation_id,''),COALESCE(r.new_conversation_id,''),a.project_id,r.request_payload_json,r.result_json,COALESCE(r.operator_principal_id,'') FROM agent_operation_recoveries r JOIN feature_areas a ON a.id=r.feature_area_id WHERE r.feature_area_id=? AND r.request_id=?`, in.FeatureAreaID, in.RequestID).Scan(&runID, &kind, &owner, &oldID, &nextID, &projectID, &payload, &result, &operator)
	if errors.Is(err, sql.ErrNoRows) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	if operator != in.OperatorPrincipalID || (in.ProjectID != "" && in.ProjectID != projectID) || runID != in.RunID || kind != string(in.OperationKind) || owner != in.OperationID || oldID != in.AgentConversationID {
		return empty, true, agentconversation.ErrConflict
	}
	upper := oldID == "" && kind == string(agentconversation.Review)
	if upper && in.ProjectID == "" {
		return empty, true, agentconversation.ErrInvalidArgument
	}
	if payload.Valid || result.Valid {
		if !payload.Valid || !result.Valid {
			return empty, true, agentconversation.ErrConflict
		}
		requested, err := upperRecoveryPayload(in)
		if err != nil {
			return empty, true, err
		}
		if payload.String != string(requested) {
			return empty, true, agentconversation.ErrConflict
		}
		var value agentconversation.RecoveryResult
		err = json.Unmarshal([]byte(result.String), &value)
		return value, true, err
	}
	if in.ExpectedUpperVersion != nil {
		return empty, true, agentconversation.ErrConflict
	}
	if upper {
		return agentconversation.RecoveryResult{OperatorPrincipalID: in.OperatorPrincipalID, BackendDisposition: "UPPER_REAUTHORIZED", FeatureAreaID: in.FeatureAreaID, RunID: in.RunID, OperationKind: in.OperationKind, OperationID: in.OperationID, RequestID: in.RequestID, Boundary: agentconversation.NoAdvance}, true, nil
	}
	return recoveryResult(in, nextID), true, nil
}
