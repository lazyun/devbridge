package sqlite

import (
	"context"
	"database/sql"

	"github.com/lazyun/devbridge/internal/executionproposal"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
)

// Registered formal prefixes cannot acquire a Run from a generic background
// queue or direct preparation call. Missing final receipt/binding stays missing.
func formalExecutionStartReadyTx(ctx context.Context, q definitionQuerier, executionID string) (bool, error) {
	var registered bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM formal_execution_t2 WHERE execution_id=?)`, executionID).Scan(&registered); err != nil {
		return false, err
	}
	if !registered {
		var adoptionTable bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='formal_adoption_business')`).Scan(&adoptionTable); err != nil {
			return false, err
		}
		if adoptionTable {
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM formal_adoption_business WHERE execution_id=?)`, executionID).Scan(&registered); err != nil {
				return false, err
			}
		}
	}
	// All new v2 Coding executions need exact runtime authority: either their
	// formal revision binding or the separately verified Recovery consumption.
	// A legacy selection/materialization cannot grant platform authority.
	if !registered {
		var raw, hash string
		err := q.QueryRowContext(ctx, `SELECT d.payload_json,d.payload_sha256 FROM execution_proposals e JOIN task_definition_revisions d ON d.id=e.task_definition_revision_id AND d.task_id=e.task_id WHERE e.id=?`, executionID).Scan(&raw, &hash)
		if err != nil {
			return false, err
		}
		d, err := task.DecodeDefinition(raw, hash)
		if err != nil {
			return false, err
		}
		registered = d.Version == 2 && d.WorkSpec.ImplementationMode == task.ImplementationCodingAgent
	}
	if !registered {
		return false, nil
	}
	var owner runtimeselection.Owner
	var status string
	err := q.QueryRowContext(ctx, `SELECT e.project_id,e.task_id,e.task_definition_revision_id,t.status FROM execution_proposals e JOIN tasks t ON t.id=e.task_id AND t.project_id=e.project_id AND t.current_execution_id=e.id AND t.current_definition_revision_id=e.task_definition_revision_id WHERE e.id=?`, executionID).Scan(&owner.ProjectID, &owner.TaskID, &owner.TaskDefinitionRevisionID, &status)
	if err != nil {
		return true, err
	}
	if status != "ACTIVE" {
		return true, fe.ErrConflict
	}
	value, err := scanExecutionProposal(q.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, executionID))
	if err != nil {
		return true, err
	}
	return true, requireExactExecutionRuntimeSelectionTx(ctx, q, value)
}

// A continuation may retain an existing binding only for the same immutable
// definition and all ten approved runtime fields. A new revision needs its own
// formal source; an acceptance decision cannot manufacture that authority.
func requireExecutionRuntimeSelectionTx(ctx context.Context, q definitionQuerier, value executionproposal.Proposal) error {
	var raw, hash string
	if err := q.QueryRowContext(ctx, `SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=? AND task_id=?`, value.TaskDefinitionRevisionID, value.TaskID).Scan(&raw, &hash); err != nil {
		return err
	}
	d, err := task.DecodeDefinition(raw, hash)
	if err != nil {
		return err
	}
	if d.Version != 2 || d.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return nil
	}
	return requireExactExecutionRuntimeSelectionTx(ctx, q, value)
}

func requireExactExecutionRuntimeSelectionTx(ctx context.Context, q definitionQuerier, value executionproposal.Proposal) error {
	var purpose bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_consumptions WHERE successor_execution_id=?)`, value.ID).Scan(&purpose); err != nil {
		return err
	}
	var authority runtimeselection.CurrentAuthority
	var err error
	if purpose {
		authority, err = readCurrentExecutionAuthorityTx(ctx, q, value)
	} else {
		authority, err = readCurrentRuntimeSelectionTx(ctx, q, executionAuthorityOwner(value))
	}
	if err != nil {
		return err
	}
	b, v, r := value.BackendSelection, authority.Values, value.RuntimeConfig
	if b == nil || v.BackendIdentity != b.ConfigID || v.BackendConfigFingerprint != b.ConfigFingerprint || v.BackendSource != b.Source || v.BackendKind != "CODEX" || v.Model != r.Model || v.ReasoningEffort != r.ReasoningEffort || v.CodexFastMode != r.CodexFastMode || v.ServiceTier != r.ServiceTier || v.RuntimeAdapterIdentity != value.RuntimeAdapterIdentity || v.RuntimeProtocolIdentity != value.RuntimeProtocolIdentity {
		return fe.ErrConflict
	}
	return nil
}

func reuseFormalExecutionAdmissionTx(ctx context.Context, tx *sql.Tx, value executionproposal.Proposal) (bool, error) {
	registered, err := formalExecutionStartReadyTx(ctx, tx, value.ID)
	if err != nil || !registered {
		return registered, err
	}
	if value.Status != executionproposal.StatusAdmitted || value.RunID != "" {
		return true, fe.ErrConflict
	}
	var exact bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposal_admissions a JOIN runtime_scope_plans p ON p.plan_id=a.runtime_scope_plan_id JOIN runtime_preparation_authorities auth ON auth.plan_id=p.plan_id WHERE a.proposal_id=? AND a.task_id=? AND a.task_definition_revision_id=? AND auth.owner_id=a.proposal_id AND auth.request_id=a.admission_request_id AND auth.payload_hash=a.payload_sha256 AND auth.generation=p.generation AND auth.disposition='CURRENT')`, value.ID, value.TaskID, value.TaskDefinitionRevisionID).Scan(&exact)
	if err != nil {
		return true, err
	}
	if !exact {
		return true, fe.ErrCorrupt
	}
	return true, nil
}

func requireTaskExecutionRuntimeBindingTx(ctx context.Context, q definitionQuerier, t task.Task) error {
	if t.CurrentDefinitionRevisionID == "" {
		return nil
	}
	var raw, hash string
	if err := q.QueryRowContext(ctx, `SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=? AND task_id=?`, t.CurrentDefinitionRevisionID, t.ID).Scan(&raw, &hash); err != nil {
		return err
	}
	d, err := task.DecodeDefinition(raw, hash)
	if err != nil {
		return err
	}
	if d.Version != 2 || d.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return nil
	}
	if t.CurrentExecutionID != "" {
		value, readErr := scanExecutionProposal(q.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, t.CurrentExecutionID))
		if readErr != nil {
			return readErr
		}
		if value.TaskID != t.ID || value.ProjectID != t.ProjectID || value.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID {
			return fe.ErrConflict
		}
		return requireExactExecutionRuntimeSelectionTx(ctx, q, value)
	}
	_, err = readCurrentRuntimeSelectionTx(ctx, q, runtimeselection.Owner{ProjectID: t.ProjectID, TaskID: t.ID, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION"})
	return err
}
