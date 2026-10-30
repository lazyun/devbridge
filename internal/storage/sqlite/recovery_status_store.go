package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

type RecoveryStatusStore struct{ db *DB }

func NewRecoveryStatusStore(db *DB) *RecoveryStatusStore { return &RecoveryStatusStore{db} }

var _ rs.Store = (*RecoveryStatusStore)(nil)

type persistedRecovery struct {
	rs.Record
	ExactReport     []byte `json:"_exactReport,omitempty"`
	ExactAssessment []byte `json:"_exactAssessment,omitempty"`
	ExactFacts      []byte `json:"_exactFacts,omitempty"`
	ExactAfter      []byte `json:"_exactAfter,omitempty"`
	ExactSchema     []byte `json:"_exactSchema,omitempty"`
}

func encodeRecovery(v rs.Record) ([]byte, error) {
	return json.Marshal(persistedRecovery{Record: v, ExactReport: v.Report, ExactAssessment: v.Assessment, ExactFacts: v.Facts, ExactSchema: v.Schema, ExactAfter: v.After})
}
func scanRecovery(row interface{ Scan(...any) error }) (rs.Record, error) {
	var b []byte
	var saved persistedRecovery
	err := row.Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return saved.Record, rs.ErrNotFound
	}
	if err != nil {
		return saved.Record, err
	}
	if err = json.Unmarshal(b, &saved); err != nil {
		return saved.Record, err
	}
	if saved.ExactReport != nil {
		saved.Report = saved.ExactReport
	}
	if saved.ExactAssessment != nil {
		saved.Assessment = saved.ExactAssessment
	}
	if saved.ExactFacts != nil {
		saved.Facts = saved.ExactFacts
	}
	if saved.ExactAfter != nil {
		saved.After = saved.ExactAfter
	}
	if saved.ExactSchema != nil {
		saved.Schema = saved.ExactSchema
	}
	return saved.Record, nil
}

func (s *RecoveryStatusStore) Read(ctx context.Context, id string) (rs.Record, error) {
	return scanRecovery(s.db.sql.QueryRowContext(ctx, `SELECT record_json FROM recovery_status_records WHERE id=?`, id))
}
func (s *RecoveryStatusStore) LatestForSource(ctx context.Context, id string) (rs.Record, error) {
	return scanRecovery(s.db.sql.QueryRowContext(ctx, `SELECT record_json FROM recovery_status_records WHERE source_run_id=? ORDER BY generation DESC LIMIT 1`, id))
}
func recoveryReadTx(ctx context.Context, tx *sql.Tx, id string) (rs.Record, error) {
	return scanRecovery(tx.QueryRowContext(ctx, `SELECT record_json FROM recovery_status_records WHERE id=?`, id))
}
func saveRecoveryTx(ctx context.Context, tx *sql.Tx, v rs.Record) error {
	b, e := encodeRecovery(v)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE recovery_status_records SET recovery_run_id=?,runtime_epoch=?,state=?,record_json=? WHERE id=?`, nullString(v.RecoveryRunID), v.RuntimeEpoch, v.State, string(b), v.ID)
	return e
}
func (s *RecoveryStatusStore) validateSourceTx(ctx context.Context, tx *sql.Tx, src rs.Source) (run.Run, string, error) {
	v, e := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, src.RunID))
	if e != nil {
		return v, "", e
	}
	inspection := src.OwnerKind == "PROJECT_INSPECTION"
	if v.ProjectID != src.ProjectID || v.FeatureAreaID != src.FeatureAreaID || v.TaskID != src.TaskID || string(v.OperationKind) != src.OperationKind || v.OperationID != src.OperationID || v.TurnID != src.TurnID || v.ThreadID != src.ThreadID || v.RuntimeEpoch != src.RuntimeEpoch || v.Purpose == run.PurposeRecoveryStatus {
		return v, "", rs.ErrConflict
	}
	if v.RuntimeWorkset == nil || v.RuntimeWorkset.ContentHash != src.RuntimeWorksetHash || (!inspection && v.RuntimeWorkset.TaskDefinitionRevisionID != src.TaskDefinitionRevisionID) {
		return v, "", rs.ErrIneligible
	}
	var currentRun bool
	if inspection {
		if src.FeatureAreaID != "" || src.OwnerID == "" || src.OwnerID != src.OperationID || src.OperationKind != "INSPECTION" {
			return v, "", rs.ErrConflict
		}
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM context_probes p JOIN tasks t ON t.id=p.task_id
 WHERE p.id=? AND p.id=? AND p.project_id=? AND p.task_id=?
   AND p.workspace_id=? AND p.workspace_id=? AND p.runtime_workset_hash=?
   AND p.canonical_target_receipt_json IS NOT NULL AND p.runtime_workset_json IS NOT NULL
   AND p.runtime_limitation='RECOVERY_REQUIRED' AND t.task_scope='PROJECT' AND t.feature_area_id IS NULL AND t.work_owner_id=p.id
   AND ((p.run_id=? AND p.runtime_epoch=?) OR EXISTS(
      SELECT 1 FROM inspection_recovery_continuations k
      JOIN recovery_status_records x ON x.id=k.recovery_id
      JOIN project_inspection_agent_claims c ON c.inspection_id=k.inspection_id AND c.run_id=k.successor_run_id
      WHERE k.successor_run_id=? AND k.inspection_id=p.id AND p.recovery_successor_run_id=k.successor_run_id
        AND c.runtime_epoch=? AND x.state='RESOLVED'
        AND json_extract(x.record_json,'$.assessment.consistency')='CONSISTENT'
        AND json_extract(x.record_json,'$.assessment.engineeringDisposition')='RECOVERED_PARTIAL'
   )))`, src.OwnerID, src.OperationID, src.ProjectID, src.TaskID, src.WorkspaceID, v.WorkspaceID, src.RuntimeWorksetHash, src.RunID, src.RuntimeEpoch, src.RunID, src.RuntimeEpoch).Scan(&currentRun)
	} else {
		e = tx.QueryRowContext(ctx, `SELECT CASE WHEN ?='EXECUTION' THEN EXISTS(SELECT 1 FROM execution_proposals WHERE id=? AND task_id=? AND COALESCE(verification_run_id,run_id)=? AND outcome IN ('RUNNING','FAILED')) WHEN ?='REVIEW' THEN EXISTS(SELECT 1 FROM reviews WHERE id=? AND linked_run_id=? AND task_id=?) ELSE 0 END`, src.OperationKind, src.OperationID, src.TaskID, src.RunID, src.OperationKind, src.OperationID, src.RunID, src.TaskID).Scan(&currentRun)
	}
	if e != nil {
		return v, "", e
	}
	if !currentRun {
		return v, "", rs.ErrIneligible
	}
	if v.Status == run.StatusSucceeded && v.Result != nil {
		return v, "", rs.ErrIneligible
	}
	var valid bool
	if inspection {
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_operation_boundaries b JOIN agent_conversations a ON a.id=b.agent_conversation_id JOIN app_server_run_bindings binding ON binding.run_id=b.run_id JOIN tasks t ON t.id=? JOIN context_probes p ON p.id=? WHERE b.run_id=? AND a.id=? AND a.owner_kind='PROJECT_INSPECTION' AND a.owner_id=p.id AND a.feature_area_id IS NULL AND a.backend_thread_id=? AND a.ended_at IS NULL AND json_extract(binding.binding_json,'$.id')=? AND t.status<>'CANCELED' AND t.task_scope='PROJECT' AND t.work_owner_id=p.id AND (p.run_id=b.run_id OR EXISTS(SELECT 1 FROM inspection_recovery_continuations k JOIN project_inspection_agent_claims c ON c.inspection_id=k.inspection_id AND c.run_id=k.successor_run_id JOIN runs successor ON successor.id=k.successor_run_id WHERE k.successor_run_id=b.run_id AND k.inspection_id=p.id AND p.recovery_successor_run_id=b.run_id AND c.runtime_epoch=successor.runtime_epoch)))`, v.TaskID, src.OwnerID, v.ID, src.AgentConversationID, src.ThreadID, src.AppServerInstanceID).Scan(&valid)
	} else {
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_operation_boundaries b JOIN agent_conversations a ON a.id=b.agent_conversation_id JOIN app_server_run_bindings binding ON binding.run_id=b.run_id JOIN tasks t ON t.id=? WHERE b.run_id=? AND a.id=? AND a.backend_thread_id=? AND a.ended_at IS NULL AND json_extract(binding.binding_json,'$.id')=? AND t.status<>'CANCELED' AND ((?='EXECUTION' AND t.current_execution_id=? AND t.current_definition_revision_id=?) OR (?='REVIEW' AND t.current_review_id=?)))`, v.TaskID, v.ID, src.AgentConversationID, src.ThreadID, src.AppServerInstanceID, src.OperationKind, src.OperationID, src.TaskDefinitionRevisionID, src.OperationKind, src.OperationID).Scan(&valid)
	}
	if e != nil {
		return v, "", e
	}
	if !valid {
		return v, "", rs.ErrIneligible
	}
	valid, e = recoveryBlockedByCancellation(ctx, tx, v.TaskID, v.ID)
	if e != nil {
		return v, "", e
	}
	if valid {
		return v, "", rs.ErrIneligible
	}
	if inspection {
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM project_inspection_agent_claims c WHERE c.inspection_id=? AND (c.run_id<>? OR c.task_id<>? OR c.project_id<>? OR c.runtime_epoch<>?))`, src.OwnerID, v.ID, v.TaskID, v.ProjectID, v.RuntimeEpoch).Scan(&valid)
	} else {
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=? AND (run_id<>? OR operation_id<>? OR operation_kind<>? OR runtime_epoch<>?))`, v.FeatureAreaID, v.ID, v.OperationID, v.OperationKind, v.RuntimeEpoch).Scan(&valid)
	}
	if e != nil {
		return v, "", e
	}
	if valid {
		return v, "", rs.ErrIneligible
	}
	disposition, e := recoveryRequestDispositionTx(ctx, tx, v.ID, true)
	if e != nil {
		return v, "", e
	}
	return v, disposition, nil
}
func (s *RecoveryStatusStore) Prepare(ctx context.Context, spec rs.Spec, schema []byte) (rs.Record, error) {
	if e := spec.Validate(); e != nil {
		return rs.Record{}, e
	}
	if !json.Valid(schema) || rs.Hash(schema) != rs.Hash(rs.ReportSchemaForSource(spec.Source)) || spec.PayloadHash != spec.ComputePayloadHash() {
		return rs.Record{}, rs.ErrConflict
	}
	tx, e := s.db.beginWrite(ctx)
	if e != nil {
		return rs.Record{}, e
	}
	defer tx.Rollback()
	requestHash := spec.PayloadHash
	existing, e := scanRecovery(tx.QueryRowContext(ctx, `SELECT record_json FROM recovery_status_records WHERE request_id=?`, spec.RequestID))
	if e == nil {
		var submitted string
		if e = tx.QueryRowContext(ctx, `SELECT submitted_payload_hash FROM recovery_status_records WHERE id=?`, existing.ID).Scan(&submitted); e != nil {
			return existing, e
		}
		if submitted != requestHash || existing.SchemaHash != rs.Hash(schema) {
			return existing, rs.ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(e, rs.ErrNotFound) {
		return existing, e
	}
	source, disp, e := s.validateSourceTx(ctx, tx, spec.Source)
	if e != nil {
		return rs.Record{}, e
	}
	var active bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_status_records WHERE source_run_id=? AND (state NOT IN ('RESOLVED','FAILED') OR (state='FAILED' AND recovery_run_id IS NOT NULL)))`, spec.RunID).Scan(&active); e != nil {
		return rs.Record{}, e
	}
	if active {
		return rs.Record{}, rs.ErrConflict
	}
	ownerKind := recoveryOwnerKind(source)
	var previousID, previousState string
	var previousGeneration, selectionVersion int
	var previousChild sql.NullString
	e = tx.QueryRowContext(ctx, `SELECT g.recovery_id,g.generation,g.selection_version,r.state,r.recovery_run_id FROM recovery_current_generations g JOIN recovery_status_records r ON r.id=g.recovery_id WHERE g.owner_kind=? AND g.owner_id=?`, ownerKind, source.OperationID).Scan(&previousID, &previousGeneration, &selectionVersion, &previousState, &previousChild)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return rs.Record{}, e
	}
	if e == nil && (previousState != "RESOLVED" && previousState != "FAILED" || previousState == "FAILED" && previousChild.Valid) {
		return rs.Record{}, rs.ErrConflict
	}
	// The caller's generation is not allocation authority. Preserve its request
	// hash for exact replay while Core allocates the next owner generation.
	spec.Generation = previousGeneration + 1
	spec.PayloadHash = spec.ComputePayloadHash()
	now := time.Now().UTC()
	v := rs.Record{ID: "recovery:" + rs.Hash([]byte(spec.ActorID+"\x00"+spec.RequestID)), Spec: spec, RuntimeEpoch: s.db.epoch, State: "PREPARED", InstructionHash: rs.Hash([]byte(spec.Instruction)), SchemaHash: rs.Hash(schema), Schema: append([]byte(nil), schema...), SourceResponseDisposition: disp, CreatedAt: now, UpdatedAt: now}
	encoded, _ := encodeRecovery(v)
	sp, _ := json.Marshal(spec)
	_, e = tx.ExecContext(ctx, `INSERT INTO recovery_status_records(id,source_run_id,source_turn_id,generation,request_id,runtime_epoch,state,spec_json,record_json,submitted_payload_hash) VALUES(?,?,?,?,?,?,?,?,?,?)`, v.ID, spec.RunID, spec.TurnID, spec.Generation, spec.RequestID, v.RuntimeEpoch, v.State, string(sp), string(encoded), requestHash)
	if e != nil {
		return v, e
	}
	if previousID == "" {
		_, e = tx.ExecContext(ctx, `INSERT INTO recovery_current_generations(owner_kind,owner_id,recovery_id,generation,selection_version) VALUES(?,?,?,?,1)`, ownerKind, source.OperationID, v.ID, spec.Generation)
	} else {
		var changed sql.Result
		changed, e = tx.ExecContext(ctx, `UPDATE recovery_current_generations SET recovery_id=?,generation=?,selection_version=selection_version+1 WHERE owner_kind=? AND owner_id=? AND recovery_id=? AND generation=? AND selection_version=?`, v.ID, spec.Generation, ownerKind, source.OperationID, previousID, previousGeneration, selectionVersion)
		if e == nil {
			if n, _ := changed.RowsAffected(); n != 1 {
				e = rs.ErrConflict
			}
		}
	}
	if e != nil {
		return v, e
	}
	return v, tx.Commit()
}
func validRecoveryAdmission(a rs.Admission) bool {
	return a.SourceTurnTerminal && a.ThreadIdle && a.NoPendingRequests && a.Quiesced && json.Valid(a.Before) && !a.ObservedAt.IsZero() && time.Since(a.ObservedAt) < time.Minute && time.Until(a.ObservedAt) < time.Second
}
func (s *RecoveryStatusStore) mutate(ctx context.Context, id, epoch string, fn func(*sql.Tx, *rs.Record) error) (rs.Record, error) {
	tx, e := s.db.beginWrite(ctx)
	if e != nil {
		return rs.Record{}, e
	}
	defer tx.Rollback()
	v, e := recoveryReadTx(ctx, tx, id)
	if e != nil {
		return v, e
	}
	if epoch != s.db.epoch || v.RuntimeEpoch != epoch {
		return v, rs.ErrStale
	}
	if e = fn(tx, &v); e != nil {
		return v, e
	}
	v.UpdatedAt = time.Now().UTC()
	if e = saveRecoveryTx(ctx, tx, v); e != nil {
		return v, e
	}
	return v, tx.Commit()
}
func (s *RecoveryStatusStore) BeforeStart(ctx context.Context, id, epoch string) (rs.Record, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, v *rs.Record) error {
		if v.State != "RUN_CREATED" {
			return rs.ErrConflict
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO run_execution_records(run_id,protocol,created_at) VALUES(?,'NATURAL_V1',?) ON CONFLICT(run_id) DO NOTHING`, v.RecoveryRunID, formatTime(time.Now().UTC())); e != nil {
			return e
		}
		if _, _, e := s.validateSourceTx(ctx, tx, v.Spec.Source); e != nil {
			return e
		}
		var modern bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_child_admissions WHERE recovery_id=?)`, id).Scan(&modern); e != nil {
			return e
		}
		if modern {
			var authorized bool
			e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_child_turn_attempts attempt JOIN recovery_child_thread_receipts thread ON thread.request_id=attempt.thread_request_id JOIN runs child ON child.id=? WHERE attempt.recovery_id=? AND thread.outcome='CREATED' AND thread.child_thread_id=attempt.child_thread_id AND child.thread_id=attempt.child_thread_id)`, v.RecoveryRunID, id).Scan(&authorized)
			if e != nil || !authorized {
				return rs.ErrConflict
			}
		}
		v.State = "UNKNOWN"
		now := time.Now().UTC()
		v.StartedAt = &now
		_, e := tx.ExecContext(ctx, `UPDATE runs SET final_codex_prompt=?,prompt_sha256=?,prompt_bytes=?,prompt_sent_at=?,started_at=?,updated_at=? WHERE id=? AND final_codex_prompt IS NULL`, v.Spec.Instruction, v.InstructionHash, len(v.Spec.Instruction), formatTime(now), formatTime(now), formatTime(now), v.RecoveryRunID)
		return e
	})
}
func (s *RecoveryStatusStore) BindTurn(ctx context.Context, id, epoch, turn string) (rs.Record, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, v *rs.Record) error {
		if turn == "" || turn == v.Spec.TurnID {
			return rs.ErrConflict
		}
		if v.RecoveryTurnID != "" {
			if turn == v.RecoveryTurnID {
				return nil
			}
			return rs.ErrConflict
		}
		if v.State != "UNKNOWN" {
			return rs.ErrConflict
		}
		var modern bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_child_turn_attempts WHERE recovery_id=?)`, id).Scan(&modern); e != nil {
			return e
		}
		if modern {
			var receiptThread, receiptTurn, runThread, outcome string
			e := tx.QueryRowContext(ctx, `SELECT receipt.child_thread_id,COALESCE(receipt.child_turn_id,''),COALESCE(r.thread_id,''),receipt.outcome FROM recovery_child_turn_attempts attempt JOIN recovery_child_turn_receipts receipt ON receipt.request_id=attempt.request_id JOIN runs r ON r.id=? WHERE attempt.recovery_id=?`, v.RecoveryRunID, id).Scan(&receiptThread, &receiptTurn, &runThread, &outcome)
			if e != nil || receiptThread == "" || receiptThread == v.Spec.ThreadID || receiptThread != runThread || receiptTurn != turn || outcome != "DISPATCHED" {
				return rs.ErrConflict
			}
		}
		v.RecoveryTurnID = turn
		v.State = "RUNNING"
		_, e := tx.ExecContext(ctx, `UPDATE runs SET turn_id=?,status='RUNNING',updated_at=? WHERE id=? AND turn_id IS NULL`, turn, formatTime(time.Now().UTC()), v.RecoveryRunID)
		return e
	})
}
func (s *RecoveryStatusStore) SaveReport(ctx context.Context, id, epoch string, report, after []byte) (rs.Record, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, v *rs.Record) error {
		if len(v.Report) > 0 {
			if bytes.Equal(v.Report, report) && bytes.Equal(v.After, after) {
				return nil
			}
			return rs.ErrConflict
		}
		if (v.State != "RUNNING" && !(v.State == "UNKNOWN" && v.RecoveryTurnID != "")) || !json.Valid(report) || !json.Valid(after) {
			return rs.ErrConflict
		}
		if _, e := rs.ParseReport(report, v.Spec.Source); e != nil {
			return e
		}
		v.Report = append([]byte(nil), report...)
		v.After = append([]byte(nil), after...)
		v.ReportHash = rs.Hash(report)
		v.State = "REPORT_READY"
		now := time.Now().UTC()
		v.CompletedAt = &now
		return nil
	})
}
func (s *RecoveryStatusStore) SaveAssessment(ctx context.Context, id, epoch string, b, facts []byte) (rs.Record, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, v *rs.Record) error {
		if len(v.Assessment) > 0 {
			if bytes.Equal(v.Assessment, b) && bytes.Equal(v.Facts, facts) {
				return nil
			}
			return rs.ErrConflict
		}
		if v.State != "REPORT_READY" || !json.Valid(b) {
			return rs.ErrConflict
		}
		var a rs.Assessment
		if e := json.Unmarshal(b, &a); e != nil {
			return e
		}
		if a.RecoveryRunID != v.RecoveryRunID || a.ReportHash != v.ReportHash || a.SourceResponseDisposition != v.SourceResponseDisposition {
			return rs.ErrConflict
		}
		var f rs.FactualObservation
		if e := json.Unmarshal(facts, &f); e != nil {
			return e
		}
		if f.Source != v.Spec.Source || f.RecoveryRunID != v.RecoveryRunID || f.ReportHash != v.ReportHash || f.Workset.ContentHash != v.Spec.RuntimeWorksetHash || a.Source != v.Spec.Source {
			return rs.ErrConflict
		}
		before, _ := json.Marshal(f.Before)
		after, _ := json.Marshal(f.After)
		if !bytes.Equal(before, v.Admission.Before) || !bytes.Equal(after, v.After) {
			return rs.ErrConflict
		}
		report, e := rs.ParseReport(v.Report, v.Spec.Source)
		if e != nil {
			return e
		}
		derived, e := rs.Assess(report, f)
		if e != nil {
			return e
		}
		db, _ := json.Marshal(derived)
		if !bytes.Equal(db, b) || derived.FactualObservationHash != rs.Hash(facts) {
			return rs.ErrConflict
		}
		v.Facts = append([]byte(nil), facts...)
		v.Assessment = append([]byte(nil), b...)
		v.AssessmentHash = rs.Hash(b)
		v.State = "ASSESSED"
		// Assessment records diagnostic facts only. Existing ownership and the
		// Recovery projection block progression without changing Task.phase;
		// resolution owns any engineering transition (INV-RECOVERY-005).
		return nil
	})
}
func (s *RecoveryStatusStore) Fail(ctx context.Context, id, epoch, reason string) (rs.Record, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, v *rs.Record) error {
		if v.State == "RESOLVED" {
			return rs.ErrConflict
		}
		v.FailureReason = reason
		if v.RecoveryTurnID != "" {
			v.State = "FAILED"
		}
		return nil
	})
}
func (s *RecoveryStatusStore) ListRecoverable(ctx context.Context) ([]rs.Record, error) {
	rows, e := s.db.sql.QueryContext(ctx, `SELECT record_json FROM recovery_status_records WHERE state NOT IN ('RESOLVED','FAILED') ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []rs.Record{}
	for rows.Next() {
		v, e := scanRecovery(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *RecoveryStatusStore) Adopt(ctx context.Context, id string) (rs.Record, error) {
	tx, e := s.db.beginWrite(ctx)
	if e != nil {
		return rs.Record{}, e
	}
	defer tx.Rollback()
	v, e := recoveryReadTx(ctx, tx, id)
	if e != nil {
		return v, e
	}
	if v.RuntimeEpoch == s.db.epoch {
		return v, nil
	}
	if v.State == "RESOLVED" || v.State == "FAILED" {
		return v, rs.ErrConflict
	}
	// The exact persisted recovery Turn may be observed/rejoined. An UNKNOWN
	// start without its Turn ID remains UNKNOWN and must never be redispatched.
	previous := v.RuntimeEpoch
	v.RuntimeEpoch = s.db.epoch
	v.UpdatedAt = time.Now().UTC()
	if e = saveRecoveryTx(ctx, tx, v); e != nil {
		return v, e
	}
	if v.RecoveryRunID != "" {
		_, e = tx.ExecContext(ctx, `INSERT INTO recovery_status_adoptions(recovery_id,run_id,previous_epoch,current_epoch,created_at) VALUES(?,?,?,?,?)`, v.ID, v.RecoveryRunID, previous, v.RuntimeEpoch, formatTime(v.UpdatedAt))
		if e != nil {
			return v, e
		}
		_, e = tx.ExecContext(ctx, `UPDATE runs SET runtime_epoch=? WHERE id=? AND runtime_epoch=?`, v.RuntimeEpoch, v.RecoveryRunID, previous)
		if e != nil {
			return v, e
		}
	} else {
		// An admitted plan may survive a crash before canonical Run creation.
		// Its admission remains frozen at the original epoch.
		_, e = tx.ExecContext(ctx, `INSERT INTO recovery_child_preparation_adoptions(recovery_id,child_plan_id,previous_epoch,current_epoch,created_at) SELECT recovery_id,child_plan_id,?,?,? FROM recovery_child_admissions WHERE recovery_id=?`, previous, v.RuntimeEpoch, formatTime(v.UpdatedAt), v.ID)
		if e != nil {
			return v, e
		}
	}
	return v, tx.Commit()
}
func (s *RecoveryStatusStore) Resolve(ctx context.Context, in rs.ResolveInput) (rs.Record, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return rs.Record{}, err
	}
	defer tx.Rollback()
	value, err := recoveryReadTx(ctx, tx, in.ID)
	if err != nil {
		return value, err
	}
	if in.RequestID == "" || in.ActorID == "" || in.PayloadHash != in.ComputePayloadHash() {
		return value, rs.ErrConflict
	}
	receipt, _ := json.Marshal(struct{ RequestID, ActorID, PayloadHash, AssessmentHash, ReportHash string }{in.RequestID, in.ActorID, in.PayloadHash, in.AssessmentHash, in.ReportHash})
	// A committed receipt is historical authority. Match it before consulting
	// the live epoch or source owner, and return without writes or materialization.
	if len(value.Resolution) > 0 {
		if value.State == "RESOLVED" && bytes.Equal(receipt, value.Resolution) {
			return value, nil
		}
		return value, rs.ErrConflict
	}
	if err = taskActionAdmissionTx(ctx, tx, value.Spec.Source.TaskID); err != nil {
		return value, err
	}
	if in.RuntimeEpoch != s.db.epoch || value.RuntimeEpoch != in.RuntimeEpoch {
		return value, rs.ErrStale
	}
	if err = s.applyRecoveryResolutionTx(ctx, tx, &value, in, receipt); err != nil {
		return value, err
	}
	value.UpdatedAt = time.Now().UTC()
	if err = saveRecoveryTx(ctx, tx, value); err != nil {
		return value, err
	}
	if err = tx.Commit(); err != nil {
		return value, err
	}
	// Only a newly committed Review-source resolution triggers materialization.
	_ = materializeReviewResults(ctx, s.db, time.Now().UTC())
	return value, nil
}

func (s *RecoveryStatusStore) applyRecoveryResolutionTx(ctx context.Context, tx *sql.Tx, v *rs.Record, in rs.ResolveInput, receipt []byte) error {
	if v.State != "ASSESSED" || in.AssessmentHash != v.AssessmentHash || in.ReportHash != v.ReportHash || !validRecoveryAdmission(in.Admission) {
		return rs.ErrConflict
	}
	var a rs.Assessment
	if err := json.Unmarshal(v.Assessment, &a); err != nil {
		return err
	}
	// Purpose belongs only to the Task owner's revision-required bridge.
	// Ignoring it on other paths would commit a payload hash that their
	// downstream resolution evidence cannot reconstruct or authorize.
	if in.Purpose != nil && (a.EngineeringDisposition != "REVISION_REQUIRED" || v.Spec.OwnerKind == "PROJECT_INSPECTION") {
		return rs.ErrConflict
	}
	source, _, e := s.validateSourceTx(ctx, tx, v.Spec.Source)
	if e != nil {
		return e
	}
	var before, after any
	if json.Unmarshal(in.Admission.Before, &before) != nil || json.Unmarshal(v.After, &after) != nil {
		return rs.ErrConflict
	}
	b1, _ := json.Marshal(before)
	b2, _ := json.Marshal(after)
	if !bytes.Equal(b1, b2) {
		return rs.ErrIneligible
	}
	if _, e = recoveryRequestDispositionTx(ctx, tx, v.RecoveryRunID, false); e != nil {
		return e
	}
	if v.Spec.OwnerKind == "PROJECT_INSPECTION" {
		var facts rs.FactualObservation
		if json.Unmarshal(v.Facts, &facts) != nil {
			return rs.ErrConflict
		}
		if a.EngineeringDisposition == "RECOVERED_COMPLETE" {
			if a.Consistency != "CONSISTENT" {
				return rs.ErrIneligible
			}
			e = s.resolveInspectionRecoveryTx(ctx, tx, v, source, facts, true)
		} else if a.EngineeringDisposition == "RECOVERED_PARTIAL" || a.EngineeringDisposition == "REVISION_REQUIRED" {
			e = s.resolveInspectionRecoveryTx(ctx, tx, v, source, facts, false)
		} else {
			return rs.ErrIneligible
		}
		if e != nil {
			return e
		}
	} else if a.EngineeringDisposition == "RECOVERED_COMPLETE" {
		if a.Consistency != "CONSISTENT" {
			return rs.ErrIneligible
		}
		if e = s.resolveCompleteTx(ctx, tx, v, source); e != nil {
			return e
		}

	} else if a.EngineeringDisposition == "RECOVERED_PARTIAL" {
		if e = s.resolvePartialTx(ctx, tx, v, source); e != nil {
			return e
		}
	} else if a.EngineeringDisposition == "REVISION_REQUIRED" {
		if e = s.resolveRevisionRequiredTx(ctx, tx, v, source, in); e != nil {
			return e
		}
	} else {
		return rs.ErrIneligible
	}
	v.Resolution = receipt
	v.State = "RESOLVED"
	return nil
}

func (s *RecoveryStatusStore) MarkUnknown(ctx context.Context, id, epoch, reason string) (rs.Record, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, v *rs.Record) error {
		if v.State == "RESOLVED" || v.State == "ASSESSED" || v.State == "REPORT_READY" {
			return rs.ErrConflict
		}
		v.State = "UNKNOWN"
		v.FailureReason = reason
		return nil
	})
}

// SaveFailedObservation preserves actual terminal probe observations even when
// its response cannot be parsed. It never supplies engineering advancement.
func (s *RecoveryStatusStore) SaveFailedObservation(ctx context.Context, id, epoch, reason string, after []byte) (rs.Record, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, v *rs.Record) error {
		if v.State == "FAILED" && v.FailureReason == reason && bytes.Equal(v.After, after) {
			return nil
		}
		if v.RecoveryTurnID == "" || (v.State != "RUNNING" && v.State != "UNKNOWN") || len(v.Report) > 0 || len(v.After) > 0 || reason == "" {
			return rs.ErrConflict
		}
		var observed, before []rs.WorktreeObservation
		if json.Unmarshal(after, &observed) != nil || json.Unmarshal(v.Admission.Before, &before) != nil || len(observed) == 0 || len(observed) != len(before) {
			return rs.ErrConflict
		}
		for i, o := range observed {
			b := before[i]
			if o.RepositoryID != b.RepositoryID || o.MemberID != b.MemberID || o.WorktreeID != b.WorktreeID || o.RuntimeAlias != b.RuntimeAlias {
				return rs.ErrConflict
			}
		}
		v.After = append([]byte(nil), after...)
		v.FailureReason = reason
		v.State = "FAILED"
		now := time.Now().UTC()
		v.CompletedAt = &now
		return nil
	})
}

func recoveryBlockedByCancellation(ctx context.Context, q definitionQuerier, taskID, runID string) (bool, error) {
	var blocked bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE (task_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')) OR (run_id=? AND state='CANCELED'))`, taskID, runID).Scan(&blocked)
	return blocked, err
}
