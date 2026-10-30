package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Only completed immutable purpose chains can resolve their source's
// progression fence. The original UNKNOWN boundary and unsafe conversation
// remain untouched; BeginRun must rotate that conversation before reuse.
func recoveryPurposeResolvedSourceRunsTx(ctx context.Context, q definitionQuerier, areaID string) (string, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_group_array(json_object('executionId',c.successor_execution_id,'sourceRunId',l.source_run_id)),'[]')
 FROM recovery_purpose_confirmation_consumptions c JOIN recovery_purpose_link_history l ON l.link_id=c.purpose_link_id AND l.link_ordinal=c.purpose_link_ordinal
 WHERE l.feature_area_id=? AND c.source_owner_kind IN ('IMPLEMENTATION','VERIFICATION') AND c.successor_execution_id<>''`, areaID).Scan(&raw)
	if err != nil {
		return "", err
	}
	var candidates []struct {
		ExecutionID string `json:"executionId"`
		SourceRunID string `json:"sourceRunId"`
	}
	if err = json.Unmarshal([]byte(raw), &candidates); err != nil {
		return "", err
	}
	sources := map[string]bool{}
	executions := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.ExecutionID == "" || candidate.SourceRunID == "" || executions[candidate.ExecutionID] {
			return "", rs.ErrPurposeReceiptMismatch
		}
		executions[candidate.ExecutionID] = true
		execution, err := scanExecutionProposal(q.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, candidate.ExecutionID))
		if err != nil {
			return "", err
		}
		// Historical resolution retains every immutable archive check without
		// reapplying the platform's current lifecycle. Owner and current platform
		// eligibility are checked separately whenever an execution is started.
		if _, found, err := readRecoveryExecutionEvidenceTx(ctx, q, execution, map[string]bool{}, false); err != nil {
			return "", err
		} else if !found {
			return "", rs.ErrPurposeReceiptMismatch
		}
		sources[candidate.SourceRunID] = true
	}
	reviewSources, err := recoveryReviewPurposeResolvedSourceRunsTx(ctx, q, areaID)
	if err != nil {
		return "", err
	}
	for _, id := range reviewSources {
		sources[id] = true
	}
	ids := make([]string, 0, len(sources))
	for id := range sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	encoded, err := json.Marshal(ids)
	return string(encoded), err
}

// A purpose-produced revision has its own exact receipt/plan/admission chain.
// It cannot borrow a Review approval for a different revision, skip an unrelated
// runtime fence, or authorize an Execution that is no longer Task.current.
func recoveryPurposeApprovedTaskTx(ctx context.Context, tx *sql.Tx, t task.Task, g wp.Guidance) (bool, error) {
	if t.CurrentExecutionID == "" {
		return false, nil
	}
	var found bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_consumptions WHERE successor_execution_id=?)`, t.CurrentExecutionID).Scan(&found); err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	action := "START_EXECUTION"
	if t.Phase == task.PhaseVerificationReady {
		action = "START_VERIFICATION"
	} else if t.Phase != task.PhaseImplementationReady {
		return true, wp.Reject("EXECUTION_PREREQUISITE_NOT_SATISFIED", g)
	}
	if t.Status != task.StatusActive || g.NextAction.Code != action {
		return true, wp.Reject("EXECUTION_PREREQUISITE_NOT_SATISFIED", g)
	}
	execution, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=? AND project_id=? AND task_id=? AND task_definition_revision_id=?`, t.CurrentExecutionID, t.ProjectID, t.ID, t.CurrentDefinitionRevisionID))
	if err != nil {
		return true, err
	}
	_, err = readCurrentExecutionAuthorityTx(ctx, tx, execution)
	return true, err
}
