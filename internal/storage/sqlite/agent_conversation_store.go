package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/run"
)

type AgentConversationStore struct{ db *DB }

func NewAgentConversationStore(db *DB) *AgentConversationStore {
	return &AgentConversationStore{db: db}
}

var _ agentconversation.Runtime = (*AgentConversationStore)(nil)

const selectAgentConversation = `SELECT id,project_id,feature_area_id,COALESCE(owner_kind,'FEATURE_AREA'),COALESCE(owner_id,feature_area_id,''),backend,COALESCE(backend_thread_id,''),COALESCE(initial_model,''),COALESCE(initial_model_source_run_id,''),COALESCE(initial_model_source_thread_id,''),COALESCE(initial_model_evidence,''),backend_selection_json,COALESCE(backend_selection_source_run_id,''),COALESCE(backend_selection_source_thread_id,''),COALESCE(backend_selection_evidence,''),host_tool_contract_version,COALESCE(current_boundary_turn_id,''),continuation_safe,COALESCE(predecessor_id,''),COALESCE(rotation_reason,''),created_at,ended_at FROM agent_conversations`

func scanAgentConversation(row interface{ Scan(...any) error }) (agentconversation.Conversation, error) {
	var v agentconversation.Conversation
	var created string
	var ended sql.NullString
	var backendSelection sql.NullString
	err := row.Scan(&v.ID, &v.ProjectID, &v.FeatureAreaID, &v.OwnerKind, &v.OwnerID, &v.Backend, &v.BackendThreadID, &v.InitialModel, &v.InitialModelSourceRunID, &v.InitialModelSourceThreadID, &v.InitialModelEvidence, &backendSelection, &v.BackendSelectionSourceRunID, &v.BackendSelectionSourceThreadID, &v.BackendSelectionEvidence, &v.HostToolContractVersion, &v.CurrentBoundaryTurnID, &v.ContinuationSafe, &v.PredecessorID, &v.RotationReason, &created, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return v, agentconversation.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if backendSelection.Valid {
		var selected codexconfig.BackendSelection
		if err := json.Unmarshal([]byte(backendSelection.String), &selected); err != nil {
			return v, err
		}
		v.BackendSelection = &selected
	}
	v.CreatedAt, err = parseTime(created)
	if err != nil {
		return v, err
	}
	if ended.Valid {
		value, e := parseTime(ended.String)
		if e != nil {
			return v, e
		}
		v.EndedAt = &value
	}
	return v, nil
}
func (s *AgentConversationStore) Get(ctx context.Context, id string) (agentconversation.Conversation, error) {
	return scanAgentConversation(s.db.sql.QueryRowContext(ctx, selectAgentConversation+` WHERE id=?`, id))
}
func (s *AgentConversationStore) Current(ctx context.Context, featureAreaID string) (agentconversation.Conversation, error) {
	return scanAgentConversation(s.db.sql.QueryRowContext(ctx, selectAgentConversation+` WHERE feature_area_id=? AND ended_at IS NULL`, featureAreaID))
}
func (s *AgentConversationStore) ListByFeatureArea(ctx context.Context, featureAreaID string) ([]agentconversation.Conversation, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectAgentConversation+` WHERE feature_area_id=? ORDER BY created_at,id`, featureAreaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []agentconversation.Conversation{}
	for rows.Next() {
		v, e := scanAgentConversation(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *AgentConversationStore) ListRecoveryReceipts(ctx context.Context, featureAreaID string) ([]agentconversation.RecoveryReceipt, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT request_id,feature_area_id,run_id,operation_kind,operation_id,COALESCE(old_conversation_id,''),COALESCE(new_conversation_id,''),authorized_epoch,created_at,result_json,COALESCE(operator_principal_id,'') FROM agent_operation_recoveries WHERE feature_area_id=? ORDER BY created_at,request_id`, featureAreaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []agentconversation.RecoveryReceipt{}
	for rows.Next() {
		var v agentconversation.RecoveryReceipt
		var created string
		var encoded sql.NullString
		if err := rows.Scan(&v.RequestID, &v.FeatureAreaID, &v.RunID, &v.OperationKind, &v.OperationID, &v.OldConversationID, &v.NewConversationID, &v.AuthorizedEpoch, &created, &encoded, &v.OperatorPrincipalID); err != nil {
			return nil, err
		}
		if encoded.Valid {
			var result agentconversation.RecoveryResult
			if err := json.Unmarshal([]byte(encoded.String), &result); err != nil {
				return nil, err
			}
			if result.RequestID != v.RequestID || result.FeatureAreaID != v.FeatureAreaID || result.RunID != v.RunID || result.OperationKind != v.OperationKind || result.OperationID != v.OperationID {
				return nil, agentconversation.ErrConflict
			}
			v.ExpectedUpperVersion, v.AuthorizedUpperVersion, v.GrantState = result.ExpectedUpperVersion, result.AuthorizedUpperVersion, result.GrantState
		}
		var parseErr error
		v.CreatedAt, parseErr = parseTime(created)
		if parseErr != nil {
			return nil, parseErr
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *AgentConversationStore) GetClaim(ctx context.Context, featureAreaID string) (agentconversation.Claim, error) {
	return scanAgentClaim(s.db.sql.QueryRowContext(ctx, `SELECT feature_area_id,operation_kind,operation_id,run_id,created_at,COALESCE(runtime_epoch,'') FROM feature_area_agent_claims WHERE feature_area_id=?`, featureAreaID))
}
func scanAgentClaim(row interface{ Scan(...any) error }) (agentconversation.Claim, error) {
	var v agentconversation.Claim
	var created string
	err := row.Scan(&v.FeatureAreaID, &v.Kind, &v.OperationID, &v.RunID, &created, &v.RuntimeEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return v, agentconversation.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.CreatedAt, err = parseTime(created)
	return v, err
}
func (s *AgentConversationStore) Boundary(ctx context.Context, runID string) (agentconversation.Boundary, error) {
	var v agentconversation.Boundary
	err := s.db.sql.QueryRowContext(ctx, `SELECT run_id,agent_conversation_id,COALESCE(entry_boundary_turn_id,''),COALESCE(exit_boundary_turn_id,''),advance_state,quiesced,thread_start_pending FROM agent_operation_boundaries WHERE run_id=?`, runID).Scan(&v.RunID, &v.AgentConversationID, &v.EntryBoundaryTurnID, &v.ExitBoundaryTurnID, &v.Advance, &v.Quiesced, &v.ThreadStartPending)
	if errors.Is(err, sql.ErrNoRows) {
		err = agentconversation.ErrNotFound
	}
	return v, err
}

// acquireAgentRunTx is the single cross-adapter reservation seam. The caller
// already holds SQLite's write lock from attempt allocation. A conflict rolls
// back attempt, Run, claim, and any authorization consumption together.
func acquireAgentRunTx(ctx context.Context, tx *sql.Tx, v run.Run, now time.Time) error {
	fa := v.FeatureAreaID
	kind, owner := agentconversation.OperationKind(v.OperationKind), v.OperationID
	if fa == "" || owner == "" || (kind != agentconversation.Review && kind != agentconversation.Execution) {
		return agentconversation.ErrUnavailable
	}
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM feature_areas WHERE id=? AND legacy_read_only=0`, fa).Scan(&status); err != nil {
		return err
	}
	if status == "RETIRING" || status == "DECOMPOSED" || status == "RETIRED" || (kind != agentconversation.Review && status != "ACTIVE") {
		return agentconversation.ErrUnavailable
	}
	if err := validateAgentRunBackendContinuityTx(ctx, tx, v); err != nil {
		return err
	}
	existing, err := scanAgentClaim(tx.QueryRowContext(ctx, `SELECT feature_area_id,operation_kind,operation_id,run_id,created_at,COALESCE(runtime_epoch,'') FROM feature_area_agent_claims WHERE feature_area_id=?`, fa))
	if err == nil {
		if existing.Kind != kind || existing.OperationID != owner || existing.RuntimeEpoch != v.RuntimeEpoch {
			return agentconversation.ErrBusy
		}
		if existing.RunID == v.ID {
			return nil
		}
		// One Execution can include serial implementation and verification Runs.
		var safe bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN agent_operation_boundaries b ON b.run_id=r.id WHERE r.id=? AND r.status='SUCCEEDED' AND b.quiesced=1 AND b.advance_state<>'UNKNOWN')`, existing.RunID).Scan(&safe); err != nil {
			return err
		}
		if !safe {
			return agentconversation.ErrBusy
		}
		_, err = tx.ExecContext(ctx, `UPDATE feature_area_agent_claims SET run_id=? WHERE feature_area_id=? AND operation_kind=? AND operation_id=? AND run_id=?`, v.ID, fa, kind, owner, existing.RunID)
		return err
	}
	if !errors.Is(err, agentconversation.ErrNotFound) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO feature_area_agent_claims(feature_area_id,operation_kind,operation_id,run_id,created_at,runtime_epoch) VALUES(?,?,?,?,?,?)`, fa, kind, owner, v.ID, formatTime(now), v.RuntimeEpoch)
	return err
}

func lockAgentFeatureArea(ctx context.Context, tx *sql.Tx, fa string) (string, error) {
	// Write first, before reads, to avoid deferred-transaction SQLITE_BUSY races.
	var project, status string
	err := tx.QueryRowContext(ctx, `UPDATE feature_areas SET updated_at=updated_at WHERE id=? AND legacy_read_only=0 RETURNING project_id,status`, fa).Scan(&project, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", agentconversation.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if status == "RETIRING" || status == "DECOMPOSED" || status == "RETIRED" {
		return "", agentconversation.ErrUnavailable
	}
	return project, nil
}
func newConversationTx(ctx context.Context, tx *sql.Tx, project, fa, predecessor string, now time.Time) (agentconversation.Conversation, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return agentconversation.Conversation{}, err
	}
	v := agentconversation.Conversation{ID: "conversation_" + hex.EncodeToString(raw[:]), ProjectID: project, FeatureAreaID: fa, OwnerKind: agentconversation.OwnerFeatureArea, OwnerID: fa, Backend: "CODEX", HostToolContractVersion: agentconversation.HostToolContractVersion, ContinuationSafe: true, PredecessorID: predecessor, CreatedAt: now}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_conversations(id,project_id,feature_area_id,owner_kind,owner_id,backend,host_tool_contract_version,predecessor_id,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, v.ID, project, fa, v.OwnerKind, v.OwnerID, v.Backend, v.HostToolContractVersion, nullString(predecessor), formatTime(now))
	return v, err
}

// newInspectionConversationTx creates the separate project Inspection owner
// stream.  It intentionally has no FeatureArea and cannot be selected through
// Current/ListByFeatureArea.
func newInspectionConversationTx(ctx context.Context, tx *sql.Tx, project, inspectionID, predecessor string, now time.Time) (agentconversation.Conversation, error) {
	if strings.TrimSpace(project) == "" || strings.TrimSpace(inspectionID) == "" {
		return agentconversation.Conversation{}, agentconversation.ErrInvalidArgument
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return agentconversation.Conversation{}, err
	}
	v := agentconversation.Conversation{ID: "conversation_" + hex.EncodeToString(raw[:]), ProjectID: project, OwnerKind: agentconversation.OwnerProjectInspection, OwnerID: inspectionID, Backend: "CODEX", HostToolContractVersion: agentconversation.HostToolContractVersion, ContinuationSafe: true, PredecessorID: predecessor, CreatedAt: now}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_conversations(id,project_id,feature_area_id,owner_kind,owner_id,backend,host_tool_contract_version,predecessor_id,created_at) VALUES(?,?,NULL,?,?,?,?,?,?)`, v.ID, project, v.OwnerKind, v.OwnerID, v.Backend, v.HostToolContractVersion, nullString(predecessor), formatTime(now))
	return v, err
}
func currentOrCreateAgentTx(ctx context.Context, tx *sql.Tx, project, fa string, now time.Time) (agentconversation.Conversation, error) {
	v, err := scanAgentConversation(tx.QueryRowContext(ctx, selectAgentConversation+` WHERE feature_area_id=? AND ended_at IS NULL`, fa))
	if errors.Is(err, agentconversation.ErrNotFound) {
		return newConversationTx(ctx, tx, project, fa, "", now)
	}
	return v, err
}
func rotateAgentTx(ctx context.Context, tx *sql.Tx, v agentconversation.Conversation, reason agentconversation.RotationReason, now time.Time) (agentconversation.Conversation, error) {
	switch reason {
	case agentconversation.ExplicitReset, agentconversation.BackendThreadUnavailable, agentconversation.HostToolContractUpgrade, agentconversation.UnsafeContextBoundary:
	default:
		return v, agentconversation.ErrInvalidArgument
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_conversations SET ended_at=?,rotation_reason=? WHERE id=? AND ended_at IS NULL`, formatTime(now), reason, v.ID)
	if err != nil {
		return v, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return v, agentconversation.ErrConflict
	}
	return newConversationTx(ctx, tx, v.ProjectID, v.FeatureAreaID, v.ID, now)
}

// Reset is explicit, FeatureArea-scoped, and idempotent by caller request ID.
// A replay returns its actual successor even if another reset has since run.
func (s *AgentConversationStore) Reset(ctx context.Context, fa, requestID string) (agentconversation.Conversation, error) {
	if strings.TrimSpace(requestID) == "" || len(requestID) > 128 {
		return agentconversation.Conversation{}, agentconversation.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return agentconversation.Conversation{}, err
	}
	defer tx.Rollback()
	project, err := lockAgentFeatureArea(ctx, tx, fa)
	if err != nil {
		return agentconversation.Conversation{}, err
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, fa); err != nil {
		return agentconversation.Conversation{}, err
	}
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT conversation_id FROM agent_context_resets WHERE feature_area_id=? AND request_id=?`, fa, requestID).Scan(&previous)
	if err == nil {
		return scanAgentConversation(tx.QueryRowContext(ctx, selectAgentConversation+` WHERE id=?`, previous))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return agentconversation.Conversation{}, err
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=?)`, fa).Scan(&busy); err != nil {
		return agentconversation.Conversation{}, err
	}
	if busy {
		return agentconversation.Conversation{}, agentconversation.ErrBusy
	}
	now := time.Now().UTC()
	v, err := currentOrCreateAgentTx(ctx, tx, project, fa, now)
	if err != nil {
		return v, err
	}
	v, err = rotateAgentTx(ctx, tx, v, agentconversation.ExplicitReset, now)
	if err != nil {
		return v, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_context_resets(feature_area_id,request_id,conversation_id) VALUES(?,?,?)`, fa, requestID, v.ID); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func (s *AgentConversationStore) BeginRun(ctx context.Context, runID string) (agentconversation.Conversation, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return agentconversation.Conversation{}, err
	}
	defer tx.Rollback()
	if err := s.db.checkRunTx(ctx, tx, runID); err != nil {
		return agentconversation.Conversation{}, err
	}
	// The claim itself is the lock/authorization truth, not a Task or workspace.
	var fa string
	err = tx.QueryRowContext(ctx, `UPDATE feature_area_agent_claims SET run_id=run_id WHERE run_id=? RETURNING feature_area_id`, runID).Scan(&fa)
	if errors.Is(err, sql.ErrNoRows) {
		return agentconversation.Conversation{}, agentconversation.ErrConflict
	}
	if err != nil {
		return agentconversation.Conversation{}, err
	}
	project, err := lockAgentFeatureArea(ctx, tx, fa)
	if err != nil {
		return agentconversation.Conversation{}, err
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, fa); err != nil {
		return agentconversation.Conversation{}, err
	}
	var eligible bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE id=? AND status='PREPARING')`, runID).Scan(&eligible); err != nil {
		return agentconversation.Conversation{}, err
	}
	if !eligible {
		return agentconversation.Conversation{}, agentconversation.ErrConflict
	}
	var entered bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_operation_boundaries WHERE run_id=?)`, runID).Scan(&entered); err != nil {
		return agentconversation.Conversation{}, err
	}
	if entered {
		return agentconversation.Conversation{}, agentconversation.ErrConflict
	}
	now := time.Now().UTC()
	v, err := currentOrCreateAgentTx(ctx, tx, project, fa, now)
	if err != nil {
		return v, err
	}
	if v.HostToolContractVersion != agentconversation.HostToolContractVersion {
		v, err = rotateAgentTx(ctx, tx, v, agentconversation.HostToolContractUpgrade, now)
	} else if !v.ContinuationSafe {
		v, err = rotateAgentTx(ctx, tx, v, agentconversation.UnsafeContextBoundary, now)
	}
	if err != nil {
		return v, err
	}
	runValue, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, runID))
	if err != nil {
		return v, err
	}
	if err = validateAgentRunBackendContinuityTx(ctx, tx, runValue); err != nil {
		return v, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_operation_boundaries(run_id,agent_conversation_id,entry_boundary_turn_id,advance_state) VALUES(?,?,?,'NO_ADVANCE')`, runID, v.ID, nullString(v.CurrentBoundaryTurnID))
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func (s *AgentConversationStore) ownedAgentBoundaryTx(ctx context.Context, tx *sql.Tx, runID string) (string, error) {
	if err := s.db.checkRunTx(ctx, tx, runID); err != nil {
		return "", err
	}
	var id string
	err := tx.QueryRowContext(ctx, `UPDATE feature_area_agent_claims SET run_id=run_id WHERE run_id=? RETURNING feature_area_id`, runID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", agentconversation.ErrConflict
	}
	if err != nil {
		return "", err
	}
	err = tx.QueryRowContext(ctx, `SELECT b.agent_conversation_id FROM agent_operation_boundaries b JOIN agent_conversations a ON a.id=b.agent_conversation_id WHERE b.run_id=? AND b.quiesced=0 AND a.ended_at IS NULL AND a.feature_area_id=?`, runID, id).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", agentconversation.ErrConflict
	}
	return id, err
}
func (s *AgentConversationStore) BindThread(ctx context.Context, runID, threadID string) error {
	return s.bindThread(ctx, runID, threadID, "", "", nil)
}
func (s *AgentConversationStore) BindThreadWithProvenance(ctx context.Context, runID, threadID, model, evidence string) error {
	return s.bindThread(ctx, runID, threadID, model, evidence, nil)
}
func (s *AgentConversationStore) BindThreadWithRuntimeProvenance(ctx context.Context, runID, threadID, model, evidence string, selection codexconfig.BackendSelection) error {
	if selection.ConfigID == "" || selection.ConfigFingerprint == "" || selection.ConfigName == "" || selection.Source == "" {
		return agentconversation.ErrInvalidArgument
	}
	return s.bindThread(ctx, runID, threadID, model, evidence, &selection)
}
func (s *AgentConversationStore) bindThread(ctx context.Context, runID, threadID, model, evidence string, selection *codexconfig.BackendSelection) error {
	if strings.TrimSpace(threadID) == "" {
		return agentconversation.ErrInvalidArgument
	}
	model = strings.TrimSpace(model)
	evidence = strings.TrimSpace(evidence)
	if model != "" && evidence == "" {
		return agentconversation.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, err := s.ownedAgentBoundaryTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	var encodedSelection any
	if selection != nil {
		encoded, marshalErr := json.Marshal(selection)
		if marshalErr != nil {
			return marshalErr
		}
		encodedSelection = string(encoded)
	}
	var boundThread, initialModel, sourceRun, sourceThread, savedEvidence string
	var savedSelection sql.NullString
	var backendSourceRun, backendSourceThread, backendEvidence string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(backend_thread_id,''),COALESCE(initial_model,''),COALESCE(initial_model_source_run_id,''),COALESCE(initial_model_source_thread_id,''),COALESCE(initial_model_evidence,''),backend_selection_json,COALESCE(backend_selection_source_run_id,''),COALESCE(backend_selection_source_thread_id,''),COALESCE(backend_selection_evidence,'') FROM agent_conversations WHERE id=?`, id).Scan(&boundThread, &initialModel, &sourceRun, &sourceThread, &savedEvidence, &savedSelection, &backendSourceRun, &backendSourceThread, &backendEvidence); err != nil {
		return err
	}
	if boundThread != "" && boundThread != threadID {
		return agentconversation.ErrConflict
	}
	if model != "" && initialModel != "" && (initialModel != model || sourceRun != runID || sourceThread != threadID || savedEvidence != evidence) {
		return agentconversation.ErrConflict
	}
	if selection != nil && savedSelection.Valid && (savedSelection.String != encodedSelection || backendSourceRun != runID || backendSourceThread != threadID || backendEvidence != evidence) {
		return agentconversation.ErrConflict
	}
	// The source Run and the conversation provenance are one local durable fact.
	// Keep the Run-side owner/epoch/status/CAS rules inside this writer instead
	// of relying on a later Runs.AttachThread transaction.
	if _, err = attachRunThreadTx(ctx, s.db, tx, runID, threadID, time.Now().UTC()); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_conversations SET backend_thread_id=?,initial_model=CASE WHEN initial_model IS NULL AND ?<>'' THEN ? ELSE initial_model END,initial_model_source_run_id=CASE WHEN initial_model IS NULL AND ?<>'' THEN ? ELSE initial_model_source_run_id END,initial_model_source_thread_id=CASE WHEN initial_model IS NULL AND ?<>'' THEN ? ELSE initial_model_source_thread_id END,initial_model_evidence=CASE WHEN initial_model IS NULL AND ?<>'' THEN ? ELSE initial_model_evidence END,backend_selection_json=CASE WHEN backend_selection_json IS NULL AND ? IS NOT NULL THEN ? ELSE backend_selection_json END,backend_selection_source_run_id=CASE WHEN backend_selection_json IS NULL AND ? IS NOT NULL THEN ? ELSE backend_selection_source_run_id END,backend_selection_source_thread_id=CASE WHEN backend_selection_json IS NULL AND ? IS NOT NULL THEN ? ELSE backend_selection_source_thread_id END,backend_selection_evidence=CASE WHEN backend_selection_json IS NULL AND ? IS NOT NULL THEN ? ELSE backend_selection_evidence END WHERE id=? AND (backend_thread_id IS NULL OR backend_thread_id=?)`, threadID, model, model, model, runID, model, threadID, model, evidence, encodedSelection, encodedSelection, encodedSelection, runID, encodedSelection, threadID, encodedSelection, evidence, id, threadID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return agentconversation.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET thread_start_pending=0,advance_state='NO_ADVANCE' WHERE run_id=? AND thread_start_pending=1`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_conversations SET continuation_safe=1 WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// attachRunThreadTx mirrors RunService.AttachThread inside an existing writer
// transaction. It deliberately uses the same domain mutation, exact runtime
// fence, immutable owner check, optimistic timestamp CAS, and accounting hooks
// as RunStore.Update, without nesting a second write transaction.
func attachRunThreadTx(ctx context.Context, db *DB, tx *sql.Tx, runID, threadID string, now time.Time) (run.Run, error) {
	if err := db.checkRunTx(ctx, tx, runID); err != nil {
		return run.Run{}, err
	}
	value, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, runID))
	if err != nil {
		return run.Run{}, err
	}
	previousOwner := value
	previousUpdatedAt := value.UpdatedAt
	changed, err := run.BindThread(&value, threadID, now)
	if err != nil {
		return run.Run{}, err
	}
	if !value.SameOwner(previousOwner) {
		return run.Run{}, run.ErrOwnerImmutable
	}
	if !changed {
		return value, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET thread_id=?,updated_at=? WHERE id=? AND status=? AND updated_at=? AND thread_id IS NULL`, value.ThreadID, formatTime(value.UpdatedAt), value.ID, string(previousOwner.Status), formatTime(previousUpdatedAt))
	if err != nil {
		return run.Run{}, conflict(err)
	}
	if n, rowsErr := result.RowsAffected(); rowsErr != nil {
		return run.Run{}, rowsErr
	} else if n != 1 {
		return run.Run{}, ErrConflict
	}
	if err = completeTaskRunPhaseTx(ctx, tx, value); err != nil {
		return run.Run{}, err
	}
	if err = updateRunTokenUsage(ctx, tx, value); err != nil {
		return run.Run{}, err
	}
	if err = finalizeAgentRunTx(ctx, tx, value); err != nil {
		return run.Run{}, err
	}
	return value, nil
}
func (s *AgentConversationStore) MarkTurnPending(ctx context.Context, runID string) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, err := s.ownedAgentBoundaryTx(ctx, tx, runID)
	if err != nil {
		return err
	}
	var bound bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN agent_conversations a ON a.id=? JOIN feature_area_agent_claims c ON c.run_id=r.id WHERE r.id=? AND r.runtime_epoch=? AND c.runtime_epoch=r.runtime_epoch AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1) AND r.status='RUNNING' AND r.thread_id=a.backend_thread_id AND a.backend_thread_id IS NOT NULL)`, id, runID, s.db.epoch).Scan(&bound); err != nil {
		return err
	}
	if !bound {
		return agentconversation.ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET advance_state='UNKNOWN' WHERE run_id=? AND advance_state='NO_ADVANCE'`, runID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return agentconversation.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_conversations SET continuation_safe=0 WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishRun remains a compatibility entry point for bounded finalization replay.
// New coordinator paths record the intent before terminalizing the Run.
func (s *AgentConversationStore) FinishRun(ctx context.Context, runID string, advance agentconversation.Advance, exit string, quiesced bool) error {
	return s.RecordFinalization(ctx, runID, agentconversation.Finalization{Advance: advance, ExitBoundaryTurnID: exit, Quiesced: quiesced})
}

func (s *AgentConversationStore) AuthorizeRun(ctx context.Context, runID string) (agentconversation.Claim, error) {
	v, err := scanAgentClaim(s.db.sql.QueryRowContext(ctx, `SELECT c.feature_area_id,c.operation_kind,c.operation_id,c.run_id,c.created_at,COALESCE(c.runtime_epoch,'') FROM feature_area_agent_claims c JOIN runs r ON r.id=c.run_id JOIN agent_operation_boundaries b ON b.run_id=r.id JOIN agent_conversations a ON a.id=b.agent_conversation_id WHERE r.id=? AND r.runtime_epoch=? AND c.runtime_epoch=r.runtime_epoch AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1) AND r.status='RUNNING' AND EXISTS(SELECT 1 FROM feature_areas f WHERE f.id=r.feature_area_id AND f.status IN ('DISCOVERING','ACTIVE')) AND b.quiesced=0 AND b.advance_state='UNKNOWN' AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id) AND a.ended_at IS NULL AND a.host_tool_contract_version=? AND a.feature_area_id=c.feature_area_id AND r.feature_area_id=c.feature_area_id AND r.thread_id=a.backend_thread_id AND r.turn_id IS NOT NULL AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id`, runID, s.db.epoch, agentconversation.HostToolContractVersion))
	if err != nil {
		return v, fmt.Errorf("authorize active Feature Area Agent operation: %w", err)
	}
	return v, nil
}

// Release rejects a stale/wrong owner. Callers cannot bypass terminal runtime
// facts with a timeout, a local error, or a different Run of the same Execution.
func (s *AgentConversationStore) Release(ctx context.Context, owner agentconversation.Claim) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if owner.RuntimeEpoch != s.db.epoch {
		return s.db.fenced(ctx, owner.RunID)
	}
	if err := s.db.checkRunTx(ctx, tx, owner.RunID); err != nil {
		return err
	}
	var actual string
	err = tx.QueryRowContext(ctx, `UPDATE feature_area_agent_claims SET run_id=run_id WHERE feature_area_id=? AND operation_kind=? AND operation_id=? AND run_id=? RETURNING run_id`, owner.FeatureAreaID, owner.Kind, owner.OperationID, owner.RunID).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		return agentconversation.ErrConflict
	}
	if err != nil {
		return err
	}
	if err = releaseFinalizedAgentOperationTx(ctx, tx, owner.RunID); err != nil {
		return err
	}
	var retained bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=?)`, owner.FeatureAreaID).Scan(&retained); err != nil {
		return err
	}
	if retained {
		return agentconversation.ErrBusy
	}
	return tx.Commit()
}

// RotateTechnical is a compare-and-swap technical replacement, never an
// ambiguous retry fallback. BackendThreadUnavailable is for a backend's
// definitive unavailable-thread result; callers must not infer it from I/O.
func (s *AgentConversationStore) RotateTechnical(ctx context.Context, fa, expectedID string, reason agentconversation.RotationReason) (agentconversation.Conversation, error) {
	if expectedID == "" || reason == agentconversation.ExplicitReset {
		return agentconversation.Conversation{}, agentconversation.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return agentconversation.Conversation{}, err
	}
	defer tx.Rollback()
	_, err = lockAgentFeatureArea(ctx, tx, fa)
	if err != nil {
		return agentconversation.Conversation{}, err
	}
	var busy bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=?)`, fa).Scan(&busy); err != nil {
		return agentconversation.Conversation{}, err
	}
	if busy {
		return agentconversation.Conversation{}, agentconversation.ErrBusy
	}
	v, err := scanAgentConversation(tx.QueryRowContext(ctx, selectAgentConversation+` WHERE feature_area_id=? AND ended_at IS NULL`, fa))
	if err != nil {
		return v, err
	}
	if v.ID != expectedID {
		return v, agentconversation.ErrConflict
	}
	if (reason == agentconversation.UnsafeContextBoundary && v.ContinuationSafe) || (reason == agentconversation.HostToolContractUpgrade && v.HostToolContractVersion == agentconversation.HostToolContractVersion) {
		return v, agentconversation.ErrInvalidArgument
	}
	v, err = rotateAgentTx(ctx, tx, v, reason, time.Now().UTC())
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}
