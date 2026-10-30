package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
)

// AdmitRecoveryChild freezes read-only intended scope from the exact source
// Workset without carrying over any source worktree, root or snapshot fact.
// It creates no Run or runtime dispatch authority.
func (s *RecoveryStatusStore) AdmitRecoveryChild(ctx context.Context, recoveryID, requestID, payloadSHA256 string, admission rs.Admission, now time.Time) (string, error) {
	if recoveryID == "" || requestID == "" || len(payloadSHA256) != 64 {
		return "", rs.ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	v, err := recoveryReadTx(ctx, tx, recoveryID)
	if err != nil {
		return "", err
	}
	var savedPlan, savedRequest, savedPayload string
	err = tx.QueryRowContext(ctx, `SELECT child_plan_id,admission_request_id,admission_payload_sha256 FROM recovery_child_admissions WHERE recovery_id=?`, recoveryID).Scan(&savedPlan, &savedRequest, &savedPayload)
	if err == nil {
		if savedRequest != requestID || savedPayload != payloadSHA256 || !reflect.DeepEqual(v.Admission, admission) {
			return "", rs.ErrConflict
		}
		return savedPlan, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if !validRecoveryAdmission(admission) {
		return "", rs.ErrIneligible
	}
	if v.State != "PREPARED" || v.RecoveryRunID != "" || v.RuntimeEpoch != s.db.epoch || v.Spec.PayloadHash != v.Spec.ComputePayloadHash() {
		return "", rs.ErrIneligible
	}
	source, _, err := s.validateSourceTx(ctx, tx, v.Spec.Source)
	if err != nil {
		return "", err
	}
	if source.RuntimeWorkset == nil || source.RuntimeWorkset.ContentHash != v.Spec.RuntimeWorksetHash || source.RuntimeWorkset.WorkspaceID == "" || source.WorkspaceID != source.RuntimeWorkset.WorkspaceID || source.ThreadID == "" || source.TurnID == "" {
		return "", rs.ErrConflict
	}
	w := source.RuntimeWorkset
	targets := make([]workset.Target, 0, len(w.Members))
	heads := map[string]string{}
	refs := map[string]string{}
	scopes := map[string]workset.WriteScope{}
	primaryMemberID := ""
	for _, member := range w.Members {
		// Member identity belongs to a workspace. A child plan must allocate its
		// own deterministic identity even before its managed worktree exists.
		childMemberID := "member_recovery_" + workflowHash(recoveryID + "\x00" + member.RepositoryID)[:32]
		if childMemberID == member.MemberID {
			return "", rs.ErrConflict
		}
		target := workset.Target{RepositoryID: member.RepositoryID, MemberID: childMemberID, MemberVersion: 1, OriginCommit: member.OriginCommit, Read: true, Write: workset.WriteScope{Kind: "NONE"}}
		targets = append(targets, target)
		heads[childMemberID] = member.OriginCommit
		scopes[childMemberID] = target.Write
		if member.MemberID == w.PrimaryMemberID {
			primaryMemberID = childMemberID
		}
	}
	if primaryMemberID == "" {
		return "", rs.ErrConflict
	}
	topology, development, err := purposeProjectContextTx(ctx, tx, v.Spec.ProjectID)
	if err != nil {
		return "", err
	}
	plan := runtimepreparation.Plan{ID: "runtime_scope_recovery_child_" + workflowHash(recoveryID + "\x00" + requestID)[:32], Generation: v.Spec.Generation, OwnerKind: runtimepreparation.OwnerRecoveryStatus, OwnerID: recoveryID, ProjectID: v.Spec.ProjectID, FeatureAreaID: v.Spec.FeatureAreaID, TaskID: v.Spec.TaskID, SourceCommand: "RECOVERY_STATUS_CHILD", SourceReceiptID: requestID, ProposalHash: v.Spec.PayloadHash, TaskDefinitionRevisionID: v.Spec.TaskDefinitionRevisionID, RepositoryTargets: targets, PrimaryMemberID: primaryMemberID, BackendIdentity: w.BackendConfigID, Model: w.Model, ReasoningEffort: w.ReasoningEffort, RuntimeAdapterIdentity: w.RuntimeAdapterIdentity, TopologyRevision: topology, DevelopmentContextHash: development, DevelopmentRefs: refs, ExpectedSourceHEADs: heads, WriteScopes: scopes, PreparationRequestID: requestID, PreparationPayloadHash: payloadSHA256, CreatedAt: now.UTC()}
	plan.ContentHash, err = plan.ComputeContentHash()
	if err != nil {
		return "", err
	}
	if err = freezeRuntimeScopePlanTx(ctx, tx, plan); err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_child_admissions(recovery_id,recovery_generation,source_run_id,source_turn_id,source_thread_id,source_runtime_workset_hash,source_workspace_id,source_app_server_instance_id,child_plan_id,runtime_epoch,admission_request_id,admission_payload_sha256,admitted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.Spec.Generation, source.ID, source.TurnID, source.ThreadID, w.ContentHash, w.WorkspaceID, v.Spec.AppServerInstanceID, plan.ID, v.RuntimeEpoch, requestID, payloadSHA256, formatTime(now))
	if err != nil {
		return "", rs.ErrConflict
	}
	v.Admission = admission
	v.UpdatedAt = now.UTC()
	if err = saveRecoveryTx(ctx, tx, v); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return plan.ID, nil
}

func (s *RecoveryStatusStore) ReadRecoveryChildPreparation(ctx context.Context, recoveryID string) (rs.ChildPreparation, error) {
	var value rs.ChildPreparation
	var targetsJSON, headsJSON, epoch string
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return value, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT p.plan_id,a.admission_request_id,a.admission_payload_sha256,p.project_id,COALESCE(p.feature_area_id,''),p.primary_member_id,p.repository_targets_json,p.expected_source_heads_json,p.backend_identity,p.model,p.reasoning_effort,p.runtime_adapter_identity,p.generation,a.source_run_id,a.source_thread_id,a.source_turn_id,a.source_runtime_workset_hash,a.source_workspace_id,a.runtime_epoch
 FROM recovery_child_admissions a JOIN runtime_scope_plans p ON p.plan_id=a.child_plan_id AND p.owner_kind='RECOVERY_STATUS' AND p.owner_id=a.recovery_id WHERE a.recovery_id=?`, recoveryID).Scan(&value.PlanID, &value.RequestID, &value.PayloadSHA256, &value.ProjectID, &value.FeatureAreaID, &value.PrimaryMemberID, &targetsJSON, &headsJSON, &value.BackendConfigID, &value.Model, &value.ReasoningEffort, &value.AdapterIdentity, &value.Generation, &value.SourceRunID, &value.SourceThreadID, &value.SourceTurnID, &value.SourceWorksetHash, &value.SourceWorkspaceID, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return value, rs.ErrNotFound
	}
	if err != nil {
		return value, err
	}
	v, err := recoveryReadTx(ctx, tx, recoveryID)
	if err != nil {
		return value, err
	}
	exactEpoch, err := recoveryDiagnosticEpochChainTx(ctx, tx, recoveryID, v.RecoveryRunID, epoch, v.RuntimeEpoch)
	if err != nil {
		return value, err
	}
	if !exactEpoch || v.RuntimeEpoch != s.db.epoch || json.Unmarshal([]byte(targetsJSON), &value.Targets) != nil || json.Unmarshal([]byte(headsJSON), &value.ExpectedHEADs) != nil || workset.ValidateTargets(value.Targets) != nil {
		return rs.ChildPreparation{}, rs.ErrConflict
	}
	return value, tx.Commit()
}
