package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

// ResolveTaskAcceptance is the sole reducer for an assessment of one terminal
// Execution. The receipt is per Execution; only ACCEPT/REJECT terminalize Task.
func (s *WorkflowProgressionStore) ResolveTaskAcceptance(ctx context.Context, in workflowprogression.TaskAcceptanceInput) (out workflowprogression.TaskAcceptanceResolution, err error) {
	defer func() {
		if err == nil && in.Option == workflowprogression.AcceptanceAccept && out.Task.ID != "" {
			committed, commitErr := s.RetryTaskCommit(context.WithoutCancel(ctx), out.Task.ProjectID, out.Task.ID)
			if commitErr != nil {
				out.CommitError = commitErr.Error()
				if in.CompletionPlan != nil {
					_, _ = s.db.sql.ExecContext(context.WithoutCancel(ctx), `UPDATE task_completion_intents SET last_error=? WHERE task_id=? AND completed_at IS NULL`, out.CommitError, out.Task.ID)
				}
			} else {
				out.Task = committed
				if in.CompletionPlan != nil {
					merged, mergeErr := s.FinishTaskCompletion(context.WithoutCancel(ctx), committed.ProjectID, committed.ID)
					if mergeErr != nil {
						out.CommitError = mergeErr.Error()
					} else {
						out.Task = merged
					}
				}
			}
		}
		if err == nil && !out.Replay && out.StartEffect == workflowprogression.StartEffectStartAuthorized && out.SuccessorExecutionID != "" {
			s.wakeApproval()
		}
	}()
	if s == nil || s.db == nil {
		return out, errors.New("workflow progression store unavailable")
	}
	in.AISessionID, in.OperatorPrincipalID, in.DecisionID, in.ExecutionID, in.Option, in.RequestID = strings.TrimSpace(in.AISessionID), strings.TrimSpace(in.OperatorPrincipalID), strings.TrimSpace(in.DecisionID), strings.TrimSpace(in.ExecutionID), strings.TrimSpace(in.Option), strings.TrimSpace(in.RequestID)
	if in.StartEffect == "" {
		in.StartEffect = workflowprogression.StartEffectNoStart
	}
	if !in.StartEffect.Valid() {
		return out, decisionresolution.ErrInvalidArgument
	}
	if in.StartEffect == workflowprogression.StartEffectStartAuthorized && in.Option != workflowprogression.AcceptanceRequestChanges && in.Option != workflowprogression.AcceptanceRequestReverification && in.Option != workflowprogression.AcceptanceRetryImplementation {
		return out, decisionresolution.ErrInvalidArgument
	}
	out.StartEffect = in.StartEffect
	if (in.AISessionID == "") == (in.OperatorPrincipalID == "") {
		return out, decisionresolution.ErrInvalidArgument
	}
	if in.DecisionID != decisionresolution.TaskAcceptanceDecisionID(in.ExecutionID) {
		return out, decisionresolution.ErrInvalidArgument
	}
	request := decisionresolution.Request{DecisionID: in.DecisionID, Kind: decisionresolution.KindTaskAcceptance, SubjectKind: decisionresolution.SubjectExecution, SubjectID: in.ExecutionID, Option: in.Option, RequestID: in.RequestID}
	if in.OperatorPrincipalID != "" {
		request.ActorKind, request.ActorID = "LOCAL_OPERATOR", in.OperatorPrincipalID
	} else {
		request.ActorKind, request.ActorID = "AI_SESSION", in.AISessionID
	}
	if err = decisionresolution.ValidateRequest(request); err != nil {
		return out, err
	}
	if err = validateAcceptancePayload(in); err != nil {
		return out, err
	}
	if in.CompletionPlan != nil {
		if in.Option != workflowprogression.AcceptanceAccept || !in.CompletionPlan.Valid() || in.CompletionPlan.TaskID != in.TaskID || in.CompletionPlan.ExecutionID != in.ExecutionID || in.CompletionPlan.RevisionID != in.TaskDefinitionRevisionID {
			return out, executionproposal.ErrConflict
		}
		// Exact accepted replay is checked under the transaction below. Only a
		// new intent probes the current source and target before admission.
		var exists bool
		if err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_completion_intents WHERE task_id=?)`, in.TaskID).Scan(&exists); err != nil {
			return out, err
		}
		if !exists {
			fresh, e := s.PrepareTaskCompletionPlan(ctx, in.CompletionPlan.ProjectID, in.TaskID, in.ExecutionID, in.TaskDefinitionRevisionID)
			if e != nil {
				return out, e
			}
			if fresh.SHA256 != in.CompletionPlan.SHA256 {
				return out, executionproposal.ErrConflict
			}
		}
	}
	if in.Option == workflowprogression.AcceptanceAccept {
		if value, handled, e := s.acceptCodexVerifiedFileResult(ctx, in); handled {
			return value, e
		}
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if in.Option == workflowprogression.AcceptanceRetryImplementation && in.OperatorPrincipalID == "" {
		if err = instructionCaller(ctx, tx, in.AISessionID); err != nil {
			return out, err
		}
	}
	var sessionProject string
	if in.OperatorPrincipalID != "" {
		actor, ok := localoperator.FromContext(ctx)
		if !ok || actor.PrincipalID() != in.OperatorPrincipalID || actor.ProjectID() == "" {
			return out, decisionresolution.ErrNotFound
		}
		sessionProject = actor.ProjectID()
	} else {
		if err = tx.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, in.AISessionID).Scan(&sessionProject); errors.Is(err, sql.ErrNoRows) {
			return out, decisionresolution.ErrNotFound
		}
		if err != nil {
			return out, err
		}
	}
	// Check the immutable observed target before receipt replay. Currentness is
	// checked below for a new mutation; replay must remain reachable after a successor.
	if in.TaskID != "" || in.FeatureAreaID != "" || in.TaskDefinitionRevisionID != "" {
		var owner, area, revision, project string
		err = tx.QueryRowContext(ctx, `SELECT e.task_id,t.feature_area_id,e.task_definition_revision_id,e.project_id FROM execution_proposals e JOIN tasks t ON t.id=e.task_id WHERE e.id=?`, in.ExecutionID).Scan(&owner, &area, &revision, &project)
		if err != nil || project != sessionProject || in.TaskID != "" && in.TaskID != owner || in.FeatureAreaID != "" && in.FeatureAreaID != area || in.TaskDefinitionRevisionID != "" && in.TaskDefinitionRevisionID != revision {
			return out, executionproposal.ErrConflict
		}
	}
	receipt, handled, err := preflightDecisionResolutionTx(ctx, tx, request, sessionProject)
	if err != nil {
		return out, err
	}
	if handled {
		if in.Option == workflowprogression.AcceptanceAccept {
			if e := validateCompletionReplay(ctx, tx, receipt.ResultID, in.RequestID, in.CompletionPlan); e != nil {
				return out, e
			}
		}
		if receipt.RequestID != request.RequestID || receipt.DecisionID != request.DecisionID || receipt.Kind != request.Kind || receipt.SubjectKind != request.SubjectKind || receipt.SubjectID != request.SubjectID || receipt.Option != request.Option {
			return out, executionproposal.ErrConflict
		}
		replayed, replayErr := s.replayTaskAcceptance(ctx, tx, in, receipt)
		if replayErr != nil || replayed.SuccessorExecutionID == "" {
			return replayed, replayErr
		}
		phase := task.PhaseImplementationReady
		if in.Option == workflowprogression.AcceptanceRequestReverification {
			phase = task.PhaseVerificationReady
		}
		var replayRevision string
		if replayErr = tx.QueryRowContext(ctx, `SELECT task_definition_revision_id FROM execution_proposals WHERE id=? AND task_id=? AND project_id=?`, replayed.SuccessorExecutionID, replayed.Task.ID, receipt.ProjectID).Scan(&replayRevision); replayErr != nil {
			return replayed, replayErr
		}
		replayErr = checkDecisionStartEffectReplayTx(ctx, tx, executionStartEffectRecord{ProjectID: receipt.ProjectID, ActorKind: request.ActorKind, ActorID: request.ActorID, RequestID: request.RequestID, Action: request.Option, Effect: in.StartEffect, TaskID: replayed.Task.ID, FeatureAreaID: replayed.Task.FeatureAreaID, RevisionID: replayRevision, ExecutionID: replayed.SuccessorExecutionID, Phase: phase, DecisionID: request.DecisionID})
		return replayed, replayErr
	}
	// REQUEST_CHANGES here authorizes a Coding Execution successor. A Direct
	// File implementation cannot be smuggled into that automatic runtime path.
	// Prior receipts were handled above without reinterpreting their old specs.
	if in.Option == workflowprogression.AcceptanceRequestChanges && in.NextWorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return out, decisionresolution.ErrInvalidArgument
	}
	predecessor, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, in.ExecutionID))
	if errors.Is(err, sql.ErrNoRows) {
		return out, executionproposal.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if predecessor.ProjectID != sessionProject {
		return out, decisionresolution.ErrNotFound
	}
	if (in.Option == workflowprogression.AcceptanceRequestChanges || in.Option == workflowprogression.AcceptanceRetryImplementation || in.Option == workflowprogression.AcceptanceRequestReverification) && predecessor.BackendSelection == nil {
		return out, executionproposal.ErrConflict
	}
	if (in.Option == workflowprogression.AcceptanceRequestChanges || in.Option == workflowprogression.AcceptanceRetryImplementation) && in.BackendSelection != nil && !sameBackendSelection(in.BackendSelection, predecessor.BackendSelection) {
		return out, executionproposal.ErrConflict
	}
	current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, predecessor.TaskID))
	if err != nil {
		return out, err
	}
	if err = taskActionAdmissionTx(ctx, tx, current.ID); err != nil {
		return out, err
	}
	if err = validateAcceptanceEligibilityTx(ctx, tx, current, predecessor, in.Option == workflowprogression.AcceptanceRetryImplementation && in.RecoveryStatusID != ""); err != nil {
		return out, err
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, current.FeatureAreaID); err != nil {
		return out, err
	}
	if in.Option == workflowprogression.AcceptanceRequestChanges {
		frozen, configErr := s.freezeRuntimeConfig(current.Model, in.NextRunRuntimeConfig)
		if configErr != nil {
			return out, decisionresolution.ErrInvalidArgument
		}
		in.NextRunRuntimeConfig = &frozen
	}
	if in.Option == workflowprogression.AcceptanceRequestReverification {
		if in.NextRunRuntimeConfig != nil {
			return out, decisionresolution.ErrInvalidArgument
		}
		config := predecessor.RuntimeConfig
		if err = config.ValidateFrozen(s.runtimeAllowlist()); err != nil {
			return out, err
		}
		in.NextRunRuntimeConfig = &config
	}
	// Preserve the original retry payload before resolving inherited runtime configuration.
	retryHash, hashErr := acceptanceContinuationHash(in)
	if hashErr != nil {
		return out, hashErr
	}
	if in.Option == workflowprogression.AcceptanceRetryImplementation {
		if err = validateImplementationRetryTx(ctx, tx, current, predecessor, in.RecoveryStatusID != ""); err != nil {
			return out, err
		}
		config := predecessor.RuntimeConfig
		if in.NextRunRuntimeConfig != nil {
			config = *in.NextRunRuntimeConfig
		}
		if config.Model == "" || config.ReasoningEffort == "" {
			return out, decisionresolution.ErrInvalidArgument
		}
		config, err = config.Normalize(s.runtimeAllowlist())
		if err != nil {
			return out, err
		}
		in.NextRunRuntimeConfig = &config
	}
	now := s.now()
	switch in.Option {
	case workflowprogression.AcceptanceAccept:
		if in.CompletionPlan != nil {
			if err = requireCompletionVerificationTx(ctx, tx, predecessor); err != nil {
				return out, err
			}
			if err = reserveTaskCompletionTx(ctx, tx, *in.CompletionPlan, in.RequestID); err != nil {
				return out, err
			}
		}
		if err = requireAcceptableVerificationTx(ctx, tx, predecessor); err != nil {
			return out, err
		}
		receipt, err = insertDecisionResolutionTx(ctx, tx, request, current.ProjectID, decisionresolution.ResultTask, current.ID, now)
		if err == nil {
			err = admitCodingTaskDeliveryTx(ctx, tx, current, predecessor, now)
			if err == nil && predecessor.ApprovedWorksetHash != "" {
				err = insertCodingTaskDeliveryTx(ctx, tx, current, predecessor, now)
			} else if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO task_commit_receipts(task_id,feature_area_id,revision_id,verified_snapshot,workspace_id,workspace_path,branch)
                SELECT t.id,t.feature_area_id,?,COALESCE(r.end_snapshot,''),COALESCE(r.workspace_id,''),COALESCE(wt.path,''),COALESCE(wt.branch,'')
                FROM tasks t JOIN feature_areas a ON a.id=t.feature_area_id
                LEFT JOIN runs r ON r.id=?
                LEFT JOIN workspace_members m ON m.workspace_id=r.workspace_id AND m.repository_id=t.repository_id LEFT JOIN worktrees wt ON wt.id=m.worktree_id
                WHERE t.id=?`, predecessor.TaskDefinitionRevisionID, terminalExecutionRunID(predecessor), current.ID)
			}
		}
	case workflowprogression.AcceptanceReject:
		receipt, err = insertDecisionResolutionTx(ctx, tx, request, current.ProjectID, decisionresolution.ResultTask, current.ID, now)
		if err == nil {
			err = transitionAcceptedTaskTx(ctx, tx, current, predecessor, task.StatusCanceled, now)
		}
	case workflowprogression.AcceptanceRetryImplementation:
		out.SuccessorExecutionID, receipt, err = continueTaskImplementationTx(ctx, tx, current, predecessor, in, request, retryHash, now)
	case workflowprogression.AcceptanceRequestChanges:
		out.SuccessorExecutionID, receipt, err = continueTaskWithChangesTx(ctx, tx, current, predecessor, in, request, now)
	case workflowprogression.AcceptanceRequestReverification:
		out.SuccessorExecutionID, receipt, err = continueTaskForReverificationTx(ctx, tx, current, predecessor, in, request, now)
	default:
		err = decisionresolution.ErrInvalidArgument
	}
	if err != nil {
		return out, err
	}
	if out.SuccessorExecutionID != "" {
		phase := task.PhaseImplementationReady
		if in.Option == workflowprogression.AcceptanceRequestReverification {
			phase = task.PhaseVerificationReady
		}
		var successorRevision string
		if err = tx.QueryRowContext(ctx, `SELECT task_definition_revision_id FROM execution_proposals WHERE id=? AND task_id=? AND project_id=?`, out.SuccessorExecutionID, current.ID, current.ProjectID).Scan(&successorRevision); err != nil {
			return out, err
		}
		if err = insertExecutionStartEffectTx(ctx, tx, executionStartEffectRecord{ProjectID: current.ProjectID, ActorKind: request.ActorKind, ActorID: request.ActorID, RequestID: request.RequestID, Action: request.Option, Effect: in.StartEffect, TaskID: current.ID, FeatureAreaID: current.FeatureAreaID, RevisionID: successorRevision, ExecutionID: out.SuccessorExecutionID, Phase: phase, DecisionID: request.DecisionID}, formatTime(now)); err != nil {
			return out, err
		}
	}
	if err = appendAcceptanceEvent(ctx, tx, current, predecessor, receipt, out.SuccessorExecutionID, in.StartEffect, now); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	out.Receipt = receipt
	out.Task, err = scanTask(s.db.sql.QueryRowContext(ctx, selectTask+` WHERE id=?`, current.ID))
	if err != nil {
		return out, err
	}
	return out, nil
}

func validateAcceptancePayload(in workflowprogression.TaskAcceptanceInput) error {
	if (len(in.NextRepositoryTargets) > 0 || in.NextPrimaryMemberID != "") && in.Option != workflowprogression.AcceptanceRequestChanges {
		return executionproposal.ErrInvalidArgument
	}
	if (in.RecoveryStatusID != "" || in.RecoveryContinuationInstruction != "") && (in.Option != workflowprogression.AcceptanceRetryImplementation || in.RecoveryStatusID == "" || in.RecoveryContinuationInstruction == "" || in.ExecutionInstructions != nil) {
		return executionproposal.ErrInvalidArgument
	}
	if in.ExecutionInstructions != nil && in.Option != workflowprogression.AcceptanceRetryImplementation {
		return decisionresolution.ErrInvalidArgument
	}
	if in.Option == workflowprogression.AcceptanceRetryImplementation && (in.TaskID == "" || in.FeatureAreaID == "" || in.TaskDefinitionRevisionID == "") {
		return decisionresolution.ErrInvalidArgument
	}
	switch in.Option {
	case workflowprogression.AcceptanceRequestChanges:
		if in.NextWorkSpec == nil || in.NextVerificationSpec == nil || validateContinuationWorkSpec(*in.NextWorkSpec) != nil || task.ValidateExecutableVerificationSpec(*in.NextVerificationSpec) != nil {
			return decisionresolution.ErrInvalidArgument
		}
	case workflowprogression.AcceptanceAccept, workflowprogression.AcceptanceReject, workflowprogression.AcceptanceRequestReverification, workflowprogression.AcceptanceRetryImplementation:
		if in.NextWorkSpec != nil || in.NextVerificationSpec != nil {
			return decisionresolution.ErrInvalidArgument
		}
	default:
		return decisionresolution.ErrInvalidArgument
	}
	if in.Option != workflowprogression.AcceptanceRequestChanges && in.Option != workflowprogression.AcceptanceRequestReverification && in.Option != workflowprogression.AcceptanceRetryImplementation && in.NextRunRuntimeConfig != nil {
		return decisionresolution.ErrInvalidArgument
	}
	if in.BackendSelection != nil && in.Option != workflowprogression.AcceptanceRequestChanges && in.Option != workflowprogression.AcceptanceRetryImplementation {
		return decisionresolution.ErrInvalidArgument
	}
	return nil
}

// A v2 multi-repository definition represents its work boundary with the
// repository-target compatibility scope. ValidateTargetWorkSpec checks its
// authored fields without interpreting that marker as a single-root scope.
func validateContinuationWorkSpec(value task.WorkSpec) error {
	if value.ExecutionScope.Kind == task.ExecutionScopeTargets {
		return task.ValidateTargetWorkSpec(value)
	}
	return task.ValidateWorkSpec(value)
}

func sameBackendSelection(a, b *codexconfig.BackendSelection) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.ConfigID == b.ConfigID && a.ConfigFingerprint == b.ConfigFingerprint
}

func validateAcceptanceEligibilityTx(ctx context.Context, tx *sql.Tx, current task.Task, predecessor executionproposal.Proposal, recoveryRetry bool) error {
	phaseEligible := current.Phase == task.PhaseAcceptancePending || current.Phase == task.PhaseImplementing || current.Phase == task.PhaseVerifying || current.Phase == task.PhaseImplementationReady || current.Phase == task.PhaseVerificationReady
	if recoveryRetry {
		// Only the option-scoped recovery continuation may exit RECOVERY_REQUIRED;
		// every other acceptance option remains fenced at this gate.
		phaseEligible = phaseEligible || current.Phase == task.PhaseRecoveryRequired
	}
	if current.CurrentDefinitionRevisionID != predecessor.TaskDefinitionRevisionID || current.LegacyReadOnly || current.Type != task.TypeDevelopment || (current.Status != task.StatusActive || !phaseEligible || current.CurrentExecutionID != predecessor.ID) || predecessor.Outcome != executionproposal.OutcomeSucceeded && predecessor.Outcome != executionproposal.OutcomeFailed && predecessor.Outcome != executionproposal.OutcomeCanceled {
		return executionproposal.ErrConflict
	}
	authority, err := readRuntimeAuthority(ctx, tx, current.FeatureAreaID)
	if err != nil {
		return err
	}
	if authority.Busy {
		return executionproposal.ErrConflict
	}
	return nil
}

func requireAcceptableVerificationTx(ctx context.Context, tx *sql.Tx, predecessor executionproposal.Proposal) error {
	if predecessor.VerificationSpec.Mode == task.VerificationModeNone {
		if predecessor.VerificationSpec.Waiver != nil && predecessor.VerificationSpec.Waiver.Source == task.VerificationWaiverUser && strings.TrimSpace(predecessor.VerificationSpec.Waiver.Reason) != "" {
			return nil
		}
		return executionproposal.ErrConflict
	}
	if predecessor.VerificationRunID == "" {
		return executionproposal.ErrConflict
	}
	var encoded string
	if err := tx.QueryRowContext(ctx, `SELECT verification_result_json FROM runs WHERE id=? AND task_id=? AND purpose='VERIFICATION' AND execution_proposal_id=? AND verification_result_json IS NOT NULL AND COALESCE(failure_code,'')<>'VERIFICATION_MUTATED_WORKTREE'`, predecessor.VerificationRunID, predecessor.TaskID, predecessor.ID).Scan(&encoded); err != nil {
		return executionproposal.ErrConflict
	}
	report, decodeErr := decodeVerificationReportJSON(encoded)
	if decodeErr != nil || report.Availability != run.VerificationReportAvailable || !run.VerificationReportMatchesSpec(*report, predecessor.VerificationSpec) {
		return executionproposal.ErrConflict
	}
	// Runtime-layout fields are introduced by v77. Their presence marks the
	// stricter multi-member Coding path; older persisted executions retain the
	// historical acceptance contract unchanged.
	if predecessor.ApprovedWorksetHash != "" {
		var raw string
		var frozen workset.RuntimeWorkset
		if err := tx.QueryRowContext(ctx, `SELECT runtime_workset_json FROM runs WHERE id=?`, predecessor.VerificationRunID).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &frozen) != nil || frozen.RunID != predecessor.VerificationRunID || !run.VerificationEvidenceComplete(*report, predecessor.VerificationSpec, &frozen) {
			return executionproposal.ErrConflict
		}
		if err := requireAuthoritativeWorksetEvidenceTx(ctx, tx, predecessor); err != nil {
			return err
		}
	}
	return nil
}

// authoritativeVerificationPasses establishes a successful Recovery assessment.
// User acceptance has a separate completeness gate and may accept failed facts.
func authoritativeVerificationPasses(report run.VerificationReport, spec task.VerificationSpec) bool {
	if !reportMatchesVerificationSpec(report, spec) {
		return false
	}
	for index, check := range report.Checks {
		if check.Required && (check.Observed == nil || check.Observed.Status != run.VerificationCheckPassed) {
			return false
		}
		declared := spec.Checks[index]
		if check.Required && declared.RepositoryID != "" {
			fact := check.Observed
			if fact.Source == nil || fact.Source.RepositoryID != declared.RepositoryID || fact.Source.RunID == "" || fact.Source.RuntimeWorksetHash == "" || fact.Source.MemberID == "" || fact.Source.MemberVersion < 1 || fact.Source.CWD == "" {
				return false
			}
			if declared.Command != "" {
				if fact.Source.Kind != "COMMAND" || fact.Source.ItemID == "" || fact.Source.CommandStatus != "completed" || fact.ExitCode == nil || *fact.ExitCode != 0 || !run.VerificationCommandMatches(declared.Command, fact.Command) {
					return false
				}
			} else if declared.Kind != task.VerificationCheckGitAssertion || fact.Source.Kind != "GIT" {
				return false
			}
		}
	}
	for _, criterion := range report.AcceptanceCriteria {
		if criterion.Reported.Status != run.VerificationCheckPassed {
			return false
		}
	}
	return true
}

func verificationObservationBindingsMatch(report run.VerificationReport, spec task.VerificationSpec, frozen workset.RuntimeWorkset) bool {
	if frozen.Validate() != nil || !reportMatchesVerificationSpec(report, spec) {
		return false
	}
	for i, check := range report.Checks {
		declared := spec.Checks[i]
		if check.Required && declared.RepositoryID != "" && (check.Observed == nil || !check.Observed.Source.Matches(frozen, declared.RepositoryID, declared.WorkingDirectory)) {
			return false
		}
	}
	return true
}

// requireAuthoritativeWorksetEvidenceTx rejects a v77 Coding closure unless
// both terminal runs carry the same frozen runtime layout and every approved
// member has exactly one clean terminal observation. The runtime workset JSON
// binds revision, execution, primary member and adapter/protocol identities;
// repository bindings provide the append-only member outcomes.
func requireAuthoritativeWorksetEvidenceTx(ctx context.Context, tx *sql.Tx, predecessor executionproposal.Proposal) error {
	implementation, err := implementationEvidence(ctx, tx, predecessor)
	if err != nil {
		return err
	}
	if implementation.ImplementationRunID == "" || predecessor.VerificationRunID == "" || predecessor.PrimaryMemberID == "" || predecessor.RuntimeAdapterIdentity == "" || predecessor.RuntimeProtocolIdentity == "" {
		return executionproposal.ErrConflict
	}
	revision, err := getDefinitionRevision(ctx, tx, predecessor.TaskDefinitionRevisionID)
	if err != nil || revision.TaskID != predecessor.TaskID || revision.Definition.Version != 2 || revision.Definition.PrimaryMemberID != predecessor.PrimaryMemberID {
		return executionproposal.ErrConflict
	}
	targetsJSON, err := json.Marshal(revision.Definition.RepositoryTargets)
	if err != nil {
		return executionproposal.ErrConflict
	}
	targetsHash := sha256.Sum256(targetsJSON)
	if hex.EncodeToString(targetsHash[:]) != predecessor.ApprovedWorksetHash {
		return executionproposal.ErrConflict
	}
	var implementationHash, verificationHash string
	for _, evidence := range []struct{ runID, executionID, purpose string }{{implementation.ImplementationRunID, implementation.ID, "IMPLEMENTATION"}, {predecessor.VerificationRunID, predecessor.ID, "VERIFICATION"}} {
		runID := evidence.runID
		var hash, raw string
		var valid bool
		err := tx.QueryRowContext(ctx, `SELECT runtime_workset_json,runtime_workset_hash, EXISTS(
 SELECT 1 FROM runs WHERE id=? AND task_id=? AND execution_proposal_id=?
   AND purpose=? AND status='SUCCEEDED' AND runtime_workset_hash IS NOT NULL
   AND json_extract(runtime_workset_json,'$.taskDefinitionRevisionId')=?
   AND json_extract(runtime_workset_json,'$.executionId')=?
   AND json_extract(runtime_workset_json,'$.primaryMemberId')=?
   AND json_extract(runtime_workset_json,'$.runtimeAdapterIdentity')=?
   AND json_extract(runtime_workset_json,'$.runtimeProtocolIdentity')=?
) FROM runs WHERE id=?`, runID, predecessor.TaskID, evidence.executionID, evidence.purpose, predecessor.TaskDefinitionRevisionID, evidence.executionID, predecessor.PrimaryMemberID, predecessor.RuntimeAdapterIdentity, predecessor.RuntimeProtocolIdentity, runID).Scan(&raw, &hash, &valid)
		if err != nil || !valid || hash == "" {
			return executionproposal.ErrConflict
		}
		var frozen workset.RuntimeWorkset
		if json.Unmarshal([]byte(raw), &frozen) != nil || frozen.Validate() != nil || frozen.RunID != runID || frozen.ContentHash != hash {
			return executionproposal.ErrConflict
		}
		// The receipt lineage authorizes only the Execution ID difference.
		// Compare all other frozen runtime authority fields without rewriting
		// either durable Run or its hash.
		frozen.ExecutionID = implementation.ID
		hash, err = frozen.ComputeContentHash()
		if err != nil {
			return err
		}
		if runID == implementation.ImplementationRunID {
			implementationHash = hash
		} else {
			verificationHash = hash
		}
		var incomplete bool
		if err = tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM run_repository_bindings WHERE run_id=?)
 OR EXISTS(SELECT 1 FROM run_repository_bindings WHERE run_id=? AND (runtime_root_identity IS NULL OR observation_state NOT IN ('OBSERVED','NO_CHANGE') OR observed_at IS NULL))`, runID, runID).Scan(&incomplete); err != nil || incomplete {
			return executionproposal.ErrConflict
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM run_repository_bindings WHERE run_id=?`, runID).Scan(&count); err != nil || count != len(revision.Definition.RepositoryTargets) {
			return executionproposal.ErrConflict
		}
		for _, target := range revision.Definition.RepositoryTargets {
			var exact bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_repository_bindings WHERE run_id=? AND repository_id=? AND member_id=? AND member_version=? AND origin_commit=?)`, runID, target.RepositoryID, target.MemberID, target.MemberVersion, target.OriginCommit).Scan(&exact); err != nil || !exact {
				return executionproposal.ErrConflict
			}
		}
	}
	if implementationHash != verificationHash {
		return executionproposal.ErrConflict
	}
	for _, target := range revision.Definition.RepositoryTargets {
		var continuous bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM run_repository_bindings i JOIN run_repository_bindings v ON v.run_id=? AND v.repository_id=i.repository_id
 WHERE i.run_id=? AND i.repository_id=? AND i.member_id=? AND i.member_version=?
   AND i.worktree_id=v.worktree_id AND i.end_head=i.start_head
   AND v.start_head=i.end_head AND v.end_head=v.start_head
   AND v.start_snapshot=i.end_snapshot AND v.end_snapshot=v.start_snapshot
   AND v.observation_state='NO_CHANGE'
)`, predecessor.VerificationRunID, implementation.ImplementationRunID, target.RepositoryID, target.MemberID, target.MemberVersion).Scan(&continuous); err != nil || !continuous {
			return executionproposal.ErrConflict
		}
	}
	var fresh bool
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT MIN(v.observed_at) FROM run_repository_bindings v WHERE v.run_id=?), '') >= COALESCE((SELECT MAX(i.observed_at) FROM run_repository_bindings i WHERE i.run_id=?), '')`, predecessor.VerificationRunID, implementation.ImplementationRunID).Scan(&fresh); err != nil || !fresh {
		return executionproposal.ErrConflict
	}
	return nil
}

func reportMatchesVerificationSpec(report run.VerificationReport, spec task.VerificationSpec) bool {
	if spec.Mode != task.VerificationModeRun || len(report.Checks) != len(spec.Checks) || len(report.AcceptanceCriteria) != len(spec.AcceptanceCriteria) {
		return false
	}
	for index, check := range report.Checks {
		declared := spec.Checks[index]
		if check.Index != index || check.Kind != string(declared.Kind) || check.Required != declared.Required || check.DeclaredCommand != declared.Command {
			return false
		}
	}
	for index, criterion := range report.AcceptanceCriteria {
		if criterion.Index != index || criterion.Criterion != spec.AcceptanceCriteria[index] {
			return false
		}
	}
	return true
}

func transitionAcceptedTaskTx(ctx context.Context, tx *sql.Tx, current task.Task, predecessor executionproposal.Proposal, target task.Status, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,updated_at=? WHERE id=? AND status='ACTIVE'`, string(target), formatTime(now), current.ID)
	if err != nil {
		return executionProposalConflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return executionproposal.ErrConflict
	}
	payload, _ := event.Payload(map[string]any{"from": current.Status, "to": target, "execution_proposal_id": predecessor.ID, "decision_id": decisionresolution.TaskAcceptanceDecisionID(predecessor.ID)})
	return appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: current.ProjectID, TaskID: current.ID, RunID: terminalExecutionRunID(predecessor), EntityType: "task", EntityID: current.ID, Payload: payload, CreatedAt: now})
}

func continueTaskWithChangesTx(ctx context.Context, tx *sql.Tx, current task.Task, predecessor executionproposal.Proposal, in workflowprogression.TaskAcceptanceInput, request decisionresolution.Request, now time.Time) (string, decisionresolution.Receipt, error) {
	if err := applyConfirmedContinuationTargetsTx(ctx, tx, &current, predecessor, in); err != nil {
		return "", decisionresolution.Receipt{}, err
	}
	nextWork := task.CloneWorkSpec(*in.NextWorkSpec)
	nextVerification := task.CloneVerificationSpec(*in.NextVerificationSpec)
	if err := normalizeApprovedContinuationSpecs(&nextWork, &nextVerification); err != nil {
		return "", decisionresolution.Receipt{}, err
	}
	planID, successorID := continuationIDs(predecessor.ID, request.Option)
	receipt, err := insertDecisionResolutionTx(ctx, tx, request, current.ProjectID, decisionresolution.ResultExecution, successorID, now)
	if err != nil {
		return "", receipt, err
	}
	workJSON, err := encodeWorkSpec(nextWork)
	if err != nil {
		return "", receipt, err
	}
	verificationJSON, err := encodeVerificationSpec(nextVerification)
	if err != nil {
		return "", receipt, err
	}
	title := nextWork.Goal
	if len([]rune(title)) > 128 {
		title = string([]rune(title)[:128])
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_plans(id,ai_session_id,content_actor_kind,content_actor_id,project_id,task_id,title,work_spec_json,verification_spec_json,status,created_at,updated_at,confirmed_at,applied_at) VALUES(?,?,?,?,?,?,?,?,?,'APPLIED',?,?,?,?)`, planID, continuationSessionEvidence(predecessor, in), continuationActorKind(in), continuationActorID(in), current.ProjectID, current.ID, title, workJSON, verificationJSON, formatTime(now), formatTime(now), formatTime(now), formatTime(now)); err != nil {
		return "", receipt, executionProposalConflict(err)
	}
	constraintsJSON, err := encodeStringItems(nextWork.Constraints)
	if err != nil {
		return "", receipt, err
	}
	current.WorkSpec, current.VerificationSpec, current.Goal, current.Constraints, current.ConfirmedTaskPlanID = nextWork, nextVerification, nextWork.Goal, append([]string(nil), nextWork.Constraints...), planID
	goals, err := definitionGoalsTx(ctx, tx, current.ID)
	if err != nil {
		return "", receipt, err
	}
	definition := task.DefinitionFromTask(current, goals)
	definition.ContinuationNormalizationVersion = 2
	payload, hash, err := task.EncodeDefinition(definition)
	if err != nil {
		return "", receipt, err
	}
	var ordinal int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0)+1 FROM task_definition_revisions WHERE task_id=?`, current.ID).Scan(&ordinal); err != nil {
		return "", receipt, err
	}
	revisionID := fmt.Sprintf("task_definition_%s_%d_%s", current.ID, ordinal, hash[:16])
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_definition_revisions(id,task_id,ordinal,payload_json,payload_sha256,created_at) VALUES(?,?,?,?,?,?)`, revisionID, current.ID, ordinal, payload, hash, formatTime(now)); err != nil {
		return "", receipt, executionProposalConflict(err)
	}
	previousStatus := current.Status
	result, updateErr := tx.ExecContext(ctx, `UPDATE tasks SET goal=?,constraints_json=?,work_spec_json=?,verification_spec_json=?,confirmed_task_plan_id=?,current_definition_revision_id=?,status='ACTIVE',phase='IMPLEMENTATION_READY',updated_at=? WHERE id=? AND status='ACTIVE'`, nextWork.Goal, constraintsJSON, workJSON, verificationJSON, planID, revisionID, formatTime(now), current.ID)
	if updateErr != nil {
		return "", receipt, executionProposalConflict(updateErr)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		if affectedErr != nil {
			return "", receipt, affectedErr
		}
		return "", receipt, executionproposal.ErrConflict
	}
	current.CurrentDefinitionRevisionID, current.Status, current.UpdatedAt = revisionID, task.StatusActive, now
	taskPayload, _ := event.Payload(map[string]any{"from": previousStatus, "to": task.StatusActive, "task_plan_id": planID, "authorization_source": "TASK_ACCEPTANCE"})
	if err = appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: current.ProjectID, TaskID: current.ID, EntityType: "task", EntityID: current.ID, Payload: taskPayload, CreatedAt: now}); err != nil {
		return "", receipt, executionProposalConflict(err)
	}
	if err = appendEvent(ctx, tx, event.Event{Type: event.TaskPlanApplied, ProjectID: current.ProjectID, TaskID: current.ID, EntityType: "task_plan", EntityID: planID, Payload: taskPayload, CreatedAt: now}); err != nil {
		return "", receipt, err
	}
	if err = insertAcceptanceSuccessorTx(ctx, tx, successorID, continuationSessionEvidence(predecessor, in), continuationActorKind(in), continuationActorID(in), current, predecessor, planID, revisionID, nextWork, nextVerification, in.NextRunRuntimeConfig, now); err != nil {
		return "", receipt, err
	}
	return successorID, receipt, nil
}

func continueTaskForReverificationTx(ctx context.Context, tx *sql.Tx, current task.Task, predecessor executionproposal.Proposal, in workflowprogression.TaskAcceptanceInput, request decisionresolution.Request, now time.Time) (string, decisionresolution.Receipt, error) {
	if predecessor.VerificationSpec.Mode != task.VerificationModeRun {
		return "", decisionresolution.Receipt{}, executionproposal.ErrConflict
	}
	// A verification-only predecessor retains its exact immutable revision.
	// Follow only durable reverification receipts to its successful implementation.
	var implemented bool
	if err := tx.QueryRowContext(ctx, `WITH RECURSIVE lineage(id) AS (
      SELECT ? UNION
      SELECT receipt.subject_id FROM decision_resolution_receipts receipt JOIN lineage ON receipt.result_id=lineage.id
      JOIN execution_proposals source ON source.id=receipt.subject_id
      WHERE receipt.decision_kind='TASK_ACCEPTANCE' AND receipt.subject_kind='EXECUTION_PROPOSAL'
       AND receipt.option='REQUEST_REVERIFICATION' AND receipt.result_kind='EXECUTION_PROPOSAL'
       AND source.task_id=? AND source.task_definition_revision_id=?
    ) SELECT EXISTS(SELECT 1 FROM lineage JOIN execution_proposals e ON e.id=lineage.id
      JOIN runs r ON r.id=e.run_id AND r.execution_proposal_id=e.id AND r.task_id=e.task_id
      WHERE r.purpose='IMPLEMENTATION' AND r.status='SUCCEEDED')`, predecessor.ID, predecessor.TaskID, predecessor.TaskDefinitionRevisionID).Scan(&implemented); err != nil || !implemented {
		return "", decisionresolution.Receipt{}, executionproposal.ErrConflict
	}
	_, successorID := continuationIDs(predecessor.ID, request.Option)
	receipt, err := insertDecisionResolutionTx(ctx, tx, request, current.ProjectID, decisionresolution.ResultExecution, successorID, now)
	if err != nil {
		return "", receipt, err
	}
	result, updateErr := tx.ExecContext(ctx, `UPDATE tasks SET status='ACTIVE',phase='VERIFICATION_READY',updated_at=? WHERE id=? AND status='ACTIVE'`, formatTime(now), current.ID)
	if updateErr != nil {
		return "", receipt, executionProposalConflict(updateErr)
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		if affectedErr != nil {
			return "", receipt, affectedErr
		}
		return "", receipt, executionproposal.ErrConflict
	}
	taskPayload, _ := event.Payload(map[string]any{"from": current.Status, "to": task.StatusActive, "authorization_source": "TASK_ACCEPTANCE", "execution_proposal_id": predecessor.ID})
	if err = appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: current.ProjectID, TaskID: current.ID, EntityType: "task", EntityID: current.ID, Payload: taskPayload, CreatedAt: now}); err != nil {
		return "", receipt, err
	}
	current.Status, current.UpdatedAt = task.StatusActive, now
	if err = insertAcceptanceSuccessorTx(ctx, tx, successorID, continuationSessionEvidence(predecessor, in), continuationActorKind(in), continuationActorID(in), current, predecessor, predecessor.TaskPlanID, predecessor.TaskDefinitionRevisionID, predecessor.WorkSpec, predecessor.VerificationSpec, in.NextRunRuntimeConfig, now); err != nil {
		return "", receipt, err
	}
	return successorID, receipt, nil
}

// The legacy ai_session_id is immutable predecessor evidence. Content actor is
// the actual source of this continuation and is always explicit for new rows.
func continuationSessionEvidence(predecessor executionproposal.Proposal, in workflowprogression.TaskAcceptanceInput) string {
	if in.AISessionID != "" {
		return in.AISessionID
	}
	return predecessor.AISessionID
}

func continuationActorKind(in workflowprogression.TaskAcceptanceInput) string {
	if in.OperatorPrincipalID != "" {
		return "LOCAL_OPERATOR"
	}
	return "AI_SESSION"
}

func continuationActorID(in workflowprogression.TaskAcceptanceInput) string {
	if in.OperatorPrincipalID != "" {
		return in.OperatorPrincipalID
	}
	return in.AISessionID
}

func insertAcceptanceSuccessorTx(ctx context.Context, tx *sql.Tx, id, sessionID, actorKind, actorID string, current task.Task, predecessor executionproposal.Proposal, planID, revisionID string, work task.WorkSpec, verification task.VerificationSpec, requested *codexruntime.NextRunRuntimeConfig, now time.Time) error {
	if work.ImplementationMode == task.ImplementationCodingAgent {
		if err := validateFeatureAreaBackendContinuityTx(ctx, tx, current.FeatureAreaID, predecessor.BackendSelection); err != nil {
			return err
		}
	}
	workJSON, err := encodeWorkSpec(work)
	if err != nil {
		return err
	}
	verificationJSON, err := encodeVerificationSpec(verification)
	if err != nil {
		return err
	}
	config := predecessor.RuntimeConfig
	if requested != nil {
		config, err = freezeExecutionRuntimeConfig(*requested)
		if err != nil {
			return err
		}
	}
	// A successor is a new authorization, not a copy of a consumed candidate.
	// Rebind its immutable v77 layout to the newly written definition while
	// preserving the already approved runtime adapter/protocol family.
	candidate := executionproposal.Proposal{
		ID:                       id,
		AISessionID:              sessionID,
		ProjectID:                current.ProjectID,
		TaskID:                   current.ID,
		TaskPlanID:               planID,
		WorkSpec:                 work,
		VerificationSpec:         verification,
		TaskDefinitionRevisionID: revisionID,
		RuntimeConfig:            config,
		BackendSelection:         predecessor.BackendSelection,
		RuntimeAdapterIdentity:   predecessor.RuntimeAdapterIdentity,
		RuntimeProtocolIdentity:  predecessor.RuntimeProtocolIdentity,
	}
	if err := bindExecutionDefinitionTx(ctx, tx, &candidate); err != nil {
		return err
	}
	if err := requireExecutionRuntimeSelectionTx(ctx, tx, candidate); err != nil {
		return err
	}
	runtimeJSON, err := encodeExecutionRuntimeConfig(config)
	if err != nil {
		return err
	}
	backendJSON, err := encodeBackendSelection(predecessor.BackendSelection)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_proposals(id,ai_session_id,content_actor_kind,content_actor_id,project_id,task_id,task_plan_id,status,created_at,updated_at,confirmed_at,work_spec_json,verification_spec_json,outcome,task_definition_revision_id,runtime_config_json,backend_selection_json,primary_member_id,approved_workset_hash,runtime_adapter_identity,runtime_protocol_identity) VALUES(?,?,?,?,?,?,?,'CONFIRMED',?,?,?,?,?,'PENDING',?,?,?,?,?,?,?)`, id, sessionID, actorKind, actorID, current.ProjectID, current.ID, planID, formatTime(now), formatTime(now), formatTime(now), workJSON, verificationJSON, candidate.TaskDefinitionRevisionID, runtimeJSON, backendJSON, nullString(candidate.PrimaryMemberID), nullString(candidate.ApprovedWorksetHash), nullString(candidate.RuntimeAdapterIdentity), nullString(candidate.RuntimeProtocolIdentity))
	if err != nil {
		return executionProposalConflict(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET current_execution_id=? WHERE id=? AND status='ACTIVE'`, id, current.ID); err != nil {
		return err
	}
	payload, _ := event.Payload(map[string]any{"authorization_source": "TASK_ACCEPTANCE", "task_definition_revision_id": revisionID})
	return appendEvent(ctx, tx, event.Event{Type: event.ExecutionProposalCreated, ProjectID: current.ProjectID, TaskID: current.ID, EntityType: "execution_proposal", EntityID: id, Payload: payload, CreatedAt: now})
}

func freezeExecutionRuntimeConfig(value codexruntime.NextRunRuntimeConfig) (codexruntime.NextRunRuntimeConfig, error) {
	if strings.TrimSpace(value.Model) == "" || strings.TrimSpace(value.ReasoningEffort) == "" {
		return codexruntime.NextRunRuntimeConfig{}, executionproposal.ErrConflict
	}
	if value.CodexFastMode {
		value.ServiceTier = "priority"
	} else {
		value.ServiceTier = ""
	}
	return value, nil
}

func (s *WorkflowProgressionStore) freezeRuntimeConfig(defaultModel string, requested *codexruntime.NextRunRuntimeConfig) (codexruntime.NextRunRuntimeConfig, error) {
	allowlist := s.runtimeAllowlist()
	value := codexruntime.NextRunRuntimeConfig{}
	if requested != nil {
		value = *requested
	}
	if value.Model == "" {
		value.Model = defaultModel
	}
	if value.Model == "" && len(allowlist.Models) > 0 {
		value.Model = allowlist.Models[0]
	}
	return value.Normalize(allowlist)
}

func matchesReplayRuntimeConfig(requested, persisted codexruntime.NextRunRuntimeConfig) bool {
	if requested.Model != "" && requested.Model != persisted.Model {
		return false
	}
	if requested.ReasoningEffort != "" && requested.ReasoningEffort != persisted.ReasoningEffort {
		return false
	}
	return requested.CodexFastMode == persisted.CodexFastMode
}

func (s *WorkflowProgressionStore) replayTaskAcceptance(ctx context.Context, tx *sql.Tx, in workflowprogression.TaskAcceptanceInput, receipt decisionresolution.Receipt) (workflowprogression.TaskAcceptanceResolution, error) {
	out := workflowprogression.TaskAcceptanceResolution{Receipt: receipt, Replay: true, StartEffect: in.StartEffect}
	if in.Option == workflowprogression.AcceptanceRetryImplementation {
		if err := checkAcceptanceContinuationReplay(ctx, tx, in); err != nil {
			return out, err
		}
	}
	predecessor, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, receipt.SubjectID))
	if err != nil {
		return out, err
	}
	out.Task, err = scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, predecessor.TaskID))
	if err != nil {
		return out, err
	}
	if receipt.ResultKind == decisionresolution.ResultExecution {
		successor, e := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, receipt.ResultID))
		if e != nil {
			return out, e
		}
		out.SuccessorExecutionID = successor.ID
		if in.BackendSelection != nil && !sameBackendSelection(in.BackendSelection, successor.BackendSelection) {
			return out, executionproposal.ErrConflict
		}
		if in.NextRunRuntimeConfig != nil {
			if !matchesReplayRuntimeConfig(*in.NextRunRuntimeConfig, successor.RuntimeConfig) {
				return out, executionproposal.ErrConflict
			}
		}
		if in.Option == workflowprogression.AcceptanceRequestChanges {
			revision, revisionErr := getDefinitionRevision(ctx, tx, successor.TaskDefinitionRevisionID)
			if revisionErr != nil {
				return out, revisionErr
			}
			candidate := revision.Definition
			if len(candidate.RepositoryTargets) > 0 {
				candidate.RepositoryTargets = in.NextRepositoryTargets
				candidate.PrimaryMemberID = in.NextPrimaryMemberID
			} else if len(in.NextRepositoryTargets) > 0 || in.NextPrimaryMemberID != "" {
				return out, executionproposal.ErrConflict
			}
			candidate.WorkSpec = task.CloneWorkSpec(*in.NextWorkSpec)
			candidate.VerificationSpec = task.CloneVerificationSpec(*in.NextVerificationSpec)
			var normalizeErr error
			switch revision.Definition.ContinuationNormalizationVersion {
			case 0: // Old receipts retain their original canonicalization contract.
				normalizeErr = normalizeContinuationSpecs(&candidate.WorkSpec, &candidate.VerificationSpec)
			case 2:
				normalizeErr = normalizeApprovedContinuationSpecs(&candidate.WorkSpec, &candidate.VerificationSpec)
			default:
				return out, executionproposal.ErrConflict
			}
			if normalizeErr != nil {
				return out, normalizeErr
			}
			_, candidateHash, hashErr := task.EncodeDefinition(candidate)
			if hashErr != nil {
				return out, hashErr
			}
			if candidateHash != revision.PayloadSHA256 {
				return out, executionproposal.ErrConflict
			}
		}
	}
	return out, nil
}

// New explicit approvals preserve all authored text. Scope normalization remains
// lexical and repository-relative; this does not claim backend per-path enforcement.
// Do not apply legacy text normalization as a fallback to a new revision replay.
func normalizeApprovedContinuationSpecs(work *task.WorkSpec, verification *task.VerificationSpec) error {
	for _, supplied := range work.ExecutionScope.Paths {
		for _, component := range strings.Split(supplied, "/") {
			if component == ".." {
				return executionproposal.ErrConflict
			}
		}
	}
	return normalizeContinuationScope(work, verification)
}

// Legacy receipt replay only: preserve the old normalization before comparing
// its immutable revision hash. Never use this to rewrite a newly approved plan.
func normalizeContinuationSpecs(work *task.WorkSpec, verification *task.VerificationSpec) error {
	work.Goal = strings.TrimSpace(work.Goal)
	for index := range work.Constraints {
		work.Constraints[index] = strings.TrimSpace(work.Constraints[index])
	}
	return normalizeContinuationScope(work, verification)
}

func normalizeContinuationScope(work *task.WorkSpec, verification *task.VerificationSpec) error {
	if work.ExecutionScope.Kind == task.ExecutionScopePaths {
		seen := make(map[string]bool, len(work.ExecutionScope.Paths))
		for index, supplied := range work.ExecutionScope.Paths {
			if strings.ContainsRune(supplied, '\x00') || strings.Contains(supplied, "\\") || strings.HasPrefix(supplied, "/") {
				return executionproposal.ErrConflict
			}
			canonical := path.Clean(supplied)
			if canonical == "." || canonical == ".." || strings.HasPrefix(canonical, "../") || seen[canonical] {
				return executionproposal.ErrConflict
			}
			seen[canonical] = true
			work.ExecutionScope.Paths[index] = canonical
		}
	} else if work.ExecutionScope.Kind == task.ExecutionScopeRepository {
		work.ExecutionScope.Paths = []string{}
	}
	if validateContinuationWorkSpec(*work) != nil || task.ValidateExecutableVerificationSpec(*verification) != nil {
		return executionproposal.ErrConflict
	}
	return nil
}

func continuationIDs(executionID, option string) (string, string) {
	sum := sha256.Sum256([]byte(executionID + "\x00" + option))
	suffix := hex.EncodeToString(sum[:8])
	return "plan_acceptance_" + suffix, "execution_acceptance_" + suffix
}
func terminalExecutionRunID(v executionproposal.Proposal) string {
	if v.VerificationRunID != "" {
		return v.VerificationRunID
	}
	return v.ImplementationRunID
}
func appendAcceptanceEvent(ctx context.Context, tx *sql.Tx, current task.Task, predecessor executionproposal.Proposal, receipt decisionresolution.Receipt, successor string, startEffect workflowprogression.StartEffect, now time.Time) error {
	payload, _ := event.Payload(map[string]any{"decision_id": receipt.DecisionID, "option": receipt.Option, "execution_proposal_id": predecessor.ID, "successor_execution_id": successor, "start_effect": startEffect})
	return appendEvent(ctx, tx, event.Event{Type: event.TaskAcceptanceResolved, ProjectID: current.ProjectID, TaskID: current.ID, RunID: terminalExecutionRunID(predecessor), EntityType: "execution_proposal", EntityID: predecessor.ID, Payload: payload, CreatedAt: now})
}
