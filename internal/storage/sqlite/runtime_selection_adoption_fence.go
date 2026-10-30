package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/confirmation"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/task"
)

// Only the original Prepare transaction arms the new producer. Replay never
// retrofits historical consumption/completion into a formal adoption source.
func captureFormalAdoptionPrepareTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent) error {
	if intent.Purpose != confirmation.Execution || intent.Action != "START_APPROVED" {
		return nil
	}
	b := intent.Content.Execution
	if b == nil || b.ExpectedTarget == nil {
		return fe.ErrConflict
	}
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, b.TaskID, intent.ProjectID))
	if err != nil {
		return err
	}
	revision, err := getDefinitionRevision(ctx, tx, t.CurrentDefinitionRevisionID)
	if err != nil {
		return err
	}
	if revision.Definition.Version != 2 || revision.Definition.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return nil
	}
	if err = validateFormalConfirmationContextTx(ctx, tx, intent); err != nil {
		return err
	}
	if t.CurrentExecutionID != "" {
		var candidateRevision string
		if err = tx.QueryRowContext(ctx, `SELECT task_definition_revision_id FROM execution_proposals WHERE id=? AND task_id=? AND project_id=?`, t.CurrentExecutionID, t.ID, t.ProjectID).Scan(&candidateRevision); err != nil {
			return err
		}
		if candidateRevision == t.CurrentDefinitionRevisionID {
			if _, err = readPriorRuntimeApprovalTx(ctx, tx, t.CurrentExecutionID); err != nil {
				return err
			}
		} else {
			revising, err := noRunRevisionApprovedTx(ctx, tx, t.ID)
			if err != nil {
				return err
			}
			if !revising {
				return fe.ErrConflict
			}
		}
	}
	expected := *b.ExpectedTarget
	if t.Status != task.StatusActive || t.Phase != task.PhaseImplementationReady || b.FeatureAreaID != t.FeatureAreaID || b.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID || expected.FeatureAreaID != t.FeatureAreaID || expected.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID || expected.ExecutionID != t.CurrentExecutionID || expected.Phase != t.Phase || expected.PrimaryMemberID != t.PrimaryMemberID || expected.ApprovedWorksetHash != fe.Hash(t.RepositoryTargets) {
		return fe.ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO formal_adoption_fences(ai_session_id,intent_id,intent_version,project_id,task_id,revision_id,content_hash) VALUES(?,?,?,?,?,?,?)`, intent.AISessionID, intent.IntentID, intent.Version, t.ProjectID, t.ID, t.CurrentDefinitionRevisionID, intent.ContentHash)
	return err
}
func readFormalAdoptionRequiredTx(ctx context.Context, q definitionQuerier, id fe.ReadIdentity) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	intent, err := formalConfirmationTx(ctx, q, id.AISessionID, id.IntentID)
	if err != nil {
		return false, err
	}
	if intent.Version != id.IntentVersion || intent.ContentHash != id.ContentHash || intent.Commit == nil || intent.Commit.RequestID != id.RequestID || fe.HashCommit(*intent.Commit) != id.TypedPayloadHash {
		return false, fe.ErrConflict
	}
	var submitted string
	if err = q.QueryRowContext(ctx, `SELECT submitted_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=? AND outcome='CONSUMED'`, id.AISessionID, id.RequestID, id.IntentID).Scan(&submitted); err != nil {
		return false, err
	}
	if submitted != id.SubmittedHash {
		return false, fe.ErrConflict
	}
	var project, taskID, revision, hash string
	var version int
	err = q.QueryRowContext(ctx, `SELECT project_id,task_id,revision_id,content_hash,intent_version FROM formal_adoption_fences WHERE ai_session_id=? AND intent_id=?`, id.AISessionID, id.IntentID).Scan(&project, &taskID, &revision, &hash, &version)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM formal_adoption_business WHERE ai_session_id=? AND (intent_id=? OR request_id=?)) OR EXISTS(SELECT 1 FROM formal_adoption_receipts WHERE ai_session_id=? AND request_id=?)`, id.AISessionID, id.IntentID, id.RequestID, id.AISessionID, id.RequestID).Scan(&exists); err != nil {
			return false, err
		}
		if exists {
			return false, fe.ErrCorrupt
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	b := intent.Content.Execution
	if intent.Purpose != confirmation.Execution || intent.Action != "START_APPROVED" || b == nil || b.ExpectedTarget == nil || project != intent.ProjectID || taskID != b.TaskID || revision != b.TaskDefinitionRevisionID || hash != id.ContentHash || version != id.IntentVersion {
		return false, fe.ErrCorrupt
	}
	return true, nil
}
func (s *WorkflowProgressionStore) ReadAdoptionRequired(ctx context.Context, id fe.ReadIdentity) (bool, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	return readFormalAdoptionRequiredTx(ctx, tx, id)
}
