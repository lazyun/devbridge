package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
)

// ExecutionRuntimeEligibility reads the same exact immutable selection required
// by a fresh continuation/start. It never consults a newer qualification or
// changes historical receipts, and it does not grant execution authority.
func (s *WorkflowProgressionStore) ExecutionRuntimeEligibility(ctx context.Context, sessionID, taskID, areaID, revisionID, executionID string) (bool, string, error) {
	return s.executionRuntimeEligibility(ctx, sessionID, "", taskID, areaID, revisionID, executionID)
}

func (s *WorkflowProgressionStore) ExecutionRuntimeEligibilityForOperator(ctx context.Context, project, taskID, areaID, revisionID, executionID string) (bool, string, error) {
	if err := localoperator.RequireProject(ctx, project); err != nil {
		return false, "", err
	}
	return s.executionRuntimeEligibility(ctx, "", project, taskID, areaID, revisionID, executionID)
}

func (s *WorkflowProgressionStore) executionRuntimeEligibility(ctx context.Context, sessionID, project, taskID, areaID, revisionID, executionID string) (bool, string, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()
	if project == "" {
		if err = instructionCaller(ctx, tx, sessionID); err != nil {
			if errors.Is(err, aisession.ErrScopeMismatch) {
				return false, "SESSION_SCOPE_MISMATCH", nil
			}
			return false, "", err
		}
		if err = tx.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, sessionID).Scan(&project); err != nil {
			return false, "", err
		}
	}
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, taskID))
	if err != nil {
		return false, "", err
	}
	x, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, executionID))
	if err != nil {
		return false, "", err
	}
	if project != t.ProjectID || x.ProjectID != project || x.TaskID != taskID || t.FeatureAreaID != areaID || revisionID == "" || t.CurrentDefinitionRevisionID != revisionID || x.TaskDefinitionRevisionID != revisionID || t.CurrentExecutionID != executionID {
		return false, "TARGET_MISMATCH", nil
	}
	return executionRuntimeEligibilityTx(ctx, tx, x, s.runtimeAllowlist())
}

func executionRuntimeEligibilityTx(ctx context.Context, q definitionQuerier, value executionproposal.Proposal, allowlist codexruntime.Allowlist) (bool, string, error) {
	var raw, hash string
	if err := q.QueryRowContext(ctx, `SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=? AND task_id=?`, value.TaskDefinitionRevisionID, value.TaskID).Scan(&raw, &hash); err != nil {
		return false, "", err
	}
	d, err := task.DecodeDefinition(raw, hash)
	if err != nil {
		return false, "", err
	}
	if d.Version != 2 || d.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return true, "", nil
	}
	if err = requireExactExecutionRuntimeSelectionTx(ctx, q, value); err != nil {
		for _, expected := range []error{runtimeselection.ErrBindingMissing, runtimeselection.ErrBindingConflict, runtimeselection.ErrCorrupt, runtimeplatform.ErrQualificationInvalidated, runtimeplatform.ErrQualificationNotCurrent, formalexecution.ErrConflict} {
			if errors.Is(err, expected) {
				return false, expected.Error(), nil
			}
		}
		return false, "", err
	}
	if err = value.RuntimeConfig.ValidateFrozen(allowlist); err != nil {
		return false, "FROZEN_RUNTIME_CONFIGURATION_UNAVAILABLE", nil
	}
	backend, err := scanCodexConfig(q.QueryRowContext(ctx, selectCodexConfig+` WHERE id=?`, value.BackendSelection.ConfigID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, "FROZEN_BACKEND_SELECTION_UNAVAILABLE", nil
	}
	if err != nil {
		return false, "", err
	}
	if !backend.Enabled || backend.Fingerprint() != value.BackendSelection.ConfigFingerprint {
		return false, "FROZEN_BACKEND_SELECTION_UNAVAILABLE", nil
	}
	return true, "", nil
}
