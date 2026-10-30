package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
)

// BindPreparedRuntimeWorkset is the sole intended v90 binding transaction.
// It writes the complete Workset, exact binding, start authority, and receipt
// together. A failed statement or injected test fault rolls the entire set
// back, leaving the durable Run and observations readable for repair.
func (s *RunStore) BindPreparedRuntimeWorkset(ctx context.Context, planID, requestID, payloadHash, authorityID string, value workset.RuntimeWorkset, now time.Time) (run.Run, error) {
	if planID == "" || requestID == "" || len(payloadHash) != 64 || authorityID == "" || value.Validate() != nil {
		return run.Run{}, ErrConflict
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return run.Run{}, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	if err = validateTaskPermissionAuthorityTx(ctx, tx, value); err != nil {
		return run.Run{}, err
	}
	var replayPayload, replayPlan, replayRun, replayWorksetHash, replayAuthority string
	err = tx.QueryRowContext(ctx, `SELECT payload_hash,plan_id,COALESCE(run_id,''),workset_content_hash,start_authority_id FROM runtime_preparation_receipts WHERE request_id=?`, requestID).Scan(&replayPayload, &replayPlan, &replayRun, &replayWorksetHash, &replayAuthority)
	if err == nil {
		if replayPayload != payloadHash || replayPlan != planID || replayRun != value.RunID || replayWorksetHash != value.ContentHash || replayAuthority != authorityID {
			return run.Run{}, ErrConflict
		}
		var bindingJSON, bindingHash, startHash string
		if err = tx.QueryRowContext(ctx, `SELECT b.workset_json,b.workset_content_hash,a.workset_content_hash FROM runtime_workset_bindings b JOIN runtime_start_authorities a ON a.authority_id=b.start_authority_id AND a.plan_id=b.plan_id AND a.run_id=b.run_id WHERE b.plan_id=? AND b.run_id=? AND b.start_authority_id=?`, planID, value.RunID, authorityID).Scan(&bindingJSON, &bindingHash, &startHash); err != nil {
			return run.Run{}, err
		}
		if bindingJSON != string(raw) || bindingHash != value.ContentHash || startHash != value.ContentHash {
			return run.Run{}, ErrConflict
		}
		stored, getErr := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, value.RunID))
		if getErr != nil {
			return run.Run{}, getErr
		}
		if err = tx.Commit(); err != nil {
			return run.Run{}, err
		}
		return stored, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, err
	}
	var ownerKind, ownerID, projectID, featureAreaID, taskID, headsJSON, targetsJSON, scopesJSON, primaryMemberID, backendIdentity, model, reasoningEffort, adapterIdentity, authorityDisposition, epoch string
	var agentID, executionPolicy, agentConfigFingerprint string
	var topology int64
	var development string
	err = tx.QueryRowContext(ctx, `SELECT p.owner_kind,p.owner_id,p.project_id,COALESCE(p.feature_area_id,''),COALESCE(p.task_id,''),p.expected_source_heads_json,p.repository_targets_json,p.write_scopes_json,p.primary_member_id,p.backend_identity,p.model,p.reasoning_effort,p.runtime_adapter_identity,p.topology_revision,p.development_context_hash,a.disposition,r.runtime_epoch,p.agent_id,p.execution_policy,p.agent_config_fingerprint FROM runtime_scope_plans p JOIN runtime_preparation_authorities a ON a.plan_id=p.plan_id JOIN runtime_preparation_attempts t ON t.plan_id=p.plan_id JOIN runs r ON r.id=t.run_id WHERE p.plan_id=? AND t.run_id=?`, planID, value.RunID).Scan(&ownerKind, &ownerID, &projectID, &featureAreaID, &taskID, &headsJSON, &targetsJSON, &scopesJSON, &primaryMemberID, &backendIdentity, &model, &reasoningEffort, &adapterIdentity, &topology, &development, &authorityDisposition, &epoch, &agentID, &executionPolicy, &agentConfigFingerprint)
	if err != nil {
		return run.Run{}, err
	}
	if agentConfigFingerprint != value.AgentConfigFingerprint || agentID != value.AgentID || executionPolicy != value.ExecutionPolicy || authorityDisposition != "CURRENT" || epoch != s.db.epoch || projectID != value.ProjectID || (ownerKind == "IMPLEMENTATION" || ownerKind == "EXECUTION" || ownerKind == "VERIFICATION") && taskID != value.TaskID || featureAreaID != value.FeatureAreaID && ownerKind != "INSPECTION" || primaryMemberID != value.PrimaryMemberID || backendIdentity != value.BackendConfigID || model != value.Model || reasoningEffort != value.ReasoningEffort || adapterIdentity != value.RuntimeAdapterIdentity {
		return run.Run{}, ErrConflict
	}
	if policy := value.TaskPermissions; policy != nil {
		var purpose string
		if err = tx.QueryRowContext(ctx, `SELECT purpose FROM runs WHERE id=?`, value.RunID).Scan(&purpose); err != nil || purpose != policy.Purpose {
			return run.Run{}, ErrConflict
		}
	}
	switch runtimepreparation.OwnerKind(ownerKind) {
	case runtimepreparation.OwnerExecution, runtimepreparation.OwnerImplementation, runtimepreparation.OwnerVerification:
		if value.ExecutionID != ownerID || value.ReviewID != "" {
			return run.Run{}, ErrConflict
		}
	case runtimepreparation.OwnerReview:
		if value.ReviewID != ownerID || value.ExecutionID != "" {
			return run.Run{}, ErrConflict
		}
	case runtimepreparation.OwnerInspection:
		if value.InspectionOperationID != ownerID || value.ProtocolVersion != workset.RuntimeProtocolVersionProjectInspection {
			return run.Run{}, ErrConflict
		}
	case runtimepreparation.OwnerRecoveryStatus:
		if value.RecoveryStatusID != ownerID || value.ProtocolVersion != workset.RuntimeProtocolVersionRecoveryStatus || taskID != value.TaskID {
			return run.Run{}, ErrConflict
		}
		var sourceRunID string
		if err = tx.QueryRowContext(ctx, `SELECT source_run_id FROM recovery_status_records WHERE id=?`, ownerID).Scan(&sourceRunID); err != nil {
			return run.Run{}, err
		}
		if value.RecoverySourceRunID != sourceRunID {
			return run.Run{}, ErrConflict
		}
	default:
		return run.Run{}, ErrConflict
	}
	currentTopology, currentDevelopment, err := purposeProjectContextTx(ctx, tx, projectID)
	if err != nil {
		return run.Run{}, err
	}
	if topology != currentTopology || development != currentDevelopment {
		return run.Run{}, ErrConflict
	}
	var heads map[string]string
	if err = json.Unmarshal([]byte(headsJSON), &heads); err != nil {
		return run.Run{}, err
	}
	if len(heads) != len(value.Members) {
		return run.Run{}, ErrConflict
	}
	var targets []workset.Target
	var scopes map[string]workset.WriteScope
	if err = json.Unmarshal([]byte(targetsJSON), &targets); err != nil {
		return run.Run{}, err
	}
	if err = json.Unmarshal([]byte(scopesJSON), &scopes); err != nil {
		return run.Run{}, err
	}
	if len(targets) != len(value.Members) {
		return run.Run{}, ErrConflict
	}
	targetByMember := make(map[string]workset.Target, len(targets))
	for _, target := range targets {
		targetByMember[target.MemberID] = target
	}
	var observationHash string
	err = tx.QueryRowContext(ctx, `SELECT observation_set_hash FROM runtime_preparation_receipts WHERE plan_id=? AND run_id=? AND outcome='OBSERVATION_RECORDED'`, planID, value.RunID).Scan(&observationHash)
	if err != nil {
		return run.Run{}, err
	}
	for _, member := range value.Members {
		target, found := targetByMember[member.MemberID]
		if !found || target.RepositoryID != member.RepositoryID || target.MemberVersion != member.MemberVersion || target.OriginCommit != member.OriginCommit || target.Read != member.Read || target.Write.Kind != member.Write.Kind || !slices.Equal(target.Write.Paths, member.Write.Paths) || scopes[member.MemberID].Kind != member.Write.Kind || !slices.Equal(scopes[member.MemberID].Paths, member.Write.Paths) || target.ReadScope.Kind != member.IntendedReadScope.Kind || !slices.Equal(target.ReadScope.Paths, member.IntendedReadScope.Paths) {
			return run.Run{}, ErrConflict
		}
		if heads[member.MemberID] != member.StartHEAD {
			return run.Run{}, ErrConflict
		}
		var repoID, workspaceID, worktreeID, root, rootIdentity, head, snapshot string
		err = tx.QueryRowContext(ctx, `SELECT repository_id,workspace_id,worktree_id,canonical_root,root_identity,start_head,start_snapshot FROM runtime_workspace_observations WHERE plan_id=? AND run_id=? AND member_id=?`, planID, value.RunID, member.MemberID).Scan(&repoID, &workspaceID, &worktreeID, &root, &rootIdentity, &head, &snapshot)
		if err != nil {
			return run.Run{}, err
		}
		if repoID != member.RepositoryID || workspaceID != value.WorkspaceID || worktreeID != member.WorktreeID || root != member.CanonicalRoot || rootIdentity != member.RootFileIdentity || head != member.StartHEAD || snapshot != member.StartSnapshot {
			return run.Run{}, ErrConflict
		}
	}
	var existing int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_workset_bindings WHERE plan_id=? OR run_id=?`, planID, value.RunID).Scan(&existing)
	if err != nil {
		return run.Run{}, err
	}
	if existing != 0 {
		return run.Run{}, ErrConflict
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runs SET runtime_workset_json=?,runtime_workset_hash=?,workspace_id=?,updated_at=? WHERE id=? AND runtime_workset_json IS NULL AND runtime_workset_hash IS NULL AND status IN ('CREATED','PREPARING')`, string(raw), value.ContentHash, value.WorkspaceID, formatTime(now), value.RunID)
	if err != nil {
		return run.Run{}, conflict(err)
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return run.Run{}, ErrConflict
	}
	if err = s.runtimeBindingFault("workset"); err != nil {
		return run.Run{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_workset_bindings(plan_id,run_id,owner_kind,owner_id,workset_id,workset_json,workset_content_hash,observation_set_hash,start_authority_id,runtime_epoch,bound_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, planID, value.RunID, ownerKind, ownerID, "workset_"+value.RunID, string(raw), value.ContentHash, observationHash, authorityID, epoch, formatTime(now))
	if err != nil {
		return run.Run{}, conflict(err)
	}
	if err = s.runtimeBindingFault("binding"); err != nil {
		return run.Run{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_start_authorities(authority_id,plan_id,run_id,owner_kind,owner_id,workset_content_hash,observation_set_hash,runtime_epoch,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, authorityID, planID, value.RunID, ownerKind, ownerID, value.ContentHash, observationHash, epoch, formatTime(now))
	if err != nil {
		return run.Run{}, conflict(err)
	}
	if err = s.runtimeBindingFault("start-authority"); err != nil {
		return run.Run{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_preparation_receipts(request_id,payload_hash,plan_id,run_id,outcome,observation_set_hash,workset_content_hash,start_authority_id,recorded_at) VALUES(?,?,?,?,'BINDING_COMMITTED',?,?,?,?)`, requestID, payloadHash, planID, value.RunID, observationHash, value.ContentHash, authorityID, formatTime(now))
	if err != nil {
		return run.Run{}, conflict(err)
	}
	if err = s.runtimeBindingFault("receipt"); err != nil {
		return run.Run{}, err
	}
	stored, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, value.RunID))
	if err != nil {
		return run.Run{}, err
	}
	if err = tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return stored, nil
}

func (s *RunStore) runtimeBindingFault(stage string) error {
	if s.db.runtimeBindingFaultHook == nil {
		return nil
	}
	return s.db.runtimeBindingFaultHook(stage)
}

// ReadPreparationReceipt is readback only. An unknown response must use this
// exact request identity before any retry of external preparation or binding.
func (s *RunStore) ReadPreparationReceipt(ctx context.Context, requestID, payloadHash string) (string, string, string, error) {
	if requestID == "" || len(payloadHash) != 64 {
		return "", "", "", ErrConflict
	}
	var storedPayload, planID, runID, outcome string
	err := s.db.sql.QueryRowContext(ctx, `SELECT payload_hash,plan_id,COALESCE(run_id,''),outcome FROM runtime_preparation_receipts WHERE request_id=?`, requestID).Scan(&storedPayload, &planID, &runID, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	if err != nil {
		return "", "", "", err
	}
	if storedPayload != payloadHash {
		return "", "", "", ErrConflict
	}
	return planID, runID, outcome, nil
}

// ValidatePreparedStart is the read-only worker gate. A durable Run alone is
// never permission to create a Thread or Turn.
func (s *RunStore) ValidatePreparedStart(ctx context.Context, runID string) error {
	return s.validatePreparedRuntime(ctx, runID, false)
}

// ValidatePreparedRejoin preserves the original immutable preparation authority.
// Only a durable chain of exact same-Turn reattachments may bridge writer epochs;
// it cannot authorize a new Thread or Turn with an old start authority.
func (s *RunStore) ValidatePreparedRejoin(ctx context.Context, runID string) error {
	return s.validatePreparedRuntime(ctx, runID, true)
}

func (s *RunStore) validatePreparedRuntime(ctx context.Context, runID string, rejoin bool) error {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var runHash, bindingHash, authorityHash, bindingEpoch, authorityEpoch, planOwnerKind, planOwnerID, proposalStatus, currentExecution, currentRevision, plannedRevision, projectID, runWorkspace, observationHash, authorityObservation string
	var plannedTopology int64
	var plannedContext string
	err = tx.QueryRowContext(ctx, `SELECT r.runtime_workset_hash,b.workset_content_hash,a.workset_content_hash,b.runtime_epoch,a.runtime_epoch,p.owner_kind,p.owner_id,COALESCE(e.status,''),COALESCE(t.current_execution_id,''),COALESCE(t.current_definition_revision_id,''),p.task_definition_revision_id,r.project_id,COALESCE(r.workspace_id,''),b.observation_set_hash,a.observation_set_hash,p.topology_revision,p.development_context_hash
 FROM runs r JOIN runtime_preparation_attempts attempt ON attempt.run_id=r.id JOIN runtime_scope_plans p ON p.plan_id=attempt.plan_id JOIN runtime_workset_bindings b ON b.plan_id=p.plan_id AND b.run_id=r.id JOIN runtime_start_authorities a ON a.plan_id=p.plan_id AND a.run_id=r.id AND a.authority_id=b.start_authority_id LEFT JOIN execution_proposals e ON e.id=r.execution_proposal_id LEFT JOIN tasks t ON t.id=r.task_id WHERE r.id=?`, runID).Scan(&runHash, &bindingHash, &authorityHash, &bindingEpoch, &authorityEpoch, &planOwnerKind, &planOwnerID, &proposalStatus, &currentExecution, &currentRevision, &plannedRevision, &projectID, &runWorkspace, &observationHash, &authorityObservation, &plannedTopology, &plannedContext)
	if err != nil {
		return ErrConflict
	}
	if runHash == "" || runHash != bindingHash || runHash != authorityHash || observationHash == "" || observationHash != authorityObservation || runWorkspace == "" || bindingEpoch == "" || bindingEpoch != authorityEpoch {
		return ErrConflict
	}
	if !rejoin {
		if bindingEpoch != s.db.epoch {
			// Recovery may adopt its exact undispatched child. This exception
			// keeps the original binding and cannot reopen a Turn attempt.
			if planOwnerKind != string(runtimepreparation.OwnerRecoveryStatus) {
				return ErrConflict
			}
			if err = s.db.checkRunTx(ctx, tx, runID); err != nil {
				return err
			}
			var undispatched bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_status_records rec JOIN runs r ON r.id=rec.recovery_run_id WHERE rec.id=? AND r.id=? AND rec.runtime_epoch=? AND r.runtime_epoch=rec.runtime_epoch AND rec.state='RUN_CREATED' AND r.status IN ('CREATED','PREPARING') AND r.turn_id IS NULL AND NOT EXISTS(SELECT 1 FROM recovery_child_turn_attempts attempt WHERE attempt.recovery_id=rec.id))`, planOwnerID, runID, s.db.epoch).Scan(&undispatched)
			if err != nil || !undispatched {
				return ErrConflict
			}
			exactEpoch, epochErr := recoveryDiagnosticEpochChainTx(ctx, tx, planOwnerID, runID, bindingEpoch, s.db.epoch)
			if epochErr != nil || !exactEpoch {
				return ErrConflict
			}
		}
	} else {
		if err = s.db.checkRunTx(ctx, tx, runID); err != nil {
			return err
		}
		var originalAuthority bool
		// Start with the receipt for the current runtime connection, then follow
		// only receipts for this exact Run, Turn and immutable server binding.
		// UNION also makes a corrupt cyclic history terminate rather than loop.
		err = tx.QueryRowContext(ctx, `WITH RECURSIVE original(epoch,connection_id) AS (
 SELECT c.previous_epoch,c.previous_connection_id
 FROM app_server_reattachments c
 JOIN runs r ON r.id=c.run_id
 JOIN agent_runtime_states s ON s.run_id=r.id
 JOIN app_server_run_bindings b ON b.run_id=r.id
 WHERE r.id=? AND r.status='RUNNING' AND r.runtime_epoch=?
 AND s.runtime_epoch=r.runtime_epoch AND s.thread_id=r.thread_id AND s.turn_id=r.turn_id
 AND c.current_epoch=s.runtime_epoch AND c.current_connection_id=s.connection_id
 AND c.thread_id=r.thread_id AND c.turn_id=r.turn_id AND c.binding_json=b.binding_json
 UNION
 SELECT c.previous_epoch,c.previous_connection_id
 FROM app_server_reattachments c JOIN original o ON c.current_epoch=o.epoch AND c.current_connection_id=o.connection_id
 JOIN runs r ON r.id=c.run_id JOIN app_server_run_bindings b ON b.run_id=r.id
 WHERE r.id=? AND c.thread_id=r.thread_id AND c.turn_id=r.turn_id AND c.binding_json=b.binding_json
 ) SELECT EXISTS(SELECT 1 FROM original WHERE epoch=?)`, runID, s.db.epoch, runID, bindingEpoch).Scan(&originalAuthority)
		if err != nil || !originalAuthority {
			return ErrConflict
		}
	}
	if planOwnerKind == "IMPLEMENTATION" && (proposalStatus != "CONSUMED" || currentExecution != planOwnerID || currentRevision != plannedRevision) {
		return ErrConflict
	}
	topology, contextHash, err := purposeProjectContextTx(ctx, tx, projectID)
	if err != nil || topology != plannedTopology || contextHash != plannedContext {
		return ErrConflict
	}
	value, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, runID))
	if err != nil || value.RuntimeWorkset == nil || value.RuntimeWorkset.ContentHash != runHash || value.RuntimeWorkset.WorkspaceID != runWorkspace || value.RuntimeWorkset.Validate() != nil {
		return ErrConflict
	}
	return tx.Commit()
}
