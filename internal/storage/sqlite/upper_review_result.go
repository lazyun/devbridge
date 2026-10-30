package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
)

// UpperReviewResultStore is the sole durable writer for an Upper LLM Review
// result.  The receipt and product/run finalization share one SQLite writer
// transaction, so a retry can only observe the original terminal result.
type UpperReviewResultStore struct{ db *DB }

func NewUpperReviewResultStore(db *DB) *UpperReviewResultStore {
	return &UpperReviewResultStore{db: db}
}

func upperReportID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "report_" + hex.EncodeToString(raw[:]), nil
}

func reportPayload(in review.SubmitInput) ([]byte, string, error) {
	// The idempotency identity includes every fenced target field, not merely
	// the report body. A receipt therefore cannot be replayed onto another Run.
	b, err := json.Marshal(in)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}

func (s *UpperReviewResultStore) SubmitReviewResult(ctx context.Context, in review.SubmitInput) (review.ReportReceipt, error) {
	if !validUpperID(in.RequestID) || !validUpperTarget(in.UpperTarget) {
		return review.ReportReceipt{}, review.ErrInvalidArgument
	}
	if err := in.Report.Validate(); err != nil {
		return review.ReportReceipt{}, err
	}
	if in.Report.SchemaVersion == 2 {
		return s.submitReviewResultV2(ctx, in)
	}
	payload, digest, err := reportPayload(in)
	if err != nil {
		return review.ReportReceipt{}, err
	}
	reportJSON, err := json.Marshal(in.Report)
	if err != nil {
		return review.ReportReceipt{}, err
	}
	// Resolve completed receipts before any fresh authorization or source IO.
	observed, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return review.ReportReceipt{}, err
	}
	defer observed.Rollback()
	if receipt, found, e := upperReportReplayTx(ctx, observed, in, digest); e != nil || found {
		return receipt, e
	}
	if err = requireUpperWorkerSubmissionTx(ctx, observed, in); err != nil {
		observed.Rollback()
		return review.ReportReceipt{}, err
	}
	if err = in.Report.ValidateAdmission(); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = requireUpperReviewCurrentTargetTx(ctx, observed, in.UpperTarget); err != nil {
		return review.ReportReceipt{}, err
	}
	observation, err := observeUpperReportEvidence(ctx, observed, in)
	if err != nil {
		return review.ReportReceipt{}, err
	}
	if err = observed.Rollback(); err != nil {
		return review.ReportReceipt{}, err
	}

	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return review.ReportReceipt{}, err
	}
	defer tx.Rollback()

	// Another submission may have completed while source IO ran. Pure replay
	// still wins before the new-command lifecycle/target checks.
	if receipt, found, e := upperReportReplayTx(ctx, tx, in, digest); e != nil || found {
		return receipt, e
	}
	if err = requireUpperWorkerSubmissionTx(ctx, tx, in); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = requireUpperReviewCurrentTargetTx(ctx, tx, in.UpperTarget); err != nil {
		return review.ReportReceipt{}, err
	}

	var reviewOwner, executor, taskID, projectID string
	err = tx.QueryRowContext(ctx, `SELECT created_by_ai_session_id,executor_kind,task_id,project_id FROM reviews WHERE id=?`, in.ReviewID).Scan(&reviewOwner, &executor, &taskID, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return review.ReportReceipt{}, review.ErrNotFound
	}
	if err != nil {
		return review.ReportReceipt{}, err
	}
	if reviewOwner != in.AISessionID || executor != string(review.ExecutorUpperLLM) || in.FeatureAreaID == "" || in.TaskID == "" {
		return review.ReportReceipt{}, review.ErrExecutorMismatch
	}
	var finalized bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_reports WHERE review_id=?)`, in.ReviewID).Scan(&finalized); err != nil {
		return review.ReportReceipt{}, err
	}
	if finalized {
		return review.ReportReceipt{}, review.ErrResultFinalized
	}
	var baseline string
	var matches bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_grants g JOIN runs r ON r.id=g.run_id
 JOIN reviews v ON v.id=g.review_id JOIN tasks t ON t.id=v.task_id JOIN ai_sessions a ON a.id=v.created_by_ai_session_id
 JOIN runtime_authority ra ON ra.singleton=1 JOIN feature_area_agent_claims c ON c.feature_area_id=v.feature_area_id
 WHERE g.review_id=? AND g.run_id=? AND g.runtime_epoch=? AND g.generation=? AND g.state IN ('ACTIVE','AWAITING_REPORT')
 AND r.id=? AND r.review_id=? AND r.runtime_epoch=? AND r.status IN ('RUNNING','COLLECTING')
 AND v.created_by_ai_session_id=? AND v.feature_area_id=? AND v.task_id=? AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING'
 AND a.project_id=v.project_id AND ra.epoch_id=r.runtime_epoch AND c.operation_kind='REVIEW' AND c.operation_id=v.id AND c.run_id=r.id AND c.runtime_epoch=r.runtime_epoch),
	COALESCE((SELECT baseline FROM review_grants WHERE review_id=?), '')`, in.ReviewID, in.RunID, in.RuntimeEpoch, in.GrantGeneration, in.RunID, in.ReviewID, in.RuntimeEpoch, in.AISessionID, in.FeatureAreaID, in.TaskID, in.ReviewID).Scan(&matches, &baseline)
	if err != nil {
		return review.ReportReceipt{}, err
	}
	if !matches {
		return review.ReportReceipt{}, review.ErrTargetStale
	}
	if err = commitUpperReportObservationTx(ctx, tx, in, observation); err != nil {
		return review.ReportReceipt{}, err
	}

	if _, err = tx.ExecContext(ctx, `UPDATE review_grants SET state='FINALIZING' WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND state IN ('ACTIVE','AWAITING_REPORT')`, in.ReviewID, in.RunID, in.RuntimeEpoch, in.GrantGeneration); err != nil {
		return review.ReportReceipt{}, err
	}
	reportID, err := upperReportID()
	if err != nil {
		return review.ReportReceipt{}, err
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_result_receipts(review_id,request_id,ai_session_id,payload_sha256,payload_json,state,created_at) VALUES(?,?,?,?,?,'FINALIZING',?)`, in.ReviewID, in.RequestID, in.AISessionID, digest, string(payload), formatTime(now)); err != nil {
		return review.ReportReceipt{}, conflict(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_reports(id,review_id,run_id,ai_session_id,baseline,payload_sha256,report_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, reportID, in.ReviewID, in.RunID, in.AISessionID, baseline, digest, string(reportJSON), formatTime(now)); err != nil {
		return review.ReportReceipt{}, conflict(err)
	}
	compat, err := json.Marshal(in.Report.CompatibilityResult())
	if err != nil {
		return review.ReportReceipt{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status='SUCCEEDED',result_text=?,finished_at=?,updated_at=? WHERE id=? AND review_id=? AND runtime_epoch=? AND status IN ('RUNNING','COLLECTING')`, string(compat), formatTime(now), formatTime(now), in.RunID, in.ReviewID, in.RuntimeEpoch)
	if err != nil {
		return review.ReportReceipt{}, conflict(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return review.ReportReceipt{}, review.ErrTargetStale
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reviews SET review_disposition=? WHERE id=? AND review_disposition IS NULL`, string(in.Report.Disposition), in.ReviewID); err != nil {
		return review.ReportReceipt{}, err
	}
	if in.Report.Disposition == review.DispositionBlocked || in.Report.Disposition == review.DispositionFurtherReviewRequired {
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_REQUIRED',required_review_depth='DEEP',updated_at=? WHERE id=? AND current_review_id=? AND status='ACTIVE' AND phase='REVIEWING'`, formatTime(now), taskID, in.ReviewID); err != nil {
			return review.ReportReceipt{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE review_grants SET state='CLOSED' WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND state='FINALIZING'`, in.ReviewID, in.RunID, in.RuntimeEpoch, in.GrantGeneration); err != nil {
		return review.ReportReceipt{}, err
	}
	value, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, in.RunID))
	if err != nil {
		return review.ReportReceipt{}, err
	}
	if err = updateRunTokenUsage(ctx, tx, value); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = finalizeAgentRunTx(ctx, tx, value); err != nil {
		return review.ReportReceipt{}, err
	}
	eventPayload, _ := event.Payload(map[string]any{"from": run.StatusRunning, "to": run.StatusSucceeded, "purpose": value.Purpose, "review_id": in.ReviewID})
	if err = appendEvent(ctx, tx, event.Event{Type: event.RunStatusChanged, ProjectID: projectID, TaskID: taskID, RunID: in.RunID, EntityType: "run", EntityID: in.RunID, Payload: eventPayload, CreatedAt: now}); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = cancelOpenApprovalsExcept(ctx, tx, in.RunID, "", "run_terminal", projectID, taskID, now); err != nil {
		return review.ReportReceipt{}, err
	}
	receipt := review.ReportReceipt{RequestID: in.RequestID, ReviewID: in.ReviewID, RunID: in.RunID, ReportID: reportID, PayloadSHA256: digest, Status: "COMPLETED", Disposition: in.Report.Disposition, CompletedAt: now}
	stored, err := json.Marshal(receipt)
	if err != nil {
		return review.ReportReceipt{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE review_result_receipts SET state='COMPLETED',receipt_json=? WHERE review_id=? AND request_id=? AND ai_session_id=? AND state='FINALIZING'`, string(stored), in.ReviewID, in.RequestID, in.AISessionID); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = completeUpperWorkerSubmissionTx(ctx, tx, in, stored); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return review.ReportReceipt{}, err
	}
	// The formal materialization is a durable post-commit protocol, never an
	// in-transaction side effect of the report receipt.
	_ = materializeReviewResults(ctx, s.db, now)
	return receipt, nil
}

// submitReviewResultV2 finalizes one multi-member Upper Review. It never
// consults legacy repository/base columns: all authority comes from immutable
// Review targets and Run member bindings.
func (s *UpperReviewResultStore) submitReviewResultV2(ctx context.Context, in review.SubmitInput) (review.ReportReceipt, error) {
	if !validUpperID(in.RequestID) || !validUpperTarget(in.UpperTarget) {
		return review.ReportReceipt{}, review.ErrInvalidArgument
	}
	payload, digest, err := reportPayload(in)
	if err != nil {
		return review.ReportReceipt{}, err
	}
	reportJSON, err := json.Marshal(in.Report)
	if err != nil {
		return review.ReportReceipt{}, err
	}
	// A successful read only proves the source at the time it was read. Before
	// publishing an approval, observe every frozen member again outside the
	// writer transaction and then fence that observation inside the final write.
	observed, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return review.ReportReceipt{}, err
	}
	if receipt, found, e := upperReportReplayTx(ctx, observed, in, digest); e != nil || found {
		observed.Rollback()
		return receipt, e
	}
	if err = requireUpperWorkerSubmissionTx(ctx, observed, in); err != nil {
		observed.Rollback()
		return review.ReportReceipt{}, err
	}
	if err = in.Report.ValidateAdmission(); err != nil {
		observed.Rollback()
		return review.ReportReceipt{}, err
	}
	observation, err := observeUpperV2Report(ctx, observed, in)
	if err != nil {
		observed.Rollback()
		return review.ReportReceipt{}, err
	}
	if err = observed.Rollback(); err != nil {
		return review.ReportReceipt{}, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return review.ReportReceipt{}, err
	}
	defer tx.Rollback()
	if receipt, found, e := upperReportReplayTx(ctx, tx, in, digest); e != nil || found {
		return receipt, e
	}
	if err = requireUpperWorkerSubmissionTx(ctx, tx, in); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = requireUpperReviewCurrentTargetV2Tx(ctx, tx, in.UpperTarget); err != nil {
		return review.ReportReceipt{}, err
	}
	if _, err = upperV2ResultTargetsTx(ctx, tx, in); err != nil {
		return review.ReportReceipt{}, review.ErrTargetStale
	}
	if err = validateUpperV2ResultEvidenceTx(ctx, tx, in); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = commitUpperV2ReportObservation(ctx, tx, in, observation); err != nil {
		return review.ReportReceipt{}, err
	}
	var taskID, projectID string
	if err = tx.QueryRowContext(ctx, `SELECT task_id,project_id FROM reviews WHERE id=? AND created_by_ai_session_id=? AND executor_kind='UPPER_LLM'`, in.ReviewID, in.AISessionID).Scan(&taskID, &projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return review.ReportReceipt{}, review.ErrNotFound
		}
		return review.ReportReceipt{}, err
	}
	// A v2 report stores a stable aggregate of its admitted origins. Per-member
	// start snapshots remain on run_repository_bindings and are deliberately
	// not collapsed into a misleading scalar "current commit".
	targets, err := upperV2ResultTargetsTx(ctx, tx, in)
	if err != nil {
		return review.ReportReceipt{}, err
	}
	baseline, err := (workset.Set{Version: workset.Version, WorkspaceID: "review:" + in.ReviewID, Targets: targets}).Fingerprint()
	if err != nil {
		return review.ReportReceipt{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE review_grants SET state='FINALIZING' WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND protocol_version=2 AND state IN ('ACTIVE','AWAITING_REPORT')`, in.ReviewID, in.RunID, in.RuntimeEpoch, in.GrantGeneration); err != nil {
		return review.ReportReceipt{}, err
	}
	reportID, err := upperReportID()
	if err != nil {
		return review.ReportReceipt{}, err
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_result_receipts(review_id,request_id,ai_session_id,payload_sha256,payload_json,state,created_at) VALUES(?,?,?,?,?,'FINALIZING',?)`, in.ReviewID, in.RequestID, in.AISessionID, digest, string(payload), formatTime(now)); err != nil {
		return review.ReportReceipt{}, conflict(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_reports(id,review_id,run_id,ai_session_id,baseline,payload_sha256,report_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, reportID, in.ReviewID, in.RunID, in.AISessionID, baseline, digest, string(reportJSON), formatTime(now)); err != nil {
		return review.ReportReceipt{}, conflict(err)
	}
	compat, err := json.Marshal(in.Report.CompatibilityResult())
	if err != nil {
		return review.ReportReceipt{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status='SUCCEEDED',result_text=?,finished_at=?,updated_at=? WHERE id=? AND review_id=? AND runtime_epoch=? AND status IN ('RUNNING','COLLECTING')`, string(compat), formatTime(now), formatTime(now), in.RunID, in.ReviewID, in.RuntimeEpoch)
	if err != nil {
		return review.ReportReceipt{}, conflict(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return review.ReportReceipt{}, review.ErrTargetStale
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reviews SET review_disposition=? WHERE id=? AND review_disposition IS NULL`, string(in.Report.Disposition), in.ReviewID); err != nil {
		return review.ReportReceipt{}, err
	}
	if in.Report.Disposition != review.DispositionApprovalReady {
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_REQUIRED',required_review_depth='DEEP',updated_at=? WHERE id=? AND current_review_id=? AND status='ACTIVE' AND phase='REVIEWING'`, formatTime(now), taskID, in.ReviewID); err != nil {
			return review.ReportReceipt{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE review_grants SET state='CLOSED' WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND protocol_version=2 AND state='FINALIZING'`, in.ReviewID, in.RunID, in.RuntimeEpoch, in.GrantGeneration); err != nil {
		return review.ReportReceipt{}, err
	}
	value, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, in.RunID))
	if err != nil {
		return review.ReportReceipt{}, err
	}
	if err = updateRunTokenUsage(ctx, tx, value); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = finalizeAgentRunTx(ctx, tx, value); err != nil {
		return review.ReportReceipt{}, err
	}
	eventPayload, _ := event.Payload(map[string]any{"from": run.StatusRunning, "to": run.StatusSucceeded, "purpose": value.Purpose, "review_id": in.ReviewID})
	if err = appendEvent(ctx, tx, event.Event{Type: event.RunStatusChanged, ProjectID: projectID, TaskID: taskID, RunID: in.RunID, EntityType: "run", EntityID: in.RunID, Payload: eventPayload, CreatedAt: now}); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = cancelOpenApprovalsExcept(ctx, tx, in.RunID, "", "run_terminal", projectID, taskID, now); err != nil {
		return review.ReportReceipt{}, err
	}
	receipt := review.ReportReceipt{RequestID: in.RequestID, ReviewID: in.ReviewID, RunID: in.RunID, ReportID: reportID, PayloadSHA256: digest, Status: "COMPLETED", Disposition: in.Report.Disposition, CompletedAt: now}
	stored, err := json.Marshal(receipt)
	if err != nil {
		return review.ReportReceipt{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE review_result_receipts SET state='COMPLETED',receipt_json=? WHERE review_id=? AND request_id=? AND ai_session_id=? AND state='FINALIZING'`, string(stored), in.ReviewID, in.RequestID, in.AISessionID); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = completeUpperWorkerSubmissionTx(ctx, tx, in, stored); err != nil {
		return review.ReportReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return review.ReportReceipt{}, err
	}
	// The formal materialization is a durable post-commit protocol, never an
	// in-transaction side effect of the report receipt.
	_ = materializeReviewResults(ctx, s.db, now)
	return receipt, nil
}

// Findings can omit an implementation proposal; their evidence still resolves
// exclusively against the immutable admitted read set.
func upperV2ResultTargetsTx(ctx context.Context, tx *sql.Tx, in review.SubmitInput) ([]workset.Target, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT repository_targets_json FROM reviews WHERE id=? AND workspace_id<>''`, in.ReviewID).Scan(&raw); err != nil {
		return nil, err
	}
	var targets []workset.Target
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil, err
	}
	if err := workset.ValidateTargets(targets); err != nil {
		return nil, err
	}
	if len(in.Report.RepositoryTargets) > 0 || in.Report.Disposition == review.DispositionApprovalReady {
		if err := validateApprovedReviewTargetsTx(ctx, tx, in.ReviewID, in.Report.RepositoryTargets); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func validateUpperV2ResultEvidenceTx(ctx context.Context, tx *sql.Tx, in review.SubmitInput) error {
	targets, err := upperV2ResultTargetsTx(ctx, tx, in)
	if err != nil {
		return err
	}
	refs := reportEvidenceRefs(in.Report)
	if in.Report.Disposition == review.DispositionApprovalReady && len(refs) == 0 {
		return fmt.Errorf("%w: approval requires successful read evidence", review.ErrEvidenceMismatch)
	}
	for _, ref := range refs {
		var ok bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_access_evidence e JOIN run_repository_bindings b ON b.run_id=e.run_id AND b.repository_id=e.repository_id
 WHERE e.id=? AND e.review_id=? AND e.run_id=? AND e.runtime_epoch=? AND e.grant_generation=? AND e.operation='UPPER_LLM_FILE_READ' AND e.outcome='SUCCESS'
 AND e.repository_id=? AND b.member_id=? AND b.member_version=? AND e.path=? AND e.digest=? AND b.preparation='READY' AND (?=0 OR (e.line_count>=? AND ?<=e.line_count)))`, ref.EvidenceID, in.ReviewID, in.RunID, in.RuntimeEpoch, in.GrantGeneration, ref.RepositoryID, ref.MemberID, memberVersionForTarget(targets, ref.RepositoryID, ref.MemberID), ref.Path, ref.Digest, ref.StartLine, ref.EndLine, ref.EndLine).Scan(&ok)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: evidence %s", review.ErrEvidenceMismatch, ref.EvidenceID)
		}
	}
	if in.Report.Disposition == review.DispositionApprovalReady {
		var incomplete bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_repository_bindings b WHERE b.run_id=? AND (b.preparation<>'READY' OR NOT EXISTS(SELECT 1 FROM review_access_evidence e WHERE e.review_id=? AND e.run_id=b.run_id AND e.repository_id=b.repository_id AND e.operation='UPPER_LLM_FILE_READ' AND e.outcome='SUCCESS' AND e.runtime_epoch=? AND e.grant_generation=?)))`, in.RunID, in.ReviewID, in.RuntimeEpoch, in.GrantGeneration).Scan(&incomplete)
		if err != nil {
			return err
		}
		if incomplete {
			return fmt.Errorf("%w: each target needs read evidence", review.ErrEvidenceMismatch)
		}
	}
	return nil
}

type upperV2ReportMemberObservation struct {
	repositoryID, memberID, worktreeID, rootPath, rootIdentity, storedRootPath, storedRootIdentity, startSnapshot, snapshot string
	memberVersion                                                                                                           int
}
type upperV2ReportObservation struct {
	allowedJSON, forbiddenJSON string
	members                    []upperV2ReportMemberObservation
}

// observeUpperV2Report validates the report against the actual filesystem,
// not only against previously persisted read evidence. Its companion below
// repeats the observation after the writer transaction has fenced lifecycle.
func observeUpperV2Report(ctx context.Context, tx *sql.Tx, in review.SubmitInput) (upperV2ReportObservation, error) {
	var out upperV2ReportObservation
	if err := requireUpperReviewCurrentTargetV2Tx(ctx, tx, in.UpperTarget); err != nil {
		return out, err
	}
	targets, err := upperV2ResultTargetsTx(ctx, tx, in)
	if err != nil {
		return out, review.ErrTargetStale
	}
	if err := validateUpperV2ResultEvidenceTx(ctx, tx, in); err != nil {
		return out, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT initial_scope_paths_json,forbidden_paths_json FROM review_grants WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND protocol_version=2 AND state IN ('ACTIVE','AWAITING_REPORT')`, in.ReviewID, in.RunID, in.RuntimeEpoch, in.GrantGeneration).Scan(&out.allowedJSON, &out.forbiddenJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, review.ErrTargetStale
		}
		return out, err
	}
	out.forbiddenJSON, err = reviewForbiddenPathsJSON(ctx, tx, in.ReviewID, out.forbiddenJSON)
	if err != nil {
		return out, err
	}
	allowed, err := decodeReviewScopePaths(out.allowedJSON)
	if err != nil {
		return out, err
	}
	var forbidden []string
	if err = json.Unmarshal([]byte(out.forbiddenJSON), &forbidden); err != nil {
		return out, err
	}
	// An evidence-free BLOCKED/findings report must remain publishable when
	// its source is unavailable. It grants no implementation authority.
	if in.Report.Disposition != review.DispositionApprovalReady && len(reportEvidenceRefs(in.Report)) == 0 {
		return out, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT b.repository_id,b.member_id,b.member_version,b.worktree_id,COALESCE(b.start_snapshot,''),COALESCE(b.root_path,''),COALESCE(b.root_identity,''),wt.path
 FROM run_repository_bindings b JOIN worktrees wt ON wt.id=b.worktree_id AND wt.status='READY'
 WHERE b.run_id=? AND b.review_id=? AND b.preparation='READY' ORDER BY b.repository_id`, in.RunID, in.ReviewID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var member upperV2ReportMemberObservation
		var configuredPath string
		if err = rows.Scan(&member.repositoryID, &member.memberID, &member.memberVersion, &member.worktreeID, &member.startSnapshot, &member.rootPath, &member.rootIdentity, &configuredPath); err != nil {
			return out, err
		}
		member.storedRootPath, member.storedRootIdentity = member.rootPath, member.rootIdentity
		member.rootPath, err = upperV2CanonicalRoot(configuredPath)
		if err != nil {
			return out, review.ErrTargetStale
		}
		member.rootIdentity, err = upperV2RootIdentity(member.rootPath)
		if err != nil {
			return out, review.ErrTargetStale
		}
		member.snapshot, err = reviewaccess.ScopedSnapshot(ctx, member.rootPath, allowed, forbidden)
		if err != nil || member.startSnapshot == "" || member.snapshot != member.startSnapshot {
			return out, fmt.Errorf("%w: source snapshot changed for %s", review.ErrEvidenceMismatch, member.repositoryID)
		}
		out.members = append(out.members, member)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.members) != len(targets) {
		return out, review.ErrTargetStale
	}
	// Re-open each referenced source through the exact recorded member root.
	for _, ref := range reportEvidenceRefs(in.Report) {
		member := upperV2ReportMember(out.members, ref.RepositoryID, ref.MemberID)
		if member == nil {
			return out, review.ErrEvidenceMismatch
		}
		content, outcome, _, e := upperReadFile(member.worktreeID, ref.Path, allowed, forbidden, tx, ctx)
		if e != nil || outcome != review.AccessSuccess || content.Digest != ref.Digest || (ref.EndLine != 0 && ref.EndLine > strings.Count(content.Content, "\n")+1) {
			return out, fmt.Errorf("%w: evidence at %s is stale", review.ErrEvidenceMismatch, ref.Path)
		}
	}
	return out, nil
}

func upperV2ReportMember(members []upperV2ReportMemberObservation, repositoryID, memberID string) *upperV2ReportMemberObservation {
	for i := range members {
		if members[i].repositoryID == repositoryID && members[i].memberID == memberID {
			return &members[i]
		}
	}
	return nil
}

func commitUpperV2ReportObservation(ctx context.Context, tx *sql.Tx, in review.SubmitInput, observed upperV2ReportObservation) error {
	if err := requireUpperReviewCurrentTargetV2Tx(ctx, tx, in.UpperTarget); err != nil {
		return err
	}
	if err := validateUpperV2ResultEvidenceTx(ctx, tx, in); err != nil {
		return err
	}
	// The authoritative writer re-resolves every row and re-observes each root;
	// this fences response loss, root swaps, cancellation/recovery generations,
	// and filesystem changes that occurred after the read transaction.
	return verifyUpperV2Observation(ctx, tx, in, observed)
}

func verifyUpperV2Observation(ctx context.Context, tx *sql.Tx, in review.SubmitInput, observed upperV2ReportObservation) error {
	allowed, err := decodeReviewScopePaths(observed.allowedJSON)
	if err != nil {
		return err
	}
	var forbidden []string
	if err = json.Unmarshal([]byte(observed.forbiddenJSON), &forbidden); err != nil {
		return err
	}
	for _, prior := range observed.members {
		var path, start, rootPath, rootIdentity string
		var version int
		err = tx.QueryRowContext(ctx, `SELECT wt.path,COALESCE(b.start_snapshot,''),COALESCE(b.root_path,''),COALESCE(b.root_identity,''),b.member_version FROM run_repository_bindings b JOIN worktrees wt ON wt.id=b.worktree_id AND wt.status='READY' WHERE b.run_id=? AND b.review_id=? AND b.repository_id=? AND b.member_id=? AND b.preparation='READY'`, in.RunID, in.ReviewID, prior.repositoryID, prior.memberID).Scan(&path, &start, &rootPath, &rootIdentity, &version)
		if err != nil || version != prior.memberVersion || start != prior.startSnapshot {
			return review.ErrTargetStale
		}
		canonical, e := upperV2CanonicalRoot(path)
		if e != nil || canonical != prior.rootPath || canonical != prior.storedRootPath || canonical != rootPath {
			return review.ErrTargetStale
		}
		identity, e := upperV2RootIdentity(canonical)
		if e != nil || identity != prior.rootIdentity || identity != prior.storedRootIdentity || identity != rootIdentity {
			return review.ErrTargetStale
		}
		snapshot, e := reviewaccess.ScopedSnapshot(ctx, canonical, allowed, forbidden)
		if e != nil || snapshot != prior.snapshot || snapshot != start {
			return review.ErrEvidenceMismatch
		}
	}
	return nil
}
func memberVersionForTarget(targets []workset.Target, repositoryID, memberID string) int {
	for _, t := range targets {
		if t.RepositoryID == repositoryID && t.MemberID == memberID {
			return t.MemberVersion
		}
	}
	return 0
}

// upperReportReplayTx only returns immutable completed results. It neither
// prepares a grant nor repeats report finalization, even after retirement.
func upperReportReplayTx(ctx context.Context, tx *sql.Tx, in review.SubmitInput, digest string) (review.ReportReceipt, bool, error) {
	var owner, oldDigest, state string
	var encoded sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT ai_session_id,payload_sha256,state,receipt_json FROM review_result_receipts WHERE review_id=? AND request_id=?`, in.ReviewID, in.RequestID).Scan(&owner, &oldDigest, &state, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return review.ReportReceipt{}, false, nil
	}
	if err != nil {
		return review.ReportReceipt{}, false, err
	}
	if owner != in.AISessionID {
		return review.ReportReceipt{}, true, review.ErrNotFound
	}
	// Claimed report replay remains a pure historical receipt read, but it
	// still belongs to the authenticated worker claim's caller scope.
	if in.WorkerClaim != nil {
		scope, e := workerScopeTx(ctx, tx, in.AISessionID)
		if e != nil {
			return review.ReportReceipt{}, true, e
		}
		var matches bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM upper_review_worker_claims WHERE run_id=? AND review_id=? AND ai_session_id=? AND caller_scope=? AND request_id=? AND worker_id=?)`, in.RunID, in.ReviewID, in.AISessionID, scope, in.WorkerClaim.RequestID, in.WorkerClaim.WorkerID).Scan(&matches)
		if e != nil {
			return review.ReportReceipt{}, true, e
		}
		if !matches {
			return review.ReportReceipt{}, true, review.ErrNotFound
		}
	}
	if oldDigest != digest {
		return review.ReportReceipt{}, true, review.ErrRequestConflict
	}
	if state != "COMPLETED" || !encoded.Valid {
		return review.ReportReceipt{}, true, review.ErrFinalizing
	}
	var receipt review.ReportReceipt
	err = json.Unmarshal([]byte(encoded.String), &receipt)
	return receipt, true, err
}

type upperReportObservation struct {
	fence           string
	worktreeID      string
	snapshot        upperSnapshotObservation
	checkedSnapshot bool
}

// upperReportFenceTx fingerprints immutable evidence and all source bindings.
// A newly committed read during report IO requires a fresh report attempt;
// it cannot introduce unvalidated content into a successful report.
func upperReportFenceTx(ctx context.Context, tx *sql.Tx, in review.SubmitInput) (string, string, string, string, error) {
	var worktreeID, allowedJSON, forbiddenJSON, binding, evidence string
	err := tx.QueryRowContext(ctx, `SELECT g.worktree_id,g.initial_scope_paths_json,g.forbidden_paths_json,
 json_array(g.run_id,g.runtime_epoch,g.generation,g.state,g.workspace_id,g.worktree_id,g.baseline,g.initial_scope_paths_json,g.forbidden_paths_json,w.path,r.start_snapshot),
 COALESCE((SELECT json_group_array(json_array(id,path,digest,byte_count,line_count,baseline,workspace_id,worktree_id)) FROM
 (SELECT e.id,e.path,e.digest,e.byte_count,e.line_count,e.baseline,e.workspace_id,e.worktree_id FROM review_access_evidence e
 WHERE e.review_id=g.review_id AND e.run_id=g.run_id AND e.runtime_epoch=g.runtime_epoch AND e.grant_generation=g.generation AND e.operation='UPPER_LLM_FILE_READ' AND e.outcome='SUCCESS' ORDER BY e.id)), '[]')
 FROM review_grants g JOIN runs r ON r.id=g.run_id JOIN worktrees w ON w.id=g.worktree_id
 WHERE g.review_id=? AND g.run_id=? AND g.runtime_epoch=? AND g.generation=? AND g.state IN ('ACTIVE','AWAITING_REPORT')`, in.ReviewID, in.RunID, in.RuntimeEpoch, in.GrantGeneration).Scan(&worktreeID, &allowedJSON, &forbiddenJSON, &binding, &evidence)
	if errors.Is(err, sql.ErrNoRows) {
		err = review.ErrTargetStale
	}
	if err != nil {
		return "", "", "", "", err
	}
	forbiddenJSON, err = reviewForbiddenPathsJSON(ctx, tx, in.ReviewID, forbiddenJSON)
	if err != nil {
		return "", "", "", "", err
	}
	return binding + "\n" + forbiddenJSON + "\n" + evidence, worktreeID, allowedJSON, forbiddenJSON, nil
}

func validateUpperEvidenceBindingsTx(ctx context.Context, tx *sql.Tx, in review.SubmitInput) error {
	refs := reportEvidenceRefs(in.Report)
	if len(refs) == 0 && in.Report.Disposition == review.DispositionApprovalReady {
		return fmt.Errorf("%w: approval requires successful read evidence", review.ErrEvidenceMismatch)
	}
	for _, ref := range refs {
		if ref.Path == "" || ref.Digest == "" {
			return fmt.Errorf("%w: approval references require path and digest", review.ErrEvidenceMismatch)
		}
		var ok bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_access_evidence e JOIN review_grants g ON g.review_id=e.review_id WHERE e.id=? AND e.review_id=? AND e.run_id=? AND e.runtime_epoch=? AND e.grant_generation=? AND e.baseline=g.baseline AND e.workspace_id=g.workspace_id AND e.worktree_id=g.worktree_id AND e.operation='UPPER_LLM_FILE_READ' AND e.outcome='SUCCESS' AND e.path=? AND e.digest=? AND (?=0 OR (e.line_count>=? AND ?<=e.line_count)))`, ref.EvidenceID, in.ReviewID, in.RunID, in.RuntimeEpoch, in.GrantGeneration, ref.Path, ref.Digest, ref.StartLine, ref.EndLine, ref.EndLine).Scan(&ok)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: evidence %s", review.ErrEvidenceMismatch, ref.EvidenceID)
		}
	}
	return nil
}

func observeUpperReportEvidence(ctx context.Context, tx *sql.Tx, in review.SubmitInput) (upperReportObservation, error) {
	var observed upperReportObservation
	if err := validateUpperEvidenceBindingsTx(ctx, tx, in); err != nil {
		return observed, err
	}
	var allowedJSON, forbiddenJSON string
	var err error
	observed.fence, observed.worktreeID, allowedJSON, forbiddenJSON, err = upperReportFenceTx(ctx, tx, in)
	if err != nil {
		return observed, err
	}
	refs := reportEvidenceRefs(in.Report)
	if len(refs) == 0 && in.Report.Disposition != review.DispositionApprovalReady {
		return observed, nil
	}
	allowed, err := decodeReviewScopePaths(allowedJSON)
	if err != nil {
		return observed, err
	}
	var forbidden []string
	if err = json.Unmarshal([]byte(forbiddenJSON), &forbidden); err != nil {
		return observed, err
	}
	if in.Report.Disposition == review.DispositionApprovalReady {
		observed.snapshot, err = observeUpperSnapshot(ctx, tx, in.UpperTarget, observed.worktreeID, allowedJSON, forbiddenJSON, false)
		if err != nil {
			return observed, err
		}
		observed.checkedSnapshot = true
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT path,digest FROM review_access_evidence WHERE review_id=? AND run_id=? AND runtime_epoch=? AND grant_generation=? AND operation='UPPER_LLM_FILE_READ' AND outcome='SUCCESS'`, in.ReviewID, in.RunID, in.RuntimeEpoch, in.GrantGeneration)
		if err != nil {
			return observed, err
		}
		for rows.Next() {
			var ref review.EvidenceRef
			if err = rows.Scan(&ref.Path, &ref.Digest); err != nil {
				rows.Close()
				return observed, err
			}
			refs = append(refs, ref)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return observed, err
		}
	}
	for _, ref := range refs {
		current, outcome, _, err := upperReadFile(observed.worktreeID, ref.Path, allowed, forbidden, tx, ctx)
		if err != nil || outcome != review.AccessSuccess || current.Digest != ref.Digest || (ref.EndLine != 0 && ref.EndLine > strings.Count(current.Content, "\n")+1) {
			return observed, fmt.Errorf("%w: evidence at %s is stale", review.ErrEvidenceMismatch, ref.Path)
		}
	}
	return observed, nil
}

// This final writer-side check contains SQL only: source IO has already been
// observed. It validates lifecycle again, and rejects evidence/grant changes
// while the read snapshot was open before any terminal result is published.
func commitUpperReportObservationTx(ctx context.Context, tx *sql.Tx, in review.SubmitInput, observed upperReportObservation) error {
	if err := requireUpperReviewCurrentTargetTx(ctx, tx, in.UpperTarget); err != nil {
		return err
	}
	if err := validateUpperEvidenceBindingsTx(ctx, tx, in); err != nil {
		return err
	}
	fence, worktreeID, _, _, err := upperReportFenceTx(ctx, tx, in)
	if err != nil {
		return err
	}
	if fence != observed.fence || worktreeID != observed.worktreeID {
		return review.ErrTargetStale
	}
	if observed.checkedSnapshot {
		_, err = commitUpperSnapshotTx(ctx, tx, in.UpperTarget, worktreeID, observed.snapshot, false)
	}
	return err
}

func reportEvidenceRefs(report review.ReviewReport) []review.EvidenceRef {
	refs := append([]review.EvidenceRef{}, report.EvidenceRefs...)
	for _, finding := range report.Findings {
		refs = append(refs, finding.EvidenceRefs...)
	}
	return refs
}

func (s *UpperReviewResultStore) GetReviewReportForSession(ctx context.Context, aiSessionID, reviewID string) (review.StoredReport, error) {
	var value review.StoredReport
	var raw, created string
	err := s.db.sql.QueryRowContext(ctx, `SELECT p.id,p.review_id,p.run_id,p.ai_session_id,p.baseline,p.payload_sha256,p.report_json,p.created_at,COALESCE((SELECT start_snapshot FROM runs WHERE id=p.run_id),'') FROM review_reports p WHERE p.review_id=? AND p.ai_session_id=?`, reviewID, aiSessionID).Scan(&value.ReportID, &value.ReviewID, &value.RunID, &value.CreatedByAISessionID, &value.Baseline, &value.PayloadSHA256, &raw, &created, &value.StartSnapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return value, review.ErrNotFound
	}
	if err != nil {
		return value, err
	}
	if err = json.Unmarshal([]byte(raw), &value.Report); err != nil {
		return value, err
	}
	value.CreatedAt, err = parseTime(created)
	return value, err
}

func (s *UpperReviewResultStore) GetReviewResultReceiptForSession(ctx context.Context, aiSessionID, reviewID, requestID string) (review.ReportReceipt, error) {
	var owner, state, raw string
	err := s.db.sql.QueryRowContext(ctx, `SELECT ai_session_id,state,COALESCE(receipt_json,'') FROM review_result_receipts WHERE review_id=? AND request_id=?`, reviewID, requestID).Scan(&owner, &state, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return review.ReportReceipt{}, review.ErrNotFound
	}
	if err != nil {
		return review.ReportReceipt{}, err
	}
	if owner != aiSessionID {
		return review.ReportReceipt{}, review.ErrNotFound
	}
	if state != "COMPLETED" || raw == "" {
		return review.ReportReceipt{}, review.ErrFinalizing
	}
	var value review.ReportReceipt
	err = json.Unmarshal([]byte(raw), &value)
	return value, err
}

var _ = run.StatusSucceeded
