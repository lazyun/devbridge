package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

type WorkflowProgressionStore struct {
	db                    *DB
	wakeMu                sync.RWMutex
	approvalWake          func()
	clock                 func() time.Time
	runtimeConfigProvider func() codexruntime.Allowlist
	// deliveryFaultHook is test-only fault injection at the Git/receipt seam.
	// It is nil in production and never changes an effect itself.
	deliveryFaultHook func(string, string) error
}

// SetDeliveryFaultHookForTest exposes the real Git-to-receipt crash seam to
// isolated integration tests. Production callers do not configure it.
func (s *WorkflowProgressionStore) SetDeliveryFaultHookForTest(hook func(string, string) error) {
	s.deliveryFaultHook = hook
}

func (s *WorkflowProgressionStore) SetRuntimeConfigProvider(provider func() codexruntime.Allowlist) {
	s.runtimeConfigProvider = provider
}

func (s *WorkflowProgressionStore) runtimeAllowlist() codexruntime.Allowlist {
	if s != nil && s.runtimeConfigProvider != nil {
		return s.runtimeConfigProvider()
	}
	return codexruntime.Default()
}

func (s *WorkflowProgressionStore) SetApprovalWake(wake func()) {
	s.wakeMu.Lock()
	defer s.wakeMu.Unlock()
	s.approvalWake = wake
}
func (s *WorkflowProgressionStore) wakeApproval() {
	s.wakeMu.RLock()
	wake := s.approvalWake
	s.wakeMu.RUnlock()
	if wake != nil {
		wake()
	}
}

func NewWorkflowProgressionStore(db *DB) *WorkflowProgressionStore {
	return &WorkflowProgressionStore{db: db, clock: time.Now}
}
func (s *WorkflowProgressionStore) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}
func workflowHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type approvalSnapshot struct {
	Version            int                          `json:"version"`
	ProjectID          string                       `json:"projectId"`
	FeatureAreaID      string                       `json:"featureAreaId"`
	ReviewID           string                       `json:"reviewId"`
	TaskID             string                       `json:"taskId"`
	RunID              string                       `json:"runId"`
	StartRequestID     string                       `json:"startRequestId"`
	StartPayloadSHA256 string                       `json:"startPayloadSha256"`
	Scope              review.Focus                 `json:"scope"`
	Goal               string                       `json:"goal"`
	Instruction        string                       `json:"instruction"`
	PromptSHA256       string                       `json:"promptSha256"`
	Repository         *review.RepositoryProvenance `json:"repository"`
	DevelopmentCycleID string                       `json:"developmentCycleId"`
	DevelopmentRef     string                       `json:"developmentRef"`
	ResultSHA256       string                       `json:"resultSha256"`
}

func captureReviewWorkflowTx(ctx context.Context, tx *sql.Tx, v review.Review) error {
	// A v2 Review has an explicit set of member origins. Selecting one current
	// development cycle would silently create scalar authority, so its legacy
	// source row intentionally records no cycle/ref.
	if len(v.RepositoryTargets) > 0 {
		_, err := tx.ExecContext(ctx, `INSERT INTO task_review_sources(task_id,review_id,development_cycle_id,development_ref,created_at) VALUES(?,?,?,?,?)`, v.TaskID, v.ID, nil, "", formatTime(v.CreatedAt))
		return err
	}
	var cycle, ref string
	err := tx.QueryRowContext(ctx, `SELECT id,development_ref FROM development_cycles WHERE project_id=? AND repository_id=? AND superseded_at IS NULL`, v.ProjectID, v.RepositoryID).Scan(&cycle, &ref)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if ref == "" && v.RepositoryProvenance != nil {
		ref = v.RepositoryProvenance.BranchRef
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_review_sources(task_id,review_id,development_cycle_id,development_ref,created_at) VALUES(?,?,?,?,?)`, v.TaskID, v.ID, nullString(cycle), ref, formatTime(v.CreatedAt))
	return err
}

// Completion and recovery both trigger the durable materialization protocol
// through Reconcile; reads never repair missing decisions. See
// review_materialization_protocol.go for the three-phase claim protocol that
// commits the formal result, snapshot and pending Decision together.
func (s *WorkflowProgressionStore) Reconcile(ctx context.Context) error {
	return materializeReviewResults(ctx, s.db, s.now())
}

func recordInvalidReviewResolutionDecisionTx(ctx context.Context, tx *sql.Tx, v review.Review, raw, startPayload, cycle, ref string, at time.Time) error {
	snap := approvalSnapshot{Version: 2, ProjectID: v.ProjectID, FeatureAreaID: v.FeatureAreaID, ReviewID: v.ID, TaskID: v.TaskID, RunID: v.RunID, StartRequestID: v.StartRequestID, StartPayloadSHA256: startPayload, Scope: v.Focus, Goal: v.EngineeringGoal, Instruction: v.Instruction, PromptSHA256: v.PromptSHA256, DevelopmentCycleID: cycle, DevelopmentRef: ref, ResultSHA256: workflowHash(raw)}
	encoded, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO review_approval_decisions(decision_id,review_id,project_id,feature_area_id,task_id,start_request_id,provenance_json,provenance_sha256,result_sha256,result_text,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(decision_id) DO NOTHING`, wp.InvalidResultDecisionID(v.ID), v.ID, v.ProjectID, v.FeatureAreaID, v.TaskID, v.StartRequestID, string(encoded), workflowHash(string(encoded)), snap.ResultSHA256, raw, formatTime(at.UTC()))
	return err
}

// recordReviewResultMaterializationTx is append-only. The raw result remains
// the authoritative model artifact; formal bytes exist only for a validated
// interpretation and never repair or replace that raw artifact.
func recordReviewResultMaterializationTx(ctx context.Context, tx *sql.Tx, reviewID, runID, outcome, raw, code, detail, formal, formalHash string, at time.Time) error {
	if len(detail) > 1024 {
		detail = detail[:1024]
	}
	var formalValue, formalDigest, targetsDigest, workDigest, verificationDigest, reasonCode, reasonDetail any
	if outcome == "VALID" {
		formalValue, formalDigest = formal, formalHash
		var result review.ImplementationResult
		if err := json.Unmarshal([]byte(formal), &result); err != nil {
			return err
		}
		hashPart := func(value any) (string, error) {
			encoded, err := json.Marshal(value)
			if err != nil {
				return "", err
			}
			return workflowHash(string(encoded)), nil
		}
		var err error
		if targetsDigest, err = hashPart(result.RepositoryTargets); err != nil {
			return err
		}
		if workDigest, err = hashPart(result.WorkSpec); err != nil {
			return err
		}
		if verificationDigest, err = hashPart(result.VerificationSpec); err != nil {
			return err
		}
	} else {
		reasonCode, reasonDetail = code, detail
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO review_result_materializations(review_id,task_id,review_ordinal,run_id,outcome,raw_result_sha256,formal_result_sha256,formal_result_json,repository_target_set_sha256,work_spec_sha256,verification_spec_sha256,reason_code,reason_detail,created_at)
 SELECT ?,v.task_id,v.ordinal,?,?,?,?,?,?,?,?,?,?,? FROM reviews v WHERE v.id=? AND v.linked_run_id=?`, reviewID, runID, outcome, workflowHash(raw), formalDigest, formalValue, targetsDigest, workDigest, verificationDigest, reasonCode, reasonDetail, formatTime(at), reviewID, runID)
	return err
}

func (s *WorkflowProgressionStore) Read(ctx context.Context, projectID, areaID string) (wp.Guidance, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return wp.Guidance{}, err
	}
	defer tx.Rollback()
	g, err := workflowStateTx(ctx, tx, projectID, areaID)
	if err != nil {
		return g, err
	}
	if err = s.applyDirectFileGuidance(ctx, tx, &g); err != nil {
		return g, err
	}
	return g, tx.Commit()
}
func workflowStateTx(ctx context.Context, q definitionQuerier, projectID, areaID string) (wp.Guidance, error) {
	return workflowTaskStateTx(ctx, q, projectID, areaID, "")
}
func (s *WorkflowProgressionStore) ReadTask(ctx context.Context, projectID, areaID, taskID string) (wp.Guidance, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return wp.Guidance{}, err
	}
	defer tx.Rollback()
	g, err := workflowTaskStateTx(ctx, tx, projectID, areaID, taskID)
	if err != nil {
		return g, err
	}
	if err = s.applyDirectFileGuidance(ctx, tx, &g); err != nil {
		return g, err
	}
	return g, tx.Commit()
}
func workflowTaskStateTx(ctx context.Context, q definitionQuerier, projectID, areaID, taskID string) (g wp.Guidance, err error) {
	g.Status = "SUCCESS"
	g.FormalResultStatus = "NOT_AVAILABLE"
	g.ReviewConfirmationStatus = "NOT_REQUIRED"
	g.AllowedReviewDepths = []task.ReviewDepth{}
	g.AvailableActions = []string{}
	g.WorkflowMode = "CURRENT"
	g.ProjectID = projectID
	g.FeatureAreaID = areaID
	defer func() {
		if err == nil && g.FeatureAreaID != "" {
			var id, runID string
			e := q.QueryRowContext(ctx, `SELECT a.id,r.id FROM approvals a JOIN runs r ON r.id=a.run_id WHERE r.feature_area_id=? AND a.status='PENDING' AND r.status IN ('RUNNING','WAITING_APPROVAL','PREPARING') ORDER BY a.created_at DESC LIMIT 1`, areaID).Scan(&id, &runID)
			if e == nil && g.RunID == runID && (g.TaskPhase == task.PhaseReviewing || g.TaskPhase == task.PhaseImplementing || g.TaskPhase == task.PhaseVerifying) {
				g.RuntimeApprovalID = id
				g.RunID = runID
				g.Set("RUNTIME_APPROVAL_REQUIRED", "RESOLVE_RUNTIME_APPROVAL", "Resolve the exact runtime permission request before the current operation can continue.")
			}
		}
		if err == nil && areaID != "" {
			var pending bool
			err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT' AND ((status='ACTIVE' AND phase='DELIVERING') OR (status='SUCCEEDED' AND commit_sha IS NULL)) AND NOT EXISTS(SELECT 1 FROM task_commit_receipts done WHERE done.task_id=tasks.id AND done.protocol_version=2 AND done.completed_at IS NOT NULL)) OR EXISTS(SELECT 1 FROM task_commit_receipts WHERE feature_area_id=? AND completed_at IS NULL) OR EXISTS(SELECT 1 FROM pending_feature_area_commits WHERE feature_area_id=? AND completed_at IS NULL)`, areaID, areaID, areaID).Scan(&pending)
			if err == nil && pending {
				// The durable Task phase keeps its shared projection identity;
				// the area overlay only adds GIT_FINALIZATION_PENDING for other
				// writers, never rebadging DELIVERING or recovery-owned Tasks.
				switch g.TaskPhase {
				case task.PhaseDelivering:
					g.Set("TASK_DELIVERING", "RETRY_GIT_FINALIZATION", "Task 已验收；Git 提交尚未完成，需恢复提交后才能继续开发。")
				case task.PhaseRecoveryRequired:
					// Formal recovery resolution owns the exit; a blind Git retry
					// stays fenced by the acceptance authority.
				default:
					g.Set("GIT_FINALIZATION_PENDING", "RETRY_GIT_FINALIZATION", "Task 已验收；Git 提交尚未完成，需恢复提交后才能继续开发。")
				}
			}
		}
		if err == nil && (g.FeatureAreaStatus == "ACTIVE" || g.FeatureAreaStatus == "DISCOVERING") {
			var authority runtimeAuthorityState
			authority, err = readRuntimeAuthority(ctx, q, areaID)
			if err == nil {
				if authority.RecoveryRequired {
					blockProgressionGuidance(&g, "RECOVERY_REQUIRED", "RECOVER_AGENT_OPERATION", "Resolve the exact interrupted agent operation before progression.")
				} else if authority.Busy && progressionActionNeedsIdle(g.NextAction.Code) {
					// Successful Implementation retains this exact Execution's claim
					// for serial Verification. This is continuation of the owner,
					// not permission for Acceptance, Git, or an unrelated writer.
					var continuation bool
					if g.NextAction.Code == "START_VERIFICATION" {
						err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_area_agent_claims c JOIN runs r ON r.id=c.run_id JOIN agent_operation_boundaries b ON b.run_id=r.id WHERE c.feature_area_id=? AND c.operation_kind='EXECUTION' AND c.operation_id=? AND c.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1) AND r.status='SUCCEEDED' AND b.quiesced=1 AND b.advance_state IN ('NO_ADVANCE','KNOWN_ADVANCE'))`, areaID, g.ExecutionID).Scan(&continuation)
					}
					if err == nil && !continuation {
						blockProgressionGuidance(&g, "FEATURE_AREA_BUSY", "WAIT_FOR_AGENT_OPERATION", "Wait for the current FeatureArea operation before progression.")
					}
				}
			}
		}
		b, _ := json.Marshal(g)
		g.Generation = workflowHash(string(b))
		if r, ok := err.(*wp.Error); ok {
			r.Guidance = g
			r.Status = "REJECTED"
		}
	}()
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM events WHERE project_id=?`, projectID).Scan(&g.SourceSequence); err != nil {
		return
	}
	if areaID == "" {
		g.Set("FEATURE_AREA_REQUIRED", "SELECT_FEATURE_AREA", "Create or select a FeatureArea before development.")
		return
	}
	var life string
	err = q.QueryRowContext(ctx, `SELECT status FROM feature_areas WHERE id=? AND project_id=?`, areaID, projectID).Scan(&life)
	if errors.Is(err, sql.ErrNoRows) {
		g.Set("FEATURE_AREA_REQUIRED", "SELECT_FEATURE_AREA", "Select a FeatureArea belonging to this Project.")
		err = wp.Reject("FEATURE_AREA_REQUIRED", g)
		return
	}
	if err != nil {
		return
	}
	g.FeatureAreaStatus = life
	if err = q.QueryRowContext(ctx, `SELECT COALESCE(current_workspace_id,'') FROM feature_areas WHERE id=?`, areaID).Scan(&g.CurrentWorkspaceID); err != nil {
		return
	}
	var phase task.Phase
	var status string
	err = q.QueryRowContext(ctx, `SELECT id,status,phase,COALESCE(current_review_id,''),COALESCE(current_execution_id,''),COALESCE(current_definition_revision_id,''),required_review_depth FROM tasks WHERE project_id=? AND feature_area_id=? AND task_type='DEVELOPMENT' AND (?='' OR id=?) ORDER BY status='ACTIVE' DESC,created_at DESC,id DESC LIMIT 1`, projectID, areaID, taskID, taskID).Scan(&g.TaskID, &status, &phase, &g.ReviewID, &g.ExecutionID, &g.TaskDefinitionRevisionID, &g.RequiredReviewDepth)
	if errors.Is(err, sql.ErrNoRows) {
		if taskID != "" {
			err = task.ErrNotFound
			return
		}
		err = nil
		g.Set("FEATURE_AREA_IDLE", "CREATE_TASK", "Create a Task for the engineering intent.")
		return
	}
	if err != nil {
		return
	}
	g.TaskPhase = phase
	g.AllowedReviewDepths = task.AllowedReviewDepths(g.RequiredReviewDepth)
	g.LatestTask = &wp.TaskFact{ID: g.TaskID, Status: status, Phase: phase}
	if status != "ACTIVE" {
		g.Set(status, "WORKFLOW_COMPLETE", "The Task is terminal.")
		return
	}
	g.CurrentTask = g.LatestTask
	if g.ReviewID != "" {
		var state string
		var depth task.ReviewDepth
		var disposition review.Disposition
		var executor review.ExecutorKind
		var accessState string
		err = q.QueryRowContext(ctx, `SELECT COALESCE(r.status,CASE WHEN v.start_failure_code IS NULL THEN 'PENDING' ELSE 'FAILED' END),COALESCE(v.linked_run_id,''),v.review_depth,COALESCE(v.review_disposition,''),v.executor_kind,COALESCE((SELECT state FROM review_grants WHERE review_id=v.id),'PREPARING') FROM reviews v LEFT JOIN runs r ON r.id=v.linked_run_id WHERE v.id=? AND v.task_id=?`, g.ReviewID, g.TaskID).Scan(&state, &g.RunID, &depth, &disposition, &executor, &accessState)
		if err != nil {
			return
		}
		g.CurrentWorkflowReview = &wp.ReviewFact{ExecutorKind: executor, AccessState: accessState, ID: g.ReviewID, Status: state, ReviewDepth: depth, ReviewDisposition: disposition}
		g.LatestReviewStatus = state
		g.ReviewRuntimeStatus = state
		if g.RunID != "" {
			var rawResult int
			if err = q.QueryRowContext(ctx, `SELECT CASE WHEN result_text IS NULL OR length(trim(result_text))=0 THEN 0 ELSE 1 END FROM runs WHERE id=?`, g.RunID).Scan(&rawResult); err != nil {
				return
			}
			g.RawResultAvailable = rawResult == 1
		}
		var outcome, reasonCode, reasonDetail string
		e := q.QueryRowContext(ctx, `SELECT outcome,COALESCE(reason_code,''),COALESCE(reason_detail,'') FROM review_result_materializations WHERE review_id=?`, g.ReviewID).Scan(&outcome, &reasonCode, &reasonDetail)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			err = e
			return
		}
		if e == nil {
			g.FormalResultStatus = outcome
			g.FormalResultAvailable = outcome == "VALID"
			g.ReviewDisposition = disposition
			if outcome != "VALID" {
				g.MaterializationFailure = &wp.ReviewMaterializationFailure{Code: reasonCode, Message: reasonDetail}
			}
		}
	}
	if g.ExecutionID != "" {
		err = q.QueryRowContext(ctx, `SELECT COALESCE(verification_run_id,run_id,'') FROM execution_proposals WHERE id=? AND task_id=?`, g.ExecutionID, g.TaskID).Scan(&g.RunID)
		if err != nil {
			return
		}
	}
	switch phase {
	case task.PhaseReviewRequired:
		g.Set(string(phase), "START_TASK_REVIEW", "Start the next read-only Review for this Task.")
		if g.RequiredReviewDepth != nil && *g.RequiredReviewDepth == task.ReviewDepthDeep {
			g.Set(string(phase), "START_TASK_REVIEW", "需要深度审查")
		}
	case task.PhaseReviewing:
		g.Set(string(phase), "WAIT_FOR_REVIEW_COMPLETION", "Wait for the current Task Review.")
		if g.CurrentWorkflowReview != nil && (g.CurrentWorkflowReview.Status == "CREATED" || g.CurrentWorkflowReview.Status == "PENDING") {
			g.Set(string(phase), "WAIT_FOR_REVIEW_START", "The authorized Review is preparing to start.")
		}
		if g.CurrentWorkflowReview != nil && g.CurrentWorkflowReview.Status == "SUCCEEDED" && (g.FormalResultStatus == "INVALID" || g.FormalResultStatus == "SCOPE_CONFLICT") {
			decisionID := wp.InvalidResultDecisionID(g.ReviewID)
			snapshot, snapshotErr := reviewDecisionSnapshotTx(ctx, q, decisionID)
			if snapshotErr != nil {
				err = snapshotErr
				return
			}
			g.DecisionID = decisionID
			g.AvailableActions = snapshot.AvailableActions
			a := wp.Approval{DecisionID: decisionID, DecisionVersion: snapshot.Version, ReasonCode: snapshot.ReasonCode, ReviewID: g.ReviewID, TaskID: g.TaskID, Status: snapshot.ProjectedStatus(), Summary: g.MaterializationFailure.Message}
			if snapshot.actionable() {
				a.AllowedActions = []string{"REQUEST_RE_REVIEW", "ABANDON_TASK"}
			}
			g.Approval, g.ReviewConfirmation = &a, &a
			g.Set(string(phase), snapshot.RequiredAction, snapshot.Message())
			break
		}
		if g.CurrentWorkflowReview != nil {
			if g.CurrentWorkflowReview.ExecutorKind == review.ExecutorUpperLLM {
				switch g.CurrentWorkflowReview.AccessState {
				case "ACTIVE":
					g.Set(string(phase), "READ_REVIEW_FILES", "Upper LLM: use review_access_get, read authorized files, then submit review_result_submit.")
				case "AWAITING_REPORT":
					g.Set(string(phase), "SUBMIT_REVIEW_RESULT", "Upper LLM must submit its formal report through review_result_submit.")
				case "FINALIZING":
					g.Set(string(phase), "WAIT_FOR_REVIEW_FINALIZATION", "Core is finalizing the Review.")
				case "PREPARING":
					g.Set(string(phase), "PREPARE_REVIEW_ENVIRONMENT", "Review environment is not ready; replay the original start request to resume preparation.")
					var failed bool
					if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_preparation_failures f WHERE f.owner_id=? AND f.run_id=? AND NOT EXISTS(SELECT 1 FROM runtime_preparation_revocations x WHERE x.plan_id=f.plan_id))`, g.ReviewID, g.RunID).Scan(&failed); err != nil {
						return
					}
					if failed {
						g.AvailableActions = []string{"CANCEL_EXECUTION"}
						g.Set(string(phase), "CANCEL_EXECUTION", "Workspace preparation failed and is settled. Cancel the exact Task to abandon this attempt; retained artifacts will be preserved.")
					}

				case "RECOVERY_REQUIRED":
					g.Set("RECOVERY_REQUIRED", "RECOVER", "Upper Review authorization expired; explicitly recover the exact Review.")
				}
			}
			switch g.CurrentWorkflowReview.Status {
			case "SUCCEEDED":
				if g.RawResultAvailable && g.FormalResultStatus == "NOT_AVAILABLE" {
					g.Set("REVIEW_MATERIALIZING", "WAIT_FOR_REVIEW_MATERIALIZATION", "Core is materializing the exact Review result.")
				} else if !g.RawResultAvailable {
					g.AvailableActions = []string{"REQUEST_RE_REVIEW", "ABANDON_TASK"}
					g.Set(string(phase), "RETRY_REVIEW_OR_ABANDON", "The Review completed without a usable result. Request another Review or abandon this Task.")
				}
			case "FAILED", "CANCELED", "INFRASTRUCTURE_FAILED":
				g.AvailableActions = []string{"REQUEST_RE_REVIEW", "ABANDON_TASK"}
				g.Set(string(phase), "RETRY_REVIEW_OR_ABANDON", "The Review did not complete successfully. Request another Review or abandon this Task.")
			}
		}
	case task.PhaseReviewApprovalPending:
		var a wp.Approval
		var raw string
		var created sql.NullString
		err = q.QueryRowContext(ctx, `SELECT decision_id,review_id,task_id,start_request_id,provenance_sha256,result_sha256,result_text,created_at FROM review_approval_decisions WHERE review_id=? AND task_id=?`, g.ReviewID, g.TaskID).Scan(&a.DecisionID, &a.ReviewID, &a.TaskID, &a.StartRequestID, &a.ProvenanceSHA256, &a.ResultSHA256, &raw, &created)
		if err != nil {
			return
		}
		if created.Valid && created.String != "" {
			var at time.Time
			at, err = parseTime(created.String)
			if err != nil {
				return
			}
			if !at.IsZero() {
				a.CreatedAt = &at
			}
		}
		g.FormalResultStatus = "VALID"
		g.FormalResultAvailable = true
		snapshot, snapshotErr := reviewDecisionSnapshotTx(ctx, q, wp.DecisionID(g.ReviewID))
		if snapshotErr != nil {
			err = snapshotErr
			return
		}
		// The reducer, not this projection, owns the Decision status and version.
		a.Status = snapshot.ProjectedStatus()
		a.DecisionVersion = snapshot.Version
		a.ReasonCode = snapshot.ReasonCode
		g.ReviewConfirmationStatus = a.Status
		if g.CurrentWorkflowReview != nil {
			g.ReviewDisposition = g.CurrentWorkflowReview.ReviewDisposition
		}
		if g.CurrentWorkflowReview != nil && g.CurrentWorkflowReview.ReviewDisposition == review.DispositionFurtherReviewRequired {
			result, e := review.ParseResult(raw)
			if e != nil {
				err = e
				return
			}
			a.Summary = result.Summary
			a.AllowedActions = []string{"REVISE", "REJECT"}
			g.AvailableActions = []string{"REQUEST_RE_REVIEW", "ABANDON_TASK"}
			g.Set(string(phase), "REVIEW_CONFIRMATION_REQUIRED", "Confirm whether to request the required follow-up Review or abandon this Task.")
		} else {
			proposal, e := parsePersistedApprovalResult(raw)
			if e != nil {
				err = e
				return
			}
			a.Proposal = &proposal
			a.Summary = proposal.Summary
			a.AllowedActions = []string{"APPROVE", "REVISE", "REJECT"}
			if proposal.WorkSpec.ImplementationMode == task.ImplementationCodingAgent {
				a.AllowedActions = append(a.AllowedActions, "APPROVE_AND_START")
			}
			g.AvailableActions = a.AllowedActions
			g.Set(string(phase), "REVIEW_CONFIRMATION_REQUIRED", "Confirm the current Task review result.")
		}
		g.Approval = &a
		g.ReviewConfirmation = &a
		g.DecisionID = a.DecisionID
	case task.PhaseNoRunSuccessorRequired:
		if _, evidenceErr := readNoRunSuccessorFacts(ctx, q, g.TaskID); evidenceErr != nil {
			err = evidenceErr
			return
		}
		g.AvailableActions = []string{"STOP_AND_REVISE", "CANCEL_TASK", "READ"}
		if _, sourceErr := readSuccessorSourcePlan(ctx, q, g.ExecutionID); sourceErr == nil {
			g.AvailableActions = append([]string{wp.NoRunSuccessorAction}, g.AvailableActions...)
			g.Set(wp.NoRunSuccessorState, wp.NoRunSuccessorAction, "前一执行已在创建 Run 前取消；需要准备后继执行。")
		} else if errors.Is(sourceErr, sql.ErrNoRows) || errors.Is(sourceErr, ErrConflict) {
			g.Set(wp.NoRunSuccessorState, "STOP_AND_REVISE", "原执行缺少可复用的运行授权；请修订任务后重新评审。")
		} else {
			err = sourceErr
			return
		}
	case task.PhaseImplementationPreparing:
		g.Set("TASK_IMPLEMENTATION_PREPARING", "WAIT_FOR_IMPLEMENTATION_PREPARATION", "后继执行已接纳，等待运行准备。")
	case task.PhaseImplementationReady:
		var needsConfirmation bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_review_skips WHERE task_id=? AND approved_revision_id IS NULL)`, g.TaskID).Scan(&needsConfirmation); err != nil {
			return
		}
		if needsConfirmation {
			g.AvailableActions = []string{"CONFIRM_AND_START_IMPLEMENTATION"}
			g.Set(string(phase), "CONFIRM_AND_START_IMPLEMENTATION", "已跳过审查，等待用户一次确认完整实施、验证方案及运行配置并开始实施。")
		} else {
			g.Set(string(phase), "START_EXECUTION", "Start implementation of the approved Task revision.")
		}
	case task.PhaseImplementing:
		g.Set(string(phase), "WAIT_FOR_IMPLEMENTATION", "Wait for implementation.")
	case task.PhaseVerificationReady:
		g.Set(string(phase), "START_VERIFICATION", "Continue verification of the current Task revision.")
	case task.PhaseVerifying:
		g.Set(string(phase), "WAIT_FOR_VERIFICATION", "Wait for verification.")
	case task.PhaseAcceptancePending:
		g.DecisionID = decisionresolution.TaskAcceptanceDecisionID(g.ExecutionID)
		g.Set(string(phase), "RESOLVE_TASK_ACCEPTANCE", "Assess the exact current Execution evidence.")
	case task.PhaseDelivering:
		g.Set("TASK_DELIVERING", "RETRY_GIT_FINALIZATION", "Acceptance is durable; observe or retry the exact frozen Git finalization. No new agent work is admitted.")
	case task.PhaseRecoveryRequired:
		g.Set("TASK_RECOVERY_REQUIRED", "RESOLVE_TASK_RECOVERY", "The durable lifecycle requires a formal recovery resolution before progression.")
		var scopeConflict bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposals e JOIN runs r ON r.id=COALESCE(e.verification_run_id,e.run_id) WHERE e.id=? AND e.task_id=? AND e.project_id=? AND e.task_definition_revision_id=? AND r.task_id=e.task_id AND r.project_id=e.project_id AND r.execution_proposal_id=e.id AND r.operation_kind='EXECUTION' AND r.operation_id=e.id AND r.purpose='IMPLEMENTATION' AND r.status IN ('FAILED','INFRASTRUCTURE_FAILED') AND r.failure_code='SCOPE_CONFLICT')`, g.ExecutionID, g.TaskID, g.ProjectID, g.TaskDefinitionRevisionID).Scan(&scopeConflict); err != nil {
			return
		}
		if scopeConflict {
			g.Set("TASK_RECOVERY_REQUIRED", "RESOLVE_TASK_RECOVERY", "实施因范围冲突停止。先保留已改文件并核对原运行、获批范围及实际变更，再按 Core 当前恢复资格处理；范围不变且核验允许才可同版本续作，范围变化须正式确认新方案。是否补审取决于工程判断与准确资格，不得直接重试、启动验证或验收。")
		}
	default:
		err = errors.New("invalid durable Task phase")
	}
	// Operation recovery and lifecycle block capabilities, never replace the
	// durable Task phase or pointers in the returned facts.
	switch life {
	case "RETIRING":
		g.Set("FEATURE_AREA_RETIRING", "WAIT_FOR_RETIREMENT", "FeatureArea is closing; no engineering advancement is allowed.")
		return
	case "RETIRED", "DECOMPOSED":
		g.Set(life, "WORKFLOW_CLOSED", "This FeatureArea is closed.")
		return
	}
	if phase == task.PhaseImplementing || phase == task.PhaseVerifying || phase == task.PhaseImplementationReady || phase == task.PhaseVerificationReady {
		var terminal bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposals e WHERE e.id=? AND e.outcome IN ('SUCCEEDED','FAILED','CANCELED') AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.feature_area_id=?))`, g.ExecutionID, areaID).Scan(&terminal); err != nil {
			return
		}
		if terminal {
			g.DecisionID = decisionresolution.TaskAcceptanceDecisionID(g.ExecutionID)
			g.Set(string(phase), "RESOLVE_TASK_ACCEPTANCE", "Assess the interrupted Execution or request a new candidate; Task phase is preserved.")
		}
	}
	if (phase == task.PhaseReviewing || phase == task.PhaseVerifying) && g.RunID != "" {
		var analysisRequired bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN run_execution_records er ON er.run_id=r.id WHERE r.id=? AND r.status='SUCCEEDED' AND er.commands_json IS NOT NULL AND NOT EXISTS(SELECT 1 FROM run_analyses a WHERE a.run_id=r.id) AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.feature_area_id=r.feature_area_id) AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.run_id=r.id AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED','CANCELED')))`, g.RunID).Scan(&analysisRequired); err != nil {
			return
		}
		if analysisRequired {
			g.DecisionID = ""
			g.AvailableActions = []string{}
			g.Set("RUN_ANALYSIS_REQUIRED", "ANALYZE_RUN_RESULT", "Codex 已执行完成，等待 GPT 分析。读取 run_analysis_get 的原始输出与执行证据，再提交 run_analysis_submit；尚未生成用户审批或验收决定。")
		}
	}
	return
}

// Resolve applies the current Task review gate; it never creates a Task.
func (s *WorkflowProgressionStore) Resolve(ctx context.Context, in wp.ResolveInput) (wp.Resolution, error) {
	return s.resolve(ctx, in, nil)
}

// Only the formal producer can enter this private T1 branch. The transaction
// independently authenticates the original confirmation and platform binding.
func (s *WorkflowProgressionStore) resolve(ctx context.Context, in wp.ResolveInput, formal *formalExecutionT1Input) (out wp.Resolution, err error) {
	var inheritedRuntime *codexruntime.NextRunRuntimeConfig
	var inheritedBackend *codexconfig.BackendSelection
	defer func() {
		if err == nil && !out.Replay && ((out.StartEffect == wp.StartEffectStartAuthorized && out.Task != nil && out.Task.WorkSpec.ImplementationMode == task.ImplementationCodingAgent && out.Task.CurrentExecutionID != "") || out.SuccessorReviewID != "") {
			s.wakeApproval()
		}
	}()
	if in.StartEffect == "" {
		in.StartEffect = wp.StartEffectNoStart
	}
	if !in.StartEffect.Valid() {
		return out, decisionresolution.ErrInvalidArgument
	}
	if in.Option != "APPROVE" && in.StartEffect != wp.StartEffectNoStart || in.StartEffect == wp.StartEffectNoStart && in.StartRequestID != "" || in.StartEffect == wp.StartEffectStartAuthorized && strings.TrimSpace(in.StartRequestID) == "" {
		return out, decisionresolution.ErrInvalidArgument
	}
	out.StartEffect = in.StartEffect
	if in.Option != "APPROVE" && in.Option != "REVISE" && in.Option != "REJECT" {
		return out, decisionresolution.ErrInvalidArgument
	}
	if in.Option == "REVISE" && !wp.ValidReviewFeedback(in.ReviewFeedback) {
		return out, decisionresolution.ErrInvalidArgument
	}
	if in.Option != "REVISE" && in.ReviewFeedback != "" || in.Option != "APPROVE" && (in.NextRunRuntimeConfig != nil || in.BackendSelection != nil || in.ExecutionInstructions != nil || in.ConfirmationIntentID != "" && in.Option != "REVISE") {
		return out, decisionresolution.ErrInvalidArgument
	}
	in.AISessionID, in.OperatorPrincipalID = strings.TrimSpace(in.AISessionID), strings.TrimSpace(in.OperatorPrincipalID)
	if (in.AISessionID == "") == (in.OperatorPrincipalID == "") {
		return out, decisionresolution.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if in.OperatorPrincipalID != "" && in.ConfirmationIntentID != "" {
		return out, decisionresolution.ErrInvalidArgument
	}
	if in.OperatorPrincipalID == "" && (in.ExecutionInstructions != nil || in.ConfirmationIntentID != "") {
		if err = instructionCaller(ctx, tx, in.AISessionID); err != nil {
			return out, err
		}
	}
	var project, raw, provenance, source, taskID string
	if in.OperatorPrincipalID != "" {
		actor, ok := localoperator.FromContext(ctx)
		if !ok || actor.PrincipalID() != in.OperatorPrincipalID || actor.ProjectID() == "" {
			return out, decisionresolution.ErrNotFound
		}
		project = actor.ProjectID()
	} else if err = tx.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, in.AISessionID).Scan(&project); err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, `SELECT task_id,review_id,result_text,provenance_json FROM review_approval_decisions WHERE decision_id=? AND project_id=?`, in.DecisionID, project).Scan(&taskID, &source, &raw, &provenance)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, wp.Reject("STALE_REVIEW_APPROVAL", out.Guidance)
		}
		return out, err
	}
	if in.TaskID != "" && in.TaskID != taskID || in.ReviewID != "" && in.ReviewID != source {
		return out, wp.Reject("STALE_REVIEW_APPROVAL", out.Guidance)
	}
	value, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, taskID))
	if err != nil {
		return out, err
	}
	if in.FeatureAreaID != "" && in.FeatureAreaID != value.FeatureAreaID {
		return out, wp.Reject("STALE_REVIEW_APPROVAL", out.Guidance)
	}
	in.TaskID, in.ReviewID, in.FeatureAreaID = taskID, source, value.FeatureAreaID
	invalidResolution := in.DecisionID == wp.InvalidResultDecisionID(source)
	decisionKind, subjectKind := wp.KindReviewApproval, "TASK_REVIEW_GATE"
	if invalidResolution {
		decisionKind, subjectKind = wp.KindInvalidReviewResultResolution, "INVALID_REVIEW_RESULT"
		if in.Option == "APPROVE" {
			return out, wp.Reject("INVALID_REVIEW_RESULT_NOT_APPROVABLE", out.Guidance)
		}
	}
	request := decisionresolution.Request{DecisionID: in.DecisionID, Kind: decisionKind, SubjectKind: subjectKind, SubjectID: taskID, SourceReviewID: source, Option: in.Option, RequestID: in.RequestID}
	if in.OperatorPrincipalID != "" {
		request.ActorKind, request.ActorID = "LOCAL_OPERATOR", in.OperatorPrincipalID
	} else {
		request.ActorKind, request.ActorID = "AI_SESSION", in.AISessionID
	}
	if err = decisionresolution.ValidateRequest(request); err != nil {
		return out, err
	}
	ctx, attempt := decisionresolution.WithAttempt(ctx, request)
	receipt, handled, err := preflightDecisionResolutionTx(ctx, tx, request, project)
	if err != nil {
		return out, err
	}
	if handled {
		if formal != nil {
			return out, fe.ErrConflict
		}
		if in.Option == "REVISE" {
			var feedbackHash string
			var successorReview, successorRun, startRequest string
			table := "review_re_review_requests"
			if invalidResolution {
				table = "review_invalid_result_resolutions"
			}
			queryErr := tx.QueryRowContext(ctx, `SELECT feedback_sha256,successor_review_id,successor_run_id,start_request_id FROM `+table+` WHERE decision_id=? AND request_id=?`, in.DecisionID, in.RequestID).Scan(&feedbackHash, &successorReview, &successorRun, &startRequest)
			if queryErr != nil {
				if errors.Is(queryErr, sql.ErrNoRows) {
					return out, executionproposal.ErrConflict
				}
				return out, queryErr
			}
			if feedbackHash != workflowHash(in.ReviewFeedback) {
				return out, executionproposal.ErrConflict
			}
			out.SuccessorReviewID, out.SuccessorRunID, out.ReviewStartRequestID = successorReview, successorRun, startRequest
		}
		if err = confirmationApprovalReplayTx(ctx, tx, in); err != nil {
			return out, err
		}
		_, _, _, conflict := attempt.Snapshot()
		if conflict {
			return out, decisionresolution.ErrInvalidArgument
		}
		if err = checkApprovalInstructionReplay(ctx, tx, in.DecisionID, in.ExecutionInstructions); err != nil {
			return out, err
		}
		out.Receipt = receipt
		if in.Option == "APPROVE" {
			var encoded, encodedBackend sql.NullString
			if queryErr := tx.QueryRowContext(ctx, `SELECT runtime_config_json,backend_selection_json FROM review_approval_materializations WHERE decision_id=?`, in.DecisionID).Scan(&encoded, &encodedBackend); queryErr != nil {
				return out, queryErr
			}
			var persisted codexruntime.NextRunRuntimeConfig
			if encoded.Valid && encoded.String != "" {
				if decodeErr := json.Unmarshal([]byte(encoded.String), &persisted); decodeErr != nil {
					return out, decodeErr
				}
			}
			if in.NextRunRuntimeConfig != nil {
				if !matchesReplayRuntimeConfig(*in.NextRunRuntimeConfig, persisted) {
					return out, executionproposal.ErrConflict
				}
			}
			persistedBackend, decodeErr := decodeBackendSelection(encodedBackend)
			if decodeErr != nil {
				return out, decodeErr
			}
			if (in.BackendSelection == nil) != (persistedBackend == nil) || in.BackendSelection != nil && (in.BackendSelection.ConfigID != persistedBackend.ConfigID || in.BackendSelection.ConfigFingerprint != persistedBackend.ConfigFingerprint) {
				return out, executionproposal.ErrConflict
			}
			candidateID := "execution_approved_" + workflowHash(taskID + "\x00" + in.DecisionID)[:32]
			var revisionID string
			e := tx.QueryRowContext(ctx, `SELECT task_definition_revision_id FROM execution_proposals WHERE id=? AND task_id=? AND project_id=?`, candidateID, taskID, project).Scan(&revisionID)
			if e == nil {
				requestID, action := in.RequestID, "APPROVE"
				if in.StartEffect == wp.StartEffectStartAuthorized {
					requestID, action = in.StartRequestID, "START_EXECUTION"
				}
				if e = checkDecisionStartEffectReplayTx(ctx, tx, executionStartEffectRecord{ProjectID: project, ActorKind: request.ActorKind, ActorID: request.ActorID, RequestID: requestID, Action: action, Effect: in.StartEffect, TaskID: taskID, FeatureAreaID: value.FeatureAreaID, RevisionID: revisionID, ExecutionID: candidateID, Phase: task.PhaseImplementationReady, DecisionID: in.DecisionID}); e != nil {
					return out, e
				}
			} else if !errors.Is(e, sql.ErrNoRows) {
				return out, e
			}
		}
		if in.AISessionID != "" {
			originalAccess, accessErr := readTaskFileAuthorization(ctx, tx, ` WHERE decision_id=? AND ai_session_id=?`, in.DecisionID, in.AISessionID)
			if accessErr == nil {
				access, e := currentTaskFileState(ctx, tx, originalAccess)
				if e != nil {
					return out, e
				}
				out.DirectFile = &access
			} else if !errors.Is(accessErr, sql.ErrNoRows) {
				return out, accessErr
			}
		}
		out.Task = &value
		out.Replay = true
		out.Guidance, err = workflowTaskStateTx(ctx, tx, project, value.FeatureAreaID, value.ID)
		if err == nil {
			err = s.applyDirectFileGuidance(ctx, tx, &out.Guidance)
		}
		return out, err
	}
	out.Guidance, err = workflowTaskStateTx(ctx, tx, project, value.FeatureAreaID, value.ID)
	if err != nil {
		return out, err
	}
	if err = taskActionAdmissionTx(ctx, tx, value.ID); err != nil {
		return out, err
	}
	// A new approval on an already approved Task requires the explicit current
	// supplemental transition and its exact newly started Review. Historical
	// approval receipts above remain readable without restoring this authority.
	if value.ReviewApprovalID != "" && value.ReviewApprovalID != in.DecisionID {
		var supplemental bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_supplemental_review_current cur JOIN task_supplemental_review_requests req ON req.id=cur.request_id JOIN task_supplemental_review_starts started ON started.request_id=req.id WHERE cur.task_id=? AND req.task_id=cur.task_id AND req.project_id=? AND req.source_revision_id=? AND started.review_id=?)`, value.ID, project, value.CurrentDefinitionRevisionID, source).Scan(&supplemental)
		if err != nil {
			return out, err
		}
		if !supplemental {
			supplemental, err = noRunRevisionReviewPendingTx(ctx, tx, value.ID)
			if err != nil {
				return out, err
			}
		}
		if !supplemental {
			return out, wp.Reject("STALE_REVIEW_APPROVAL", out.Guidance)
		}
	}
	// Use the same current-authority facts as the actionable projection. Keep
	// receipt replay above this gate so response loss never reopens authority.
	authority, err := readRuntimeAuthority(ctx, tx, value.FeatureAreaID)
	if err != nil {
		return out, err
	}
	if authority.Busy {
		return out, wp.Reject("FEATURE_AREA_BUSY", out.Guidance)
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, value.FeatureAreaID); err != nil {
		return out, err
	}
	validPhase := value.Phase == task.PhaseReviewApprovalPending || invalidResolution && value.Phase == task.PhaseReviewing
	if value.Status != task.StatusActive || !validPhase || value.CurrentReviewID != source {
		return out, wp.Reject("STALE_REVIEW_APPROVAL", out.Guidance)
	}
	var conclusion review.Disposition
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(review_disposition,'') FROM reviews WHERE id=? AND task_id=?`, source, taskID).Scan(&conclusion); err != nil {
		return out, err
	}
	if !invalidResolution && in.Option == "APPROVE" && conclusion != review.DispositionApprovalReady {
		return out, wp.Reject("REVIEW_APPROVAL_NOT_AVAILABLE", out.Guidance)
	}
	if !invalidResolution && in.Option != "APPROVE" && conclusion != review.DispositionApprovalReady && conclusion != review.DispositionFurtherReviewRequired {
		return out, wp.Reject("STALE_REVIEW_APPROVAL", out.Guidance)
	}
	var eligible bool
	if invalidResolution {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM reviews v JOIN runs r ON r.id=v.linked_run_id JOIN review_result_materializations m ON m.review_id=v.id AND m.run_id=r.id JOIN feature_areas f ON f.id=v.feature_area_id WHERE v.id=? AND v.task_id=? AND r.status='SUCCEEDED' AND r.result_text=? AND m.outcome IN ('INVALID','SCOPE_CONFLICT') AND m.raw_result_sha256=? AND f.status IN ('DISCOVERING','ACTIVE') AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.feature_area_id=f.id))`, source, taskID, raw, workflowHash(raw)).Scan(&eligible)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM reviews v JOIN runs r ON r.id=v.linked_run_id LEFT JOIN review_result_materializations m ON m.review_id=v.id AND m.run_id=r.id JOIN feature_areas f ON f.id=v.feature_area_id WHERE v.id=? AND v.task_id=? AND r.status='SUCCEEDED' AND ((?='APPROVE' AND v.review_disposition='APPROVAL_READY') OR (?<>'APPROVE' AND v.review_disposition IN ('APPROVAL_READY','FURTHER_REVIEW_REQUIRED'))) AND ((m.outcome='VALID' AND m.formal_result_sha256=?) OR (m.review_id IS NULL AND r.result_text=?)) AND f.status IN ('DISCOVERING','ACTIVE')
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.feature_area_id=f.id)
	 AND NOT EXISTS(SELECT 1 FROM task_review_sources s JOIN development_cycles c ON c.id=s.development_cycle_id WHERE s.review_id=v.id AND c.superseded_at IS NOT NULL))`, source, taskID, in.Option, in.Option, workflowHash(raw), raw).Scan(&eligible)
	}
	if err != nil {
		return out, err
	}
	if !eligible {
		return out, wp.Reject("STALE_REVIEW_APPROVAL", out.Guidance)
	}
	var snap approvalSnapshot
	if err = json.Unmarshal([]byte(provenance), &snap); err != nil {
		return out, err
	}
	if snap.TaskID != taskID || snap.ReviewID != source || snap.ResultSHA256 != workflowHash(raw) {
		return out, wp.Reject("STALE_REVIEW_APPROVAL", out.Guidance)
	}
	if in.Option == "APPROVE" {
		proposal, parseErr := confirmedApprovalResultTx(ctx, tx, raw, in)
		if parseErr != nil {
			return out, parseErr
		}
		if in.BackendSelection != nil {
			var id, name, path string
			var enabled int
			if err = tx.QueryRowContext(ctx, `SELECT id,name,codex_path,enabled FROM codex_configs WHERE id=?`, in.BackendSelection.ConfigID).Scan(&id, &name, &path, &enabled); err != nil || enabled == 0 {
				return out, wp.Reject("BACKEND_SELECTION_INVALID", out.Guidance)
			}
			config := codexconfig.Config{ID: id, Name: name, CodexPath: path}
			if config.Fingerprint() != in.BackendSelection.ConfigFingerprint {
				return out, wp.Reject("BACKEND_SELECTION_INVALID", out.Guidance)
			}
			selected := config.Selection("REVIEW_APPROVAL")
			if formal == nil {
				in.BackendSelection = &selected
			}
		}
		if proposal.WorkSpec.ImplementationMode != task.ImplementationCodingAgent && proposal.WorkSpec.ImplementationMode != task.ImplementationDirectFile {
			return out, wp.Reject("IMPLEMENTATION_MODE_REQUIRED", out.Guidance)
		}
		if proposal.Version == 2 && proposal.WorkSpec.ImplementationMode == task.ImplementationCodingAgent {
			// Preserve the Review's frozen selection as approval provenance. For
			// NO_START this is audit history only: a later Implementation must use
			// its own exact START_APPROVED selection. Runtime-authorized approval
			// creates its candidate from this source in this same transaction.
			sourceReview, reviewErr := scanReview(tx.QueryRowContext(ctx, reviewSelect+` WHERE r.id=? AND r.task_id=?`, source, taskID))
			if reviewErr != nil {
				return out, fmt.Errorf("load frozen v2 Review runtime selection: %w", errors.Join(reviewErr, decisionresolution.ErrInvalidArgument))
			}
			if sourceReview.BackendSelection != nil {
				selection := *sourceReview.BackendSelection
				if in.BackendSelection != nil && (in.BackendSelection.ConfigID != selection.ConfigID || in.BackendSelection.ConfigFingerprint != selection.ConfigFingerprint) {
					return out, fmt.Errorf("v2 Review backend selection mismatch: %w", decisionresolution.ErrInvalidArgument)
				}
				inheritedBackend = &selection
			}
			if sourceReview.RuntimeConfig.Model != "" || sourceReview.RuntimeConfig.ReasoningEffort != "" {
				config := sourceReview.RuntimeConfig
				if in.NextRunRuntimeConfig != nil && *in.NextRunRuntimeConfig != config {
					return out, fmt.Errorf("v2 Review runtime selection mismatch: %w", decisionresolution.ErrInvalidArgument)
				}
				inheritedRuntime = &config
			}
		}
		if proposal.Version == 2 {
			if err = validateApprovedReviewTargetsTx(ctx, tx, source, proposal.RepositoryTargets); err != nil {
				return out, wp.Reject("STALE_REVIEW_APPROVAL", out.Guidance)
			}
		}
		if proposal.Version != 2 && proposal.WorkSpec.ImplementationMode == task.ImplementationCodingAgent && in.BackendSelection == nil {
			return out, wp.Reject("BACKEND_SELECTION_REQUIRED", out.Guidance)
		}
		if proposal.WorkSpec.ImplementationMode == task.ImplementationDirectFile && proposal.VerificationSpec.Mode != task.VerificationModeRun && in.BackendSelection != nil {
			return out, wp.Reject("BACKEND_SELECTION_NOT_APPLICABLE", out.Guidance)
		}
		if proposal.Version != 2 && proposal.WorkSpec.ImplementationMode == task.ImplementationCodingAgent {
			if err = validateFeatureAreaBackendContinuityTx(ctx, tx, value.FeatureAreaID, in.BackendSelection); err != nil {
				return out, wp.Reject("FEATURE_AREA_BACKEND_CONTINUITY_CONFLICT", out.Guidance)
			}
		}
		if proposal.Version != 2 && proposal.WorkSpec.ImplementationMode == task.ImplementationDirectFile && proposal.VerificationSpec.Mode == task.VerificationModeRun {
			if in.BackendSelection == nil || in.NextRunRuntimeConfig == nil {
				return out, wp.Reject("CODEX_VERIFICATION_SELECTION_REQUIRED", out.Guidance)
			}
			if err = validateFeatureAreaBackendContinuityTx(ctx, tx, value.FeatureAreaID, in.BackendSelection); err != nil {
				return out, err
			}
			frozen, configErr := s.freezeRuntimeConfig(value.Model, in.NextRunRuntimeConfig)
			if configErr != nil {
				return out, configErr
			}
			in.NextRunRuntimeConfig = &frozen
		} else if proposal.Version == 2 && in.StartEffect == wp.StartEffectNoStart {
			// Preserve the definition-only compatibility path without inventing
			// a runtime selection. APPROVE_AND_START has an explicit start effect
			// and therefore continues through Coding runtime validation below.
		} else if proposal.WorkSpec.ImplementationMode == task.ImplementationDirectFile {
			if in.NextRunRuntimeConfig != nil {
				return out, decisionresolution.ErrInvalidArgument
			}
		} else {
			if in.OperatorPrincipalID != "" && in.NextRunRuntimeConfig == nil {
				return out, decisionresolution.ErrInvalidArgument
			}
			frozen, configErr := s.freezeRuntimeConfig(value.Model, in.NextRunRuntimeConfig)
			if configErr != nil {
				return out, decisionresolution.ErrInvalidArgument
			}
			in.NextRunRuntimeConfig = &frozen
		}
	}
	var executionBasis *fe.Archive
	if formal != nil {
		if in.Option != "APPROVE" || in.OperatorPrincipalID != "" || in.StartEffect != wp.StartEffectNoStart {
			return out, fe.ErrConflict
		}
		executionBasis, err = prepareFormalExecutionT1Tx(ctx, tx, formal, value)
		if err != nil {
			return out, err
		}
	}
	formalBasis, err := prepareDefinitionApprovalBusinessTx(ctx, tx, in, value)
	if err != nil {
		return out, err
	}
	now := s.now()
	out.Receipt, err = insertDecisionResolutionTx(ctx, tx, request, project, decisionresolution.ResultTask, taskID, now)
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_approval_receipt_context(decision_id,ai_session_id,actor_kind,actor_id,created_at) VALUES(?,?,?,?,?)`, in.DecisionID, nullString(in.AISessionID), nullString(request.ActorKind), nullString(request.ActorID), formatTime(now)); err != nil {
		return out, err
	}
	switch in.Option {
	case "APPROVE":
		authorSession := in.AISessionID
		proposal, e := confirmedApprovalResultTx(ctx, tx, raw, in)
		if e != nil {
			return out, e
		}
		// Coding approval is authored by the decision actor and needs no
		// borrowed Session. Direct File still grants the original Session author.
		if authorSession == "" && proposal.WorkSpec.ImplementationMode == task.ImplementationDirectFile {
			if err = tx.QueryRowContext(ctx, `SELECT COALESCE(created_by_ai_session_id,'') FROM reviews WHERE id=? AND task_id=? AND project_id=?`, source, taskID, project).Scan(&authorSession); err != nil || authorSession == "" {
				if err == nil {
					err = decisionresolution.ErrInvalidArgument
				}
				return out, err
			}
		}
		proposal.WorkSpec, proposal.VerificationSpec, e = task.ApplyExecutionInstructions(proposal.WorkSpec, proposal.VerificationSpec, in.ExecutionInstructions)
		if e != nil {
			return out, e
		}
		work, e := encodeWorkSpec(proposal.WorkSpec)
		if e != nil {
			return out, e
		}
		if e = task.ValidateExecutableVerificationSpec(proposal.VerificationSpec); e != nil {
			return out, e
		}
		verification, e := encodeVerificationSpec(proposal.VerificationSpec)
		if e != nil {
			return out, e
		}
		constraints, e := encodeStringItems(proposal.WorkSpec.Constraints)
		if e != nil {
			return out, e
		}
		var model, effort string
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(model,''),COALESCE(reasoning_effort,'') FROM reviews WHERE id=? AND task_id=?`, source, taskID).Scan(&model, &effort); err != nil {
			return out, err
		}
		if model != "" {
			value.Model = model
		}
		if effort != "" {
			value.ReasoningEffort = effort
		}
		// Original intent remains immutable; approved specs provide implementation authority.
		value.ReviewApprovalID = in.DecisionID
		value.RepositoryTargets = workset.CloneTargets(proposal.RepositoryTargets)
		value.PrimaryMemberID = proposal.PrimaryMemberID
		value.WorkSpec = proposal.WorkSpec
		value.VerificationSpec = proposal.VerificationSpec
		value.Goal = proposal.WorkSpec.Goal
		value.Constraints = proposal.WorkSpec.Constraints
		if _, err = tx.ExecContext(ctx, `UPDATE feature_areas SET status='ACTIVE',updated_at=? WHERE id=? AND status='DISCOVERING'`, formatTime(now), value.FeatureAreaID); err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET review_approval_id=?,goal=?,constraints_json=?,work_spec_json=?,verification_spec_json=?,model=?,reasoning_effort=?,updated_at=? WHERE id=? AND current_review_id=? AND phase='REVIEW_APPROVAL_PENDING' AND status='ACTIVE'`, in.DecisionID, value.Goal, constraints, work, verification, nullString(value.Model), nullString(value.ReasoningEffort), formatTime(now), taskID, source); err != nil {
			return out, err
		}
		if _, err = ensureTaskDefinitionTx(ctx, tx, &value, now); err != nil {
			return out, err
		}
		if in.ConfirmationIntentID != "" {
			if _, err = tx.ExecContext(ctx, `INSERT INTO confirmation_approval_sources(decision_id,intent_id,revision_id) VALUES(?,?,?)`, in.DecisionID, in.ConfirmationIntentID, value.CurrentDefinitionRevisionID); err != nil {
				return out, err
			}
		}
		authorInput := in
		authorInput.AISessionID = authorSession
		if proposal.Version != 2 {
			if err = recordApprovalInstructionsTx(ctx, tx, value, authorInput, now); err != nil {
				return out, err
			}
		}
		var runtimeConfig codexruntime.NextRunRuntimeConfig
		if in.NextRunRuntimeConfig != nil {
			runtimeConfig, err = freezeExecutionRuntimeConfig(*in.NextRunRuntimeConfig)
		} else if inheritedRuntime != nil {
			runtimeConfig, err = freezeExecutionRuntimeConfig(*inheritedRuntime)
			if err != nil {
				return out, err
			}
		}
		runtimeJSON, configErr := encodeExecutionRuntimeConfig(runtimeConfig)
		if configErr != nil {
			return out, configErr
		}
		backend := in.BackendSelection
		if backend == nil {
			backend = inheritedBackend
		}
		backendJSON, encodeErr := encodeBackendSelection(backend)
		if encodeErr != nil {
			return out, encodeErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO review_approval_materializations(decision_id,task_id,initial_revision_id,ai_session_id,actor_kind,actor_id,created_at,runtime_config_json,backend_selection_json) VALUES(?,?,?,?,?,?,?,?,?)`, in.DecisionID, taskID, value.CurrentDefinitionRevisionID, nullString(authorSession), request.ActorKind, request.ActorID, formatTime(now), runtimeJSON, backendJSON); err != nil {
			return out, err
		}
		targetPhase := task.PhaseImplementationReady
		if executionBasis != nil {
			targetPhase = task.PhaseImplementationPreparing
			if err = sealFormalExecutionT1Tx(ctx, tx, executionBasis, value, formatTime(now)); err != nil {
				return out, err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET phase=?,required_review_depth=NULL WHERE id=?`, string(targetPhase), taskID)
		if executionBasis != nil {
			value.Phase = targetPhase
		} else if err == nil && proposal.Version == 2 && in.StartEffect == wp.StartEffectNoStart {
			// Definition-only approval remains available for compatibility. The
			// product APPROVE_AND_START path supplies START_AUTHORIZED and takes
			// the Coding branch below in the same transaction.
			value.Phase = task.PhaseImplementationReady
		} else if err == nil && proposal.WorkSpec.ImplementationMode == task.ImplementationDirectFile {
			value.Phase = task.PhaseImplementationReady
			access, e := materializeTaskFileAuthorizationTx(ctx, tx, value, authorSession, in.DecisionID, false, now)
			if e != nil {
				return out, e
			}
			if in.OperatorPrincipalID == "" {
				out.DirectFile = &access
			}
		} else if err == nil {
			value.Phase = task.PhaseImplementationReady
			if proposal.Version == 2 {
				err = createRuntimeAuthorizedApprovalExecutionTx(ctx, tx, value, authorSession, now)
			} else {
				err = createApprovedExecutionTx(ctx, tx, value, authorSession, now)
			}
			if err != nil {
				return out, err
			}
			requestID, action := in.RequestID, "APPROVE"
			if in.StartEffect == wp.StartEffectStartAuthorized {
				requestID, action = in.StartRequestID, "START_EXECUTION"
			}
			if err = insertExecutionStartEffectTx(ctx, tx, executionStartEffectRecord{ProjectID: project, ActorKind: request.ActorKind, ActorID: request.ActorID, RequestID: requestID, Action: action, Effect: in.StartEffect, TaskID: taskID, FeatureAreaID: value.FeatureAreaID, RevisionID: value.CurrentDefinitionRevisionID, ExecutionID: approvedExecutionID(value), Phase: task.PhaseImplementationReady, DecisionID: in.DecisionID}, formatTime(now)); err != nil {
				return out, err
			}
		}
	case "REVISE":
		previous, reviewErr := scanReview(tx.QueryRowContext(ctx, reviewSelect+` WHERE r.id=? AND r.task_id=? AND r.project_id=?`, source, taskID, project))
		if reviewErr != nil {
			return out, reviewErr
		}
		successor, reserved, startRequest, _, successorErr := reReviewSuccessorTx(ctx, s.db, tx, previous, in.ReviewFeedback, in.RequestID, in.DecisionID, formatTime(now), invalidResolution)
		if successorErr != nil {
			return out, successorErr
		}
		out.SuccessorReviewID, out.SuccessorRunID, out.ReviewStartRequestID = successor.ID, reserved.ID, startRequest
	case "REJECT":
		if invalidResolution {
			var outcome, rawHash string
			if err = tx.QueryRowContext(ctx, `SELECT outcome,raw_result_sha256 FROM review_result_materializations WHERE review_id=?`, source).Scan(&outcome, &rawHash); err != nil {
				return out, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO review_invalid_result_resolutions(decision_id,task_id,previous_review_id,previous_review_ordinal,source_outcome,source_raw_result_sha256,request_id,action,created_at) SELECT ?,?,?,ordinal,?,?,?,'ABANDON_TASK',? FROM reviews WHERE id=?`, in.DecisionID, taskID, source, outcome, rawHash, in.RequestID, formatTime(now), source); err != nil {
				return out, err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET status='CANCELED',required_review_depth=NULL,updated_at=? WHERE id=?`, formatTime(now), taskID)
	}
	if err != nil {
		return out, err
	}
	value, err = scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, taskID))
	if err != nil {
		return out, err
	}
	if err = sealDefinitionApprovalBusinessTx(ctx, tx, formalBasis, value); err != nil {
		return out, err
	}
	out.Task = &value
	out.Guidance, err = workflowTaskStateTx(ctx, tx, project, value.FeatureAreaID, value.ID)
	if err != nil {
		return out, err
	}
	if err = s.applyDirectFileGuidance(ctx, tx, &out.Guidance); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// validateApprovedTaskTx fences canonical Execution admission. A superseding Review, closing FeatureArea or changed revision fences
// advancement, while already running owners retain canonical cancellation.
// recoveryAuthorized is only set inside the formal recovery-continuation
// transaction, whose recovery-record binding is validated in the same write;
// every other admission path remains fenced by RECOVERY_REQUIRED.
func validateApprovedTaskTx(ctx context.Context, tx *sql.Tx, t task.Task, recoveryAuthorized bool) error {
	var fileRegistration bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_task_authorizations WHERE task_id=? AND task_definition_revision_id=?)`, t.ID, t.CurrentDefinitionRevisionID).Scan(&fileRegistration); err != nil {
		return err
	}
	if t.WorkSpec.ImplementationMode == task.ImplementationDirectFile || fileRegistration {
		return errors.New("DIRECT_FILE_IMPLEMENTATION: approved file work cannot start a Coding Agent")
	}
	if t.WorkSpec.ImplementationMode == "" {
		var existingCoding bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposals e WHERE e.id=? AND e.task_id=? AND e.project_id=? AND e.task_definition_revision_id=? AND e.status IN ('CONFIRMED','CONSUMED'))`, t.CurrentExecutionID, t.ID, t.ProjectID, t.CurrentDefinitionRevisionID).Scan(&existingCoding); err != nil {
			return err
		}
		if !existingCoding {
			return errors.New("IMPLEMENTATION_MODE_REQUIRED: legacy definition has no existing Coding authorization")
		}
	} else if t.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return errors.New("unsupported implementation mode")
	}

	if t.Status != task.StatusActive || t.Phase.ReviewStage() {
		return errors.New("Task review gate has not been approved")
	}
	g, err := workflowTaskStateTx(ctx, tx, t.ProjectID, t.FeatureAreaID, t.ID)
	if err != nil {
		return err
	}
	if g.CurrentState == "FEATURE_AREA_RETIRING" || g.CurrentState == "RETIRED" {
		return wp.Reject("FEATURE_AREA_RETIRING", g)
	}
	if (g.TaskPhase == task.PhaseRecoveryRequired || g.CurrentState == "RECOVERY_REQUIRED") && !recoveryAuthorized {
		return wp.Reject("EXECUTION_PREREQUISITE_NOT_SATISFIED", g)
	}
	if t.Status == task.StatusCanceled || t.Status == task.StatusFailed || t.Status == task.StatusSucceeded {
		return wp.Reject("DEVELOPMENT_TASK_NOT_READY", g)
	}
	if purpose, err := recoveryPurposeApprovedTaskTx(ctx, tx, t, g); err != nil || purpose {
		return err
	}
	var successorAuthorized bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposals e JOIN decision_resolution_receipts r ON r.result_kind='EXECUTION_PROPOSAL' AND r.result_id=e.id JOIN execution_proposals predecessor ON predecessor.id=r.subject_id WHERE e.task_id=? AND e.task_definition_revision_id=? AND predecessor.task_id=e.task_id AND r.project_id=e.project_id AND r.decision_kind='TASK_ACCEPTANCE' AND r.subject_kind='EXECUTION_PROPOSAL' AND r.option IN ('REQUEST_CHANGES','REQUEST_REVERIFICATION','RETRY_IMPLEMENTATION') AND e.id=(SELECT current_execution_id FROM tasks WHERE id=e.task_id))`, t.ID, t.CurrentDefinitionRevisionID).Scan(&successorAuthorized); err != nil {
		return err
	}
	if successorAuthorized {
		return nil
	}
	if t.ReviewApprovalID == "" {
		if approved, err := unreviewedDefinitionApprovedTx(ctx, tx, t); err != nil || approved {
			return err
		}
		return errors.New("Task review gate has not been approved")
	}
	var exact bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_approval_materializations m JOIN decision_resolution_receipts r ON r.decision_id=m.decision_id WHERE m.task_id=? AND m.decision_id=? AND m.initial_revision_id=? AND r.option='APPROVE')`, t.ID, t.ReviewApprovalID, t.CurrentDefinitionRevisionID).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		exact, err = instructionRevisionAuthorizedTx(ctx, tx, t)
		if err != nil {
			return err
		}
		if !exact {
			return wp.Reject("REVIEW_APPROVAL_REQUIRED", g)
		}
	}
	return nil
}

var _ wp.Store = (*WorkflowProgressionStore)(nil)

// Only immutable persisted approval snapshots enter this compatibility reader.
// v44 snapshots have no disposition field; migration records APPROVAL_READY on
// their source Review without rewriting any result or provenance hash.
func parsePersistedApprovalResult(raw string) (review.ImplementationResult, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return review.ImplementationResult{}, err
	}
	if _, present := object["reviewDisposition"]; !present {
		object["reviewDisposition"] = json.RawMessage(`"APPROVAL_READY"`)
		encoded, err := json.Marshal(object)
		if err != nil {
			return review.ImplementationResult{}, err
		}
		raw = string(encoded)
	}
	return review.ParseImplementationResult(raw)
}

// validateAdmittedReviewTargets preserves the separation between a Review's
// frozen read authority and the later proposed write scope. A proposal may
// narrow or grant write paths, but it cannot replace a repository, member or
// origin snapshot after the Review was admitted. The admitted set is supplied
// as the durable JSON captured with the claim, so the check is pure.
func validateAdmittedReviewTargets(admittedJSON string, workspaceBound bool, proposed []workset.Target) error {
	if err := workset.ValidateTargets(proposed); err != nil {
		return err
	}
	if !workspaceBound {
		return ErrConflict
	}
	var admitted []workset.Target
	if err := json.Unmarshal([]byte(admittedJSON), &admitted); err != nil {
		return err
	}
	if err := review.ValidateAdmittedTargets(admitted, proposed); err != nil {
		return ErrConflict
	}
	return nil
}

func validateApprovedReviewTargetsTx(ctx context.Context, tx *sql.Tx, reviewID string, proposed []workset.Target) error {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT repository_targets_json FROM reviews WHERE id=? AND workspace_id<>''`, reviewID).Scan(&raw); err != nil {
		return err
	}
	return validateAdmittedReviewTargets(raw, true, proposed)
}
