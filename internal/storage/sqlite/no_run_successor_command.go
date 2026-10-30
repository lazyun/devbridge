package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

func successorReject(code string, in wp.PrepareSuccessorExecution) error {
	g := wp.NoRunSuccessorGuidance(in.ProjectID, in.TaskID, in.PredecessorExecutionID)
	switch code {
	case "RUNTIME_SCOPE_SOURCE_STALE", "BACKEND_CONFIGURATION_CHANGED", "RUNTIME_CONFIGURATION_CHANGED", "RUNTIME_ADAPTER_IDENTITY_CHANGED", "RUNTIME_PROTOCOL_IDENTITY_CHANGED":
		g.Set(wp.NoRunSuccessorState, "STOP_AND_REVISE", "Runtime authority changed; revise the execution before preparing a successor.")
		rejection := wp.Reject(code, g)
		rejection.Outcome = "REJECTED_NOT_CONSUMED"
		rejection.RetryDisposition = "REPREPARE_REQUIRED"
		return rejection
	default:
		return wp.Reject(code, g)
	}
}

// PrepareSuccessorExecution is the one business writer for a no-Run successor.
// Receipt preflight always precedes Task phase and effect-authority tests.
func (s *WorkflowProgressionStore) PrepareSuccessorExecution(ctx context.Context, in wp.PrepareSuccessorExecution) (wp.SuccessorCommandReceipt, error) {
	var empty wp.SuccessorCommandReceipt
	if in.RequestID == "" || len(in.RequestID) > 128 || len(in.PayloadSHA256) != 64 || in.ProjectID == "" || in.TaskID == "" || in.PredecessorExecutionID == "" || in.ExpectedCurrentExecutionID != in.PredecessorExecutionID || in.ExpectedDefinitionRevisionID == "" || in.ExpectedTaskState != wp.NoRunSuccessorState || in.ActorAuthority == "" {
		return empty, successorReject("INVALID_ARGUMENT", in)
	}
	// Authenticate and replay using a read-only snapshot before writer epoch,
	// current Task policy or any effect-authority continuation.
	read, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, err
	}
	if err = authorizeNoRunSuccessorActor(ctx, read, in); err != nil {
		read.Rollback()
		return empty, err
	}
	saved, found, err := readExactNoRunSuccessorReceipt(ctx, read, in)
	read.Rollback()
	if err != nil || found {
		return saved, err
	}
	pinned, err := s.db.beginRuntimePlatformWrite(ctx)
	if err != nil {
		return empty, err
	}
	defer pinned.Rollback()
	tx := pinned.Tx
	if err = authorizeNoRunSuccessorActor(ctx, tx, in); err != nil {
		return empty, err
	}
	saved, found, err = readExactNoRunSuccessorReceipt(ctx, tx, in)
	if err != nil || found {
		return saved, err
	}
	// A distinct request on this exact predecessor acknowledges the one-time
	// effect. It may write only its own immutable command receipt.
	var authorityID, successorID, planID, revisionID, authorityTask string
	err = tx.QueryRowContext(ctx, `SELECT authority_id,successor_execution_id,runtime_scope_plan_id,task_definition_revision_id,task_id FROM no_run_successor_effect_authorities WHERE predecessor_execution_id=?`, in.PredecessorExecutionID).Scan(&authorityID, &successorID, &planID, &revisionID, &authorityTask)
	if err == nil {
		if revisionID != in.ExpectedDefinitionRevisionID || authorityTask != in.TaskID {
			return empty, successorReject("SOURCE_REVISION_STALE", in)
		}
		var phase task.Phase
		var current string
		if err = tx.QueryRowContext(ctx, `SELECT phase,COALESCE(current_execution_id,'') FROM tasks WHERE id=? AND project_id=? AND status='ACTIVE'`, in.TaskID, in.ProjectID).Scan(&phase, &current); err != nil || phase != task.PhaseImplementationPreparing || current != successorID {
			return empty, successorReject("TASK_STATE_STALE", in)
		}
		saved = wp.SuccessorCommandReceipt{RequestID: in.RequestID, PayloadSHA256: in.PayloadSHA256, TaskID: in.TaskID, PredecessorExecutionID: in.PredecessorExecutionID, SuccessorEffectAuthorityID: authorityID, SuccessorExecutionID: successorID, RuntimeScopePlanID: planID, RuntimePreparationAuthorityID: planID, Outcome: wp.NoRunSuccessorAlreadyExists}
		if err = insertSuccessorReceiptTx(ctx, tx, saved, in, s.now()); err != nil {
			return empty, err
		}
		if err = pinned.Commit(); err != nil {
			return empty, err
		}
		return saved, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	var phase task.Phase
	var current, revision, status string
	if err = tx.QueryRowContext(ctx, `SELECT phase,COALESCE(current_execution_id,''),COALESCE(current_definition_revision_id,''),status FROM tasks WHERE id=? AND project_id=?`, in.TaskID, in.ProjectID).Scan(&phase, &current, &revision, &status); errors.Is(err, sql.ErrNoRows) {
		return empty, successorReject("TASK_NOT_CURRENT", in)
	} else if err != nil {
		return empty, err
	}
	if status != "ACTIVE" || current != in.ExpectedCurrentExecutionID || revision != in.ExpectedDefinitionRevisionID {
		return empty, successorReject("TASK_NOT_CURRENT", in)
	}
	if phase != task.PhaseNoRunSuccessorRequired {
		return empty, successorReject("TASK_STATE_STALE", in)
	}
	facts, err := readNoRunSuccessorFacts(ctx, tx, in.TaskID)
	if err != nil || facts.ProjectID != in.ProjectID || facts.PredecessorExecutionID != in.PredecessorExecutionID || facts.DefinitionRevisionID != in.ExpectedDefinitionRevisionID {
		return empty, successorReject("NO_RUN_SUCCESSOR_AUTHORITY_UNAVAILABLE", in)
	}
	var eligibilityTask, eligibilityRevision, cancelRequest string
	var consumed sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT task_id,task_definition_revision_id,effective_cancellation_request_id,consumed_successor_execution_id FROM no_run_successor_eligibilities WHERE predecessor_execution_id=?`, in.PredecessorExecutionID).Scan(&eligibilityTask, &eligibilityRevision, &cancelRequest, &consumed)
	if err != nil || eligibilityTask != in.TaskID || eligibilityRevision != revision || cancelRequest != facts.EffectiveCancellationRequestID || consumed.Valid {
		return empty, successorReject("NO_RUN_SUCCESSOR_AUTHORITY_UNAVAILABLE", in)
	}
	predecessor, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, in.PredecessorExecutionID))
	if err != nil {
		return empty, err
	}
	sourcePlan, err := readSuccessorSourcePlan(ctx, tx, in.PredecessorExecutionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrConflict) {
			return empty, successorReject("RUNTIME_SCOPE_SOURCE_STALE", in)
		}
		return empty, err
	}
	topology, development, err := purposeProjectContextTx(ctx, tx, in.ProjectID)
	if err != nil {
		return empty, err
	}
	if topology != sourcePlan.TopologyRevision || development != sourcePlan.DevelopmentContextHash {
		return empty, successorReject("RUNTIME_SCOPE_SOURCE_STALE", in)
	}
	if err = validateSuccessorSourceScopeTx(ctx, tx, in, predecessor, sourcePlan); err != nil {
		return empty, err
	}
	currentAuthority, err := s.resolveCurrentNoRunAuthorityTx(ctx, tx, in)
	if err != nil {
		return empty, err
	}
	if code := noRunAuthorityDriftCode(sourcePlan, currentAuthority); code != "" {
		return empty, successorReject(code, in)
	}
	if err = taskActionAdmissionTx(ctx, tx, in.TaskID); err != nil {
		return empty, err
	}
	now := s.now().UTC()
	successorID = "execution_successor_" + workflowHash(in.PredecessorExecutionID + "\x00" + in.RequestID)[:32]
	planID = "runtime_scope_execution_" + workflowHash(successorID + "\x00" + in.RequestID)[:32]
	prepRequest := "successor_prep_" + workflowHash(in.RequestID + "\x00" + successorID)[:32]
	authorityID = "successor_effect_" + workflowHash(in.PredecessorExecutionID)[:32]
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_proposals(id,ai_session_id,content_actor_kind,content_actor_id,project_id,task_id,task_plan_id,status,created_at,updated_at,confirmed_at,work_spec_json,verification_spec_json,outcome,task_definition_revision_id,runtime_config_json,backend_selection_json,primary_member_id,approved_workset_hash,runtime_adapter_identity,runtime_protocol_identity)
      SELECT ?,ai_session_id,content_actor_kind,content_actor_id,project_id,task_id,task_plan_id,'CONFIRMED',?,?,?,work_spec_json,verification_spec_json,'PENDING',task_definition_revision_id,runtime_config_json,backend_selection_json,primary_member_id,approved_workset_hash,runtime_adapter_identity,runtime_protocol_identity FROM execution_proposals WHERE id=? AND status='ADMITTED' AND outcome='CANCELED' AND run_id IS NULL`, successorID, formatTime(now), formatTime(now), formatTime(now), in.PredecessorExecutionID)
	if err != nil {
		return empty, conflict(fmt.Errorf("insert no-Run successor: %w", err))
	}
	result, err := tx.ExecContext(ctx, `UPDATE no_run_successor_eligibilities SET consumed_successor_execution_id=? WHERE predecessor_execution_id=? AND consumed_successor_execution_id IS NULL`, successorID, in.PredecessorExecutionID)
	if err != nil {
		return empty, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return empty, successorReject("NO_RUN_SUCCESSOR_AUTHORITY_UNAVAILABLE", in)
	}
	plan := sourcePlan
	plan.ID = planID
	plan.OwnerID = successorID
	plan.OwnerKind = runtimepreparation.OwnerExecution
	plan.BackendIdentity = currentAuthority.BackendIdentity
	plan.BackendConfigFingerprint = currentAuthority.BackendConfigFingerprint
	plan.BackendSource = currentAuthority.BackendSource
	plan.BackendKind = currentAuthority.BackendKind
	plan.Model = currentAuthority.Model
	plan.ReasoningEffort = currentAuthority.ReasoningEffort
	plan.CodexFastMode = currentAuthority.CodexFastMode
	plan.ServiceTier = currentAuthority.ServiceTier
	plan.RuntimeAdapterIdentity = currentAuthority.RuntimeAdapterIdentity
	plan.RuntimeProtocolIdentity = currentAuthority.RuntimeProtocolIdentity
	plan.AuthoritySnapshotHash = currentAuthority.AuthoritySnapshotHash
	plan.SourceCommand = wp.NoRunSuccessorAction
	plan.SourceReceiptID = in.RequestID
	plan.SourceExecutionID = in.PredecessorExecutionID
	plan.SuccessorExecutionID = successorID
	plan.PreparationRequestID = prepRequest
	plan.PreparationPayloadHash = in.PayloadSHA256
	plan.CreatedAt = now
	plan.ContentHash, err = plan.ComputeContentHash()
	if err != nil {
		return empty, err
	}
	if err = freezeRuntimeScopePlanTx(ctx, tx, plan); err != nil {
		return empty, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_proposal_admissions(proposal_id,admission_request_id,payload_sha256,source_decision_id,task_id,task_definition_revision_id,runtime_scope_plan_id,runtime_preparation_authority_id,admitted_at) VALUES(?,?,?,'',?,?,?,?,?)`, successorID, prepRequest, in.PayloadSHA256, in.TaskID, revision, planID, planID, formatTime(now))
	if err != nil {
		return empty, conflict(fmt.Errorf("successor admission: %w", err))
	}
	result, err = tx.ExecContext(ctx, `UPDATE execution_proposals SET status='ADMITTED',updated_at=? WHERE id=? AND status='CONFIRMED' AND outcome='PENDING' AND run_id IS NULL`, formatTime(now), successorID)
	if err != nil {
		return empty, conflict(err)
	}
	n, err = result.RowsAffected()
	if err != nil || n != 1 {
		return empty, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO no_run_successor_effect_authorities(authority_id,task_id,task_definition_revision_id,predecessor_execution_id,effective_cancellation_project_id,effective_cancellation_request_id,successor_execution_id,runtime_scope_plan_id,runtime_preparation_authority_id,predecessor_runtime_scope_plan_id,predecessor_plan_content_hash,predecessor_authority_snapshot_hash,verified_current_authority_snapshot_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, authorityID, in.TaskID, revision, in.PredecessorExecutionID, in.ProjectID, cancelRequest, successorID, planID, planID, sourcePlan.ID, sourcePlan.ContentHash, sourcePlan.AuthoritySnapshotHash, plan.AuthoritySnapshotHash, formatTime(now))
	if err != nil {
		return empty, conflict(fmt.Errorf("successor effect: %w", err))
	}
	saved = wp.SuccessorCommandReceipt{RequestID: in.RequestID, PayloadSHA256: in.PayloadSHA256, TaskID: in.TaskID, PredecessorExecutionID: in.PredecessorExecutionID, SuccessorEffectAuthorityID: authorityID, SuccessorExecutionID: successorID, RuntimeScopePlanID: planID, RuntimePreparationAuthorityID: planID, Outcome: wp.NoRunSuccessorCreated}
	if err = insertSuccessorReceiptTx(ctx, tx, saved, in, now); err != nil {
		return empty, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE tasks SET current_execution_id=?,phase='IMPLEMENTATION_PREPARING',updated_at=? WHERE id=? AND status='ACTIVE' AND phase='NO_RUN_SUCCESSOR_REQUIRED' AND current_execution_id=? AND current_definition_revision_id=?`, successorID, formatTime(now), in.TaskID, in.PredecessorExecutionID, revision)
	if err != nil {
		return empty, conflict(fmt.Errorf("successor Task repoint: %w", err))
	}
	n, err = result.RowsAffected()
	if err != nil || n != 1 {
		return empty, ErrConflict
	}
	_ = predecessor // source remains immutable; materialized for full row validation.
	if err = pinned.Commit(); err != nil {
		return empty, err
	}
	return saved, nil
}

// noRunAuthorityDriftCode preserves the frozen public priority order. The
// caller invokes it only after source and current authority readback succeeds.
func noRunAuthorityDriftCode(source, current runtimepreparation.Plan) string {
	if source.BackendIdentity != current.BackendIdentity || source.BackendConfigFingerprint != current.BackendConfigFingerprint || source.BackendSource != current.BackendSource || source.BackendKind != current.BackendKind {
		return "BACKEND_CONFIGURATION_CHANGED"
	}
	if source.Model != current.Model || source.ReasoningEffort != current.ReasoningEffort || source.CodexFastMode != current.CodexFastMode || source.ServiceTier != current.ServiceTier {
		return "RUNTIME_CONFIGURATION_CHANGED"
	}
	if source.RuntimeAdapterIdentity != current.RuntimeAdapterIdentity {
		return "RUNTIME_ADAPTER_IDENTITY_CHANGED"
	}
	if source.RuntimeProtocolIdentity != current.RuntimeProtocolIdentity {
		return "RUNTIME_PROTOCOL_IDENTITY_CHANGED"
	}
	return ""
}

func validateSuccessorSourceScopeTx(ctx context.Context, tx *sql.Tx, in wp.PrepareSuccessorExecution, predecessor executionproposal.Proposal, source runtimepreparation.Plan) error {
	current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, in.TaskID, in.ProjectID))
	if err != nil || current.CurrentDefinitionRevisionID != in.ExpectedDefinitionRevisionID || current.WorkSpec.ImplementationMode != task.ImplementationCodingAgent || !reflect.DeepEqual(current.WorkSpec, predecessor.WorkSpec) || !reflect.DeepEqual(current.VerificationSpec, predecessor.VerificationSpec) {
		return successorReject("SOURCE_REVISION_STALE", in)
	}
	var targets []workset.Target
	if len(current.RepositoryTargets) > 0 {
		targets = workset.CloneTargets(current.RepositoryTargets)
		if source.PrimaryMemberID != current.PrimaryMemberID || predecessor.PrimaryMemberID != current.PrimaryMemberID {
			return successorReject("RUNTIME_SCOPE_SOURCE_STALE", in)
		}
	} else {
		if current.RepositoryID == "" || source.PrimaryMemberID != current.RepositoryID || current.BaseCommit == "" {
			return successorReject("RUNTIME_SCOPE_SOURCE_STALE", in)
		}
		targets = []workset.Target{{RepositoryID: current.RepositoryID, MemberID: current.RepositoryID, MemberVersion: 1, OriginCommit: current.BaseCommit, Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}}}
	}
	if !reflect.DeepEqual(source.RepositoryTargets, targets) || source.RuntimeAdapterIdentity != predecessor.RuntimeAdapterIdentity && predecessor.RuntimeAdapterIdentity != "" {
		return successorReject("RUNTIME_SCOPE_SOURCE_STALE", in)
	}
	for _, target := range targets {
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories WHERE id=? AND project_id=? AND registration_state='ACTIVE'`, target.RepositoryID, in.ProjectID).Scan(&active); err != nil || active != 1 || source.ExpectedSourceHEADs[target.MemberID] != target.OriginCommit || !reflect.DeepEqual(source.WriteScopes[target.MemberID], target.Write) {
			return successorReject("RUNTIME_SCOPE_SOURCE_STALE", in)
		}
	}
	if predecessor.BackendSelection != nil && source.BackendIdentity != predecessor.BackendSelection.ConfigID {
		return successorReject("RUNTIME_SCOPE_SOURCE_STALE", in)
	}
	if predecessor.RuntimeConfig.Model != "" && source.Model != predecessor.RuntimeConfig.Model || predecessor.RuntimeConfig.ReasoningEffort != "" && source.ReasoningEffort != predecessor.RuntimeConfig.ReasoningEffort {
		return successorReject("RUNTIME_SCOPE_SOURCE_STALE", in)
	}
	return nil
}

func readSuccessorReceiptTx(ctx context.Context, tx definitionQuerier, requestID string) (wp.SuccessorCommandReceipt, error) {
	var r wp.SuccessorCommandReceipt
	err := tx.QueryRowContext(ctx, `SELECT c.request_id,c.payload_sha256,c.task_id,c.predecessor_execution_id,c.successor_effect_authority_id,c.successor_execution_id,a.runtime_scope_plan_id,a.runtime_preparation_authority_id,c.outcome FROM no_run_successor_command_receipts c JOIN no_run_successor_effect_authorities a ON a.authority_id=c.successor_effect_authority_id WHERE c.request_id=?`, requestID).Scan(&r.RequestID, &r.PayloadSHA256, &r.TaskID, &r.PredecessorExecutionID, &r.SuccessorEffectAuthorityID, &r.SuccessorExecutionID, &r.RuntimeScopePlanID, &r.RuntimePreparationAuthorityID, &r.Outcome)
	return r, err
}
func insertSuccessorReceiptTx(ctx context.Context, tx *sql.Tx, r wp.SuccessorCommandReceipt, in wp.PrepareSuccessorExecution, now time.Time) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO no_run_successor_command_identities(request_id,project_id,actor_authority,task_id,predecessor_execution_id,revision_id,payload_sha256,identity_json,identity_sha256) VALUES(?,?,?,?,?,?,?,?,?)`, in.RequestID, in.ProjectID, in.ActorAuthority, in.TaskID, in.PredecessorExecutionID, in.ExpectedDefinitionRevisionID, in.PayloadSHA256, string(raw), workflowHash(string(raw)))
	if err != nil {
		return conflict(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO no_run_successor_command_receipts(request_id,payload_sha256,task_id,predecessor_execution_id,successor_effect_authority_id,successor_execution_id,outcome,created_at) VALUES(?,?,?,?,?,?,?,?)`, r.RequestID, r.PayloadSHA256, r.TaskID, r.PredecessorExecutionID, r.SuccessorEffectAuthorityID, r.SuccessorExecutionID, r.Outcome, formatTime(now))
	return conflict(err)
}

func readSuccessorSourcePlan(ctx context.Context, tx definitionQuerier, predecessorID string) (runtimepreparation.Plan, error) {
	var kind runtimepreparation.OwnerKind
	if err := tx.QueryRowContext(ctx, `SELECT p.owner_kind FROM execution_proposal_admissions a JOIN runtime_scope_plans p ON p.plan_id=a.runtime_scope_plan_id WHERE a.proposal_id=?`, predecessorID).Scan(&kind); err != nil {
		return runtimepreparation.Plan{}, err
	}
	if kind == runtimepreparation.OwnerExecution {
		return readSuccessorSourcePlanWithOwner(ctx, tx, predecessorID, kind)
	}
	if kind != runtimepreparation.OwnerImplementation {
		return runtimepreparation.Plan{}, ErrConflict
	}
	// Only the exact new formal producer can use IMPLEMENTATION here. Older
	// plans cannot acquire authority merely because they have the same owner kind.
	var sourceID string
	err := tx.QueryRowContext(ctx, `SELECT s.source_id FROM runtime_selection_sources s JOIN formal_execution_t2 t2 ON t2.ai_session_id=s.ai_session_id AND t2.request_id=s.request_id JOIN formal_execution_receipts r ON r.ai_session_id=t2.ai_session_id AND r.request_id=t2.request_id WHERE t2.execution_id=? AND s.approval_request_id=t2.request_id`, predecessorID).Scan(&sourceID)
	if err != nil {
		return runtimepreparation.Plan{}, err
	}
	source, err := readRuntimeSelectionSourceTx(ctx, tx, sourceID)
	if err != nil {
		return runtimepreparation.Plan{}, err
	}
	r, err := readFormalExecutionReceiptTx(ctx, tx, fe.ReadIdentity{AISessionID: source.AISessionID, IntentID: source.IntentID, RequestID: source.RequestID, IntentVersion: source.IntentVersion, ContentHash: source.ContentHash, TypedPayloadHash: source.SourceReceiptPayloadHash, SubmittedHash: source.SourceReceiptSubmittedHash})
	if err != nil {
		return runtimepreparation.Plan{}, err
	}
	plan, err := readSuccessorSourcePlanWithOwner(ctx, tx, predecessorID, kind)
	if err != nil {
		return plan, err
	}
	if r.T2.ExecutionID != predecessorID || r.T1.Identity.SourceOriginID != sourceID || !reflect.DeepEqual(plan, r.Plan) {
		return plan, ErrConflict
	}
	return plan, nil
}

func readSuccessorSourcePlanWithOwner(ctx context.Context, tx definitionQuerier, predecessorID string, expectedOwner runtimepreparation.OwnerKind) (runtimepreparation.Plan, error) {
	var p runtimepreparation.Plan
	var targets, refs, heads, scopes, created string
	var fast int
	var admittedPlanID, authorityPlanID, authorityOwnerKind, authorityOwnerID, authorityDisposition string
	err := tx.QueryRowContext(ctx, `SELECT p.plan_id,p.generation,p.owner_kind,p.owner_id,p.project_id,COALESCE(p.feature_area_id,''),COALESCE(p.task_id,''),p.source_command,p.source_receipt_id,p.selected_variant_id,p.proposal_hash,p.task_definition_revision_id,p.source_execution_id,p.source_review_id,p.successor_execution_id,p.successor_review_id,p.repository_targets_json,p.primary_member_id,p.backend_identity,p.model,p.reasoning_effort,p.runtime_adapter_identity,p.topology_revision,p.development_context_hash,p.development_refs_json,p.expected_source_heads_json,p.write_scopes_json,p.preparation_request_id,p.preparation_payload_hash,p.content_hash,p.created_at,p.backend_config_fingerprint,p.backend_source,p.backend_kind,p.codex_fast_mode,p.service_tier,p.runtime_protocol_identity,p.authority_snapshot_hash,a.runtime_scope_plan_id,a.runtime_preparation_authority_id,auth.owner_kind,auth.owner_id,auth.disposition FROM runtime_scope_plans p JOIN execution_proposal_admissions a ON a.runtime_scope_plan_id=p.plan_id JOIN runtime_preparation_authorities auth ON auth.plan_id=a.runtime_preparation_authority_id WHERE a.proposal_id=?`, predecessorID).Scan(&p.ID, &p.Generation, &p.OwnerKind, &p.OwnerID, &p.ProjectID, &p.FeatureAreaID, &p.TaskID, &p.SourceCommand, &p.SourceReceiptID, &p.SelectedVariantID, &p.ProposalHash, &p.TaskDefinitionRevisionID, &p.SourceExecutionID, &p.SourceReviewID, &p.SuccessorExecutionID, &p.SuccessorReviewID, &targets, &p.PrimaryMemberID, &p.BackendIdentity, &p.Model, &p.ReasoningEffort, &p.RuntimeAdapterIdentity, &p.TopologyRevision, &p.DevelopmentContextHash, &refs, &heads, &scopes, &p.PreparationRequestID, &p.PreparationPayloadHash, &p.ContentHash, &created, &p.BackendConfigFingerprint, &p.BackendSource, &p.BackendKind, &fast, &p.ServiceTier, &p.RuntimeProtocolIdentity, &p.AuthoritySnapshotHash, &admittedPlanID, &authorityPlanID, &authorityOwnerKind, &authorityOwnerID, &authorityDisposition)
	if err != nil {
		return p, err
	}
	p.CodexFastMode = fast != 0
	for _, x := range []struct {
		raw string
		out any
	}{{targets, &p.RepositoryTargets}, {refs, &p.DevelopmentRefs}, {heads, &p.ExpectedSourceHEADs}, {scopes, &p.WriteScopes}} {
		if err = json.Unmarshal([]byte(x.raw), x.out); err != nil {
			return p, fmt.Errorf("%w: source Plan JSON: %v", ErrConflict, err)
		}
	}
	p.CreatedAt, err = parseTime(created)
	if err != nil {
		return p, fmt.Errorf("%w: source Plan timestamp: %v", ErrConflict, err)
	}
	if err = p.Validate(); err != nil {
		return p, fmt.Errorf("%w: source Plan validation: %v", ErrConflict, err)
	}
	if p.OwnerKind != expectedOwner || p.OwnerID != predecessorID || admittedPlanID != p.ID || authorityPlanID != p.ID || authorityOwnerKind != string(expectedOwner) || authorityOwnerID != predecessorID || authorityDisposition != "CURRENT" {
		return p, ErrConflict
	}
	return p, nil
}

func authorizeNoRunSuccessorActor(ctx context.Context, q definitionQuerier, in wp.PrepareSuccessorExecution) error {
	if strings.HasPrefix(in.ActorAuthority, "operator:") {
		actor, ok := localoperator.FromContext(ctx)
		if !ok || actor.Check(in.ProjectID, in.TaskID, "PREPARE_SUCCESSOR_EXECUTION") != nil || in.ActorAuthority != "operator:"+actor.PrincipalID() {
			return successorReject("PROJECT_MISMATCH", in)
		}
		return nil
	}
	var project string
	if err := q.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, in.ActorAuthority).Scan(&project); err != nil || project != in.ProjectID {
		return successorReject("PROJECT_MISMATCH", in)
	}
	return nil
}
func readExactNoRunSuccessorReceipt(ctx context.Context, q definitionQuerier, in wp.PrepareSuccessorExecution) (wp.SuccessorCommandReceipt, bool, error) {
	r, err := readSuccessorReceiptTx(ctx, q, in.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return wp.SuccessorCommandReceipt{}, true, err
	}
	if r.PredecessorExecutionID != in.PredecessorExecutionID || r.TaskID != in.TaskID {
		return wp.SuccessorCommandReceipt{}, true, successorReject("REQUEST_IDENTITY_CONFLICT", in)
	}
	if r.PayloadSHA256 != in.PayloadSHA256 {
		return wp.SuccessorCommandReceipt{}, true, successorReject("REQUEST_PAYLOAD_CONFLICT", in)
	}
	var raw, hash, project, actor, taskID, predecessor, revision, payload string
	err = q.QueryRowContext(ctx, `SELECT identity_json,identity_sha256,project_id,actor_authority,task_id,predecessor_execution_id,revision_id,payload_sha256 FROM no_run_successor_command_identities WHERE request_id=?`, in.RequestID).Scan(&raw, &hash, &project, &actor, &taskID, &predecessor, &revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return wp.SuccessorCommandReceipt{}, true, successorReject("ORIGINAL_COMMAND_IDENTITY_UNAVAILABLE", in)
	}
	if err != nil {
		return wp.SuccessorCommandReceipt{}, true, err
	}
	var original wp.PrepareSuccessorExecution
	if fe.Decode([]byte(raw), hash, &original) != nil || original.RequestID != in.RequestID || original.ProjectID != project || original.ActorAuthority != actor || original.TaskID != taskID || original.PredecessorExecutionID != predecessor || original.ExpectedDefinitionRevisionID != revision || original.PayloadSHA256 != payload {
		return wp.SuccessorCommandReceipt{}, true, ErrConflict
	}
	if original != in {
		return wp.SuccessorCommandReceipt{}, true, successorReject("REQUEST_IDENTITY_CONFLICT", in)
	}
	var exact bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposals e JOIN no_run_successor_effect_authorities a ON a.predecessor_execution_id=e.id WHERE e.id=? AND e.project_id=? AND e.task_id=? AND e.task_definition_revision_id=? AND a.authority_id=? AND a.successor_execution_id=? AND a.runtime_scope_plan_id=?)`, predecessor, project, taskID, revision, r.SuccessorEffectAuthorityID, r.SuccessorExecutionID, r.RuntimeScopePlanID).Scan(&exact)
	if err != nil {
		return wp.SuccessorCommandReceipt{}, true, err
	}
	if !exact {
		return wp.SuccessorCommandReceipt{}, true, ErrConflict
	}
	return r, true, nil
}
