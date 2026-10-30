package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
)

const naturalExecutionV130SQL = `
CREATE TABLE run_execution_records (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 protocol TEXT NOT NULL CHECK(protocol='NATURAL_V1'),
 commands_json TEXT CHECK(commands_json IS NULL OR json_valid(commands_json)),
 created_at TEXT NOT NULL
);
CREATE TRIGGER natural_result_identity BEFORE UPDATE OF run_id,protocol,created_at ON run_execution_records BEGIN SELECT RAISE(ABORT,'execution result contract immutable'); END;
CREATE TRIGGER natural_result_evidence BEFORE UPDATE OF commands_json ON run_execution_records WHEN OLD.commands_json IS NOT NULL BEGIN SELECT RAISE(ABORT,'execution evidence immutable'); END;
CREATE TRIGGER natural_result_no_delete BEFORE DELETE ON run_execution_records BEGIN SELECT RAISE(ABORT,'execution result contract durable'); END;
CREATE TABLE run_analyses (
 run_id TEXT PRIMARY KEY REFERENCES run_execution_records(run_id),
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 request_id TEXT NOT NULL,
 payload_hash TEXT NOT NULL,
 source_hash TEXT NOT NULL,
 kind TEXT NOT NULL,
 report_json TEXT NOT NULL CHECK(json_valid(report_json)),
 review_result_json TEXT CHECK(review_result_json IS NULL OR json_valid(review_result_json)),
 receipt_json TEXT NOT NULL CHECK(json_valid(receipt_json)),
 created_at TEXT NOT NULL,
 UNIQUE(ai_session_id,request_id)
);
CREATE TRIGGER run_analysis_no_update BEFORE UPDATE ON run_analyses BEGIN SELECT RAISE(ABORT,'analysis immutable'); END;
CREATE TRIGGER run_analysis_no_delete BEFORE DELETE ON run_analyses BEGIN SELECT RAISE(ABORT,'analysis durable'); END;
DROP TRIGGER workflow_event_enqueue;
CREATE TRIGGER workflow_event_enqueue AFTER INSERT ON events
WHEN NEW.task_id IS NOT NULL AND NEW.type IN ('task.status_changed','run.status_changed','run.analysis_recorded','approval.requested','execution.completed','execution.implementation_completed','execution.verification_completed','runtime.input_required')
BEGIN
 INSERT INTO workflow_event_deliveries(subscription_id,event_id,next_attempt_at)
 SELECT s.id,NEW.id,NEW.created_at FROM workflow_event_subscriptions s
 JOIN ai_sessions a ON a.id=s.ai_session_id AND a.project_id=s.project_id
 WHERE s.state='ACTIVE' AND s.task_id=NEW.task_id AND s.project_id=NEW.project_id
 AND julianday(s.expires_at)>julianday('now');
END;
`

func (s *RunStore) PrepareNaturalResult(ctx context.Context, id string) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var thread, turn, status string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(thread_id,''),COALESCE(turn_id,''),status FROM runs WHERE id=?`, id).Scan(&thread, &turn, &status)
	if err != nil {
		return err
	}
	if err = s.db.checkUsageRunTx(ctx, tx, id, thread, turn); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_execution_records WHERE run_id=?)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	if turn != "" || (status != "PREPARING" && status != "RUNNING") {
		return runanalysis.ErrStale
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_execution_records(run_id,protocol,created_at) VALUES(?,'NATURAL_V1',?)`, id, formatTime(time.Now().UTC()))
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *RunStore) RecordExecutionCommands(ctx context.Context, id string, commands json.RawMessage) error {
	if !json.Valid(commands) || len(commands) > 16<<20 {
		return runanalysis.ErrInvalid
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var thread, turn string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(thread_id,''),COALESCE(turn_id,'') FROM runs WHERE id=?`, id).Scan(&thread, &turn); err != nil {
		return err
	}
	if err = s.db.checkRunTx(ctx, tx, id); err != nil {
		return err
	}
	if thread == "" || turn == "" {
		return runanalysis.ErrStale
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND status IN ('RUNNING','COLLECTING') AND thread_id=? AND turn_id=?)`, id, thread, turn).Scan(&active); err != nil {
		return err
	}
	if !active {
		return runanalysis.ErrStale
	}
	var old sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT commands_json FROM run_execution_records WHERE run_id=?`, id).Scan(&old); err != nil {
		return err
	}
	if old.Valid {
		if old.String == string(commands) {
			return nil
		}
		return runanalysis.ErrConflict
	}
	_, err = tx.ExecContext(ctx, `UPDATE run_execution_records SET commands_json=? WHERE run_id=? AND commands_json IS NULL`, string(commands), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

type RunAnalysisStore struct{ db *DB }

func NewRunAnalysisStore(db *DB) *RunAnalysisStore { return &RunAnalysisStore{db} }

func analysisScope(ctx context.Context, q definitionQuerier, session, id string) error {
	if err := instructionCaller(ctx, q, session); err != nil {
		return err
	}
	var allowed bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN ai_sessions a ON a.project_id=r.project_id WHERE a.id=? AND r.id=?)`, session, id).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return runanalysis.ErrStale
	}
	return nil
}
func (s *RunAnalysisStore) Find(ctx context.Context, in runanalysis.Input) (runanalysis.Receipt, bool, error) {
	var receipt runanalysis.Receipt
	if err := analysisScope(ctx, s.db.sql, in.AISessionID, in.RunID); err != nil {
		return receipt, false, err
	}
	var owner, id, hash, raw string
	err := s.db.sql.QueryRowContext(ctx, `SELECT ai_session_id,run_id,payload_hash,receipt_json FROM run_analyses WHERE run_id=? OR (ai_session_id=? AND request_id=?)`, in.RunID, in.AISessionID, in.RequestID).Scan(&owner, &id, &hash, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, false, err
	}
	if owner != in.AISessionID || id != in.RunID || hash != runanalysis.Hash(in) {
		return receipt, false, runanalysis.ErrConflict
	}
	err = json.Unmarshal([]byte(raw), &receipt)
	return receipt, err == nil, err
}
func (s *RunAnalysisStore) Source(ctx context.Context, session, id string) (runanalysis.Source, error) {
	return analysisSource(ctx, s.db.sql, session, id)
}
func analysisSource(ctx context.Context, q definitionQuerier, session, id string) (runanalysis.Source, error) {
	out := runanalysis.Source{State: "NOT_REQUIRED", Commands: []run.ObservedCommand{}}
	if err := analysisScope(ctx, q, session, id); err != nil {
		return out, err
	}
	v, err := scanRun(q.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, id))
	if err != nil {
		return out, err
	}
	if v.Result != nil {
		observed, e := readGitEvidence(ctx, q, v.ID)
		if e == nil {
			v.Result.GitEvidence = observed
		} else if !errors.Is(e, ErrNotFound) {
			return out, e
		}
	}
	out.Run = v
	if v.ResultProtocol != run.NaturalResultProtocol {
		return out, nil
	}
	out.State = "WAITING_EXECUTION"
	if v.Status == run.StatusFailed || v.Status == run.StatusInfrastructureFailed || v.Status == run.StatusCanceled {
		out.State = "NOT_REQUIRED"
	}
	var commands sql.NullString
	if err = q.QueryRowContext(ctx, `SELECT commands_json FROM run_execution_records WHERE run_id=?`, id).Scan(&commands); err != nil {
		return out, err
	}
	if commands.Valid {
		var evidence run.ExecutionEvidence
		if json.Unmarshal([]byte(commands.String), &evidence) != nil {
			return out, runanalysis.ErrInvalid
		}
		out.Commands, out.ObservedChecks = evidence.Commands, evidence.Checks
	}
	switch {
	case v.Purpose == run.PurposeRecoveryStatus:
		out.Kind = "RECOVERY"
		recovered, e := scanRecovery(q.QueryRowContext(ctx, `SELECT record_json FROM recovery_status_records WHERE recovery_run_id=?`, v.ID))
		if e != nil {
			return out, e
		}
		out.Recovery = &recovered
		var ev run.ExecutionEvidence
		if commands.Valid {
			if e = json.Unmarshal([]byte(commands.String), &ev); e != nil {
				return out, e
			}
			out.RecoveryAfter = ev.RecoveryAfter
		}
	case v.OperationKind == run.OperationInspection:
		out.Kind = "INSPECTION"
	case v.ReviewID != "":
		out.Kind = "REVIEW"
		reviewed, e := scanReview(q.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, v.ReviewID))
		if e != nil {
			return out, e
		}
		out.Review = &reviewed
	case v.Purpose == run.PurposeVerification:
		out.Kind = "VERIFICATION"
		var spec string
		if err = q.QueryRowContext(ctx, `SELECT verification_spec_json FROM execution_proposals WHERE id=? AND task_id=?`, v.ExecutionProposalID, v.TaskID).Scan(&spec); err != nil {
			return out, err
		}
		if out.VerificationSpec, err = decodeVerificationSpec(spec); err != nil {
			return out, err
		}
	default:
		out.Kind = "EXECUTION"
	}
	if v.Status == run.StatusSucceeded && commands.Valid {
		out.State = "REQUIRED"
		if out.Kind == "EXECUTION" {
			out.State = "OPTIONAL"
		}
	}
	var raw, receipt, recordedHash string
	var reviewResult sql.NullString
	err = q.QueryRowContext(ctx, `SELECT report_json,receipt_json,review_result_json,source_hash FROM run_analyses WHERE run_id=?`, id).Scan(&raw, &receipt, &reviewResult, &recordedHash)
	if err == nil {
		out.State = "RECORDED"
		out.Report = []byte(raw)
		out.Receipt = &runanalysis.Receipt{}
		if err = json.Unmarshal([]byte(receipt), out.Receipt); err != nil {
			return out, err
		}
		if out.Receipt.RunID != id || out.Receipt.Kind != out.Kind {
			return out, runanalysis.ErrInvalid
		}
		out.RecordedSourceHash = recordedHash
		if out.Kind == "REVIEW" {
			if !reviewResult.Valid {
				return out, runanalysis.ErrInvalid
			}
			parsed, parseErr := review.ParseResult(reviewResult.String)
			if parseErr != nil {
				return out, fmt.Errorf("%w: recorded Review result: %v", runanalysis.ErrInvalid, parseErr)
			}
			out.ReviewResult = &parsed
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	b, _ := json.Marshal([]any{v.ID, v.ProjectID, v.TaskID, v.ReviewID, v.ExecutionProposalID, v.RuntimeEpoch, v.ThreadID, v.TurnID, v.Status, v.Result, v.RuntimeWorkset, out.Commands, out.ObservedChecks, out.VerificationSpec, out.Review, out.Recovery, out.RecoveryAfter})
	out.SourceHash = workflowHash(string(b))
	return out, nil
}
func (s *RunAnalysisStore) Commit(ctx context.Context, in runanalysis.Input, source runanalysis.Source, result runanalysis.Result) (runanalysis.Receipt, error) {
	out := runanalysis.Receipt{}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	current, err := analysisSource(ctx, tx, in.AISessionID, in.RunID)
	if err != nil {
		return out, err
	}
	if current.State == "RECORDED" {
		if current.Receipt != nil && current.Receipt.PayloadSHA256 == runanalysis.Hash(in) {
			return *current.Receipt, nil
		}
		return out, runanalysis.ErrConflict
	}
	if (current.State != "REQUIRED" && current.State != "OPTIONAL") || current.SourceHash != source.SourceHash {
		return out, runanalysis.ErrStale
	}
	v := current.Run
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t WHERE t.id=? AND t.project_id=? AND t.status='ACTIVE'
 AND ((?<>'' AND t.current_review_id=? AND t.phase='REVIEWING') OR (?<>'' AND t.current_execution_id=? AND t.phase IN ('IMPLEMENTING','VERIFICATION_READY','VERIFYING','ACCEPTANCE_PENDING')))
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.feature_area_id=t.feature_area_id)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.run_id=? AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED','CANCELED'))
 AND NOT EXISTS(SELECT 1 FROM feature_areas f WHERE f.id=t.feature_area_id AND f.status NOT IN ('ACTIVE','DISCOVERING')))`, v.TaskID, v.ProjectID, v.ReviewID, v.ReviewID, v.ExecutionProposalID, v.ExecutionProposalID, v.ID).Scan(&eligible)
	if err != nil {
		return out, err
	}
	if current.Kind == "EXECUTION" {
		// An implementation summary is an annotation of immutable completed
		// evidence, not authority to advance or recover its verification Run.
		// Keep an exact current Execution/revision/implementation binding, but
		// permit that Execution's own verification claim to remain untouched.
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM tasks t
 JOIN execution_proposals e ON e.id=t.current_execution_id AND e.task_id=t.id AND e.project_id=t.project_id
  AND e.task_definition_revision_id=t.current_definition_revision_id
 JOIN runs r ON r.id=e.run_id AND r.task_id=t.id AND r.project_id=t.project_id
  AND r.feature_area_id=t.feature_area_id AND r.execution_proposal_id=e.id
 WHERE r.id=? AND r.purpose='IMPLEMENTATION' AND r.status='SUCCEEDED'
 AND t.status='ACTIVE' AND t.phase IN ('IMPLEMENTING','VERIFICATION_READY','VERIFYING','ACCEPTANCE_PENDING','RECOVERY_REQUIRED')
 AND EXISTS(SELECT 1 FROM feature_areas f WHERE f.id=t.feature_area_id AND f.project_id=t.project_id AND f.status IN ('ACTIVE','DISCOVERING'))
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.run_id=r.id AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED','CANCELED'))
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.feature_area_id=t.feature_area_id AND NOT (
  c.operation_kind='EXECUTION' AND c.operation_id=e.id AND c.run_id=COALESCE(e.verification_run_id,'')
  AND EXISTS(SELECT 1 FROM runs vr WHERE vr.id=c.run_id AND vr.execution_proposal_id=e.id
   AND vr.task_id=t.id AND vr.project_id=t.project_id AND vr.feature_area_id=t.feature_area_id AND vr.purpose='VERIFICATION')
 )))`, v.ID).Scan(&eligible)
		if err != nil {
			return out, err
		}
	}
	if current.Kind == "INSPECTION" {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM context_probes p JOIN tasks t ON t.id=p.task_id WHERE p.id=? AND t.id=? AND t.task_type='INSPECTION' AND t.status='SUCCEEDED' AND p.status='SUCCEEDED' AND COALESCE(p.recovery_successor_run_id,p.run_id)=? AND NOT EXISTS(SELECT 1 FROM project_inspection_agent_claims c WHERE c.inspection_id=p.id))`, v.OperationID, v.TaskID, v.ID).Scan(&eligible)
		if err != nil {
			return out, err
		}
	}
	if current.Kind == "RECOVERY" {
		r := current.Recovery
		if r == nil || r.State != "RUNNING" || len(r.Report) > 0 || r.RuntimeEpoch != v.RuntimeEpoch || r.RecoveryTurnID != v.TurnID || !json.Valid(current.RecoveryAfter) {
			return out, runanalysis.ErrStale
		}
		if _, _, err = NewRecoveryStatusStore(s.db).validateSourceTx(ctx, tx, r.Spec.Source); err != nil {
			return out, err
		}
		if _, _, err = readRecoveryDiagnosticEvidenceTx(ctx, tx, *r, true); err != nil {
			return out, err
		}
		if _, err = recoveryRequestDispositionTx(ctx, tx, v.ID, false); err != nil {
			return out, err
		}
		eligible = true
	}
	if !eligible {
		return out, runanalysis.ErrStale
	}
	now := time.Now().UTC()
	out = runanalysis.Receipt{RunID: in.RunID, RequestID: in.RequestID, Kind: current.Kind, PayloadSHA256: runanalysis.Hash(in), SubmittedAt: now}
	receipt, _ := json.Marshal(out)
	var reviewJSON any
	if len(result.Review) > 0 {
		reviewJSON = string(result.Review)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_analyses(run_id,ai_session_id,request_id,payload_hash,source_hash,kind,report_json,review_result_json,receipt_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, in.RunID, in.AISessionID, in.RequestID, out.PayloadSHA256, source.SourceHash, current.Kind, string(in.Report), reviewJSON, string(receipt), formatTime(now))
	if err != nil {
		return out, err
	}
	if result.Inspection != nil {
		hash := workflowHash(string(result.Inspection))
		if _, err = tx.ExecContext(ctx, `UPDATE context_probes SET result=?,report_hash=?,result_hash=?,updated_at=? WHERE id=? AND COALESCE(recovery_successor_run_id,run_id)=? AND status='SUCCEEDED'`, string(result.Inspection), hash, hash, formatTime(now), v.OperationID, v.ID); err != nil {
			return out, err
		}
	}
	if result.Recovery != nil {
		r := *current.Recovery
		r.Report = append([]byte(nil), result.Recovery...)
		r.ReportHash = workflowHash(string(r.Report))
		r.After = append([]byte(nil), current.RecoveryAfter...)
		r.AnalysisRequired = false
		r.State = "REPORT_READY"
		r.UpdatedAt = now
		if err = saveRecoveryTx(ctx, tx, r); err != nil {
			return out, err
		}
	}
	if result.Verification != nil {
		encoded, e := encodeVerificationReportJSON(*result.Verification)
		if e != nil {
			return out, e
		}
		res, e := tx.ExecContext(ctx, `UPDATE runs SET verification_result_json=? WHERE id=? AND status='SUCCEEDED' AND verification_result_json IS NULL`, encoded, v.ID)
		if e != nil {
			return out, e
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return out, runanalysis.ErrConflict
		}
		v.VerificationReport = result.Verification
		v.UpdatedAt = now
		if e = completeTaskRunPhaseTx(ctx, tx, v); e != nil {
			return out, e
		}
	}
	payload, _ := event.Payload(map[string]any{"kind": current.Kind, "requestId": in.RequestID, "sourceHash": source.SourceHash})
	if err = appendEvent(ctx, tx, event.Event{Type: event.RunAnalysisRecorded, ProjectID: v.ProjectID, TaskID: v.TaskID, RunID: v.ID, EntityType: "run_analysis", EntityID: v.ID, Payload: payload, CreatedAt: now}); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	if len(result.Review) > 0 {
		_ = materializeReviewResults(ctx, s.db, now)
	}
	return out, nil
}

// RecordedForOperator reads the exact persisted report after the local Project
// boundary. It never borrows the analysis author's AI Session or recomputes it.
func (s *RunAnalysisStore) RecordedForOperator(ctx context.Context, project, id string) (runanalysis.RecordedReport, error) {
	out := runanalysis.RecordedReport{State: "NOT_REQUIRED"}
	if err := localoperator.RequireProject(ctx, project); err != nil {
		return out, err
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	value, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.project_id=?`, id, project))
	if err != nil {
		return out, err
	}
	out.State = value.AnalysisState
	var report, receipt string
	err = tx.QueryRowContext(ctx, `SELECT report_json,receipt_json FROM run_analyses WHERE run_id=?`, id).Scan(&report, &receipt)
	if errors.Is(err, sql.ErrNoRows) {
		if out.State == "RECORDED" {
			return out, runanalysis.ErrInvalid
		}
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if out.State != "RECORDED" || !json.Valid([]byte(report)) {
		return out, runanalysis.ErrInvalid
	}
	out.Receipt = &runanalysis.Receipt{}
	if json.Unmarshal([]byte(receipt), out.Receipt) != nil || out.Receipt.RunID != id {
		return out, runanalysis.ErrInvalid
	}
	out.Report = json.RawMessage(report)
	return out, nil
}
