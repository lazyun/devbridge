package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// reReviewSuccessorTx creates the only successor permitted by a consumed
// confirmation. It deliberately reuses the Review/Run reservation primitives
// used by normal admission; dispatch stays outside this transaction.
func reReviewSuccessorTx(ctx context.Context, db *DB, tx *sql.Tx, previous review.Review, feedback, requestID, decisionID string, now string, invalidResolution bool) (review.Review, run.Run, string, string, error) {
	if !wp.ValidReviewFeedback(feedback) {
		return review.Review{}, run.Run{}, "", "", errors.New("REVIEW_FEEDBACK_INVALID")
	}
	var resultHash, outcome string
	if err := tx.QueryRowContext(ctx, `SELECT CASE WHEN outcome='VALID' THEN formal_result_sha256 ELSE raw_result_sha256 END,outcome FROM review_result_materializations WHERE review_id=? AND task_id=? AND run_id=?`, previous.ID, previous.TaskID, previous.RunID).Scan(&resultHash, &outcome); err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	if resultHash == "" || (!invalidResolution && outcome != "VALID") || (invalidResolution && outcome != "INVALID" && outcome != "SCOPE_CONFLICT") {
		return review.Review{}, run.Run{}, "", "", errors.New("REVIEW_RESULT_NOT_REVIEWABLE")
	}
	if len(previous.RepositoryTargets) > 0 {
		if err := validateReviewTargetsTx(ctx, tx, previous); err != nil {
			return review.Review{}, run.Run{}, "", "", err
		}
		for _, target := range previous.RepositoryTargets {
			var memberID, origin string
			var memberVersion int
			if err := tx.QueryRowContext(ctx, `SELECT id,identity_version,origin_commit FROM workspace_members WHERE workspace_id=? AND repository_id=?`, previous.WorkspaceID, target.RepositoryID).Scan(&memberID, &memberVersion, &origin); err != nil || memberID != target.MemberID || memberVersion != target.MemberVersion || origin != target.OriginCommit {
				if err == nil {
					err = review.ErrConflict
				}
				return review.Review{}, run.Run{}, "", "", err
			}
		}
	}
	if previous.ExecutorKind.Effective() == review.ExecutorCodingAgent {
		if err := validateFeatureAreaBackendContinuityTx(ctx, tx, previous.FeatureAreaID, previous.BackendSelection); err != nil {
			return review.Review{}, run.Run{}, "", "", err
		}
	}
	feedbackHash := workflowHash(feedback)
	identity := workflowHash(decisionID + "\x00" + requestID + "\x00" + feedbackHash)
	reviewID := "review_rereview_" + identity[:32]
	runID := "run_rereview_" + identity[:32]
	startRequestID := "rereview_start_" + identity[:32]
	startPayload := workflowHash(previous.ID + "\x00" + resultHash + "\x00" + feedbackHash + "\x00" + previous.WorkspaceID + "\x00" + encodeReviewTargets(previous.RepositoryTargets))

	// The predecessor has already been resolved. Re-open only the narrow
	// admission state required by insertReviewTx; its latest-review and
	// terminal-run fences are rechecked there under this writer transaction.
	depth := previous.ReviewDepth
	if previous.ReviewDisposition == review.DispositionFurtherReviewRequired {
		depth = task.ReviewDepthDeep
	}
	expectedPhase := "REVIEW_APPROVAL_PENDING"
	if invalidResolution {
		expectedPhase = "REVIEWING"
	}
	// A Review started by STOP_AND_REVISE retains its exact canceled no-Run
	// predecessor until a new approval replaces the definition and Execution.
	retainedPredecessor, err := noRunRevisionReviewPendingTx(ctx, tx, previous.TaskID)
	if err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_REQUIRED',required_review_depth=?,updated_at=? WHERE id=? AND status='ACTIVE' AND phase=? AND current_review_id=? AND (current_execution_id IS NULL OR ?)`, string(depth), now, previous.TaskID, expectedPhase, previous.ID, retainedPredecessor)
	if err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	if n, e := changed.RowsAffected(); e != nil || n != 1 {
		if e != nil {
			return review.Review{}, run.Run{}, "", "", e
		}
		return review.Review{}, run.Run{}, "", "", errors.New("REVIEW_CONFIRMATION_STALE")
	}

	var inheritedForbidden string
	if err = tx.QueryRowContext(ctx, `SELECT forbidden_paths_json FROM review_grants WHERE review_id=?`, previous.ID).Scan(&inheritedForbidden); err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	inheritedForbidden, err = reviewForbiddenPathsJSON(ctx, tx, previous.ID, inheritedForbidden)
	if err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	if err = json.Unmarshal([]byte(inheritedForbidden), &previous.ForbiddenPaths); err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	successor := previous
	successor.ID, successor.RunID, successor.Ordinal = reviewID, "", 0
	successor.ReviewDisposition, successor.TerminalStatus, successor.ReportID = "", "", ""
	successor.StartRequestID = ""
	successor.FinalCodexPrompt, successor.PromptSHA256, successor.PromptBytes, successor.PromptSentAt = "", "", 0, nil
	successor.StartFailure, successor.ContinuationAttemptedAt = nil, nil
	successor.CreatedAt, successor.ReviewDepth = mustParseSQLiteTime(now), depth
	// Preserve the prior canonical request and add a bounded, verbatim user
	// instruction. The user cannot supply scope, target, backend or runtime.
	successor.Instruction = previous.Instruction + "\n\nRe-review feedback:\n" + feedback
	if successor.RepositoryProvenance != nil {
		p := *successor.RepositoryProvenance
		p.ReviewID = successor.ID
		successor.RepositoryProvenance = &p
	}
	if err = insertReviewTx(ctx, tx, successor); err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	if len(successor.RepositoryTargets) == 0 {
		if successor.RepositoryProvenance == nil {
			return review.Review{}, run.Run{}, "", "", review.ErrInvalidArgument
		}
		provenance := successor.RepositoryProvenance
		var dirty any
		if provenance.WorkingTreeDirtyAtAdmission != nil {
			dirty = *provenance.WorkingTreeDirtyAtAdmission
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO review_repository_provenance(review_id,repository_id,reviewed_commit,captured_at,repository_head,branch_kind,branch_ref,working_tree_dirty,inspection_mode) VALUES(?,?,?,?,?,?,?,?,?)`, successor.ID, provenance.RepositoryID, provenance.Commit, now, provenance.RepositoryHEAD, provenance.BranchKind, nullString(provenance.BranchRef), dirty, provenance.InspectionMode); err != nil {
			return review.Review{}, run.Run{}, "", "", err
		}
	}
	// insertReviewTx allocated the ordinal and made successor current before
	// reserving a Run, exactly as normal Review admission requires.
	if err = tx.QueryRowContext(ctx, `SELECT ordinal FROM reviews WHERE id=? AND task_id=?`, successor.ID, successor.TaskID).Scan(&successor.Ordinal); err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
	if successor.Focus.Kind == review.FocusFocused {
		scope = run.ReviewScope{Kind: run.ReviewScopePaths, Paths: append([]string(nil), successor.Focus.Paths...)}
	}
	plan := run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope}
	if successor.Focus.Kind == review.FocusFocused {
		plan.Mode = run.ReviewModeFocused
	}
	reserved, err := createRunAttempt(ctx, db, tx, "", runID, successor.CreatedAt, runMetadata{ReviewID: successor.ID, Purpose: run.PurposeScopeReview, CodexInstruction: successor.Instruction, ReviewScope: scope, ReviewPlan: plan})
	if err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reviews SET linked_run_id=? WHERE id=? AND linked_run_id IS NULL`, reserved.ID, successor.ID); err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	grantMode := reviewgrant.ModeRepository
	if successor.ExecutorKind == review.ExecutorUpperLLM && scope.Kind == run.ReviewScopePaths {
		grantMode = reviewgrant.ModeFocused
	}
	paths, err := encodeReviewScopePaths(scope.Paths)
	if err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	selected, err := json.Marshal(struct {
		Version int   `json:"version"`
		Items   []any `json:"items"`
	}{Version: 1, Items: []any{}})
	if err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	forbidden, err := json.Marshal(append([]string{}, successor.ForbiddenPaths...))
	if err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_grants(review_id,feature_area_id,project_id,repository_id,mode,initial_scope_kind,initial_scope_paths_json,selected_modules_json,created_at,forbidden_paths_json,protocol_version) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, successor.ID, successor.FeatureAreaID, successor.ProjectID, nullString(successor.RepositoryID), grantMode, scope.Kind, paths, string(selected), now, string(forbidden), reviewProtocol(successor)); err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	for _, target := range successor.RepositoryTargets {
		if _, err = tx.ExecContext(ctx, `INSERT INTO run_repository_bindings(run_id,review_id,project_id,feature_area_id,workspace_id,repository_id,member_id,member_version,origin_commit,preparation) VALUES(?,?,?,?,?,?,?,?,?,'PENDING')`, reserved.ID, successor.ID, successor.ProjectID, successor.FeatureAreaID, successor.WorkspaceID, target.RepositoryID, target.MemberID, target.MemberVersion, target.OriginCommit); err != nil {
			return review.Review{}, run.Run{}, "", "", err
		}
	}
	if successor.ExecutorKind == review.ExecutorUpperLLM {
		if _, err = tx.ExecContext(ctx, `UPDATE review_grants SET state='PREPARING',run_id=?,runtime_epoch=?,baseline=? WHERE review_id=?`, reserved.ID, reserved.RuntimeEpoch, successor.BaseCommit, successor.ID); err != nil {
			return review.Review{}, run.Run{}, "", "", err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO feature_area_review_start_requests(feature_area_id,request_id,payload_sha256,outcome,review_id,run_id,created_at) VALUES(?,?,?,?,?,?,?)`, successor.FeatureAreaID, startRequestID, startPayload, "ACCEPTED", successor.ID, reserved.ID, now); err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	if err = captureReviewWorkflowTx(ctx, tx, successor); err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	if invalidResolution {
		if _, err = tx.ExecContext(ctx, `INSERT INTO review_invalid_result_resolutions(decision_id,task_id,previous_review_id,previous_review_ordinal,source_outcome,source_raw_result_sha256,successor_review_id,successor_run_id,start_request_id,start_payload_sha256,request_id,feedback,feedback_sha256,action,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'REQUEST_RE_REVIEW',?)`, decisionID, previous.TaskID, previous.ID, previous.Ordinal, outcome, resultHash, successor.ID, reserved.ID, startRequestID, startPayload, requestID, feedback, feedbackHash, now); err != nil {
			return review.Review{}, run.Run{}, "", "", err
		}
	} else if _, err = tx.ExecContext(ctx, `INSERT INTO review_re_review_requests(decision_id,task_id,previous_review_id,previous_review_ordinal,previous_formal_result_sha256,successor_review_id,successor_run_id,start_request_id,start_payload_sha256,request_id,feedback,feedback_sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, decisionID, previous.TaskID, previous.ID, previous.Ordinal, resultHash, successor.ID, reserved.ID, startRequestID, startPayload, requestID, feedback, feedbackHash, now); err != nil {
		return review.Review{}, run.Run{}, "", "", err
	}
	return successor, reserved, startRequestID, startPayload, nil
}

func mustParseSQLiteTime(raw string) (out time.Time) {
	out, err := parseTime(raw)
	if err != nil {
		panic(fmt.Sprintf("invalid internal SQLite timestamp: %v", err))
	}
	return out
}
