package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/runtimepreparation"
)

// freezeRuntimeScopePlanTx is called inside the business admission transaction,
// after its durable owner identity is created. It writes no Run, Workset,
// start authority or dispatch fact.
func freezeRuntimeScopePlanTx(ctx context.Context, tx *sql.Tx, plan runtimepreparation.Plan) error {
	if err := freezeRuntimeScopePlanOnlyTx(ctx, tx, plan); err != nil {
		return err
	}
	return writeRuntimePreparationAuthorityTx(ctx, tx, plan)
}

// A formal T2 freezes intended scope; only T3 may issue preparation authority.
func freezeRuntimeScopePlanOnlyTx(ctx context.Context, tx *sql.Tx, plan runtimepreparation.Plan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	var savedID, savedHash, savedOwnerKind, savedOwnerID string
	err := tx.QueryRowContext(ctx, `SELECT plan_id,content_hash,owner_kind,owner_id FROM runtime_scope_plans WHERE preparation_request_id=?`, plan.PreparationRequestID).Scan(&savedID, &savedHash, &savedOwnerKind, &savedOwnerID)
	if err == nil {
		if savedID == plan.ID && savedHash == plan.ContentHash && savedOwnerKind == string(plan.OwnerKind) && savedOwnerID == plan.OwnerID {
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	encode := func(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }
	targets, err := encode(plan.RepositoryTargets)
	if err != nil {
		return err
	}
	refs, err := encode(plan.DevelopmentRefs)
	if err != nil {
		return err
	}
	heads, err := encode(plan.ExpectedSourceHEADs)
	if err != nil {
		return err
	}
	scopes, err := encode(plan.WriteScopes)
	if err != nil {
		return err
	}
	var executionID, reviewID, inspectionID, recoveryID any
	switch plan.OwnerKind {
	case runtimepreparation.OwnerExecution, runtimepreparation.OwnerImplementation, runtimepreparation.OwnerVerification:
		executionID = plan.OwnerID
	case runtimepreparation.OwnerReview:
		reviewID = plan.OwnerID
	case runtimepreparation.OwnerInspection:
		inspectionID = plan.OwnerID
	case runtimepreparation.OwnerRecoveryStatus:
		recoveryID = plan.OwnerID
	default:
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_scope_plans(plan_id,generation,owner_kind,owner_id,owner_execution_id,owner_review_id,owner_inspection_id,owner_recovery_id,project_id,feature_area_id,task_id,source_command,source_receipt_id,selected_variant_id,proposal_hash,task_definition_revision_id,source_execution_id,source_review_id,successor_execution_id,successor_review_id,repository_targets_json,primary_member_id,backend_identity,model,reasoning_effort,runtime_adapter_identity,topology_revision,development_context_hash,development_refs_json,expected_source_heads_json,write_scopes_json,preparation_request_id,preparation_payload_hash,content_hash,created_at,backend_config_fingerprint,backend_source,backend_kind,codex_fast_mode,service_tier,runtime_protocol_identity,authority_snapshot_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		plan.ID, plan.Generation, string(plan.OwnerKind), plan.OwnerID, executionID, reviewID, inspectionID, recoveryID, plan.ProjectID, nullString(plan.FeatureAreaID), nullString(plan.TaskID), plan.SourceCommand, plan.SourceReceiptID, plan.SelectedVariantID, plan.ProposalHash, plan.TaskDefinitionRevisionID, plan.SourceExecutionID, plan.SourceReviewID, plan.SuccessorExecutionID, plan.SuccessorReviewID, targets, plan.PrimaryMemberID, plan.BackendIdentity, plan.Model, plan.ReasoningEffort, plan.RuntimeAdapterIdentity, plan.TopologyRevision, plan.DevelopmentContextHash, refs, heads, scopes, plan.PreparationRequestID, plan.PreparationPayloadHash, plan.ContentHash, formatTime(plan.CreatedAt), plan.BackendConfigFingerprint, plan.BackendSource, plan.BackendKind, boolInt(plan.CodexFastMode), plan.ServiceTier, plan.RuntimeProtocolIdentity, plan.AuthoritySnapshotHash)
	if err != nil {
		return conflict(err)
	}
	return nil
}

func writeRuntimePreparationAuthorityTx(ctx context.Context, tx *sql.Tx, plan runtimepreparation.Plan) error {
	var kind, owner, request, payload, disposition string
	var generation int
	err := tx.QueryRowContext(ctx, `SELECT owner_kind,owner_id,generation,request_id,payload_hash,disposition FROM runtime_preparation_authorities WHERE plan_id=?`, plan.ID).Scan(&kind, &owner, &generation, &request, &payload, &disposition)
	if err == nil {
		if kind != string(plan.OwnerKind) || owner != plan.OwnerID || generation != plan.Generation || request != plan.PreparationRequestID || payload != plan.PreparationPayloadHash || disposition != "CURRENT" {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_preparation_authorities(plan_id,owner_kind,owner_id,generation,request_id,payload_hash,disposition,created_at) VALUES(?,?,?,?,?,?,'CURRENT',?)`, plan.ID, string(plan.OwnerKind), plan.OwnerID, plan.Generation, plan.PreparationRequestID, plan.PreparationPayloadHash, formatTime(plan.CreatedAt))
	return conflict(err)
}
