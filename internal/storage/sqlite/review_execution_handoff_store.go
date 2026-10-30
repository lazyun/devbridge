package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executionproposal"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// New approvals use a stable approval-owned identity. Existing persisted
// candidates retain their original IDs and reservation replay reads those IDs.
func approvedExecutionID(t task.Task) string {
	return "execution_approved_" + workflowHash(t.ID + "\x00" + t.ReviewApprovalID)[:32]
}

// createApprovedExecutionTx materializes the first immutable candidate. Runtime
// delivery remains separately controlled by its execution_start_effect row.
func createApprovedExecutionTx(ctx context.Context, tx *sql.Tx, t task.Task, sessionID string, now time.Time) error {
	return createApprovedExecutionWithSourceTx(ctx, tx, t, sessionID, now, nil, nil, false, nil, nil)
}

// This entry is used only inside the runtime-authorized Review approval
// transaction, which also commits its exact START_AUTHORIZED effect.
func createRuntimeAuthorizedApprovalExecutionTx(ctx context.Context, tx *sql.Tx, t task.Task, sessionID string, now time.Time) error {
	return createApprovedExecutionWithSourceTx(ctx, tx, t, sessionID, now, nil, nil, true, nil, nil)
}

func createApprovedExecutionForStartTx(ctx context.Context, tx *sql.Tx, t task.Task, sessionID string, now time.Time, actor executionStartActor, expected *wp.TaskExecutionExpectation) error {
	return createApprovedExecutionWithSourceTx(ctx, tx, t, sessionID, now, &actor, expected, false, nil, nil)
}

func createFormalApprovedExecutionTx(ctx context.Context, tx *sql.Tx, t task.Task, p fe.Progress, now time.Time) error {
	if p.Validate() != nil || p.T2 != nil || p.Archive.Fence.TaskID != t.ID || p.Archive.RevisionID != t.CurrentDefinitionRevisionID || t.Phase != task.PhaseImplementationPreparing {
		return fe.ErrConflict
	}
	return createApprovedExecutionWithSourceTx(ctx, tx, t, p.Archive.Fence.AISessionID, now, nil, nil, true, &p, &p.Archive.Descriptor)
}

func createQualifiedApprovedExecutionForStartTx(ctx context.Context, tx *sql.Tx, t task.Task, session string, now time.Time, actor executionStartActor, expected *wp.TaskExecutionExpectation, descriptor runtimeplatform.Descriptor) error {
	if descriptor.Validate() != nil || descriptor.RuntimeRole != "IMPLEMENTATION" {
		return fe.ErrConflict
	}
	return createApprovedExecutionWithSourceTx(ctx, tx, t, session, now, &actor, expected, false, nil, &descriptor)
}
func formalDescriptorRuntimeIdentities(d runtimeplatform.Descriptor) (string, string) {
	adapter, _ := json.Marshal(map[string]string{"adapterId": d.AdapterID, "adapterImplementationDigest": d.AdapterImplementationDigest})
	protocol, _ := json.Marshal(map[string]string{"protocolId": d.ProtocolID, "protocolSchemaDigest": d.ProtocolSchemaDigest})
	return string(adapter), string(protocol)
}

func createApprovedExecutionWithSourceTx(ctx context.Context, tx *sql.Tx, t task.Task, sessionID string, now time.Time, start *executionStartActor, expected *wp.TaskExecutionExpectation, approvalTransaction bool, formal *fe.Progress, descriptor *runtimeplatform.Descriptor) error {
	if err := validateApprovedTaskTx(ctx, tx, t, false); err != nil {
		return err
	}
	if t.CurrentExecutionID != "" && formal == nil {
		revising, err := noRunRevisionApprovedTx(ctx, tx, t.ID)
		if err != nil {
			return err
		}
		if !revising {
			return executionproposal.ErrConflict
		}
	}
	work, err := encodeWorkSpec(t.WorkSpec)
	if err != nil {
		return err
	}
	verification, err := encodeVerificationSpec(t.VerificationSpec)
	if err != nil {
		return err
	}
	planID := "plan_approved_" + workflowHash(t.ID + "\x00" + t.ReviewApprovalID)[:32]
	title := t.Goal
	if len([]rune(title)) > 128 {
		title = string([]rune(title)[:128])
	}
	actorKind, actorID := "AI_SESSION", sessionID
	var savedKind, savedID sql.NullString
	if e := tx.QueryRowContext(ctx, `SELECT actor_kind,actor_id FROM review_approval_receipt_context WHERE decision_id=?`, t.ReviewApprovalID).Scan(&savedKind, &savedID); e == nil && savedKind.Valid && savedID.Valid {
		actorKind, actorID = savedKind.String, savedID.String
	} else if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_plans(id,ai_session_id,content_actor_kind,content_actor_id,project_id,task_id,title,work_spec_json,verification_spec_json,status,created_at,updated_at,confirmed_at,applied_at) VALUES(?,?,?,?,?,?,?,?,?,'APPLIED',?,?,?,?)`, planID, nullString(sessionID), actorKind, actorID, t.ProjectID, t.ID, title, work, verification, formatTime(now), formatTime(now), formatTime(now), formatTime(now)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET confirmed_task_plan_id=? WHERE id=?`, planID, t.ID); err != nil {
		return err
	}
	candidate := executionproposal.Proposal{ID: approvedExecutionID(t), AISessionID: sessionID, ProjectID: t.ProjectID, TaskID: t.ID, TaskPlanID: planID, WorkSpec: t.WorkSpec, VerificationSpec: t.VerificationSpec, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID}
	if len(t.RepositoryTargets) != 0 {
		candidate.RuntimeAdapterIdentity = executionproposal.RuntimeAdapterCodexAppServerV1
		candidate.RuntimeProtocolIdentity = executionproposal.RuntimeProtocolCodex0149
	}
	if descriptor != nil {
		candidate.RuntimeAdapterIdentity, candidate.RuntimeProtocolIdentity = formalDescriptorRuntimeIdentities(*descriptor)
	}

	var runtimeJSON sql.NullString
	var backendJSON sql.NullString
	if t.ReviewApprovalID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT runtime_config_json,backend_selection_json FROM review_approval_materializations WHERE decision_id=? AND task_id=? AND (initial_revision_id=? OR EXISTS(SELECT 1 FROM execution_instruction_sources s WHERE s.revision_id=? AND s.source_revision_id=review_approval_materializations.initial_revision_id AND s.decision_id=review_approval_materializations.decision_id AND s.source_kind='EXECUTION_START'))`, t.ReviewApprovalID, t.ID, t.CurrentDefinitionRevisionID, t.CurrentDefinitionRevisionID).Scan(&runtimeJSON, &backendJSON); err != nil {
			return err
		}
	}
	revision, err := getDefinitionRevision(ctx, tx, t.CurrentDefinitionRevisionID)
	if err != nil {
		return err
	}
	if (revision.Definition.Version == 2 || t.ReviewApprovalID == "") && !approvalTransaction {
		// A later first start follows a definition-only approval. Historical
		// materializations can contain the Review's runtime for provenance, but
		// those values never authorize this Implementation. The exact original
		// START_APPROVED request supplies the new selection even when both old
		// materialization fields are populated.
		if start != nil && start.Kind == "LOCAL_OPERATOR" && start.SelectionHash != "" {
			runtimeJSON, backendJSON, err = operatorDefinitionOnlyStartRuntimeTx(ctx, tx, t, start, expected)
		} else {
			runtimeJSON, backendJSON, err = definitionOnlyStartRuntimeTx(ctx, tx, t, sessionID, start, expected)
		}
		if err != nil {
			return err
		}
	}
	if candidate.BackendSelection, err = decodeBackendSelection(backendJSON); err != nil {
		return err
	}
	if runtimeJSON.Valid && runtimeJSON.String != "" {
		if err := json.Unmarshal([]byte(runtimeJSON.String), &candidate.RuntimeConfig); err != nil {
			return err
		}
	}
	if err = bindExecutionDefinitionTx(ctx, tx, &candidate); err != nil {
		return err
	}
	runtimeConfig, err := encodeExecutionRuntimeConfig(candidate.RuntimeConfig)
	if err != nil {
		return err
	}
	backend, err := encodeBackendSelection(candidate.BackendSelection)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_proposals(id,ai_session_id,content_actor_kind,content_actor_id,project_id,task_id,task_plan_id,status,created_at,updated_at,confirmed_at,work_spec_json,verification_spec_json,outcome,task_definition_revision_id,runtime_config_json,backend_selection_json,primary_member_id,approved_workset_hash,runtime_adapter_identity,runtime_protocol_identity) VALUES(?,?,?,?,?,?,?,'CONFIRMED',?,?,?,?,?,'PENDING',?,?,?,?,?,?,?)`, candidate.ID, nullString(sessionID), actorKind, actorID, t.ProjectID, t.ID, planID, formatTime(now), formatTime(now), formatTime(now), work, verification, candidate.TaskDefinitionRevisionID, runtimeConfig, backend, nullString(candidate.PrimaryMemberID), nullString(candidate.ApprovedWorksetHash), nullString(candidate.RuntimeAdapterIdentity), nullString(candidate.RuntimeProtocolIdentity))
	if err != nil {
		return err
	}
	phase := task.PhaseImplementationReady
	if formal != nil {
		phase = task.PhaseImplementationPreparing
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET current_execution_id=? WHERE id=? AND project_id=? AND current_execution_id IS ? AND current_definition_revision_id=? AND review_approval_id IS ? AND phase=? AND status='ACTIVE'`, candidate.ID, t.ID, t.ProjectID, nullString(t.CurrentExecutionID), t.CurrentDefinitionRevisionID, nullString(t.ReviewApprovalID), string(phase))
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed != 1 {
		return executionproposal.ErrConflict
	}
	authorizationSource := "REVIEW_APPROVAL"
	if t.ReviewApprovalID == "" {
		authorizationSource = "IMPLEMENTATION_CONFIRMATION"
	}
	payload, _ := event.Payload(map[string]any{"review_approval_id": t.ReviewApprovalID, "authorization_source": authorizationSource, "task_definition_revision_id": candidate.TaskDefinitionRevisionID})
	return appendEvent(ctx, tx, event.Event{Type: event.ExecutionProposalCreated, ProjectID: t.ProjectID, TaskID: t.ID, EntityType: "execution_proposal", EntityID: candidate.ID, Payload: payload, CreatedAt: now})
}

// This authenticates an already consumed selection, not command completion.
// No caller flag, another request's intent, or Review runtime can substitute for
// the exact actor and START_IMPLEMENTATION step about to create this candidate.
func definitionOnlyStartRuntimeTx(ctx context.Context, tx *sql.Tx, t task.Task, sessionID string, actor *executionStartActor, expected *wp.TaskExecutionExpectation) (sql.NullString, sql.NullString, error) {
	var empty sql.NullString
	if actor == nil || expected == nil || actor.Kind != "AI_SESSION" || actor.ID == "" || actor.ID != sessionID || actor.RequestID == "" || actor.Action != "START_EXECUTION" {
		return empty, empty, executionproposal.ErrStartAuthorizationUnavailable
	}
	var count int
	var intentID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*),MIN(ci.intent_id) FROM confirmation_intents ci JOIN confirmation_commit_requests r ON r.ai_session_id=ci.ai_session_id AND r.intent_id=ci.intent_id AND r.request_id=json_extract(ci.intent_json,'$.commit.requestId'), json_each(ci.intent_json,'$.steps') step
 WHERE ci.ai_session_id=? AND ci.project_id=? AND json_extract(ci.intent_json,'$.purpose')='EXECUTION'
 AND json_extract(ci.intent_json,'$.content.execution.taskId')=? AND json_extract(ci.intent_json,'$.content.execution.featureAreaId')=?
 AND ((json_extract(ci.intent_json,'$.action')='START_APPROVED' AND json_extract(ci.intent_json,'$.content.execution.taskDefinitionRevisionId')=?)
 OR (json_extract(ci.intent_json,'$.action')='CONFIRM_AND_START_IMPLEMENTATION' AND EXISTS(SELECT 1 FROM task_review_skips s WHERE s.task_id=? AND s.project_id=ci.project_id AND s.confirmation_intent_id=ci.intent_id AND s.approved_revision_id=?)))
 AND json_extract(ci.intent_json,'$.state') IN ('CONFIRMING','PARTIAL','UNKNOWN') AND r.outcome='CONSUMED' AND length(r.submitted_sha256)=64
 AND json_extract(step.value,'$.kind')='START_IMPLEMENTATION' AND json_extract(step.value,'$.requestId')=? AND json_extract(step.value,'$.state') IN ('PENDING','UNKNOWN')`, actor.ID, t.ProjectID, t.ID, t.FeatureAreaID, t.CurrentDefinitionRevisionID, t.ID, t.CurrentDefinitionRevisionID, actor.RequestID).Scan(&count, &intentID)
	if err != nil {
		return empty, empty, err
	}
	if count != 1 || !intentID.Valid {
		return empty, empty, executionproposal.ErrStartAuthorizationUnavailable
	}
	intent, err := confirmationRead(ctx, tx, actor.ID, intentID.String)
	if err != nil {
		return empty, empty, err
	}
	b, commit := intent.Content.Execution, intent.Commit
	if b == nil || commit == nil || !confirmationCommitIdentity(intent, *commit) || b.Action != intent.Action || b.TaskID != t.ID || b.FeatureAreaID != t.FeatureAreaID || t.Phase != task.PhaseImplementationReady || commit.Selection.RuntimeConfig == nil || commit.Selection.Backend == nil || commit.BackendSelection == nil {
		return empty, empty, executionproposal.ErrStartAuthorizationUnavailable
	}
	switch intent.Action {
	case "START_APPROVED":
		if b.ExpectedTarget == nil || *b.ExpectedTarget != *expected || b.ExpectedTarget.FeatureAreaID != t.FeatureAreaID || b.ExpectedTarget.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID || b.ExpectedTarget.ExecutionID != t.CurrentExecutionID || b.ExpectedTarget.Phase != task.PhaseImplementationReady {
			return empty, empty, executionproposal.ErrStartAuthorizationUnavailable
		}
	case "CONFIRM_AND_START_IMPLEMENTATION":
		// The skip receipt links this exact consumed confirmation to its frozen
		// definition. Require its completed first step too; a latest-Task read
		// alone must never widen an old confirmation's start authority.
		if b.ExpectedTarget != nil || b.ExecutionID != "" || b.ReviewID != "" || b.DecisionID != "" || t.CurrentExecutionID != "" || t.CurrentReviewID != "" || t.ReviewApprovalID != "" || *expected != (wp.TaskExecutionExpectation{FeatureAreaID: t.FeatureAreaID, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, Phase: task.PhaseImplementationReady}) {
			return empty, empty, executionproposal.ErrStartAuthorizationUnavailable
		}
		if len(intent.Steps) != 2 || intent.Steps[0].Kind != "CONFIRM_IMPLEMENTATION" || intent.Steps[0].State != "COMPLETED" || intent.Steps[0].TaskID != t.ID || intent.Steps[0].RevisionID != t.CurrentDefinitionRevisionID || intent.Steps[1].Kind != "START_IMPLEMENTATION" || intent.Steps[1].RequestID != actor.RequestID {
			return empty, empty, executionproposal.ErrStartAuthorizationUnavailable
		}
	default:
		return empty, empty, executionproposal.ErrStartAuthorizationUnavailable
	}
	if commit.Selection.Backend.ConfigID != commit.BackendSelection.ConfigID || commit.Selection.Backend.ConfigFingerprint != commit.BackendSelection.ConfigFingerprint || commit.BackendSelection.ConfigID == "" || commit.BackendSelection.ConfigFingerprint == "" {
		return empty, empty, executionproposal.ErrConflict
	}
	var savedPayload string
	if err = tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND intent_id=? AND request_id=? AND outcome='CONSUMED'`, actor.ID, intent.IntentID, commit.RequestID).Scan(&savedPayload); err != nil {
		return empty, empty, err
	}
	encoded, err := json.Marshal(commit)
	if err != nil {
		return empty, empty, err
	}
	if workflowHash(string(encoded)) != savedPayload {
		return empty, empty, executionproposal.ErrConflict
	}
	content, err := json.Marshal(intent.Content)
	if err != nil {
		return empty, empty, err
	}
	if workflowHash(string(content)) != intent.ContentHash {
		return empty, empty, executionproposal.ErrConflict
	}
	frozen, err := freezeExecutionRuntimeConfig(*commit.Selection.RuntimeConfig)
	if err != nil || frozen != *commit.Selection.RuntimeConfig {
		return empty, empty, executionproposal.ErrConflict
	}
	variantCount := 0
	for _, variant := range intent.Content.Variants {
		if variant.ID == commit.VariantID && variant.ImplementationMode == task.ImplementationCodingAgent {
			variantCount++
		}
	}
	if variantCount != 1 {
		return empty, empty, executionproposal.ErrConflict
	}
	runtimeJSON, err := json.Marshal(commit.Selection.RuntimeConfig)
	if err != nil {
		return empty, empty, err
	}
	backendJSON, err := json.Marshal(commit.BackendSelection)
	if err != nil {
		return empty, empty, err
	}
	return sql.NullString{String: string(runtimeJSON), Valid: true}, sql.NullString{String: string(backendJSON), Valid: true}, nil
}

// PendingApprovedExecutions is a bounded durable queue. The reservation path
// rechecks authority under the writer lock; this query grants no runtime access.
func (s *ExecutionProposalStore) PendingApprovedExecutions(ctx context.Context, after string) ([]executionproposal.ApprovedExecution, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT e.id FROM execution_proposals e JOIN tasks t ON t.id=e.task_id JOIN feature_areas f ON f.id=t.feature_area_id WHERE e.id>? AND e.outcome IN ('PENDING','RUNNING') AND t.status='ACTIVE' AND t.legacy_read_only=0 AND f.status='ACTIVE' AND e.id=t.current_execution_id AND e.task_definition_revision_id=t.current_definition_revision_id AND e.project_id=t.project_id
 AND ((e.status IN ('CONFIRMED','ADMITTED') AND e.run_id IS NULL AND (t.phase IN ('IMPLEMENTATION_READY','VERIFICATION_READY') OR (t.phase='IMPLEMENTATION_PREPARING' AND e.status='ADMITTED' AND `+noRunSuccessorContinuationSQL+`))) OR (e.status='CONSUMED' AND EXISTS(SELECT 1 FROM runtime_handoffs h JOIN runs r ON r.id=h.run_id WHERE h.run_id=COALESCE(e.verification_run_id,e.run_id) AND h.state='REGISTERED' AND r.status='CREATED' AND h.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1))))
 AND (`+noRunSuccessorContinuationSQL+` OR EXISTS(SELECT 1 FROM execution_start_effects s WHERE s.effect='START_AUTHORIZED' AND s.execution_id=e.id AND s.project_id=e.project_id AND s.task_id=e.task_id AND s.task_definition_revision_id=e.task_definition_revision_id AND s.feature_area_id=t.feature_area_id
  AND ((e.status IN ('CONFIRMED','ADMITTED') AND s.expected_phase=t.phase) OR (e.status='CONSUMED' AND EXISTS(SELECT 1 FROM runs sr WHERE sr.id=COALESCE(e.verification_run_id,e.run_id) AND ((s.expected_phase='IMPLEMENTATION_READY' AND sr.purpose='IMPLEMENTATION') OR (s.expected_phase='VERIFICATION_READY' AND sr.purpose='VERIFICATION') OR (s.expected_phase='IMPLEMENTATION_READY' AND sr.purpose='VERIFICATION' AND EXISTS(SELECT 1 FROM runs implementation WHERE implementation.id=e.run_id AND implementation.execution_proposal_id=e.id AND implementation.purpose='IMPLEMENTATION' AND implementation.status='SUCCEEDED')))))))
 OR (NOT EXISTS(SELECT 1 FROM execution_start_effects recorded WHERE recorded.execution_id=e.id AND recorded.project_id=e.project_id AND recorded.task_id=e.task_id AND recorded.task_definition_revision_id=e.task_definition_revision_id)
  AND e.status='CONSUMED' AND EXISTS(SELECT 1 FROM runs legacy_run WHERE legacy_run.id=COALESCE(e.verification_run_id,e.run_id) AND legacy_run.execution_proposal_id=e.id AND legacy_run.status='CREATED'
   AND ((legacy_run.purpose='IMPLEMENTATION') OR (legacy_run.purpose='VERIFICATION' AND EXISTS(SELECT 1 FROM runs implementation WHERE implementation.id=e.run_id AND implementation.execution_proposal_id=e.id AND implementation.purpose='IMPLEMENTATION' AND implementation.status='SUCCEEDED')))
   AND (EXISTS(SELECT 1 FROM operator_task_execution_start_receipts o WHERE o.project_id=e.project_id AND o.actor_kind='LOCAL_OPERATOR' AND length(o.actor_id)>0 AND length(o.request_id)>0 AND o.task_id=e.task_id AND o.feature_area_id=t.feature_area_id AND o.task_definition_revision_id=e.task_definition_revision_id AND o.expected_execution_id=e.id AND o.execution_id=e.id AND o.run_id=legacy_run.id AND length(o.payload_sha256)=64 AND ((o.action='START_EXECUTION' AND o.expected_phase='IMPLEMENTATION_READY' AND legacy_run.purpose='IMPLEMENTATION') OR (o.action='START_VERIFICATION' AND o.expected_phase='VERIFICATION_READY' AND legacy_run.purpose='VERIFICATION')))
    OR EXISTS(SELECT 1 FROM confirmation_intents ci, json_each(ci.intent_json,'$.steps') step WHERE ci.project_id=e.project_id AND ci.ai_session_id=e.ai_session_id AND json_type(ci.intent_json,'$.commit')='object' AND json_extract(ci.intent_json,'$.state') IN ('CONFIRMING','PARTIAL','UNKNOWN','COMPLETED') AND json_extract(ci.intent_json,'$.action') IN ('APPROVE_AND_START','START_APPROVED','CONFIRM_AND_START_IMPLEMENTATION','RETRY_IMPLEMENTATION','REQUEST_CHANGES') AND json_extract(ci.intent_json,'$.content.execution.taskId')=e.task_id AND json_extract(ci.intent_json,'$.content.execution.featureAreaId')=t.feature_area_id AND json_extract(step.value,'$.kind')='START_IMPLEMENTATION' AND length(json_extract(step.value,'$.requestId'))>0 AND json_extract(step.value,'$.executionId')=e.id AND json_extract(step.value,'$.revisionId')=e.task_definition_revision_id AND json_extract(step.value,'$.runId')=legacy_run.id AND json_extract(step.value,'$.effect')='RESERVED')))))
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(e.id,t.id)) ORDER BY e.id LIMIT 16`, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []executionproposal.ApprovedExecution
	for rows.Next() {
		var v executionproposal.ApprovedExecution
		if err = rows.Scan(&v.ExecutionID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	ready := out[:0]
	for _, v := range out {
		if _, readyErr := formalExecutionStartReadyTx(ctx, s.db.sql, v.ExecutionID); readyErr == nil {
			ready = append(ready, v)
		}
	}
	return ready, nil
}
