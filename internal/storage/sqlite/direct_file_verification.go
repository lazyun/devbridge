package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

// File results are immutable implementation evidence. The separate Execution
// owns only Codex verification: it never invents an IMPLEMENTATION Run.
func createFileVerificationTx(ctx context.Context, tx *sql.Tx, result *directfile.ImplementationResult, now time.Time) error {
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, result.TaskID))
	if err != nil {
		return err
	}
	if t.VerificationSpec.Mode != task.VerificationModeRun {
		return nil
	}
	if t.CurrentExecutionID != "" || t.CurrentDefinitionRevisionID != result.TaskDefinitionRevisionID || t.WorkSpec.ImplementationMode != task.ImplementationDirectFile {
		return fileResultConflict()
	}
	if err = task.ValidateExecutableVerificationSpec(t.VerificationSpec); err != nil {
		return err
	}
	work, err := encodeWorkSpec(t.WorkSpec)
	if err != nil {
		return err
	}
	verification, err := encodeVerificationSpec(t.VerificationSpec)
	if err != nil {
		return err
	}
	candidate := executionproposal.Proposal{ID: "execution_file_verify_" + workflowHash(result.ID)[:32], AISessionID: result.AISessionID, ProjectID: t.ProjectID, TaskID: t.ID, TaskPlanID: "plan_file_verify_" + workflowHash(result.ID)[:32], TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, WorkSpec: t.WorkSpec, VerificationSpec: t.VerificationSpec}
	var runtime, backend sql.NullString
	query := `SELECT runtime_config_json,backend_selection_json FROM direct_file_verification_selections WHERE task_id=? AND revision_id=?`
	args := []any{t.ID, t.CurrentDefinitionRevisionID}
	if len(t.RepositoryTargets) == 0 {
		query = `SELECT runtime_config_json,backend_selection_json FROM review_approval_materializations WHERE task_id=? AND initial_revision_id=? AND decision_id=?`
		args = append(args, t.ReviewApprovalID)
	}
	if err = tx.QueryRowContext(ctx, query, args...).Scan(&runtime, &backend); err != nil {
		return err
	}
	if !runtime.Valid || !backend.Valid || json.Unmarshal([]byte(runtime.String), &candidate.RuntimeConfig) != nil {
		return directfile.Failure("CODEX_VERIFICATION_SELECTION_REQUIRED", "Approve an explicit Codex verification backend and model before file work")
	}
	candidate.BackendSelection, err = decodeBackendSelection(backend)
	if err != nil || candidate.BackendSelection == nil {
		return directfile.Failure("CODEX_VERIFICATION_SELECTION_REQUIRED", "Codex verification backend is missing")
	}
	if len(t.RepositoryTargets) > 0 {
		candidate.RuntimeAdapterIdentity = executionproposal.RuntimeAdapterCodexAppServerV1
		candidate.RuntimeProtocolIdentity = executionproposal.RuntimeProtocolCodex0149
	}
	if err = bindExecutionDefinitionKindTx(ctx, tx, &candidate, true); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_plans(id,ai_session_id,content_actor_kind,content_actor_id,project_id,task_id,title,work_spec_json,verification_spec_json,status,created_at,updated_at,confirmed_at,applied_at) VALUES(?,?,'AI_SESSION',?,?,?,?,?,?,'APPLIED',?,?,?,?)`, candidate.TaskPlanID, result.AISessionID, result.AISessionID, t.ProjectID, t.ID, "Codex verification of file result", work, verification, formatTime(now), formatTime(now), formatTime(now), formatTime(now)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET confirmed_task_plan_id=? WHERE id=?`, candidate.TaskPlanID, t.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO direct_file_verifications(result_id,execution_id,task_id,revision_id,created_at) VALUES(?,?,?,?,?)`, result.ID, candidate.ID, t.ID, t.CurrentDefinitionRevisionID, formatTime(now)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO execution_proposals(id,ai_session_id,content_actor_kind,content_actor_id,project_id,task_id,task_plan_id,status,created_at,updated_at,confirmed_at,work_spec_json,verification_spec_json,outcome,task_definition_revision_id,runtime_config_json,backend_selection_json,primary_member_id,approved_workset_hash,runtime_adapter_identity,runtime_protocol_identity) VALUES(?,?,'AI_SESSION',?,?,?,?,'CONFIRMED',?,?,?,?,?,'PENDING',?,?,?,?,?,?,?)`, candidate.ID, result.AISessionID, result.AISessionID, t.ProjectID, t.ID, candidate.TaskPlanID, formatTime(now), formatTime(now), formatTime(now), work, verification, candidate.TaskDefinitionRevisionID, runtime.String, backend.String, nullString(candidate.PrimaryMemberID), nullString(candidate.ApprovedWorksetHash), nullString(candidate.RuntimeAdapterIdentity), nullString(candidate.RuntimeProtocolIdentity)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET current_execution_id=? WHERE id=? AND current_execution_id IS NULL`, candidate.ID, t.ID); err != nil {
		return err
	}
	result.ExecutionID = candidate.ID
	result.VerificationStatus = "READY"
	return nil
}

func projectFileVerification(ctx context.Context, q definitionQuerier, result *directfile.ImplementationResult) error {
	var id, runID, outcome string
	err := q.QueryRowContext(ctx, `SELECT e.id,COALESCE(e.verification_run_id,''),e.outcome FROM direct_file_verifications v JOIN execution_proposals e ON e.id=v.execution_id WHERE v.result_id=? AND v.task_id=? AND v.revision_id=?`, result.ID, result.TaskID, result.TaskDefinitionRevisionID).Scan(&id, &runID, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	result.ExecutionID, result.VerificationRunID = id, runID
	result.VerificationStatus = outcome
	if outcome == "PENDING" {
		result.VerificationStatus = "READY"
	}
	// SUCCEEDED is transport completion, never a claim that all checks passed.
	return nil
}

func requireDirectFileVerificationSourceTx(ctx context.Context, tx *sql.Tx, p executionproposal.Proposal) error {
	var body, workflow string
	err := tx.QueryRowContext(ctx, `SELECT r.result_json,r.workflow_id FROM direct_file_verifications v JOIN direct_file_implementation_results r ON r.id=v.result_id JOIN direct_file_task_authorizations g ON g.workflow_id=r.workflow_id JOIN tasks t ON t.id=v.task_id JOIN feature_areas a ON a.id=t.feature_area_id AND a.project_id=t.project_id AND a.status='ACTIVE' WHERE v.execution_id=? AND v.task_id=? AND v.revision_id=? AND t.current_execution_id=v.execution_id AND t.current_definition_revision_id=v.revision_id AND t.phase='VERIFICATION_READY' AND t.status='ACTIVE' AND g.state='CLOSED' AND g.task_definition_revision_id=v.revision_id AND g.decision_id=t.review_approval_id`, p.ID, p.TaskID, p.TaskDefinitionRevisionID).Scan(&body, &workflow)
	if err != nil {
		return executionproposal.ErrConflict
	}
	var result directfile.ImplementationResult
	if json.Unmarshal([]byte(body), &result) != nil || p.WorkSpec.ImplementationMode != task.ImplementationDirectFile || p.BackendSelection == nil {
		return executionproposal.ErrConflict
	}
	grant, err := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, workflow)
	if err != nil {
		return err
	}
	if grant.ProtocolVersion == 2 {
		members, e := multiResultMembers(ctx, tx, grant)
		if e != nil {
			return e
		}
		for i := range members {
			members[i].WorktreePath = ""
			members[i].RootIdentity = ""
		}
		if !reflect.DeepEqual(members, result.Members) {
			return fileResultConflict()
		}
	} else {
		_, _, path, _, e := taskFileWorkspace(ctx, tx, grant)
		if e != nil {
			return e
		}
		snapshot, e := git.NewClient(git.Config{}).Snapshot(ctx, path)
		if e != nil {
			return e
		}
		if snapshot != result.Snapshot {
			return fileResultConflict()
		}
	}
	return nil
}

func requireFileVerificationEvidenceTx(ctx context.Context, tx *sql.Tx, t task.Task, result directfile.ImplementationResult) error {
	if t.VerificationSpec.Mode == task.VerificationModeNone {
		if t.CurrentExecutionID != "" {
			return fileResultConflict()
		}
		return nil
	}
	p, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=? AND task_id=? AND task_definition_revision_id=?`, t.CurrentExecutionID, t.ID, t.CurrentDefinitionRevisionID))
	if err != nil {
		return fileResultConflict()
	}
	var exact bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_verifications WHERE result_id=? AND execution_id=? AND task_id=? AND revision_id=?)`, result.ID, p.ID, t.ID, t.CurrentDefinitionRevisionID).Scan(&exact); err != nil || !exact {
		return fileResultConflict()
	}
	r, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.execution_proposal_id=? AND r.purpose='VERIFICATION'`, p.VerificationRunID, p.ID))
	if err != nil || p.ImplementationRunID != "" || p.Outcome != executionproposal.OutcomeSucceeded || r.Status != run.StatusSucceeded || (r.Failure != nil && r.Failure.Code == "VERIFICATION_MUTATED_WORKTREE") || r.VerificationReport == nil {
		return directfile.Failure("VERIFICATION_NOT_RUN", "Codex verification has not completed with required observed evidence")
	}
	var frozen *workset.RuntimeWorkset
	if p.ApprovedWorksetHash != "" {
		var raw string
		var value workset.RuntimeWorkset
		if err = tx.QueryRowContext(ctx, `SELECT runtime_workset_json FROM runs WHERE id=?`, r.ID).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &value) != nil || value.Validate() != nil || value.RunID != r.ID || value.ExecutionID != p.ID || value.TaskDefinitionRevisionID != p.TaskDefinitionRevisionID || value.PrimaryMemberID != p.PrimaryMemberID || value.RuntimeAdapterIdentity != p.RuntimeAdapterIdentity || value.RuntimeProtocolIdentity != p.RuntimeProtocolIdentity {
			return fileResultConflict()
		}
		frozen = &value
		for _, m := range result.Members {
			var continuous bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_repository_bindings WHERE run_id=? AND repository_id=? AND member_id=? AND member_version=? AND worktree_id=? AND start_head=? AND end_head=start_head AND start_snapshot=? AND end_snapshot=start_snapshot AND observation_state='NO_CHANGE')`, r.ID, m.RepositoryID, m.MemberID, m.MemberVersion, m.WorktreeID, m.StartHead, m.EndSnapshot).Scan(&continuous); err != nil || !continuous {
				return fileResultConflict()
			}
		}
	}
	if p.ApprovedWorksetHash == "" {
		grant, e := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, result.WorkflowID)
		if e != nil || result.Snapshot == "" || r.StartSnapshot != result.Snapshot || r.EndSnapshot != result.Snapshot || r.WorkspaceID != grant.WorkspaceID || r.WorktreeID != grant.WorktreeID {
			return fileResultConflict()
		}
	}
	if !run.VerificationEvidenceComplete(*r.VerificationReport, p.VerificationSpec, frozen) {
		return directfile.Failure("VERIFICATION_EVIDENCE_INCOMPLETE", "Codex must execute every required check; file inspection or agent claims cannot replace observed command results")
	}
	return nil
}

// Selection is captured from the same consumed user confirmation that starts
// file implementation. No Review model, default or later GPT input substitutes.
func freezeFileVerificationSelectionTx(ctx context.Context, tx *sql.Tx, in directfile.StartInput, revision task.DefinitionRevision, now time.Time) error {
	if revision.Definition.VerificationSpec.Mode != task.VerificationModeRun {
		return nil
	}
	if err := task.ValidateExecutableVerificationSpec(revision.Definition.VerificationSpec); err != nil {
		return err
	}
	if len(revision.Definition.RepositoryTargets) > 1 && revision.Definition.PrimaryMemberID == "" {
		return directfile.Failure("RUNTIME_WORKSET_INCOMPLETE", "Codex verification of multiple repositories requires an approved primaryMemberId")
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_verification_selections WHERE task_id=? AND revision_id=?)`, in.TaskID, in.TaskDefinitionRevisionID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	} // recovery retains the original immutable selection
	intent, err := confirmationRead(ctx, tx, in.AISessionID, in.ConfirmationIntentID)
	if err != nil {
		return err
	}
	if intent.Commit == nil || intent.Commit.Selection.RuntimeConfig == nil || intent.Commit.BackendSelection == nil {
		return directfile.Failure("CODEX_VERIFICATION_SELECTION_REQUIRED", "Select the Codex backend and model for verification")
	}
	runtime, err := encodeExecutionRuntimeConfig(*intent.Commit.Selection.RuntimeConfig)
	if err != nil {
		return err
	}
	backend, err := encodeBackendSelection(intent.Commit.BackendSelection)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO direct_file_verification_selections(task_id,revision_id,ai_session_id,intent_id,runtime_config_json,backend_selection_json,created_at) VALUES(?,?,?,?,?,?,?)`, in.TaskID, in.TaskDefinitionRevisionID, in.AISessionID, in.ConfirmationIntentID, runtime, backend, formatTime(now))
	return err
}

func recordFileVerificationAcceptanceTx(ctx context.Context, tx *sql.Tx, t task.Task, in directfile.ImplementationAcceptInput, now time.Time) error {
	if t.CurrentExecutionID == "" {
		return nil
	}
	_, err := insertDecisionResolutionTx(ctx, tx, decisionresolution.Request{DecisionID: decisionresolution.TaskAcceptanceDecisionID(t.CurrentExecutionID), Kind: decisionresolution.KindTaskAcceptance, SubjectKind: decisionresolution.SubjectExecution, SubjectID: t.CurrentExecutionID, Option: "ACCEPT", RequestID: in.RequestID, ActorKind: "AI_SESSION", ActorID: in.AISessionID}, t.ProjectID, decisionresolution.ResultTask, t.ID, now)
	return err
}

func (s *WorkflowProgressionStore) acceptCodexVerifiedFileResult(ctx context.Context, in wp.TaskAcceptanceInput) (out wp.TaskAcceptanceResolution, handled bool, err error) {
	var result directfile.ImplementationResult
	var body, area string
	err = s.db.sql.QueryRowContext(ctx, `SELECT r.result_json,t.feature_area_id FROM direct_file_verifications v JOIN direct_file_implementation_results r ON r.id=v.result_id JOIN tasks t ON t.id=v.task_id WHERE v.execution_id=?`, in.ExecutionID).Scan(&body, &area)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, true, err
	}
	if err = json.Unmarshal([]byte(body), &result); err != nil {
		return out, true, err
	}
	if in.AISessionID != result.AISessionID || in.OperatorPrincipalID != "" || in.DecisionID != wp.TaskAcceptanceDecisionID(in.ExecutionID) || in.TaskID != "" && in.TaskID != result.TaskID || in.FeatureAreaID != "" && in.FeatureAreaID != area || in.TaskDefinitionRevisionID != "" && in.TaskDefinitionRevisionID != result.TaskDefinitionRevisionID {
		return out, true, executionproposal.ErrConflict
	}
	accepted, e := s.AcceptFileImplementationResult(ctx, directfile.ImplementationAcceptInput{AISessionID: in.AISessionID, TaskID: result.TaskID, TaskDefinitionRevisionID: result.TaskDefinitionRevisionID, WorkflowID: result.WorkflowID, ResultID: result.ID, RequestID: in.RequestID, ProtocolVersion: result.ProtocolVersion, RuntimeEpoch: result.RuntimeEpoch, GrantGeneration: result.GrantGeneration, CompletionPlan: in.CompletionPlan})
	if e != nil {
		return out, true, e
	}
	out.Task, err = scanTask(s.db.sql.QueryRowContext(ctx, selectTask+` WHERE id=?`, result.TaskID))
	if err != nil {
		return out, true, err
	}
	// The canonical reader retains exact actor/request identity through restart.
	receipts, e := NewDecisionResolutionStore(s.db).ListTaskAcceptance(ctx, result.TaskID)
	if e != nil {
		return out, true, e
	}
	for _, receipt := range receipts {
		if receipt.DecisionID == in.DecisionID && receipt.RequestID == in.RequestID {
			out.Receipt = receipt
		}
	}
	if out.Receipt.DecisionID == "" {
		return out, true, executionproposal.ErrConflict
	}
	out.Replay = accepted.Replay
	out.CommitError = accepted.CommitError
	out.StartEffect = wp.StartEffectNoStart
	return out, true, nil
}
