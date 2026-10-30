package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
)

type ConfirmationStore struct{ db *DB }

func NewConfirmationStore(db *DB) *ConfirmationStore { return &ConfirmationStore{db: db} }

func confirmationCommitFenceTx(ctx context.Context, tx *sql.Tx, out confirmation.Intent) (*confirmation.Failure, error) {
	if err := taskActionConfirmationFenceTx(ctx, tx, out); err != nil {
		var failure *confirmation.Failure
		if errors.As(err, &failure) {
			return failure, nil
		}
		return nil, err
	}
	if out.Purpose == confirmation.RecoveryPurpose {
		return recoveryPurposeCommitFenceTx(ctx, tx, out)
	}
	return confirmationDecisionFenceTx(ctx, tx, out, true)
}

// Replacement preparation may inspect the Decision but cannot materialize a
// missing business row. Only the actual consumption writer may repair it.
func confirmationDecisionFenceTx(ctx context.Context, tx *sql.Tx, out confirmation.Intent, allowRepair bool) (*confirmation.Failure, error) {
	body := out.Content.Review
	if body == nil || (out.Action != "REVISE" && out.Action != "REJECT") {
		return nil, nil
	}
	if body.PreparedTopologyRevision > 0 {
		var current int64
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM project_repository_topologies WHERE project_id=?`, out.ProjectID).Scan(&current); err != nil {
			return nil, err
		}
		if current != body.PreparedTopologyRevision {
			return confirmation.NewFailure("PROJECT_TOPOLOGY_CHANGED", "CORE", "UNIQUE_CONSUMPTION", "The Project repository topology changed after this confirmation was prepared.", confirmation.PrepareNewVersion, nil), nil
		}
	}
	if body.PreparedDevelopmentContextHash != "" {
		type branchIdentity struct {
			RepositoryID     string `json:"repositoryId"`
			DevelopmentRef   string `json:"developmentRef"`
			DevelopmentCycle string `json:"developmentCycleId"`
			SelectedHead     string `json:"selectedHeadSha"`
		}
		rows, err := tx.QueryContext(ctx, `SELECT r.id,COALESCE(c.development_ref,''),COALESCE(c.id,''),COALESCE(c.selected_head_sha,'') FROM repositories r LEFT JOIN development_cycles c ON c.project_id=r.project_id AND c.repository_id=r.id AND c.superseded_at IS NULL WHERE r.project_id=? AND r.registration_state='ACTIVE' ORDER BY r.id`, out.ProjectID)
		if err != nil {
			return nil, err
		}
		identities := []branchIdentity{}
		for rows.Next() {
			var value branchIdentity
			if err = rows.Scan(&value.RepositoryID, &value.DevelopmentRef, &value.DevelopmentCycle, &value.SelectedHead); err != nil {
				rows.Close()
				return nil, err
			}
			identities = append(identities, value)
		}
		if err = rows.Close(); err != nil {
			return nil, err
		}
		if confirmation.ValueHash(identities) != body.PreparedDevelopmentContextHash {
			return confirmation.NewFailure("DEVELOPMENT_BRANCH_CONFIGURATION_CHANGED", "CORE", "UNIQUE_CONSUMPTION", "The Project development branch configuration changed after this confirmation was prepared.", confirmation.PrepareNewVersion, nil), nil
		}
	}
	var currentReview, phase, status string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(current_review_id,''),phase,status FROM tasks WHERE id=? AND project_id=? AND feature_area_id=?`, body.TaskID, out.ProjectID, body.FeatureAreaID).Scan(&currentReview, &phase, &status); err != nil {
		return nil, err
	}
	if currentReview != body.ReviewID {
		return confirmation.NewFailure("CURRENT_REVIEW_CHANGED", "DOMAIN", "UNIQUE_CONSUMPTION", "The Task no longer points to the Review frozen by this confirmation.", confirmation.PrepareNewVersion, nil), nil
	}
	expectedPhase := "REVIEW_APPROVAL_PENDING"
	if strings.HasPrefix(body.DecisionID, "INVALID_REVIEW_RESULT_RESOLUTION:") {
		expectedPhase = "REVIEWING"
	}
	if status != "ACTIVE" || phase != expectedPhase {
		return confirmation.NewFailure("TASK_PHASE_CHANGED", "DOMAIN", "UNIQUE_CONSUMPTION", "The Task phase changed after this confirmation was prepared.", confirmation.PrepareNewVersion, nil), nil
	}
	// The commit writer resolves the frozen Decision through the same reducer
	// every read surface uses. A proven invalid-result Decision whose row is
	// absent is repaired here, inside the transaction that consumes it, so a
	// legitimate PENDING Decision always commits.
	snapshot, err := reviewDecisionSnapshotTx(ctx, tx, body.DecisionID)
	if err != nil {
		return nil, err
	}
	if snapshot.actionable() {
		if body.PreparedDecisionVersion != "" && body.PreparedDecisionVersion != snapshot.Version {
			return confirmation.NewFailure("DECISION_VERSION_CHANGED", "DOMAIN", "UNIQUE_CONSUMPTION", "The Review Decision changed after this confirmation was prepared.", confirmation.PrepareNewVersion, nil), nil
		}
		if allowRepair && snapshot.Status == reviewDecisionRecoverable {
			if err = repairInvalidReviewResolutionDecisionTx(ctx, tx, snapshot); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	code, message := "DECISION_NOT_PENDING", "The frozen Review Decision is no longer pending."
	if snapshot.ReasonCode == "DECISION_SUPERSEDED" || snapshot.ReasonCode == "DECISION_OWNER_CHANGED" {
		code = snapshot.ReasonCode
	}
	if snapshot.ReasonMessage != "" {
		message = snapshot.ReasonMessage
	}
	return confirmation.NewFailure(code, "DOMAIN", "UNIQUE_CONSUMPTION", message, confirmation.PrepareNewVersion, nil), nil
}

func confirmationRead(ctx context.Context, q definitionQuerier, session, id string) (out confirmation.Intent, err error) {
	if err = instructionCaller(ctx, q, session); err != nil {
		return
	}
	var raw string
	err = q.QueryRowContext(ctx, `SELECT intent_json FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, id, session).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &out)
	}
	if err == nil {
		var ambiguous bool
		err = q.QueryRowContext(ctx, `SELECT `+confirmationAmbiguousJSON("document")+` FROM (SELECT ? AS document)`, raw).Scan(&ambiguous)
		if err == nil && ambiguous {
			err = ErrConflict
		}
	}
	return
}
func (s *ConfirmationStore) Get(ctx context.Context, session, id string) (confirmation.Intent, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return confirmation.Intent{}, err
	}
	defer tx.Rollback()
	out, err := confirmationRead(ctx, tx, session, id)
	if err != nil {
		return out, err
	}
	if out.Commit != nil {
		sub, readErr := confirmationSubmissionTx(ctx, tx, out, out.Commit.RequestID)
		if readErr != nil {
			return out, readErr
		}
		out.Submission = &sub
	} else {
		// A recorded rejection has no intent.Commit: the write was refused before
		// any step ran. Its receipt is still the intent's current durable fact.
		sub, found, readErr := latestConfirmationSubmissionTx(ctx, tx, out)
		if readErr != nil {
			return out, readErr
		}
		if found {
			out.Submission = &sub
		}
	}
	out, err = recoverConfirmationTx(ctx, tx, out, s.db.epoch)
	if err == nil && out.CanResume && out.Submission != nil {
		out.Submission.NextAction = confirmation.ContinueOriginalSteps
	}
	return out, err
}

// ListProject returns submitted confirmation facts for a Project. Prepared but
// unsubmitted intents are intentionally absent: they are still ordinary Card
// proposals and must not suppress Project actions.
func (s *ConfirmationStore) ListProject(ctx context.Context, session, project string) ([]confirmation.Intent, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = instructionCaller(ctx, tx, session); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT intent_json, `+confirmationAmbiguousJSON("intent_json")+` FROM confirmation_intents WHERE ai_session_id=? AND project_id=? AND json_extract(intent_json,'$.action')='CREATE_TASK_AND_REVIEW' AND json_extract(intent_json,'$.commit') IS NOT NULL AND json_extract(intent_json,'$.state') IN ('CONFIRMING','PARTIAL','UNKNOWN') ORDER BY intent_id`, session, project)
	if err != nil {
		return nil, err
	}
	var values []confirmation.Intent
	for rows.Next() {
		var raw string
		var ambiguous bool
		var value confirmation.Intent
		if err = rows.Scan(&raw, &ambiguous); err != nil {
			rows.Close()
			return nil, err
		}
		if ambiguous {
			rows.Close()
			return nil, ErrConflict
		}
		if err = json.Unmarshal([]byte(raw), &value); err != nil {
			rows.Close()
			return nil, err
		}
		if value.Commit != nil {
			values = append(values, value)
		}
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range values {
		sub, readErr := confirmationSubmissionTx(ctx, tx, values[i], values[i].Commit.RequestID)
		if readErr != nil {
			return nil, readErr
		}
		values[i].Submission = &sub
		values[i], err = recoverConfirmationTx(ctx, tx, values[i], s.db.epoch)
		if err != nil {
			return nil, err
		}
		if values[i].CanResume {
			values[i].Submission.NextAction = confirmation.ContinueOriginalSteps
		}
	}
	return values, nil
}
func confirmationSave(ctx context.Context, tx *sql.Tx, out confirmation.Intent) error {
	out.Submission = nil // Request outcomes are read from their immutable receipt, never intent state.
	out.ReplacementReceipt = nil
	b, e := json.Marshal(out)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE confirmation_intents SET intent_json=? WHERE intent_id=? AND ai_session_id=?`, string(b), out.IntentID, out.AISessionID)
	return e
}
func (s *ConfirmationStore) Prepare(ctx context.Context, project string, in confirmation.PrepareInput) (confirmation.Intent, error) {
	if in.SourceIntentID != "" {
		return confirmation.Intent{}, confirmation.ErrRequestConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return confirmation.Intent{}, err
	}
	defer tx.Rollback()
	out, err := prepareConfirmationIntentTx(ctx, tx, project, in, time.Now().UTC(), s.db.epoch)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// prepareConfirmationIntentTx is the single canonical prepare path. The caller
// owns the writer transaction, allowing Recovery Resolve to bind the intent to
// its source and resolution atomically without bypassing prepare semantics.
func prepareConfirmationIntentTx(ctx context.Context, tx *sql.Tx, project string, in confirmation.PrepareInput, now time.Time, epoch string) (out confirmation.Intent, err error) {
	if err = instructionCaller(ctx, tx, in.AISessionID); err != nil {
		return
	}
	if in.SubmittedHash == "" {
		return out, confirmation.ErrLegacyReplayUnproven
	}
	if err = checkAdoptionStartPrepareRequestTx(ctx, tx, in.AISessionID, in.RequestID); err != nil {
		return out, err
	}
	out, err = replayConfirmationPrepareTx(ctx, tx, in.AISessionID, in.RequestID, in.SubmittedHash, epoch)
	if err == nil {
		return out, nil
	}
	if !errors.Is(err, confirmation.ErrRequestNotObserved) {
		return out, err
	}
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(project_id,'') FROM ai_sessions WHERE id=?`, in.AISessionID).Scan(&current); err != nil {
		return
	}
	if current != project {
		return out, ErrConflict
	}
	b, err := json.Marshal(in)
	if err != nil {
		return
	}
	hash := workflowHash(string(b))
	if in.SourceIntentID != "" {
		old, e := confirmationRead(ctx, tx, in.AISessionID, in.SourceIntentID)
		if e != nil {
			return out, e
		}
		if old.State != "PENDING" || old.Purpose != in.Purpose || old.ProjectID != project {
			return out, ErrConflict
		}
		old.State = "SUPERSEDED"
		if e = confirmationSave(ctx, tx, old); e != nil {
			return out, e
		}
	}
	out = confirmation.Intent{IntentID: "confirm_" + workflowHash(in.AISessionID + "\x00" + in.RequestID)[:32], Version: 1, AISessionID: in.AISessionID, ProjectID: project, Purpose: in.Purpose, Action: in.Action, State: "PENDING", ContentHash: hash, Content: in, Steps: []confirmation.Step{}, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	raw, err := json.Marshal(out)
	if err != nil {
		return
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO confirmation_intents(intent_id,ai_session_id,project_id,prepare_request_id,prepare_hash,intent_json,submitted_sha256) VALUES(?,?,?,?,?,?,?)`, out.IntentID, in.AISessionID, project, in.RequestID, hash, string(raw), in.SubmittedHash)
	if err != nil {
		return
	}
	if err = bindTaskActionConfirmationTx(ctx, tx, out); err != nil {
		return confirmation.Intent{}, err
	}
	// Use the production reader inside this transaction, then compare every
	// immutable fact before any caller may commit the intent.
	reloaded, readErr := confirmationRead(ctx, tx, in.AISessionID, out.IntentID)
	if readErr != nil {
		return confirmation.Intent{}, confirmation.NewFailure("CONFIRMATION_PREPARE_READBACK_FAILED", "STORAGE", "PREPARE", "The prepared confirmation could not be read back within its prepare transaction.", confirmation.PrepareNewVersion, readErr)
	}
	if complete, reasonCode, reasonMessage := confirmation.IntentFactsComplete(&reloaded); !complete {
		return confirmation.Intent{}, confirmation.NewFailure("CONFIRMATION_PREPARE_FACTS_INCOMPLETE", "CORE", "PREPARE", reasonMessage, confirmation.PrepareNewVersion, errors.New(reasonCode))
	}
	if mismatch := confirmationPrepareReadbackMismatch(out, reloaded); mismatch != "" {
		return confirmation.Intent{}, confirmation.NewFailure("CONFIRMATION_PREPARE_READBACK_MISMATCH", "CORE", "PREPARE", "The confirmation read back from the production reader differs from the facts prepared in this transaction.", confirmation.PrepareNewVersion, errors.New(mismatch))
	}
	if err = captureFormalAdoptionPrepareTx(ctx, tx, reloaded); err != nil {
		return confirmation.Intent{}, err
	}
	if err = captureFormalExecutionPrepareTx(ctx, tx, reloaded); err != nil {
		return confirmation.Intent{}, err
	}
	if err = captureDefinitionApprovalPrepareTx(ctx, tx, reloaded); err != nil {
		return confirmation.Intent{}, err
	}
	if err = selectPreparedDecisionTx(ctx, tx, reloaded); err != nil {
		return confirmation.Intent{}, err
	}
	return reloaded, nil
}

// confirmationPrepareReadbackMismatch compares the immutable facts written in
// the prepare transaction against the facts the production reader
// reconstructs. It returns an empty string when they match, so prepare may be
// marked visible, and a diagnostic code otherwise. Field-set differences are
// detected by comparing the canonical content hash the intent froze.
func confirmationPrepareReadbackMismatch(written, reloaded confirmation.Intent) string {
	switch {
	case reloaded.IntentID != written.IntentID:
		return "INTENT_ID"
	case reloaded.Version != written.Version:
		return "VERSION"
	case reloaded.AISessionID != written.AISessionID:
		return "AI_SESSION_ID"
	case reloaded.ProjectID != written.ProjectID:
		return "PROJECT_ID"
	case reloaded.Purpose != written.Purpose:
		return "PURPOSE"
	case reloaded.Action != written.Action:
		return "ACTION"
	case reloaded.ContentHash != written.ContentHash:
		return "CONTENT_HASH"
	case confirmation.ValueHash(reloaded.Content) != confirmation.ValueHash(written.Content):
		return "CONTENT"
	case len(reloaded.Steps) != len(written.Steps):
		return "STEPS"
	}
	return ""
}

// ClassifyPreparationState is the read-only historical partial-intent
// classifier. It reconstructs the intent through the exact production reader used
// by confirmation_get and gathers only durable facts (intent state, the commit
// request ledger, any immutable receipt outcome and surviving business objects),
// then applies the authoritative taxonomy. It never mutates the intent, ledger
// or receipts and never infers non-consumption from missing client events.
func (s *ConfirmationStore) ClassifyPreparationState(ctx context.Context, session, intentID string) (confirmation.PreparationClassification, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return confirmation.PreparationClassification{}, err
	}
	defer tx.Rollback()
	out, err := confirmationRead(ctx, tx, session, intentID)
	if errors.Is(err, sql.ErrNoRows) {
		// No durable row: nothing was ever prepared.
		return confirmation.ClassifyPreparationEvidence(confirmation.PreparationEvidence{}), nil
	}
	if err != nil {
		return confirmation.PreparationClassification{}, err
	}
	complete, _, _ := confirmation.IntentFactsComplete(&out)
	evidence := confirmation.PreparationEvidence{
		IntentRowExists:      true,
		IntentFactsComplete:  complete,
		State:                out.State,
		CommitAttemptExists:  out.Commit != nil,
		BusinessObjectsExist: confirmationBusinessObjectsExistTx(ctx, tx, out),
	}
	var attempted int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM confirmation_commit_requests WHERE ai_session_id=? AND intent_id=?)`, session, intentID).Scan(&attempted); err != nil {
		return confirmation.PreparationClassification{}, err
	}
	if attempted == 1 {
		evidence.CommitAttemptExists = true
	}
	if out.Commit != nil {
		if sub, subErr := confirmationSubmissionTx(ctx, tx, out, out.Commit.RequestID); subErr == nil && sub.Recorded {
			evidence.ReceiptRecorded = true
			evidence.ReceiptOutcome = sub.Outcome
		}
	}
	return confirmation.ClassifyPreparationEvidence(evidence), nil
}

func confirmationBusinessObjectsExistTx(ctx context.Context, q definitionQuerier, intent confirmation.Intent) bool {
	exists := func(query, id string) bool {
		if id == "" {
			return false
		}
		var present int
		if err := q.QueryRowContext(ctx, query, id).Scan(&present); err != nil {
			return false
		}
		return present == 1
	}
	for _, step := range intent.Steps {
		if exists(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=?)`, step.TaskID) ||
			exists(`SELECT EXISTS(SELECT 1 FROM reviews WHERE id=?)`, step.ReviewID) ||
			exists(`SELECT EXISTS(SELECT 1 FROM runs WHERE id=?)`, step.RunID) {
			return true
		}
	}
	return false
}
func (s *ConfirmationStore) BeginCommit(ctx context.Context, in confirmation.CommitInput, steps []confirmation.Step) (out confirmation.Intent, fresh bool, err error) {
	// An original immutable request receipt is a read, including after a
	// writer epoch change. Unobserved requests still cross the writer fence
	// and repeat all identity/consumption checks under that transaction.
	read, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, false, err
	}
	out, err = confirmationRead(ctx, read, in.AISessionID, in.IntentID)
	if err != nil {
		read.Rollback()
		return out, false, err
	}
	if !confirmationCommitIdentity(out, in) {
		read.Rollback()
		return out, false, confirmation.ErrRequestConflict
	}
	prior, observed, err := confirmationReplayCommitTx(ctx, read, out, in)
	read.Rollback()
	if err != nil {
		return out, false, err
	}
	if observed {
		out.Submission = &prior
		if prior.Outcome == confirmation.RejectedNotConsumed {
			return out, false, prior.Failure
		}
		return out, false, nil
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback()
	out, err = confirmationRead(ctx, tx, in.AISessionID, in.IntentID)
	if err != nil {
		return
	}
	if !confirmationCommitIdentity(out, in) {
		return out, false, confirmation.ErrRequestConflict
	}
	prior, observed, err = confirmationReplayCommitTx(ctx, tx, out, in)
	if err != nil {
		return out, false, err
	}
	if observed {
		out.Submission = &prior
		if prior.Outcome == confirmation.RejectedNotConsumed {
			return out, false, prior.Failure
		}
		return out, false, nil
	}
	if out.Commit != nil {
		if !confirmationSameChoice(*out.Commit, in) {
			return out, false, confirmation.ErrRequestConflict
		}
		sub := confirmationConsumedSubmission(out, in)
		if err = confirmationInsertSubmissionTx(ctx, tx, in, sub); err != nil {
			return out, false, err
		}
		if err = tx.Commit(); err != nil {
			return out, false, err
		}
		out.Submission = &sub
		return out, false, nil
	}
	if out.State != "PENDING" || !time.Now().Before(out.ExpiresAt) {
		return out, false, ErrConflict
	}
	var project string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(project_id,'') FROM ai_sessions WHERE id=?`, in.AISessionID).Scan(&project); err != nil {
		return
	}
	if project != out.ProjectID {
		return out, false, ErrConflict
	}
	valid := false
	for _, v := range out.Content.Variants {
		if v.ID == in.VariantID {
			valid = true
		}
	}
	if !valid || in.RequestID == "" {
		return out, false, ErrConflict
	}
	if failure, fenceErr := confirmationCommitFenceTx(ctx, tx, out); fenceErr != nil {
		return out, false, fenceErr
	} else if failure != nil {
		sub := confirmation.Submission{Request: confirmationOriginalRequest(in), PayloadSHA256: in.SubmittedHash, Outcome: confirmation.RejectedNotConsumed, Recorded: true, Failure: failure, NextAction: failure.NextAction}
		if err = confirmationInsertSubmissionTx(ctx, tx, in, sub); err != nil {
			return out, false, err
		}
		if err = recordPurposeRejectionTx(ctx, tx, out, in, failure); err != nil {
			return out, false, err
		}
		if err = tx.Commit(); err != nil {
			return out, false, err
		}
		out.Submission = &sub
		return out, false, failure
	}
	sub := confirmationConsumedSubmission(out, in)
	if err = confirmationInsertSubmissionTx(ctx, tx, in, sub); err != nil {
		return out, false, err
	}
	// Keep the authorized pre-write snapshot until the transaction commits.
	// A save/commit error cannot expose staged choices or steps as durable facts.
	next := out
	in.OriginalRequest = nil
	next.Commit = &in
	next.Steps = steps
	next.State = "CONFIRMING"
	if _, err = tx.ExecContext(ctx, `UPDATE confirmation_intents SET worker_epoch=? WHERE intent_id=?`, s.db.epoch, out.IntentID); err != nil {
		return
	}
	if err = confirmationSave(ctx, tx, next); err != nil {
		return
	}
	err = tx.Commit()
	if err == nil {
		out = next
		out.Submission = &sub
	}
	return out, err == nil, err
}
func (s *ConfirmationStore) RecordStep(ctx context.Context, session, id string, version int, step confirmation.Step, state string) (out confirmation.Intent, err error) {
	// A stable historical result is read before acquiring writer/epoch fences.
	// New writes re-read the intent after taking the normal write lock below.
	if prior, completed, readErr := s.readCompletedStepReplay(ctx, session, id, version, step, state); readErr != nil || completed {
		if readErr == nil && step.Kind == "APPROVE" {
			_, readErr = recoverApprovalTx(ctx, s.db.sql, prior, &step)
		}
		return prior, readErr
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback()
	out, err = confirmationRead(ctx, tx, session, id)
	if err != nil {
		return
	}
	if out.Version != version || out.Commit == nil {
		return out, ErrConflict
	}
	// A completed intent is a durable result. A delayed worker may read back
	// its exact final step, but cannot rewrite it or reopen the workflow.
	if out.State == "COMPLETED" {
		if step.Kind == "APPROVE" {
			if _, err = recoverApprovalTx(ctx, tx, out, &step); err != nil {
				return out, err
			}
		}
		return out, completedStepReplay(out, version, step, state)
	}
	if step.Kind == "APPROVE" && step.State == "COMPLETED" {
		if _, err = recoverApprovalTx(ctx, tx, out, &step); err != nil {
			return out, err
		}
	}
	// Keep the returned value at its durable pre-write state on any failure.
	next := out
	next.Steps = append([]confirmation.Step(nil), out.Steps...)
	found := false
	for i, old := range out.Steps {
		if old.Kind == step.Kind && old.RequestID == step.RequestID {
			if old.State == "COMPLETED" {
				a, _ := json.Marshal(old)
				b, _ := json.Marshal(step)
				if string(a) != string(b) {
					return out, ErrConflict
				}
			}
			next.Steps[i] = step
			found = true
			break
		}
	}
	if !found {
		return out, fmt.Errorf("unknown confirmation step")
	}
	switch state {
	case "CONFIRMING", "PARTIAL", "UNKNOWN", "COMPLETED", "REJECTED":
	default:
		return out, ErrConflict
	}
	next.State = state
	if state == "COMPLETED" && !completedConfirmation(next) {
		return out, ErrConflict
	}
	if state == "COMPLETED" {
		// The immutable request must prove consumption, including exact typed
		// payload verification for historical receipts without raw submission.
		sub, readErr := confirmationSubmissionTx(ctx, tx, out, out.Commit.RequestID)
		if readErr != nil {
			return out, readErr
		}
		if !sub.Recorded || sub.Outcome != confirmation.Consumed || sub.ReceiptRequestID != out.Commit.RequestID {
			return out, ErrConflict
		}
	}
	if err = confirmationSave(ctx, tx, next); err != nil {
		return
	}
	err = tx.Commit()
	if err == nil {
		out = next
	}
	return
}
