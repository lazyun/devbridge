package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func operatorSelectedStartActor(ctx context.Context, in wp.OperatorExecutionStartRequest) (string, error) {
	a, ok := localoperator.FromContext(ctx)
	if !ok || a.Check(in.ProjectID, in.TaskID, "START_EXECUTION") != nil {
		return "", localoperator.ErrAccessDenied
	}
	if in.RequestID == "" || in.Expected.FeatureAreaID == "" || in.Expected.TaskDefinitionRevisionID == "" || in.Expected.Phase != task.PhaseImplementationReady || in.Expected.PrimaryMemberID == "" || len(in.Expected.ApprovedWorksetHash) != 64 || in.RuntimeConfig.Model == "" || in.RuntimeConfig.ReasoningEffort == "" || in.BackendSelection.ConfigID == "" || in.BackendSelection.ConfigFingerprint == "" {
		return "", executionproposal.ErrInvalidArgument
	}
	return a.PrincipalID(), nil
}

type operatorStartCommand struct {
	Request                           wp.OperatorExecutionStartRequest
	ActorID, RevisionHash, ApprovalID string
	Runtime                           codexruntime.NextRunRuntimeConfig
	Backend                           codexconfig.BackendSelection
}

// This immutable record is only a selection command. Completed admission is
// proved independently by its exact effect and admission, never by row existence.
func readOperatorStartCommand(ctx context.Context, q definitionQuerier, project, actor, request string) (operatorStartCommand, bool, error) {
	var c operatorStartCommand
	var taskID, area, revision, expectedExecution, phase, primary, workset, raw, hash, runtimeJSON, backendJSON string
	err := q.QueryRowContext(ctx, `SELECT task_id,feature_area_id,revision_id,revision_hash,approval_id,expected_execution_id,expected_phase,primary_member_id,workset_hash,request_json,payload_sha256,runtime_config_json,backend_selection_json FROM operator_execution_start_commands WHERE project_id=? AND actor_id=? AND request_id=?`, project, actor, request).Scan(&taskID, &area, &revision, &c.RevisionHash, &c.ApprovalID, &expectedExecution, &phase, &primary, &workset, &raw, &hash, &runtimeJSON, &backendJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if err = json.Unmarshal([]byte(raw), &c.Request); err != nil {
		return c, true, err
	}
	canonical, _ := json.Marshal(c.Request)
	e := c.Request.Expected
	if string(canonical) != raw || c.Request.Hash() != hash || c.Request.ProjectID != project || c.Request.TaskID != taskID || c.Request.RequestID != request || e.FeatureAreaID != area || e.TaskDefinitionRevisionID != revision || e.ExecutionID != expectedExecution || string(e.Phase) != phase || e.PrimaryMemberID != primary || e.ApprovedWorksetHash != workset {
		return c, true, executionproposal.ErrConflict
	}
	if err = json.Unmarshal([]byte(runtimeJSON), &c.Runtime); err != nil {
		return c, true, err
	}
	if err = json.Unmarshal([]byte(backendJSON), &c.Backend); err != nil {
		return c, true, err
	}
	canonicalRuntime, _ := json.Marshal(c.Runtime)
	canonicalBackend, _ := json.Marshal(c.Backend)
	if string(canonicalRuntime) != runtimeJSON || string(canonicalBackend) != backendJSON {
		return c, true, executionproposal.ErrConflict
	}
	frozen, freezeErr := freezeExecutionRuntimeConfig(c.Request.RuntimeConfig)
	if freezeErr != nil || frozen != c.Runtime || c.Backend.ConfigID != c.Request.BackendSelection.ConfigID || c.Backend.ConfigFingerprint != c.Request.BackendSelection.ConfigFingerprint {
		return c, true, executionproposal.ErrConflict
	}
	rev, err := getDefinitionRevision(ctx, q, revision)
	if err != nil {
		return c, true, err
	}
	if rev.TaskID != taskID || rev.PayloadSHA256 != c.RevisionHash || rev.Definition.ReviewApprovalID != c.ApprovalID || rev.Definition.Version != 2 || rev.Definition.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return c, true, executionproposal.ErrConflict
	}
	c.ActorID = actor
	return c, true, nil
}

func readOperatorStartOutcome(ctx context.Context, q definitionQuerier, in wp.OperatorExecutionStartRequest, actor string) (executionproposal.Proposal, run.Run, bool, error) {
	c, found, err := readOperatorStartCommand(ctx, q, in.ProjectID, actor, in.RequestID)
	if err != nil || !found {
		return executionproposal.Proposal{}, run.Run{}, found, err
	}
	if c.Request != in {
		return executionproposal.Proposal{}, run.Run{}, true, executionproposal.ErrConflict
	}
	var effect executionStartEffectRecord
	var payload, planID, admissionRequest, admissionPayload string
	err = q.QueryRowContext(ctx, `SELECT s.project_id,s.actor_kind,s.actor_id,s.request_id,s.action,s.effect,s.task_id,s.feature_area_id,s.task_definition_revision_id,s.execution_id,s.expected_phase,COALESCE(s.decision_id,''),s.payload_sha256,a.runtime_scope_plan_id,a.admission_request_id,a.payload_sha256
 FROM execution_start_effects s JOIN execution_proposal_admissions a ON a.proposal_id=s.execution_id AND a.task_id=s.task_id AND a.task_definition_revision_id=s.task_definition_revision_id
 JOIN runtime_scope_plans p ON p.plan_id=a.runtime_scope_plan_id AND p.owner_kind='EXECUTION' AND p.owner_id=s.execution_id AND p.project_id=s.project_id AND p.task_id=s.task_id AND p.feature_area_id=s.feature_area_id AND p.task_definition_revision_id=s.task_definition_revision_id AND p.source_receipt_id=s.request_id AND p.preparation_request_id=s.request_id AND p.preparation_payload_hash=s.payload_sha256
 JOIN runtime_preparation_authorities auth ON auth.plan_id=a.runtime_preparation_authority_id AND auth.plan_id=p.plan_id AND auth.owner_kind=p.owner_kind AND auth.owner_id=p.owner_id AND auth.generation=p.generation AND auth.request_id=s.request_id AND auth.payload_hash=s.payload_sha256
 WHERE s.project_id=? AND s.actor_kind='LOCAL_OPERATOR' AND s.actor_id=? AND s.request_id=?`, in.ProjectID, actor, in.RequestID).Scan(&effect.ProjectID, &effect.ActorKind, &effect.ActorID, &effect.RequestID, &effect.Action, &effect.Effect, &effect.TaskID, &effect.FeatureAreaID, &effect.RevisionID, &effect.ExecutionID, &effect.Phase, &effect.DecisionID, &payload, &planID, &admissionRequest, &admissionPayload)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, true, executionProposalConflict(err)
	}
	wanted := executionStartEffectRecord{ProjectID: in.ProjectID, ActorKind: "LOCAL_OPERATOR", ActorID: actor, RequestID: in.RequestID, Action: "START_EXECUTION", Effect: wp.StartEffectStartAuthorized, TaskID: in.TaskID, FeatureAreaID: in.Expected.FeatureAreaID, RevisionID: in.Expected.TaskDefinitionRevisionID, ExecutionID: "execution_approved_" + workflowHash(in.TaskID + "\x00" + c.ApprovalID)[:32], Phase: task.PhaseImplementationReady}
	wantedHash, _ := executionStartEffectPayload(wanted)
	if effect != wanted || payload != wantedHash || admissionRequest != in.RequestID || admissionPayload != payload || planID == "" {
		return executionproposal.Proposal{}, run.Run{}, true, executionproposal.ErrConflict
	}
	v, err := scanExecutionProposal(q.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=? AND project_id=? AND task_id=?`, effect.ExecutionID, in.ProjectID, in.TaskID))
	if err != nil {
		return v, run.Run{}, true, err
	}
	if v.TaskDefinitionRevisionID != effect.RevisionID || v.RuntimeConfig != c.Runtime || v.BackendSelection == nil || !reflect.DeepEqual(*v.BackendSelection, c.Backend) || !matchesExecutionLayout(in.Expected, v) {
		return v, run.Run{}, true, executionproposal.ErrConflict
	}
	if v.ImplementationRunID == "" {
		if v.Status != executionproposal.StatusAdmitted {
			return v, run.Run{}, true, executionproposal.ErrConflict
		}
		return v, run.Run{}, true, nil
	}
	r, err := scanRun(q.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.project_id=? AND r.task_id=? AND r.execution_proposal_id=? AND r.purpose='IMPLEMENTATION' AND EXISTS(SELECT 1 FROM runtime_preparation_attempts a WHERE a.run_id=r.id AND a.plan_id=?)`, v.ImplementationRunID, in.ProjectID, in.TaskID, v.ID, planID))
	return v, r, true, err
}

func (s *WorkflowProgressionStore) ReadOperatorSelectedTaskExecution(ctx context.Context, in wp.OperatorExecutionStartRequest) (executionproposal.Proposal, run.Run, bool, error) {
	actor, err := operatorSelectedStartActor(ctx, in)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, false, err
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, false, err
	}
	defer tx.Rollback()
	return readOperatorStartOutcome(ctx, tx, in, actor)
}

func (s *WorkflowProgressionStore) ReserveOperatorSelectedTaskExecution(ctx context.Context, in wp.OperatorExecutionStartRequest, backend codexconfig.BackendSelection) (executionproposal.Proposal, run.Run, error) {
	actor, err := operatorSelectedStartActor(ctx, in)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	// Historical completion is readable before the writer fence or current
	// runtime inventory. The original full request must still match exactly.
	v, r, found, err := s.ReadOperatorSelectedTaskExecution(ctx, in)
	if err != nil {
		return v, r, err
	}
	if found {
		if r.ID != "" {
			return v, r, nil
		}
		return continueAdmittedExecution(ctx, s.db, v.ID, s.now())
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return v, r, err
	}
	defer tx.Rollback()
	if v, r, found, err = readOperatorStartOutcome(ctx, tx, in, actor); err != nil || found {
		if err != nil || r.ID != "" {
			return v, r, err
		}
		if err = tx.Rollback(); err != nil {
			return v, r, err
		}
		return continueAdmittedExecution(ctx, s.db, v.ID, s.now())
	}
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, in.TaskID, in.ProjectID))
	if err != nil {
		return v, r, err
	}
	e := in.Expected
	if t.Status != task.StatusActive || t.Phase != task.PhaseImplementationReady || t.FeatureAreaID != e.FeatureAreaID || t.CurrentDefinitionRevisionID != e.TaskDefinitionRevisionID || t.CurrentExecutionID != e.ExecutionID || len(t.RepositoryTargets) == 0 || t.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return v, r, executionproposal.ErrConflict
	}
	authority, err := readRuntimeAuthority(ctx, tx, t.FeatureAreaID)
	if err != nil {
		return v, r, err
	}
	if authority.Busy {
		return v, r, wp.Reject("FEATURE_AREA_BUSY", wp.Guidance{})
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, t.FeatureAreaID); err != nil {
		return v, r, err
	}
	if t.CurrentExecutionID != "" {
		eligible, err := noRunRevisionApprovedTx(ctx, tx, t.ID)
		if err != nil {
			return v, r, err
		}
		if !eligible {
			return v, r, executionproposal.ErrConflict
		}
	}
	revision, err := getDefinitionRevision(ctx, tx, t.CurrentDefinitionRevisionID)
	if err != nil {
		return v, r, err
	}
	if revision.Definition.Version != 2 {
		return v, r, executionproposal.ErrConflict
	}
	frozen, err := in.RuntimeConfig.Normalize(s.runtimeAllowlist())
	if err != nil {
		return v, r, err
	}
	config, err := scanCodexConfig(tx.QueryRowContext(ctx, selectCodexConfig+` WHERE id=?`, in.BackendSelection.ConfigID))
	if err != nil {
		return v, r, err
	}
	if !config.Enabled || config.Fingerprint() != in.BackendSelection.ConfigFingerprint || backend.ConfigID != in.BackendSelection.ConfigID || backend.ConfigFingerprint != in.BackendSelection.ConfigFingerprint {
		return v, r, executionproposal.ErrConflict
	}
	if err = validateFeatureAreaBackendContinuityTx(ctx, tx, t.FeatureAreaID, &backend); err != nil {
		return v, r, err
	}
	owner, err := operatorStartDefinitionAuthorTx(ctx, tx, t)
	if err != nil {
		return v, r, err
	}
	raw, _ := json.Marshal(in)
	runtimeJSON, _ := json.Marshal(frozen)
	backendJSON, err := encodeBackendSelection(&backend)
	if err != nil {
		return v, r, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO operator_execution_start_commands(project_id,actor_id,request_id,task_id,feature_area_id,revision_id,revision_hash,approval_id,expected_execution_id,expected_phase,primary_member_id,workset_hash,request_json,payload_sha256,runtime_config_json,backend_selection_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.ProjectID, actor, in.RequestID, in.TaskID, e.FeatureAreaID, e.TaskDefinitionRevisionID, revision.PayloadSHA256, t.ReviewApprovalID, e.ExecutionID, e.Phase, e.PrimaryMemberID, e.ApprovedWorksetHash, string(raw), in.Hash(), string(runtimeJSON), backendJSON, formatTime(s.now()))
	if err != nil {
		return v, r, conflict(err)
	}
	v, r, err = s.reserveTaskExecutionExpectedTx(ctx, tx, t, owner, &e, executionStartActor{Kind: "LOCAL_OPERATOR", ID: actor, RequestID: in.RequestID, Action: "START_EXECUTION", SelectionHash: in.Hash()})
	if err != nil {
		return v, r, err
	}
	if v.Status != executionproposal.StatusAdmitted || r.ID != "" {
		return v, r, executionproposal.ErrConflict
	}
	if _, _, _, err = readOperatorStartOutcome(ctx, tx, in, actor); err != nil {
		return v, r, err
	}
	if err = tx.Commit(); err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	return continueAdmittedExecution(ctx, s.db, v.ID, s.now())
}

// Definition provenance is independent of the operator authorizing this start.
// A local approval legitimately has no AI Session. Preserve that null instead
// of borrowing the Review creator; a missing or mixed durable actor chain fails.
func operatorStartDefinitionAuthorTx(ctx context.Context, q definitionQuerier, t task.Task) (string, error) {
	var session, kind, actor string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(m.ai_session_id,''),m.actor_kind,m.actor_id
 FROM review_approval_materializations m
 JOIN review_approval_receipt_context c ON c.decision_id=m.decision_id AND c.actor_kind=m.actor_kind AND c.actor_id=m.actor_id AND c.ai_session_id IS m.ai_session_id
 JOIN decision_resolution_receipts r ON r.decision_id=m.decision_id AND r.actor_kind=m.actor_kind AND r.actor_id=m.actor_id
 WHERE m.decision_id=? AND m.task_id=? AND m.initial_revision_id=?
 AND r.project_id=? AND r.decision_kind='REVIEW_APPROVAL' AND r.subject_kind='TASK_REVIEW_GATE' AND r.subject_id=m.task_id AND r.option='APPROVE' AND r.result_kind=? AND r.result_id=m.task_id`, t.ReviewApprovalID, t.ID, t.CurrentDefinitionRevisionID, t.ProjectID, decisionresolution.ResultTask).Scan(&session, &kind, &actor)
	if err != nil {
		return "", executionProposalConflict(err)
	}
	if actor == "" || kind == "AI_SESSION" && (session == "" || session != actor) || kind == "LOCAL_OPERATOR" && session != "" || kind != "AI_SESSION" && kind != "LOCAL_OPERATOR" {
		return "", executionproposal.ErrConflict
	}
	return session, nil
}

// A local start has its own authenticated producer, separate from AI intents.
// Only the private transaction that wrote this exact selection carries its
// hash. The older raw operator Reserve API cannot turn inherited metadata or
// another request's selection into fresh runtime authority.
func operatorDefinitionOnlyStartRuntimeTx(ctx context.Context, tx *sql.Tx, t task.Task, actor *executionStartActor, expected *wp.TaskExecutionExpectation) (sql.NullString, sql.NullString, error) {
	var empty sql.NullString
	a, ok := localoperator.FromContext(ctx)
	if !ok || actor == nil || expected == nil || actor.Kind != "LOCAL_OPERATOR" || actor.ID != a.PrincipalID() || actor.Action != "START_EXECUTION" || actor.SelectionHash == "" || a.Check(t.ProjectID, t.ID, actor.Action) != nil {
		return empty, empty, executionproposal.ErrStartAuthorizationUnavailable
	}
	c, found, err := readOperatorStartCommand(ctx, tx, t.ProjectID, actor.ID, actor.RequestID)
	if err != nil {
		return empty, empty, err
	}
	if !found || c.Request.Hash() != actor.SelectionHash || c.Request.TaskID != t.ID || c.Request.Expected != *expected || c.ApprovalID != t.ReviewApprovalID || expected.ExecutionID != t.CurrentExecutionID || expected.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID {
		return empty, empty, executionproposal.ErrStartAuthorizationUnavailable
	}
	runtimeJSON, _ := json.Marshal(c.Runtime)
	backendJSON, _ := json.Marshal(c.Backend)
	return sql.NullString{String: string(runtimeJSON), Valid: true}, sql.NullString{String: string(backendJSON), Valid: true}, nil
}
