package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
)

const taskReviewSkipV126SQL = `
CREATE TABLE task_review_skips (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id),
 project_id TEXT NOT NULL REFERENCES projects(id),
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 request_id TEXT NOT NULL,
 payload_sha256 TEXT NOT NULL,
 created_at TEXT NOT NULL,
 confirmation_intent_id TEXT REFERENCES confirmation_intents(intent_id),
 approved_revision_id TEXT REFERENCES task_definition_revisions(id),
 approved_at TEXT,
 CHECK ((confirmation_intent_id IS NULL AND approved_revision_id IS NULL AND approved_at IS NULL)
 OR (confirmation_intent_id IS NOT NULL AND approved_revision_id IS NOT NULL AND approved_at IS NOT NULL))
);
CREATE TRIGGER task_review_skip_identity_immutable BEFORE UPDATE ON task_review_skips
WHEN NEW.task_id IS NOT OLD.task_id OR NEW.project_id IS NOT OLD.project_id OR NEW.ai_session_id IS NOT OLD.ai_session_id
 OR NEW.request_id IS NOT OLD.request_id OR NEW.payload_sha256 IS NOT OLD.payload_sha256 OR NEW.created_at IS NOT OLD.created_at
 OR OLD.approved_revision_id IS NOT NULL
BEGIN SELECT RAISE(ABORT,'review skip receipt is immutable'); END;
CREATE TRIGGER task_review_skip_no_delete BEFORE DELETE ON task_review_skips
BEGIN SELECT RAISE(ABORT,'review skip receipt is durable'); END;
`

// Reuse the existing canonical successor exceptions instead of inventing a
// second acceptance policy for tasks whose user explicitly waived Review.
func migrateTaskReviewSkipV126(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, taskReviewSkipV126SQL); err != nil {
		return err
	}
	for _, guard := range []struct{ name, old, new string }{
		{"approved_task_semantics_immutable", "OLD.review_approval_id IS NOT NULL", "EXISTS(SELECT 1 FROM task_review_skips s WHERE s.task_id=OLD.id AND s.approved_revision_id IS NOT NULL) AND OLD.review_approval_id IS NULL"},
		{"approved_task_revision_immutable", "EXISTS(SELECT 1 FROM review_approval_materializations m WHERE m.task_id=NEW.id AND m.initial_revision_id IS NOT NEW.current_definition_revision_id)", "EXISTS(SELECT 1 FROM task_review_skips s WHERE s.task_id=OLD.id AND s.approved_revision_id IS NOT NULL AND s.approved_revision_id IS NOT NEW.current_definition_revision_id) AND OLD.review_approval_id IS NULL"},
	} {
		var source string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, guard.name).Scan(&source); err != nil {
			return err
		}
		if !strings.Contains(source, guard.old) {
			return errors.New("Review skip migration: existing approval guard is unrecognized")
		}
		source = strings.Replace(source, guard.name, "unreviewed_"+guard.name, 1)
		source = strings.Replace(source, guard.old, guard.new, 1)
		if _, err := tx.ExecContext(ctx, source); err != nil {
			return err
		}
	}
	return nil
}

func (s *TaskStore) CanConfirmUnreviewedImplementation(ctx context.Context, session, id string) (bool, error) {
	var available bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_review_skips s JOIN tasks t ON t.id=s.task_id JOIN ai_sessions a ON a.id=? AND a.project_id=t.project_id JOIN feature_areas f ON f.id=t.feature_area_id AND f.project_id=t.project_id AND f.status IN ('DISCOVERING','ACTIVE') AND f.legacy_read_only=0 WHERE t.id=? AND t.status='ACTIVE' AND t.phase='IMPLEMENTATION_READY' AND t.current_review_id IS NULL AND t.current_execution_id IS NULL AND t.review_approval_id IS NULL AND s.approved_revision_id IS NULL)`, session, id).Scan(&available)
	return available, err
}

func (s *TaskStore) ConfirmUnreviewedImplementation(ctx context.Context, in task.UnreviewedImplementationInput) (task.Task, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback()
	intent, err := confirmationRead(ctx, tx, in.AISessionID, in.IntentID)
	if err != nil {
		return task.Task{}, err
	}
	if intent.Purpose != confirmation.Execution || (intent.Action != "CONFIRM_IMPLEMENTATION" && intent.Action != "CONFIRM_AND_START_IMPLEMENTATION") || intent.State != "CONFIRMING" || intent.Commit == nil || intent.Commit.RequestID != in.RequestID || intent.Content.Execution == nil || intent.Content.Execution.TaskID != in.TaskID || intent.Content.Execution.TaskDefinitionRevisionID != in.ExpectedRevisionID || intent.Content.Execution.RepositoryID != in.RepositoryID || intent.Content.Execution.BaseCommit != in.BaseCommit {
		return task.Task{}, ErrConflict
	}
	var consumedHash string
	if err = tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND intent_id=? AND request_id=? AND outcome='CONSUMED'`, in.AISessionID, in.IntentID, in.RequestID).Scan(&consumedHash); err != nil {
		return task.Task{}, err
	}
	committedJSON, err := json.Marshal(intent.Commit)
	if err != nil {
		return task.Task{}, err
	}
	if !confirmationCommitIdentity(intent, *intent.Commit) || consumedHash != workflowHash(string(committedJSON)) {
		return task.Task{}, ErrConflict
	}
	if intent.Action == "CONFIRM_AND_START_IMPLEMENTATION" {
		b, commit := intent.Content.Execution, intent.Commit
		if b.Action != intent.Action || b.ReviewID != "" || b.DecisionID != "" || b.ExecutionID != "" || b.ExpectedTarget != nil || commit.Selection.RuntimeConfig == nil || commit.Selection.Backend == nil || commit.BackendSelection == nil || commit.Selection.Backend.ConfigID != commit.BackendSelection.ConfigID || commit.Selection.Backend.ConfigFingerprint != commit.BackendSelection.ConfigFingerprint || commit.BackendSelection.ConfigID == "" || commit.BackendSelection.ConfigFingerprint == "" || len(intent.Steps) != 2 || intent.Steps[0].Kind != "CONFIRM_IMPLEMENTATION" || intent.Steps[1].Kind != "START_IMPLEMENTATION" {
			return task.Task{}, ErrConflict
		}
		frozen, err := freezeExecutionRuntimeConfig(*commit.Selection.RuntimeConfig)
		if err != nil || frozen != *commit.Selection.RuntimeConfig {
			return task.Task{}, ErrConflict
		}
	}
	var variant *confirmation.Variant
	for i := range intent.Content.Variants {
		if intent.Content.Variants[i].ID == intent.Commit.VariantID {
			variant = &intent.Content.Variants[i]
			break
		}
	}
	if variant == nil || variant.WorkSpec == nil || variant.VerificationSpec == nil || !reflect.DeepEqual(*variant.WorkSpec, in.WorkSpec) || !reflect.DeepEqual(*variant.VerificationSpec, in.VerificationSpec) {
		return task.Task{}, ErrConflict
	}
	value, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, in.TaskID))
	if err != nil {
		return value, err
	}
	var original, revision sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT confirmation_intent_id,approved_revision_id FROM task_review_skips WHERE task_id=? AND project_id=?`, value.ID, intent.ProjectID).Scan(&original, &revision)
	if err != nil {
		return value, err
	}
	if revision.Valid {
		if original.String != in.IntentID || revision.String != value.CurrentDefinitionRevisionID {
			return value, ErrConflict
		}
		return value, nil
	}
	ctx = taskaction.WithIntent(ctx, intent.IntentID)
	if err = taskActionAdmissionTx(ctx, tx, value.ID); err != nil {
		return value, err
	}
	if value.ProjectID != intent.ProjectID || value.Status != task.StatusActive || value.Phase != task.PhaseImplementationReady || value.CurrentDefinitionRevisionID != in.ExpectedRevisionID || value.CurrentReviewID != "" || value.CurrentExecutionID != "" || value.ReviewApprovalID != "" {
		return value, ErrConflict
	}
	if value.RepositoryID == "" {
		value.RepositoryID, value.BaseCommit = in.RepositoryID, in.BaseCommit
	} else if (in.RepositoryID != "" && in.RepositoryID != value.RepositoryID) || (in.BaseCommit != "" && in.BaseCommit != value.BaseCommit) {
		return value, ErrConflict
	}
	if value.RepositoryID == "" || len(value.BaseCommit) != 40 {
		return value, ErrConflict
	}
	if _, err := hex.DecodeString(value.BaseCommit); err != nil {
		return value, ErrConflict
	}
	var usable bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_areas WHERE id=? AND project_id=? AND status IN ('DISCOVERING','ACTIVE') AND legacy_read_only=0)`, value.FeatureAreaID, value.ProjectID).Scan(&usable); err != nil {
		return value, err
	}
	if !usable {
		return value, ErrConflict
	}
	if in.WorkSpec.ImplementationMode != task.ImplementationCodingAgent || len(variant.RepositoryTargets) != 0 {
		return value, errors.New("unreviewed confirmation requires an exact scalar Coding Agent definition")
	}
	work, err := encodeWorkSpec(in.WorkSpec)
	if err != nil {
		return value, err
	}
	verification, err := encodeVerificationSpec(in.VerificationSpec)
	if err != nil {
		return value, err
	}
	if err = ensureRepositoryActiveTx(ctx, tx, value.ProjectID, value.RepositoryID); err != nil {
		return value, err
	}
	now := time.Now().UTC()
	if intent.Action == "CONFIRM_AND_START_IMPLEMENTATION" {
		// Like reviewed approval, confirming an executable implementation moves
		// a discovering area into active development in the same transaction.
		if _, err = tx.ExecContext(ctx, `UPDATE feature_areas SET status='ACTIVE',updated_at=? WHERE id=? AND project_id=? AND status='DISCOVERING' AND legacy_read_only=0`, formatTime(now), value.FeatureAreaID, value.ProjectID); err != nil {
			return value, err
		}
	}
	value.WorkSpec = task.CloneWorkSpec(in.WorkSpec)
	value.VerificationSpec = task.CloneVerificationSpec(in.VerificationSpec)
	// Preserve the original requirement in its creation event; compatibility
	// fields track the independently confirmed implementation definition.
	constraints, err := json.Marshal(map[string]any{"version": 1, "items": in.WorkSpec.Constraints})
	if err != nil {
		return value, err
	}
	value.Goal, value.Constraints = in.WorkSpec.Goal, append([]string(nil), in.WorkSpec.Constraints...)
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET repository_id=?,base_commit=?,goal=?,constraints_json=?,work_spec_json=?,verification_spec_json=?,updated_at=? WHERE id=?`, value.RepositoryID, value.BaseCommit, value.Goal, string(constraints), work, verification, formatTime(now), value.ID); err != nil {
		return value, err
	}
	frozen, err := ensureTaskDefinitionTx(ctx, tx, &value, now)
	if err != nil {
		return value, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_review_skips SET confirmation_intent_id=?,approved_revision_id=?,approved_at=? WHERE task_id=? AND approved_revision_id IS NULL`, in.IntentID, frozen.ID, formatTime(now), value.ID); err != nil {
		return value, err
	}
	payload, err := event.Payload(map[string]any{"confirmationIntentId": in.IntentID, "revisionId": frozen.ID, "reviewMode": "NONE"})
	if err != nil {
		return value, err
	}
	if err = appendEvent(ctx, tx, event.Event{Type: "task.implementation_confirmed", ProjectID: value.ProjectID, TaskID: value.ID, EntityType: "task", EntityID: value.ID, Payload: payload, CreatedAt: now}); err != nil {
		return value, err
	}
	if err = tx.Commit(); err != nil {
		return value, err
	}
	return s.Get(ctx, value.ID)
}

func unreviewedDefinitionApprovedTx(ctx context.Context, tx definitionQuerier, value task.Task) (bool, error) {
	var approved bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_review_skips s JOIN confirmation_intents c ON c.intent_id=s.confirmation_intent_id JOIN confirmation_commit_requests r ON r.intent_id=c.intent_id AND r.ai_session_id=c.ai_session_id AND r.request_id=json_extract(c.intent_json,'$.commit.requestId') AND r.outcome='CONSUMED' WHERE s.task_id=? AND s.project_id=? AND s.approved_revision_id=? AND json_extract(c.intent_json,'$.action') IN ('CONFIRM_IMPLEMENTATION','CONFIRM_AND_START_IMPLEMENTATION') AND json_extract(c.intent_json,'$.state') IN ('CONFIRMING','UNKNOWN','PARTIAL','COMPLETED'))`, value.ID, value.ProjectID, value.CurrentDefinitionRevisionID).Scan(&approved)
	return approved, err
}
