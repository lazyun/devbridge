package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

type ReviewStore struct{ db *DB }

func NewReviewStore(db *DB) *ReviewStore { return &ReviewStore{db: db} }

func (s *ReviewStore) checkReviewRunTx(ctx context.Context, tx *sql.Tx, id string) error {
	var runID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(linked_run_id,'') FROM reviews WHERE id=?`, id).Scan(&runID); err != nil {
		return err
	}
	if runID == "" {
		return review.ErrConflict
	}
	return s.db.checkRunTx(ctx, tx, runID)
}

func (s *ReviewStore) Create(ctx context.Context, v review.Review) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = insertReviewTx(ctx, tx, v); err != nil {
		return err
	}
	return tx.Commit()
}

func insertReviewTx(ctx context.Context, tx *sql.Tx, v review.Review) error {
	if v.RepositoryID != "" {
		if err := ensureRepositoryActiveTx(ctx, tx, v.ProjectID, v.RepositoryID); err != nil {
			return err
		}
	}
	for _, target := range v.RepositoryTargets {
		if err := ensureRepositoryActiveTx(ctx, tx, v.ProjectID, target.RepositoryID); err != nil {
			return err
		}
	}
	if len(v.RepositoryTargets) != 0 {
		if err := validateReviewTargetsTx(ctx, tx, v); err != nil {
			return err
		}
	}
	v.ExecutorKind = v.ExecutorKind.Effective()
	if !v.ExecutorKind.Valid() {
		return review.ErrExecutorMismatch
	}
	runtimeConfig, err := json.Marshal(v.RuntimeConfig)
	if err != nil {
		return err
	}
	var backendSelection any
	if v.BackendSelection != nil {
		encoded, e := json.Marshal(v.BackendSelection)
		if e != nil {
			return e
		}
		backendSelection = string(encoded)
	}
	var executorMetadata any
	if v.ExecutorMetadata != nil {
		data, e := json.Marshal(v.ExecutorMetadata)
		if e != nil {
			return e
		}
		executorMetadata = string(data)
	}
	if v.TaskID == "" || !v.ReviewDepth.Valid() {
		return review.ErrInvalidArgument
	}
	if err := checkReviewDepthTx(ctx, tx, v); err != nil {
		return err
	}
	var ordinal int
	var admissible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t WHERE t.id=? AND t.project_id=? AND t.feature_area_id=? AND t.repository_id IS ? AND t.task_type='DEVELOPMENT' AND t.status='ACTIVE' AND t.phase IN ('REVIEW_REQUIRED','REVIEWING')
      AND NOT EXISTS(SELECT 1 FROM reviews v LEFT JOIN runs r ON r.id=v.linked_run_id WHERE v.id=t.current_review_id AND v.start_failure_code IS NULL AND (r.id IS NULL OR r.status NOT IN ('SUCCEEDED','FAILED','CANCELED','INFRASTRUCTURE_FAILED'))))`, v.TaskID, v.ProjectID, v.FeatureAreaID, nullString(v.RepositoryID)).Scan(&admissible)
	if err != nil {
		return err
	}
	if !admissible {
		return review.ErrConflict
	}
	if v.ExecutorKind == review.ExecutorCodingAgent {
		if err = validateFeatureAreaBackendContinuityTx(ctx, tx, v.FeatureAreaID, v.BackendSelection); err != nil {
			return err
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0)+1 FROM reviews WHERE task_id=?`, v.TaskID).Scan(&ordinal); err != nil {
		return err
	}
	paths, err := json.Marshal(struct {
		Version int                         `json:"version"`
		Paths   []string                    `json:"paths"`
		Modules []run.ReviewModuleSelection `json:"modules,omitempty"`
	}{1, append([]string{}, v.Focus.Paths...), append([]run.ReviewModuleSelection(nil), v.Focus.Modules...)})
	if err != nil {
		return err
	}
	if v.AuthorKind == string(review.AuthorLocalOperator) {
		var current string
		err = tx.QueryRowContext(ctx, `SELECT c.request_id FROM task_supplemental_review_current c JOIN task_supplemental_review_requests q ON q.id=c.request_id WHERE c.task_id=?`, v.TaskID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			if v.SupplementalRequestID != "" {
				return review.ErrConflict
			}
		} else if err != nil {
			return err
		} else if current != v.SupplementalRequestID {
			return review.ErrConflict
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO reviews(id,project_id,feature_area_id,created_by_ai_session_id,author_kind,author_id,repository_id,codex_config_id,base_commit,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,model,reasoning_effort,created_at,task_id,ordinal,review_depth,executor_kind,executor_metadata_json,runtime_config_json,runtime_confirmation_id,backend_selection_json,workspace_id,repository_targets_json,primary_member_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.ProjectID, v.FeatureAreaID, nullString(v.CreatedByAISessionID), v.AuthorKind, v.AuthorID, nullString(v.RepositoryID), nullString(v.CodexConfigID), v.BaseCommit, v.Focus.Kind, v.Focus.Summary, string(paths), v.EngineeringGoal, v.Instruction, nullString(v.Model), nullString(v.ReasoningEffort), formatTime(v.CreatedAt), v.TaskID, ordinal, v.ReviewDepth, v.ExecutorKind, executorMetadata, string(runtimeConfig), v.RuntimeConfirmationID, backendSelection, nullString(v.WorkspaceID), encodeReviewTargets(v.RepositoryTargets), nullString(v.PrimaryMemberID))
	if err != nil {
		return conflict(err)
	}
	// Pin what this Review actually read at admission. In particular, a Task
	// created in a composed confirmation has no earlier START_REVIEW intent.
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_definition_admissions(review_id,project_id,task_id,revision_id,revision_hash) SELECT ?,t.project_id,t.id,d.id,d.payload_sha256 FROM tasks t JOIN task_definition_revisions d ON d.id=t.current_definition_revision_id AND d.task_id=t.id WHERE t.id=? AND t.project_id=?`, v.ID, v.TaskID, v.ProjectID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_supplemental_review_starts(review_id,request_id) SELECT ?,c.request_id FROM task_supplemental_review_current c JOIN tasks t ON t.id=c.task_id WHERE c.task_id=? AND t.phase IN ('REVIEW_REQUIRED','REVIEWING')`, v.ID, v.TaskID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET current_review_id=?,phase='REVIEWING',required_review_depth=?,updated_at=? WHERE id=? AND status='ACTIVE' AND phase IN ('REVIEW_REQUIRED','REVIEWING')`, v.ID, v.ReviewDepth, formatTime(v.CreatedAt), v.TaskID)
	return err
}

// AdmitStart serializes command replay, Feature Area admission, and formal
// Review/Run creation under one writer transaction. Busy is committed only as
// a command receipt; the candidate Review and Run never enter history.
func (s *ReviewStore) AdmitStart(ctx context.Context, candidate review.Review, requestID, payloadSHA256, runID, instruction string, scope run.ReviewScope, plan run.ReviewPlan) (review.StartAdmission, error) {

	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return review.StartAdmission{}, err
	}
	defer tx.Rollback()
	var storedHash, outcome string
	var storedReviewID, storedRunID, ownerKind, ownerID, ownerRunID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256,outcome,review_id,run_id,owner_operation_kind,owner_operation_id,owner_run_id FROM feature_area_review_start_requests WHERE feature_area_id=? AND request_id=?`, candidate.FeatureAreaID, requestID).Scan(&storedHash, &outcome, &storedReviewID, &storedRunID, &ownerKind, &ownerID, &ownerRunID)
	if err == nil {
		if storedHash != payloadSHA256 {
			return review.StartAdmission{}, review.ErrRequestConflict
		}
		if outcome == "FEATURE_AREA_BUSY" {
			return review.StartAdmission{}, &review.AdmissionBusyError{Owner: agentconversation.Claim{FeatureAreaID: candidate.FeatureAreaID, Kind: agentconversation.OperationKind(ownerKind.String), OperationID: ownerID.String, RunID: ownerRunID.String}}
		}
		storedReview, getReviewErr := scanReview(tx.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, storedReviewID.String))
		if getReviewErr != nil {
			return review.StartAdmission{}, getReviewErr
		}
		storedRun, getRunErr := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, storedRunID.String))
		if getRunErr != nil {
			return review.StartAdmission{}, getRunErr
		}
		return review.StartAdmission{Review: storedReview, Run: storedRun, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return review.StartAdmission{}, err
	}
	if err = taskActionAdmissionTx(ctx, tx, candidate.TaskID); err != nil {
		return review.StartAdmission{}, err
	}
	if err = confirmationReviewAdmissionTx(ctx, tx, candidate, requestID); err != nil {
		return review.StartAdmission{}, err
	}
	if err = checkReviewDepthTx(ctx, tx, candidate); err != nil {
		return review.StartAdmission{}, err
	}
	// Replay is resolved before checking the current lifecycle so a committed
	// command keeps its original outcome even if the FeatureArea later changes.
	// beginWrite already serializes this lookup with competing admissions.
	if _, err = lockAgentFeatureArea(ctx, tx, candidate.FeatureAreaID); err != nil {
		return review.StartAdmission{}, err
	}
	owner, claimErr := scanAgentClaim(tx.QueryRowContext(ctx, `SELECT feature_area_id,operation_kind,operation_id,run_id,created_at,COALESCE(runtime_epoch,'') FROM feature_area_agent_claims WHERE feature_area_id=?`, candidate.FeatureAreaID))
	if claimErr == nil {
		if _, err = tx.ExecContext(ctx, `INSERT INTO feature_area_review_start_requests(feature_area_id,request_id,payload_sha256,outcome,owner_operation_kind,owner_operation_id,owner_run_id,created_at) VALUES(?,?,?,?,?,?,?,?)`, candidate.FeatureAreaID, requestID, payloadSHA256, "FEATURE_AREA_BUSY", owner.Kind, owner.OperationID, owner.RunID, formatTime(candidate.CreatedAt)); err != nil {
			return review.StartAdmission{}, conflict(err)
		}
		if err = tx.Commit(); err != nil {
			return review.StartAdmission{}, err
		}
		return review.StartAdmission{}, &review.AdmissionBusyError{Owner: owner}
	}
	if !errors.Is(claimErr, agentconversation.ErrNotFound) {
		return review.StartAdmission{}, claimErr
	}

	if err = insertReviewTx(ctx, tx, candidate); err != nil {
		return review.StartAdmission{}, err
	}
	if len(candidate.RepositoryTargets) == 0 {
		if candidate.RepositoryProvenance == nil {
			return review.StartAdmission{}, review.ErrInvalidArgument
		}
		provenance := *candidate.RepositoryProvenance
		if provenance.ReviewID != candidate.ID || provenance.RepositoryID != candidate.RepositoryID || provenance.Commit != candidate.BaseCommit || provenance.RepositoryHEAD != candidate.BaseCommit || provenance.CapturedAt.IsZero() {
			return review.StartAdmission{}, review.ErrInvalidArgument
		}
		var dirty any
		if provenance.WorkingTreeDirtyAtAdmission != nil {
			dirty = *provenance.WorkingTreeDirtyAtAdmission
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO review_repository_provenance(review_id,repository_id,reviewed_commit,captured_at,repository_head,branch_kind,branch_ref,working_tree_dirty,inspection_mode) VALUES(?,?,?,?,?,?,?,?,?)`, provenance.ReviewID, provenance.RepositoryID, provenance.Commit, formatTime(provenance.CapturedAt), provenance.RepositoryHEAD, provenance.BranchKind, nullString(provenance.BranchRef), dirty, provenance.InspectionMode); err != nil {
			return review.StartAdmission{}, conflict(err)
		}
	}
	createdRun, err := createRunAttempt(ctx, s.db, tx, "", runID, candidate.CreatedAt, runMetadata{ReviewID: candidate.ID, Purpose: run.PurposeScopeReview, CodexInstruction: instruction, ReviewScope: scope, ReviewPlan: plan})
	if err != nil {
		return review.StartAdmission{}, err
	}
	linked, err := tx.ExecContext(ctx, `UPDATE reviews SET linked_run_id=? WHERE id=? AND linked_run_id IS NULL`, runID, candidate.ID)
	if err != nil {
		return review.StartAdmission{}, conflict(err)
	}
	if count, _ := linked.RowsAffected(); count != 1 {
		return review.StartAdmission{}, review.ErrConflict
	}
	grantScope := run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}
	grantMode := reviewgrant.ModeRepository
	if candidate.ExecutorKind == review.ExecutorUpperLLM {
		grantScope = plan.InitialScope
		if grantScope.Kind == run.ReviewScopePaths {
			grantMode = reviewgrant.ModeFocused
		}
	}
	paths, err := encodeReviewScopePaths(grantScope.Paths)
	if err != nil {
		return review.StartAdmission{}, err
	}
	selected, err := json.Marshal(struct {
		Version int   `json:"version"`
		Items   []any `json:"items"`
	}{Version: 1, Items: []any{}})
	if err != nil {
		return review.StartAdmission{}, err
	}
	forbiddenJSON, err := json.Marshal(append([]string{}, candidate.ForbiddenPaths...))
	if err != nil {
		return review.StartAdmission{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_grants(review_id,feature_area_id,project_id,repository_id,mode,initial_scope_kind,initial_scope_paths_json,selected_modules_json,created_at,forbidden_paths_json,protocol_version) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, candidate.ID, candidate.FeatureAreaID, candidate.ProjectID, nullString(candidate.RepositoryID), grantMode, grantScope.Kind, paths, string(selected), formatTime(candidate.CreatedAt), string(forbiddenJSON), reviewProtocol(candidate)); err != nil {
		return review.StartAdmission{}, conflict(err)
	}
	for _, target := range candidate.RepositoryTargets {
		if _, err = tx.ExecContext(ctx, `INSERT INTO run_repository_bindings(run_id,review_id,project_id,feature_area_id,workspace_id,repository_id,member_id,member_version,origin_commit,preparation) VALUES(?,?,?,?,?,?,?,?,?,'PENDING')`, runID, candidate.ID, candidate.ProjectID, candidate.FeatureAreaID, candidate.WorkspaceID, target.RepositoryID, target.MemberID, target.MemberVersion, target.OriginCommit); err != nil {
			return review.StartAdmission{}, err
		}
	}
	if candidate.ExecutorKind == review.ExecutorUpperLLM {
		if _, err = tx.ExecContext(ctx, `UPDATE review_grants SET state='PREPARING',run_id=?,runtime_epoch=?,baseline=? WHERE review_id=?`, createdRun.ID, createdRun.RuntimeEpoch, candidate.BaseCommit, candidate.ID); err != nil {
			return review.StartAdmission{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO feature_area_review_start_requests(feature_area_id,request_id,payload_sha256,outcome,review_id,run_id,created_at) VALUES(?,?,?,?,?,?,?)`, candidate.FeatureAreaID, requestID, payloadSHA256, "ACCEPTED", candidate.ID, runID, formatTime(candidate.CreatedAt)); err != nil {
		return review.StartAdmission{}, conflict(err)
	}
	if err := captureReviewWorkflowTx(ctx, tx, candidate); err != nil {
		return review.StartAdmission{}, err
	}
	if err = tx.Commit(); err != nil {
		return review.StartAdmission{}, err
	}
	if err = s.db.sql.QueryRowContext(ctx, `SELECT ordinal FROM reviews WHERE id=?`, candidate.ID).Scan(&candidate.Ordinal); err != nil {
		return review.StartAdmission{}, err
	}
	candidate.RunID = runID
	candidate.StartRequestID = requestID
	return review.StartAdmission{Review: candidate, Run: createdRun}, nil
}
func (s *ReviewStore) Get(ctx context.Context, id string) (review.Review, error) {
	return s.queryOne(ctx, `WHERE r.id=?`, id)
}
func (s *ReviewStore) ListByFeatureArea(ctx context.Context, featureAreaID string) ([]review.Review, error) {
	rows, err := s.db.sql.QueryContext(ctx, reviewSelect+` WHERE r.feature_area_id=? ORDER BY r.created_at,r.id`, featureAreaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []review.Review{}
	for rows.Next() {
		v, e := scanReview(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *ReviewStore) queryOne(ctx context.Context, suffix string, args ...any) (review.Review, error) {
	v, e := scanReview(s.db.sql.QueryRowContext(ctx, reviewSelect+` `+suffix, args...))
	if errors.Is(e, sql.ErrNoRows) {
		return review.Review{}, review.ErrNotFound
	}
	return v, e
}

const reviewSelect = `SELECT r.id,r.project_id,r.feature_area_id,COALESCE(r.created_by_ai_session_id,''),COALESCE(r.author_kind,''),COALESCE(r.author_id,''),COALESCE((SELECT s.request_id FROM feature_area_review_start_requests s WHERE s.review_id=r.id),''),COALESCE(r.repository_id,''),COALESCE(r.codex_config_id,''),COALESCE(r.base_commit,''),COALESCE(r.linked_run_id,''),r.focus_kind,r.focus_summary,r.focus_paths_json,r.engineering_goal,r.instruction,COALESCE(r.model,''),COALESCE(r.reasoning_effort,''),COALESCE(r.final_codex_prompt,''),COALESCE(r.prompt_sha256,''),COALESCE(r.prompt_bytes,0),r.prompt_sent_at,r.start_failure_code,r.start_blocker,r.start_failure_at,r.created_at,r.continuation_attempted_at,p.repository_id,p.reviewed_commit,p.repository_head,p.branch_kind,p.branch_ref,p.working_tree_dirty,p.inspection_mode,p.captured_at,COALESCE(r.task_id,''),r.ordinal,COALESCE((SELECT status FROM runs WHERE id=r.linked_run_id AND status IN ('SUCCEEDED','FAILED','CANCELED','INFRASTRUCTURE_FAILED')),''),r.review_depth,COALESCE(r.review_disposition,''),r.executor_kind,r.executor_metadata_json,COALESCE((SELECT id FROM review_reports WHERE review_id=r.id),''),(SELECT json_object('protocolVersion',g.protocol_version,'reviewCreatedAt',r.created_at,'repositoryTargets',json(r.repository_targets_json),'state',g.state,'startSnapshot',COALESCE((SELECT start_snapshot FROM runs WHERE id=g.run_id),''),'blocker',g.preparation_error,'workspaceId',COALESCE(g.workspace_id,''),'worktreeId',COALESCE(g.worktree_id,''),'baseline',COALESCE(g.baseline,r.base_commit),'target',json_object('aiSessionId',r.created_by_ai_session_id,'featureAreaId',r.feature_area_id,'taskId',r.task_id,'reviewId',r.id,'runId',COALESCE(r.linked_run_id,''),'runtimeEpoch',COALESCE(g.runtime_epoch,''),'grantGeneration',g.generation),'allowedPaths',json(COALESCE(json_extract(g.initial_scope_paths_json,'$.paths'),'[]')),'forbiddenPaths',json(g.forbidden_paths_json),'lastObservedAt',g.last_observed_at) FROM review_grants g WHERE g.review_id=r.id AND r.executor_kind='UPPER_LLM'),r.runtime_config_json,r.runtime_confirmation_id,r.backend_selection_json,COALESCE(r.workspace_id,''),r.repository_targets_json,COALESCE(r.primary_member_id,'') FROM reviews r LEFT JOIN review_repository_provenance p ON p.review_id=r.id`

type reviewScanner interface{ Scan(...any) error }

func scanReview(s reviewScanner) (review.Review, error) {
	var v review.Review
	var kind, paths, created string
	var sent, fc, fb, fa, continuation sql.NullString
	var provenanceRepository, provenanceCommit, repositoryHEAD, branchKind, branchRef, inspectionMode, provenanceCaptured sql.NullString
	var workingTreeDirty sql.NullBool
	var bytes int64
	var metadata, access, backendSelection sql.NullString
	var runtimeConfig, targetsJSON string
	err := s.Scan(&v.ID, &v.ProjectID, &v.FeatureAreaID, &v.CreatedByAISessionID, &v.AuthorKind, &v.AuthorID, &v.StartRequestID, &v.RepositoryID, &v.CodexConfigID, &v.BaseCommit, &v.RunID, &kind, &v.Focus.Summary, &paths, &v.EngineeringGoal, &v.Instruction, &v.Model, &v.ReasoningEffort, &v.FinalCodexPrompt, &v.PromptSHA256, &bytes, &sent, &fc, &fb, &fa, &created, &continuation, &provenanceRepository, &provenanceCommit, &repositoryHEAD, &branchKind, &branchRef, &workingTreeDirty, &inspectionMode, &provenanceCaptured, &v.TaskID, &v.Ordinal, &v.TerminalStatus, &v.ReviewDepth, &v.ReviewDisposition, &v.ExecutorKind, &metadata, &v.ReportID, &access, &runtimeConfig, &v.RuntimeConfirmationID, &backendSelection, &v.WorkspaceID, &targetsJSON, &v.PrimaryMemberID)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(targetsJSON), &v.RepositoryTargets); err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(runtimeConfig), &v.RuntimeConfig); err != nil {
		return v, err
	}
	if backendSelection.Valid {
		if err := json.Unmarshal([]byte(backendSelection.String), &v.BackendSelection); err != nil {
			return v, err
		}
	}
	if access.Valid {
		if err := json.Unmarshal([]byte(access.String), &v.UpperAccess); err != nil {
			return v, err
		}
	}
	if metadata.Valid {
		if err := json.Unmarshal([]byte(metadata.String), &v.ExecutorMetadata); err != nil {
			return v, err
		}
	}
	v.Focus.Kind = review.FocusKind(kind)
	v.PromptBytes = bytes
	var e struct {
		Version int                         `json:"version"`
		Paths   []string                    `json:"paths"`
		Modules []run.ReviewModuleSelection `json:"modules,omitempty"`
	}
	if json.Unmarshal([]byte(paths), &e) != nil || e.Version != 1 {
		return v, fmt.Errorf("decode review focus paths")
	}
	v.Focus.Paths = e.Paths
	v.Focus.Modules = e.Modules
	var x error
	v.CreatedAt, x = parseTime(created)
	if x != nil {
		return v, x
	}
	if provenanceCommit.Valid {
		captured, parseErr := parseTime(provenanceCaptured.String)
		if parseErr != nil {
			return v, parseErr
		}
		provenance := &review.RepositoryProvenance{ReviewID: v.ID, RepositoryID: provenanceRepository.String, Commit: provenanceCommit.String, RepositoryHEAD: repositoryHEAD.String, BranchKind: branchKind.String, BranchRef: branchRef.String, InspectionMode: inspectionMode.String, CapturedAt: captured}
		if workingTreeDirty.Valid {
			dirty := workingTreeDirty.Bool
			provenance.WorkingTreeDirtyAtAdmission = &dirty
		}
		v.RepositoryProvenance = provenance
	}
	if sent.Valid {
		x, e := parseTime(sent.String)
		if e != nil {
			return v, e
		}
		v.PromptSentAt = &x
	}
	if fc.Valid || fb.Valid || fa.Valid {
		if !fc.Valid || !fb.Valid || !fa.Valid {
			return v, fmt.Errorf("incomplete review start failure")
		}
		x, e := parseTime(fa.String)
		if e != nil {
			return v, e
		}
		v.StartFailure = &review.StartFailure{Code: fc.String, Blocker: fb.String, RecordedAt: x}
	}
	if continuation.Valid {
		x, parseErr := parseTime(continuation.String)
		if parseErr != nil {
			return v, parseErr
		}
		v.ContinuationAttemptedAt = &x
	}
	return v, nil
}

func (s *ReviewStore) ClaimContinuation(ctx context.Context, id string, now time.Time) (bool, error) {
	result, err := s.db.execWrite(ctx, `UPDATE reviews SET continuation_attempted_at=? WHERE id=? AND executor_kind='CODING_AGENT' AND continuation_attempted_at IS NULL AND (start_failure_code IS NOT NULL OR EXISTS(SELECT 1 FROM runs WHERE runs.id=reviews.linked_run_id AND runs.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')))`, formatTime(now), id)
	if err != nil {
		return false, conflict(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 1 {
		return true, nil
	}
	var exists int
	if err := s.db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM reviews WHERE id=?`, id).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		return false, review.ErrNotFound
	}
	return false, nil
}
func (s *ReviewStore) LinkRun(ctx context.Context, reviewID, runID string) error {
	tx, e := s.db.beginWrite(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var matches bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM reviews v JOIN runs r ON r.id=? WHERE v.id=? AND r.review_id=v.id AND r.project_id=v.project_id AND r.feature_area_id=v.feature_area_id AND r.operation_kind='REVIEW' AND r.operation_id=v.id AND r.task_id=v.task_id)`, runID, reviewID).Scan(&matches); e != nil {
		return e
	}
	if !matches {
		return review.ErrConflict
	}
	updated, e := tx.ExecContext(ctx, `UPDATE reviews SET linked_run_id=? WHERE id=? AND (linked_run_id IS NULL OR linked_run_id=?)`, runID, reviewID, runID)
	if e != nil {
		return conflict(e)
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return review.ErrConflict
	}
	updated, e = tx.ExecContext(ctx, `UPDATE runs SET review_id=? WHERE id=? AND (review_id IS NULL OR review_id=?)`, reviewID, runID, reviewID)
	if e != nil {
		return conflict(e)
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return review.ErrConflict
	}
	return tx.Commit()
}
func (s *ReviewStore) RecordPrompt(ctx context.Context, id, prompt string, sent time.Time) (review.Review, error) {
	if prompt == "" || len(prompt) > 64<<10 {
		return review.Review{}, review.ErrInvalidArgument
	}
	sum := sha256.Sum256([]byte(prompt))
	tx, e := s.db.beginWrite(ctx)
	if e != nil {
		return review.Review{}, e
	}
	defer tx.Rollback()
	if e = s.checkReviewRunTx(ctx, tx, id); e != nil {
		return review.Review{}, e
	}
	res, e := tx.ExecContext(ctx, `UPDATE reviews SET final_codex_prompt=?,prompt_sha256=?,prompt_bytes=?,prompt_sent_at=? WHERE id=? AND instruction=? AND final_codex_prompt IS NULL`, prompt, hex.EncodeToString(sum[:]), len([]byte(prompt)), formatTime(sent), id, prompt)
	if e != nil {
		return review.Review{}, e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		existing, err := s.Get(ctx, id)
		if err != nil || existing.FinalCodexPrompt != prompt {
			return review.Review{}, review.ErrConflict
		}
		return existing, nil
	}
	if e = tx.Commit(); e != nil {
		return review.Review{}, e
	}
	return s.Get(ctx, id)
}
func (s *ReviewStore) RecordAccess(ctx context.Context, v review.AccessEvidence) error {
	if v.ID == "" || v.ReviewID == "" || v.Operation == "" || len(v.Operation) > 128 || len(v.Code) > 256 || v.Outcome != review.AccessSuccess && v.Outcome != review.AccessForbidden && v.Outcome != review.AccessFailure {
		return review.ErrInvalidArgument
	}
	tx, e := s.db.beginWrite(ctx)
	if e != nil {
		return conflict(e)
	}
	defer tx.Rollback()
	if e = s.checkReviewRunTx(ctx, tx, v.ReviewID); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO review_access_evidence(id,review_id,operation,outcome,code,recorded_at) VALUES(?,?,?,?,?,?)`, v.ID, v.ReviewID, v.Operation, v.Outcome, nullString(v.Code), formatTime(v.RecordedAt))
	if e != nil {
		return conflict(e)
	}
	return tx.Commit()
}
func (s *ReviewStore) SummarizeEvidence(ctx context.Context, id string) (review.EvidenceSummary, error) {
	var out review.EvidenceSummary
	var last sql.NullString
	e := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_grants g JOIN reviews r ON r.id=g.review_id WHERE g.review_id=? AND (r.executor_kind='CODING_AGENT' OR g.state='ACTIVE')),COALESCE(SUM(outcome='SUCCESS' AND (operation='UPPER_LLM_FILE_READ' OR NOT EXISTS(SELECT 1 FROM reviews WHERE id=review_access_evidence.review_id AND executor_kind='UPPER_LLM'))),0),COALESCE(SUM(outcome='FORBIDDEN'),0),COALESCE(SUM(outcome='FAILURE'),0),MAX(recorded_at) FROM review_access_evidence WHERE review_id=?`, id, id).Scan(&out.AuthorizationGranted, &out.Successes, &out.Forbidden, &out.Failures, &last)
	if e != nil {
		return out, e
	}
	if last.Valid {
		x, e := parseTime(last.String)
		if e != nil {
			return out, e
		}
		out.LastObservedAt = &x
	}
	return out, nil
}
func (s *ReviewStore) RecordStartFailure(ctx context.Context, id string, v review.StartFailure) error {
	if v.Code == "" || len(v.Code) > 256 || v.Blocker == "" || len(v.Blocker) > 4096 {
		return review.ErrInvalidArgument
	}
	r, e := s.db.execWrite(ctx, `UPDATE reviews SET start_failure_code=?,start_blocker=?,start_failure_at=? WHERE id=? AND start_failure_code IS NULL`, v.Code, v.Blocker, formatTime(v.RecordedAt), id)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return review.ErrConflict
	}
	return nil
}

// RecordRepositoryProvenance durably records the repository state one Review
// was based on. The record is immutable: an insert over an existing row is a
// no-op that preserves the original provenance.
func (s *ReviewStore) RecordRepositoryProvenance(ctx context.Context, provenance review.RepositoryProvenance) error {
	if provenance.ReviewID == "" || provenance.RepositoryID == "" || provenance.Commit == "" {
		return review.ErrInvalidArgument
	}
	var dirty any
	if provenance.WorkingTreeDirtyAtAdmission != nil {
		dirty = *provenance.WorkingTreeDirtyAtAdmission
	}
	_, err := s.db.execWrite(ctx, `INSERT OR IGNORE INTO review_repository_provenance(review_id,repository_id,reviewed_commit,captured_at,repository_head,branch_kind,branch_ref,working_tree_dirty,inspection_mode) VALUES(?,?,?,?,?,?,?,?,?)`,
		provenance.ReviewID, provenance.RepositoryID, provenance.Commit, formatTime(provenance.CapturedAt), provenance.RepositoryHEAD, provenance.BranchKind, nullString(provenance.BranchRef), dirty, provenance.InspectionMode)
	if err != nil {
		return fmt.Errorf("record review repository provenance: %w", err)
	}
	return nil
}

// RepositoryProvenance returns the recorded provenance for one Review.
func (s *ReviewStore) RepositoryProvenance(ctx context.Context, reviewID string) (review.RepositoryProvenance, error) {
	var provenance review.RepositoryProvenance
	var capturedAt string
	var dirty sql.NullBool
	err := s.db.sql.QueryRowContext(ctx, `SELECT review_id,repository_id,reviewed_commit,captured_at,COALESCE(repository_head,''),COALESCE(branch_kind,'UNKNOWN'),COALESCE(branch_ref,''),working_tree_dirty,COALESCE(inspection_mode,'FEATURE_AREA_WORKSPACE') FROM review_repository_provenance WHERE review_id=?`, reviewID).
		Scan(&provenance.ReviewID, &provenance.RepositoryID, &provenance.Commit, &capturedAt, &provenance.RepositoryHEAD, &provenance.BranchKind, &provenance.BranchRef, &dirty, &provenance.InspectionMode)
	if errors.Is(err, sql.ErrNoRows) {
		return review.RepositoryProvenance{}, review.ErrProvenanceNotFound
	}
	if err != nil {
		return review.RepositoryProvenance{}, fmt.Errorf("read review repository provenance: %w", err)
	}
	captured, err := parseTime(capturedAt)
	if err != nil {
		return review.RepositoryProvenance{}, fmt.Errorf("decode review repository provenance timestamp: %w", err)
	}
	provenance.CapturedAt = captured
	if dirty.Valid {
		provenance.WorkingTreeDirtyAtAdmission = &dirty.Bool
	}
	return provenance, nil
}

// checkReviewDepthTx runs after replay but before any new command receipt or
// owner claim is written. The writer transaction fences concurrent admissions.
func checkReviewDepthTx(ctx context.Context, tx *sql.Tx, v review.Review) error {
	if !v.ReviewDepth.Valid() {
		return review.ErrInvalidArgument
	}
	var required *task.ReviewDepth
	var phase task.Phase
	if err := tx.QueryRowContext(ctx, `SELECT required_review_depth,phase FROM tasks WHERE id=? AND project_id=? AND feature_area_id=? AND repository_id IS ?`, v.TaskID, v.ProjectID, v.FeatureAreaID, nullString(v.RepositoryID)).Scan(&required, &phase); err != nil {
		return err
	}
	if required == nil || !required.Valid() {
		// STOP_AND_REVISE changes only phase. The prior approval cleared the
		// persisted floor, so this exact canceled-before-Run path inherits its
		// preceding Review's floor until the new Review records its own depth.
		var inherited task.ReviewDepth
		err := tx.QueryRowContext(ctx, `SELECT previous.review_depth FROM tasks t JOIN reviews previous ON previous.id=t.current_review_id AND previous.task_id=t.id AND previous.project_id=t.project_id WHERE t.id=? AND t.phase='REVIEW_REQUIRED' AND `+noRunSuccessorEligibleSQL+` AND `+noRunRevisionPredecessorSQL, v.TaskID).Scan(&inherited)
		if errors.Is(err, sql.ErrNoRows) || err == nil && !inherited.Valid() {
			return review.ErrConflict
		}
		if err != nil {
			return err
		}
		required = &inherited
	}
	if !v.ReviewDepth.Satisfies(*required) {
		return &review.DepthTooShallowError{RequestedDepth: v.ReviewDepth, RequiredReviewDepth: *required, TaskID: v.TaskID, Phase: phase}
	}
	return nil
}

func reviewProtocol(v review.Review) int {
	if len(v.RepositoryTargets) > 0 {
		return 2
	}
	return 1
}
func encodeReviewTargets(targets []workset.Target) string {
	data, _ := json.Marshal(append([]workset.Target{}, targets...))
	return string(data)
}
func validateReviewTargetsTx(ctx context.Context, tx *sql.Tx, v review.Review) error {
	if !v.ExecutorKind.Effective().Valid() || v.RepositoryID != "" || v.BaseCommit != "" || v.WorkspaceID == "" {
		return review.ErrInvalidArgument
	}
	if err := workset.ValidateTargets(v.RepositoryTargets); err != nil {
		return err
	}
	aliases := map[string]bool{}
	for _, target := range v.RepositoryTargets {
		if target.Write.Kind != "NONE" {
			return review.ErrInvalidArgument
		}
		var alias string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM repositories WHERE id=? AND project_id=?`, target.RepositoryID, v.ProjectID).Scan(&alias); err != nil {
			return review.ErrInvalidArgument
		}
		if alias == "" || aliases[alias] {
			return review.ErrInvalidArgument
		}
		aliases[alias] = true
		var id, origin string
		var version int
		err := tx.QueryRowContext(ctx, `SELECT id,identity_version,origin_commit FROM workspace_members WHERE workspace_id=? AND repository_id=?`, v.WorkspaceID, target.RepositoryID).Scan(&id, &version, &origin)
		if err == nil && (id != target.MemberID || version != target.MemberVersion || origin != target.OriginCommit) {
			return review.ErrConflict
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}
