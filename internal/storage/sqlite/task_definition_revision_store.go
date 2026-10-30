package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
)

func (s *TaskStore) GetDefinitionRevision(ctx context.Context, id string) (task.DefinitionRevision, error) {
	return getDefinitionRevision(ctx, s.db.sql, id)
}
func (s *ExecutionProposalStore) GetDefinitionRevision(ctx context.Context, id string) (task.DefinitionRevision, error) {
	return getDefinitionRevision(ctx, s.db.sql, id)
}

type definitionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getDefinitionRevision(ctx context.Context, q definitionQuerier, id string) (task.DefinitionRevision, error) {
	var value task.DefinitionRevision
	var payload, created string
	err := q.QueryRowContext(ctx, `SELECT id,task_id,ordinal,payload_json,payload_sha256,created_at FROM task_definition_revisions WHERE id=?`, id).Scan(&value.ID, &value.TaskID, &value.Ordinal, &payload, &value.PayloadSHA256, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return value, task.ErrNotFound
	}
	if err != nil {
		return value, err
	}
	if value.Definition, err = task.DecodeDefinition(payload, value.PayloadSHA256); err != nil {
		return value, err
	}
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return value, err
	}
	var source task.InstructionSource
	err = q.QueryRowContext(ctx, `SELECT s.source_kind,COALESCE(s.source_revision_id,''),d.review_id,s.decision_id,s.request_id,COALESCE(s.ai_session_id,''),s.instructions_sha256 FROM execution_instruction_sources s JOIN review_approval_decisions d ON d.decision_id=s.decision_id WHERE s.revision_id=? AND s.task_id=?
 UNION ALL SELECT 'TASK_ACCEPTANCE',c.source_revision_id,COALESCE((SELECT review_id FROM review_approval_decisions WHERE decision_id=json_extract(src.payload_json,'$.reviewApprovalId')),''),c.decision_id,c.request_id,c.ai_session_id,c.payload_sha256
 FROM task_acceptance_continuations c JOIN task_definition_revisions src ON src.id=c.source_revision_id JOIN task_definition_revisions dst ON dst.id=c.revision_id
 WHERE c.revision_id=? AND dst.task_id=? AND src.task_id=dst.task_id AND c.source_revision_id<>c.revision_id`, value.ID, value.TaskID, value.ID, value.TaskID).Scan(&source.Kind, &source.SourceRevisionID, &source.ReviewID, &source.DecisionID, &source.RequestID, &source.AISessionID, &source.PayloadSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	value.InstructionSource = &source
	return value, nil
}

func definitionGoalsTx(ctx context.Context, tx *sql.Tx, taskID string) ([]task.Goal, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,position,title FROM task_goals WHERE task_id=? ORDER BY position`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []task.Goal
	for rows.Next() {
		value := task.Goal{TaskID: taskID}
		if err := rows.Scan(&value.ID, &value.Position, &value.Title); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// ensureTaskDefinitionTx snapshots current authored semantics in the same write
// transaction. Identical payloads reuse the current immutable revision.
func ensureTaskDefinitionTx(ctx context.Context, tx *sql.Tx, value *task.Task, now time.Time) (task.DefinitionRevision, error) {
	if value.LegacyReadOnly || value.FeatureAreaID == "" {
		return task.DefinitionRevision{}, task.ErrLegacyReadOnly
	}
	goals, err := definitionGoalsTx(ctx, tx, value.ID)
	if err != nil {
		return task.DefinitionRevision{}, err
	}
	definition := task.DefinitionFromTask(*value, goals)
	var current task.DefinitionRevision
	if value.CurrentDefinitionRevisionID != "" {
		current, err = getDefinitionRevision(ctx, tx, value.CurrentDefinitionRevisionID)
		if err != nil {
			return current, err
		}
		if current.TaskID != value.ID {
			return current, ErrConflict
		}
		// Preserve the persisted continuation contract on ordinary re-encoding.
		// A different explicit Review approval starts its own materialization.
		if current.Definition.ReviewApprovalID == definition.ReviewApprovalID {
			definition.ContinuationNormalizationVersion = current.Definition.ContinuationNormalizationVersion
		}
	}
	payload, hash, err := task.EncodeDefinition(definition)
	if err != nil {
		return task.DefinitionRevision{}, err
	}
	if current.ID != "" && current.PayloadSHA256 == hash {
		return current, nil
	}
	var ordinal int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0)+1 FROM task_definition_revisions WHERE task_id=?`, value.ID).Scan(&ordinal); err != nil {
		return task.DefinitionRevision{}, err
	}
	revision := task.DefinitionRevision{ID: fmt.Sprintf("task_definition_%s_%d_%s", value.ID, ordinal, hash[:16]), TaskID: value.ID, Ordinal: ordinal, Definition: definition, PayloadSHA256: hash, CreatedAt: now.UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_definition_revisions(id,task_id,ordinal,payload_json,payload_sha256,created_at) VALUES(?,?,?,?,?,?)`, revision.ID, revision.TaskID, revision.Ordinal, payload, hash, formatTime(revision.CreatedAt)); err != nil {
		return revision, conflict(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET current_definition_revision_id=? WHERE id=? AND legacy_read_only=0`, revision.ID, value.ID); err != nil {
		return revision, conflict(err)
	}
	value.CurrentDefinitionRevisionID = revision.ID
	return revision, nil
}

// bindExecutionDefinitionTx binds the exact immutable authority and verifies that
// the execution contract has not expanded or changed the approved definition.
func bindExecutionDefinitionTx(ctx context.Context, tx *sql.Tx, value *executionproposal.Proposal) error {
	return bindExecutionDefinitionKindTx(ctx, tx, value, false)
}
func bindExecutionDefinitionKindTx(ctx context.Context, tx *sql.Tx, value *executionproposal.Proposal, fileVerification bool) error {
	// This function binds a new Coding authorization. Existing consumed Run
	// receipts and reservations use their original replay paths, not this gate.
	if value.WorkSpec.ImplementationMode != task.ImplementationCodingAgent && !(fileVerification && value.WorkSpec.ImplementationMode == task.ImplementationDirectFile && value.VerificationSpec.Mode == task.VerificationModeRun) {
		return executionproposal.ErrConflict
	}
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, value.TaskID))
	if err != nil {
		return err
	}
	if t.ProjectID != value.ProjectID || t.Type != task.TypeDevelopment || t.LegacyReadOnly {
		return executionproposal.ErrConflict
	}
	if value.TaskDefinitionRevisionID == "" {
		if t.ConfirmedTaskPlanID != value.TaskPlanID {
			return executionproposal.ErrConflict
		}
		value.TaskDefinitionRevisionID = t.CurrentDefinitionRevisionID
	}
	revision, err := getDefinitionRevision(ctx, tx, value.TaskDefinitionRevisionID)
	if err != nil {
		return err
	}
	if revision.Definition.Version == 2 {
		if fileVerification && revision.Definition.PrimaryMemberID == "" && len(revision.Definition.RepositoryTargets) == 1 {
			revision.Definition.PrimaryMemberID = revision.Definition.RepositoryTargets[0].MemberID
		}
		// Historical v2 bytes remain readable. A new multi-member Coding
		// dispatch additionally needs a pinned runtime adapter/protocol.
		if revision.Definition.PrimaryMemberID == "" || value.RuntimeAdapterIdentity == "" || value.RuntimeProtocolIdentity == "" {
			return task.ErrRepositoryTargetsUnsupported
		}
		raw, hashErr := json.Marshal(revision.Definition.RepositoryTargets)
		if hashErr != nil {
			return hashErr
		}
		sum := sha256.Sum256(raw)
		expectedHash := hex.EncodeToString(sum[:])
		if (value.PrimaryMemberID != "" && value.PrimaryMemberID != revision.Definition.PrimaryMemberID) || (value.ApprovedWorksetHash != "" && value.ApprovedWorksetHash != expectedHash) {
			return executionproposal.ErrConflict
		}
		value.PrimaryMemberID = revision.Definition.PrimaryMemberID
		value.ApprovedWorksetHash = expectedHash
	} else {
		value.PrimaryMemberID, value.ApprovedWorksetHash = "", ""
	}
	if revision.TaskID != t.ID || revision.ID != t.CurrentDefinitionRevisionID || revision.Definition.RepositoryID != t.RepositoryID {
		return executionproposal.ErrConflict
	}
	work, err := encodeWorkSpec(value.WorkSpec)
	if err != nil {
		return err
	}
	verification, err := encodeVerificationSpec(value.VerificationSpec)
	if err != nil {
		return err
	}
	expectedWork, err := encodeWorkSpec(revision.Definition.WorkSpec)
	if err != nil {
		return err
	}
	expectedVerification, err := encodeVerificationSpec(revision.Definition.VerificationSpec)
	if err != nil {
		return err
	}
	if work != expectedWork || verification != expectedVerification {
		return executionproposal.ErrConflict
	}
	value.ProvenanceKind = "EXACT"
	return nil
}
