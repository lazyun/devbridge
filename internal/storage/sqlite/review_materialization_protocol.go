package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Review result materialization is a durable three-phase protocol, never an
// in-flight function state or a transaction-local transient: (1) a claim
// transaction admits each eligible Review and commits a MATERIALIZING claim
// fencing the exact raw result, runtime workset and admission provenance;
// (2) the deterministic materialization (strict parse, schema validation and
// frozen-scope validation) runs outside every transaction with no runtime or
// Git side effect; (3) a finalize transaction CASes the exact claim token,
// re-checks all durable facts, and commits the formal
// VALID/INVALID/SCOPE_CONFLICT row, its single Decision and the Task Phase
// together with the claim terminalization. A crash between (1) and (3) leaves
// the claim open and the next sweep resumes the same token. A cancellation
// that lands first terminalizes the claim VOIDED with no formal result and no
// Decision. The protocol never creates an Execution or rewrites any current
// operation pointer.

type reviewMaterializationClaim struct {
	ReviewID            string
	ProjectID           string
	TaskID              string
	RunID               string
	ClaimToken          string
	Generation          int
	RawResultSHA256     string
	RuntimeWorksetSHA   string
	ProvenanceSHA256    string
	NaturalAnalysis     bool
	Raw                 string
	Review              review.Review
	StartPayloadSHA256  string
	DevelopmentCycleID  string
	DevelopmentRef      string
	AdmittedTargetsJSON string
	WorkspaceBound      bool
}

const (
	reviewMaterializationInvalid = "INVALID"
	reviewMaterializationScope   = "SCOPE_CONFLICT"
	reviewMaterializationValid   = "VALID"

	materializationKindInvalid    = "INVALID"
	materializationKindScope      = "SCOPE_CONFLICT"
	materializationKindBlocked    = "VALID_BLOCKED"
	materializationKindFormalOnly = "VALID_FORMAL_ONLY"
	materializationKindApproval   = "VALID_APPROVAL"
)

type reviewMaterializationOutcome struct {
	Terminal       string // VALID / INVALID / SCOPE_CONFLICT
	Kind           string // terminal branch executed by the finalize transaction
	ReasonCode     string
	ReasonDetail   string
	FormalJSON     string
	Disposition    review.Disposition
	DecisionID     string
	ProvenanceJSON string
	ProvenanceSHA  string
	ResultSHA      string
}

// reviewRunWorksetFenceHash derives the durable workset fence for a Review Run
// that binds no canonical runtime workset: the immutable workspace, worktree
// and snapshot columns of the exact terminal Run row.
func reviewRunWorksetFenceHash(runID, workspaceID, worktreeID, startSnapshot, endSnapshot string) string {
	return workflowHash(strings.Join([]string{"review-materialization-workset", runID, workspaceID, worktreeID, startSnapshot, endSnapshot}, "\x00"))
}

func newReviewMaterializationClaimToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "rmc-" + hex.EncodeToString(buf), nil
}

// claimReviewMaterializationsTx is phase (1). New admissions insert one
// MATERIALIZING claim per eligible Review; a Review whose claim is already
// open is resumed with its stored token instead of a competing claim, so a
// crash after the claim commit needs no repair write.
func claimReviewMaterializationsTx(ctx context.Context, tx *sql.Tx, at time.Time) ([]reviewMaterializationClaim, error) {
	rows, err := tx.QueryContext(ctx, `SELECT v.id FROM reviews v JOIN task_review_sources s ON s.review_id=v.id JOIN runs r ON r.id=v.linked_run_id
 WHERE EXISTS(SELECT 1 FROM agent_executions ae WHERE ae.id=r.agent_execution_id AND ae.provenance_kind<>'LEGACY_UNKNOWN') AND r.status='SUCCEEDED' AND ((NOT EXISTS(SELECT 1 FROM run_execution_records er WHERE er.run_id=r.id) AND r.result_text IS NOT NULL AND length(trim(r.result_text))>0) OR EXISTS(SELECT 1 FROM run_analyses an WHERE an.run_id=r.id AND an.kind='REVIEW' AND an.review_result_json IS NOT NULL))
 AND EXISTS(SELECT 1 FROM tasks t WHERE t.id=v.task_id AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING')
 AND NOT EXISTS(SELECT 1 FROM review_result_materializations m WHERE m.review_id=v.id)
 AND NOT EXISTS(SELECT 1 FROM review_result_materialization_claims c WHERE c.review_id=v.id)
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=r.id)
 AND EXISTS(SELECT 1 FROM feature_area_review_start_requests a WHERE a.review_id=v.id AND a.outcome='ACCEPTED')`)
	if err != nil {
		return nil, err
	}
	var admitted []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		admitted = append(admitted, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	admittedSet := make(map[string]struct{}, len(admitted))
	for _, id := range admitted {
		admittedSet[id] = struct{}{}
	}
	var claims []reviewMaterializationClaim
	for _, id := range admitted {
		claim, ok, e := loadReviewMaterializationSource(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		token, e := newReviewMaterializationClaimToken()
		if e != nil {
			return nil, e
		}
		claim.ClaimToken = token
		claim.Generation = 1
		if _, e = tx.ExecContext(ctx, `INSERT INTO review_result_materialization_claims(review_id,project_id,task_id,run_id,state,raw_result_sha256,runtime_workset_hash,provenance_sha256,generation,claim_token,created_at,updated_at) VALUES(?,?,?,?, 'MATERIALIZING',?,?,?,?,?,?,?)`, claim.ReviewID, claim.ProjectID, claim.TaskID, claim.RunID, claim.RawResultSHA256, claim.RuntimeWorksetSHA, claim.ProvenanceSHA256, claim.Generation, claim.ClaimToken, formatTime(at), formatTime(at)); e != nil {
			return nil, e
		}
		claims = append(claims, claim)
	}
	openRows, err := tx.QueryContext(ctx, `SELECT review_id FROM review_result_materialization_claims WHERE state='MATERIALIZING' ORDER BY created_at,review_id`)
	if err != nil {
		return nil, err
	}
	var open []string
	for openRows.Next() {
		var id string
		if err = openRows.Scan(&id); err != nil {
			openRows.Close()
			return nil, err
		}
		open = append(open, id)
	}
	err = openRows.Err()
	openRows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range open {
		if _, fresh := admittedSet[id]; fresh {
			continue // already carried by the new-admission pass above
		}
		claim, _, e := loadReviewMaterializationSource(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		claims = append(claims, claim)
	}
	return claims, nil
}

// loadReviewMaterializationSource reads the exact durable inputs of one Review
// inside the claim transaction. For an open claim the stored fence and token
// win over freshly derived values, and a source row that has disappeared still
// yields the claim identity so the finalize transaction can terminalize it.
func loadReviewMaterializationSource(ctx context.Context, tx *sql.Tx, reviewID string) (reviewMaterializationClaim, bool, error) {
	claim := reviewMaterializationClaim{ReviewID: reviewID}
	var storedProject, storedTask, storedRun, storedToken string
	var storedGeneration int
	var storedRaw, storedWorkset, storedProvenance sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT project_id,task_id,run_id,claim_token,generation,raw_result_sha256,runtime_workset_hash,provenance_sha256 FROM review_result_materialization_claims WHERE review_id=?`, reviewID).
		Scan(&storedProject, &storedTask, &storedRun, &storedToken, &storedGeneration, &storedRaw, &storedWorkset, &storedProvenance)
	storedClaim := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return claim, false, err
	}
	var v review.Review
	v, err = scanReview(tx.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, reviewID))
	if errors.Is(err, sql.ErrNoRows) {
		if storedClaim {
			claim.ProjectID, claim.TaskID, claim.RunID = storedProject, storedTask, storedRun
			claim.ClaimToken, claim.Generation = storedToken, storedGeneration
			claim.RawResultSHA256, claim.RuntimeWorksetSHA, claim.ProvenanceSHA256 = storedRaw.String, storedWorkset.String, storedProvenance.String
			return claim, false, nil
		}
		return claim, false, nil
	}
	if err != nil {
		return claim, false, err
	}
	var raw, runtimeHash, runWorkspace, runWorktree, runStart, runEnd, payload, cycle, ref, targets any
	var workspaceBound bool
	err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT an.review_result_json FROM run_analyses an WHERE an.run_id=r.id),r.result_text,''),COALESCE(r.runtime_workset_hash,''),COALESCE(r.workspace_id,''),COALESCE(r.worktree_id,''),COALESCE(r.start_snapshot,''),COALESCE(r.end_snapshot,''),COALESCE(a.payload_sha256,''),COALESCE(s.development_cycle_id,''),COALESCE(s.development_ref,''),COALESCE(v.repository_targets_json,''),(COALESCE(v.workspace_id,'')<>'')
 FROM reviews v LEFT JOIN runs r ON r.id=v.linked_run_id LEFT JOIN feature_area_review_start_requests a ON a.review_id=v.id AND a.outcome='ACCEPTED' LEFT JOIN task_review_sources s ON s.review_id=v.id WHERE v.id=?`, reviewID).
		Scan(&raw, &runtimeHash, &runWorkspace, &runWorktree, &runStart, &runEnd, &payload, &cycle, &ref, &targets, &workspaceBound)
	if err != nil {
		return claim, false, err
	}
	text := stringOf(raw)
	runtime := stringOf(runtimeHash)
	worksetFence := runtime
	if worksetFence == "" {
		worksetFence = reviewRunWorksetFenceHash(v.RunID, stringOf(runWorkspace), stringOf(runWorktree), stringOf(runStart), stringOf(runEnd))
	}
	claim.ProjectID = v.ProjectID
	claim.TaskID = v.TaskID
	claim.RunID = v.RunID
	claim.Raw = text
	claim.Review = v
	if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_execution_records WHERE run_id=?)`, v.RunID).Scan(&claim.NaturalAnalysis); e != nil {
		return claim, false, e
	}
	claim.StartPayloadSHA256 = stringOf(payload)
	claim.DevelopmentCycleID = stringOf(cycle)
	claim.DevelopmentRef = stringOf(ref)
	claim.AdmittedTargetsJSON = stringOf(targets)
	claim.WorkspaceBound = workspaceBound
	claim.RawResultSHA256 = workflowHash(text)
	claim.RuntimeWorksetSHA = worksetFence
	claim.ProvenanceSHA256 = stringOf(payload)
	if storedClaim {
		claim.ClaimToken = storedToken
		claim.Generation = storedGeneration
		if storedRaw.Valid {
			claim.RawResultSHA256 = storedRaw.String
		}
		if storedWorkset.Valid {
			claim.RuntimeWorksetSHA = storedWorkset.String
		}
		if storedProvenance.Valid {
			claim.ProvenanceSHA256 = storedProvenance.String
		}
		return claim, true, nil
	}
	if v.StartRequestID == "" || v.RunID == "" || text == "" || strings.TrimSpace(text) == "" || claim.ProvenanceSHA256 == "" {
		return claim, false, nil
	}
	return claim, true, nil
}

func stringOf(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

// computeReviewMaterialization is phase (2): pure deterministic work outside
// any transaction. It performs the strict parse, schema validation and the
// frozen-scope check against the targets admitted with the claim, and it has
// no runtime or Git side effect.
func (c reviewMaterializationClaim) computeReviewMaterialization() (reviewMaterializationOutcome, error) {
	result, parseErr := review.ParseResult(c.Raw)
	if c.Review.ExecutorKind.Effective() == review.ExecutorCodingAgent && !c.NaturalAnalysis {
		result, parseErr = review.ParseModelResult(c.Raw, c.Review.RepositoryTargets, c.Review.PrimaryMemberID)
	}
	if parseErr != nil {
		return reviewMaterializationOutcome{Terminal: reviewMaterializationInvalid, Kind: materializationKindInvalid, ReasonCode: "RESULT_INVALID", ReasonDetail: parseErr.Error()}, nil
	}
	if err := review.ValidateProposalAdmission(result); err != nil {
		return reviewMaterializationOutcome{Terminal: reviewMaterializationInvalid, Kind: materializationKindInvalid, ReasonCode: "PROPOSAL_NOT_EXECUTABLE", ReasonDetail: err.Error()}, nil
	}
	formal, err := json.Marshal(result)
	if err != nil {
		return reviewMaterializationOutcome{}, err
	}
	if result.Version == 2 && result.ReviewDisposition == review.DispositionApprovalReady {
		if targetErr := validateAdmittedReviewTargets(c.AdmittedTargetsJSON, c.WorkspaceBound, result.RepositoryTargets); targetErr != nil {
			return reviewMaterializationOutcome{Terminal: reviewMaterializationScope, Kind: materializationKindScope, ReasonCode: "FROZEN_TARGET_MISMATCH", ReasonDetail: targetErr.Error()}, nil
		}
	}
	outcome := reviewMaterializationOutcome{Terminal: reviewMaterializationValid, Disposition: result.ReviewDisposition, FormalJSON: string(formal)}
	if result.ReviewDisposition == review.DispositionBlocked {
		outcome.Kind = materializationKindBlocked
		return outcome, nil
	}
	if result.Version == 2 {
		if !c.WorkspaceBound || c.AdmittedTargetsJSON == "" {
			outcome.Kind = materializationKindFormalOnly
			return outcome, nil
		}
	} else if c.Review.RepositoryProvenance == nil {
		outcome.Kind = materializationKindFormalOnly
		return outcome, nil
	}
	v := c.Review
	snap := approvalSnapshot{Version: result.Version, ProjectID: v.ProjectID, FeatureAreaID: v.FeatureAreaID, ReviewID: v.ID, TaskID: v.TaskID, RunID: v.RunID, StartRequestID: v.StartRequestID, StartPayloadSHA256: c.StartPayloadSHA256, Scope: v.Focus, Goal: v.EngineeringGoal, Instruction: v.Instruction, PromptSHA256: v.PromptSHA256, Repository: v.RepositoryProvenance, DevelopmentCycleID: c.DevelopmentCycleID, DevelopmentRef: c.DevelopmentRef, ResultSHA256: workflowHash(string(formal))}
	encoded, err := json.Marshal(snap)
	if err != nil {
		return outcome, err
	}
	outcome.Kind = materializationKindApproval
	outcome.DecisionID = wp.DecisionID(c.ReviewID)
	outcome.ProvenanceJSON = string(encoded)
	outcome.ProvenanceSHA = workflowHash(string(encoded))
	outcome.ResultSHA = snap.ResultSHA256
	return outcome, nil
}

// finalizeReviewMaterializationClaimTx is phase (3). It CASes the exact claim
// (token + generation + MATERIALIZING state), re-verifies every durable fact
// the claim fenced, checks the Task was not canceled or superseded, and then
// commits the formal materialization, its single Decision and the Task Phase
// update together with the FINALIZED terminalization — one commit. Facts that
// can never become eligible again terminalize the claim VOIDED instead.
func finalizeReviewMaterializationClaimTx(ctx context.Context, db *DB, claim reviewMaterializationClaim, outcome reviewMaterializationOutcome, at time.Time) error {
	tx, err := db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, storedProject, storedTask, storedRun, storedToken string
	var storedGeneration int
	var storedRaw, storedWorkset, storedProvenance string
	err = tx.QueryRowContext(ctx, `SELECT project_id,task_id,run_id,state,claim_token,generation,raw_result_sha256,runtime_workset_hash,provenance_sha256 FROM review_result_materialization_claims WHERE review_id=?`, claim.ReviewID).
		Scan(&storedProject, &storedTask, &storedRun, &state, &storedToken, &storedGeneration, &storedRaw, &storedWorkset, &storedProvenance)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "MATERIALIZING" || storedToken != claim.ClaimToken || storedGeneration != claim.Generation || storedRun != claim.RunID || storedTask != claim.TaskID || storedProject != claim.ProjectID {
		return nil // stale, duplicate or late finalizer: only the exact open claim terminalizes
	}
	var reviewTask, linkedRun sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT task_id,linked_run_id FROM reviews WHERE id=?`, claim.ReviewID).Scan(&reviewTask, &linkedRun)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var taskStatus, taskPhase, currentReview sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT status,phase,COALESCE(current_review_id,'') FROM tasks WHERE id=?`, storedTask).Scan(&taskStatus, &taskPhase, &currentReview)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	reviewGone := errors.Is(err, sql.ErrNoRows)
	var runStatus sql.NullString
	var rawText, runtimeHash, runWorkspace, runWorktree, runStart, runEnd sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT status,COALESCE((SELECT an.review_result_json FROM run_analyses an WHERE an.run_id=runs.id),result_text),COALESCE(runtime_workset_hash,''),COALESCE(workspace_id,''),COALESCE(worktree_id,''),COALESCE(start_snapshot,''),COALESCE(end_snapshot,'') FROM runs WHERE id=?`, storedRun).Scan(&runStatus, &rawText, &runtimeHash, &runWorkspace, &runWorktree, &runStart, &runEnd)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	runGone := errors.Is(err, sql.ErrNoRows)
	var payload sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM feature_area_review_start_requests WHERE review_id=? AND outcome='ACCEPTED'`, claim.ReviewID).Scan(&payload); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var materialized, liveClaims bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_result_materializations WHERE review_id=?)`, claim.ReviewID).Scan(&materialized); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE run_id=?)`, storedRun).Scan(&liveClaims); err != nil {
		return err
	}
	// The claim fenced either the canonical bound runtime workset hash or, when
	// the Run binds none, the derived immutable terminal-row fence. Equality
	// with the recomputed value means the fence held.
	currentWorkset := runtimeHash.String
	if currentWorkset == "" {
		currentWorkset = reviewRunWorksetFenceHash(storedRun, runWorkspace.String, runWorktree.String, runStart.String, runEnd.String)
	}
	// Facts that no later transition can restore: the exact claim is
	// terminalized VOIDED without writing any formal result or Decision.
	voidReason := ""
	switch {
	case reviewGone || runGone:
		voidReason = "source gone"
	case !taskStatus.Valid || taskStatus.String != string(task.StatusActive):
		voidReason = "task terminal"
	case !reviewTask.Valid || reviewTask.String != storedTask || !linkedRun.Valid || linkedRun.String != storedRun:
		voidReason = "review identity drifted"
	case !currentReview.Valid || currentReview.String != claim.ReviewID:
		voidReason = "review superseded"
	case !runStatus.Valid || runStatus.String != "SUCCEEDED":
		voidReason = "run not succeeded"
	case !rawText.Valid || workflowHash(rawText.String) != storedRaw:
		voidReason = "raw result drifted"
	case currentWorkset != storedWorkset:
		voidReason = "workset drifted"
	case !payload.Valid || payload.String != storedProvenance:
		voidReason = "provenance drifted"
	case materialized:
		voidReason = "formal result already present"
	}
	if voidReason != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE review_result_materialization_claims SET state='VOIDED',finalized_at=?,updated_at=? WHERE review_id=? AND claim_token=? AND generation=? AND state='MATERIALIZING'`, formatTime(at), formatTime(at), claim.ReviewID, storedToken, storedGeneration); err != nil {
			return err
		}
		return tx.Commit()
	}
	// Transient in-flight fences: the Review conversation may still be
	// quiescing or a formal recovery may park the Task mid-flight. The claim
	// stays open and a later sweep retries finalize.
	if liveClaims || !taskPhase.Valid || taskPhase.String != string(task.PhaseReviewing) {
		return nil
	}
	v, err := scanReview(tx.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, claim.ReviewID))
	if err != nil {
		return err
	}
	raw := rawText.String
	decisionID := ""
	switch outcome.Kind {
	case materializationKindInvalid, materializationKindScope:
		if err = recordReviewResultMaterializationTx(ctx, tx, claim.ReviewID, claim.RunID, outcome.Terminal, raw, outcome.ReasonCode, outcome.ReasonDetail, "", "", at); err != nil {
			return err
		}
		var cycle, ref string
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(s.development_cycle_id,''),COALESCE(s.development_ref,'') FROM task_review_sources s WHERE s.review_id=?`, claim.ReviewID).Scan(&cycle, &ref); err != nil {
			return err
		}
		if err = recordInvalidReviewResolutionDecisionTx(ctx, tx, v, raw, payload.String, cycle, ref, at); err != nil {
			return err
		}
		decisionID = wp.InvalidResultDecisionID(claim.ReviewID)
	default:
		if err = recordReviewResultMaterializationTx(ctx, tx, claim.ReviewID, claim.RunID, reviewMaterializationValid, raw, "", "", outcome.FormalJSON, outcome.ResultSHA, at); err != nil {
			return err
		}
		if outcome.Disposition != "" {
			if _, err = tx.ExecContext(ctx, `UPDATE reviews SET review_disposition=? WHERE id=? AND review_disposition IS NULL`, outcome.Disposition, claim.ReviewID); err != nil {
				return err
			}
		}
		switch outcome.Kind {
		case materializationKindBlocked:
			if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_REQUIRED',required_review_depth='DEEP',updated_at=? WHERE id=? AND current_review_id=? AND status='ACTIVE' AND phase='REVIEWING'`, formatTime(at), claim.TaskID, claim.ReviewID); err != nil {
				return err
			}
		case materializationKindFormalOnly:
		case materializationKindApproval:
			if _, err = tx.ExecContext(ctx, `INSERT INTO review_approval_decisions(decision_id,review_id,project_id,feature_area_id,task_id,start_request_id,provenance_json,provenance_sha256,result_sha256,result_text,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, outcome.DecisionID, claim.ReviewID, v.ProjectID, v.FeatureAreaID, v.TaskID, v.StartRequestID, outcome.ProvenanceJSON, outcome.ProvenanceSHA, outcome.ResultSHA, outcome.FormalJSON, formatTime(at.UTC())); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_APPROVAL_PENDING',required_review_depth=NULL,updated_at=? WHERE id=? AND current_review_id=? AND status='ACTIVE' AND phase='REVIEWING'`, formatTime(at), claim.TaskID, claim.ReviewID); err != nil {
				return err
			}
			decisionID = outcome.DecisionID
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE review_result_materialization_claims SET state='FINALIZED',outcome=?,decision_id=?,finalized_at=?,updated_at=? WHERE review_id=? AND claim_token=? AND generation=? AND state='MATERIALIZING'`, outcome.Terminal, nullString(decisionID), formatTime(at), formatTime(at), claim.ReviewID, storedToken, storedGeneration)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		// The exact claim was terminalized by a competing writer after this
		// transaction read it; roll back without committing any formal write.
		return nil
	}
	return tx.Commit()
}

// resolveReviewRecoveryCompleteTx atomically forms the formal review result,
// Decision, and Task Phase advance for Review Recovery COMPLETE. All effects
// share the same transaction as the RecoveryStatus RESOLVED write, eliminating
// the observable REVIEWING intermediate state.
func resolveReviewRecoveryCompleteTx(ctx context.Context, tx *sql.Tx, v *rs.Record, source run.Run, facts rs.FactualObservation, at time.Time) error {
	if source.Purpose != run.PurposeScopeReview || source.ReviewID == "" {
		return rs.ErrIneligible
	}
	// Parse and validate the review result.
	parsedResult, parseErr := review.ParseResult(string(facts.OwnerResult))
	if parseErr != nil {
		return parseErr
	}
	formalJSON, err := json.Marshal(parsedResult)
	if err != nil {
		return err
	}
	// Compute the Decision ID using the canonical helper.
	decisionID := wp.DecisionID(source.ReviewID)
	resultSHA := workflowHash(string(formalJSON))
	// Read the Review to get project/feature area context.
	reviewRow, err := scanReview(tx.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, source.ReviewID))
	if err != nil {
		return err
	}
	// Identity fence: source Task ID must match the Review's canonical Task ID.
	// Any mismatch indicates stale or inconsistent state; fail closed.
	if source.TaskID != reviewRow.TaskID {
		return rs.ErrConflict
	}
	// The canonical Decision insert requires REVIEWING authority. Re-enter it
	// only for this exact active Review, inside the resolution transaction; no
	// intermediate phase is visible if any later result/Decision write fails.
	var taskPhase string
	if err := tx.QueryRowContext(ctx, `SELECT phase FROM tasks WHERE id=? AND current_review_id=? AND status='ACTIVE'`, reviewRow.TaskID, source.ReviewID).Scan(&taskPhase); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rs.ErrConflict
		}
		return err
	}
	if taskPhase != "REVIEWING" && taskPhase != "RECOVERY_REQUIRED" {
		return rs.ErrConflict
	}
	if taskPhase == "RECOVERY_REQUIRED" {
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEWING',updated_at=? WHERE id=? AND current_review_id=? AND status='ACTIVE' AND phase='RECOVERY_REQUIRED'`, formatTime(at), reviewRow.TaskID, source.ReviewID); err != nil {
			return err
		}
	}
	// Read the start request payload SHA and other context.
	var startPayloadSHA, developmentCycleID, developmentRef, admittedTargetsJSON sql.NullString
	var workspaceBound bool
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(a.payload_sha256,''),COALESCE(s.development_cycle_id,''),COALESCE(s.development_ref,''),COALESCE(v.repository_targets_json,''),(COALESCE(v.workspace_id,'')<>'')
		FROM reviews v
		LEFT JOIN feature_area_review_start_requests a ON a.review_id=v.id AND a.outcome='ACCEPTED'
		LEFT JOIN task_review_sources s ON s.review_id=v.id
		WHERE v.id=?`, source.ReviewID).Scan(&startPayloadSHA, &developmentCycleID, &developmentRef, &admittedTargetsJSON, &workspaceBound); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if parsedResult.Version == 2 && parsedResult.ReviewDisposition == review.DispositionApprovalReady {
		if err := validateAdmittedReviewTargets(admittedTargetsJSON.String, workspaceBound, parsedResult.RepositoryTargets); err != nil {
			return fmt.Errorf("review recovery targets differ from admitted authority: %w", rs.ErrConflict)
		}
	}
	// Build the approval snapshot for the Decision.
	snap := approvalSnapshot{
		Version:            parsedResult.Version,
		ProjectID:          reviewRow.ProjectID,
		FeatureAreaID:      reviewRow.FeatureAreaID,
		ReviewID:           reviewRow.ID,
		TaskID:             reviewRow.TaskID,
		RunID:              reviewRow.RunID,
		StartRequestID:     reviewRow.StartRequestID,
		StartPayloadSHA256: startPayloadSHA.String,
		Scope:              reviewRow.Focus,
		Goal:               reviewRow.EngineeringGoal,
		Instruction:        reviewRow.Instruction,
		PromptSHA256:       reviewRow.PromptSHA256,
		Repository:         reviewRow.RepositoryProvenance,
		DevelopmentCycleID: developmentCycleID.String,
		DevelopmentRef:     developmentRef.String,
		ResultSHA256:       resultSHA,
	}
	provenanceJSON, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	provenanceSHA := workflowHash(string(provenanceJSON))
	// Record the formal materialization.
	if err := recordReviewResultMaterializationTx(ctx, tx, source.ReviewID, source.ID, "VALID", string(facts.OwnerResult), "", "", string(formalJSON), resultSHA, at); err != nil {
		return err
	}
	// Update the review disposition if not already set.
	if parsedResult.ReviewDisposition != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE reviews SET review_disposition=? WHERE id=? AND review_disposition IS NULL`, parsedResult.ReviewDisposition, source.ReviewID); err != nil {
			return err
		}
	}
	// Create the Decision and advance Task Phase based on the disposition.
	switch {
	case parsedResult.ReviewDisposition == review.DispositionBlocked:
		// BLOCKED: revert to REVIEW_REQUIRED for deeper review.
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_REQUIRED',required_review_depth='DEEP',updated_at=? WHERE id=? AND current_review_id=? AND status='ACTIVE' AND phase IN ('REVIEWING','RECOVERY_REQUIRED')`, formatTime(at), reviewRow.TaskID, source.ReviewID)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return rs.ErrConflict
		}
	case parsedResult.Version == 2 && (!workspaceBound || admittedTargetsJSON.String == ""):
		// FORMAL_ONLY: just the formal result, no Decision.
		// For recovery COMPLETE, we require an approval-capable result.
		return fmt.Errorf("review recovery COMPLETE requires approval-capable result: %w", rs.ErrIneligible)
	case parsedResult.Version == 1 && reviewRow.RepositoryProvenance == nil:
		// FORMAL_ONLY: no provenance.
		return fmt.Errorf("review recovery COMPLETE requires provenance for Decision: %w", rs.ErrIneligible)
	default:
		// APPROVAL: create the Decision and advance to REVIEW_APPROVAL_PENDING.
		// A live diagnostic leaves the Task REVIEWING; boot recovery may have
		// placed it in RECOVERY_REQUIRED. Resolution owns either transition.
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_approval_decisions(decision_id,review_id,project_id,feature_area_id,task_id,start_request_id,provenance_json,provenance_sha256,result_sha256,result_text,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, decisionID, source.ReviewID, reviewRow.ProjectID, reviewRow.FeatureAreaID, reviewRow.TaskID, reviewRow.StartRequestID, string(provenanceJSON), provenanceSHA, resultSHA, string(formalJSON), formatTime(at.UTC())); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_APPROVAL_PENDING',required_review_depth=NULL,updated_at=? WHERE id=? AND current_review_id=? AND status='ACTIVE' AND phase IN ('REVIEWING','RECOVERY_REQUIRED')`, formatTime(at), reviewRow.TaskID, source.ReviewID)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return rs.ErrConflict
		}
	}
	return nil
}

// materializeReviewResults drives the three phases with separate transactions.
// It is called after the transaction that produced the Review terminal fact
// has committed, by Reconcile sweeps and at boot; the durable claim makes any
// interruption recoverable without repair writes.
func materializeReviewResults(ctx context.Context, db *DB, at time.Time) error {
	if db == nil {
		return nil
	}
	if hook := db.reviewMaterializationFaultHook; hook != nil {
		if err := hook("", "BEFORE_CLAIM"); err != nil {
			return err
		}
	}
	tx, err := db.beginWrite(ctx)
	if err != nil {
		return err
	}
	claims, err := claimReviewMaterializationsTx(ctx, tx, at)
	if err != nil {
		tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for _, claim := range claims {
		if hook := db.reviewMaterializationFaultHook; hook != nil {
			if err = hook(claim.ReviewID, "AFTER_CLAIM"); err != nil {
				return err
			}
		}
		outcome, err := claim.computeReviewMaterialization()
		if err != nil {
			return err
		}
		if err = finalizeReviewMaterializationClaimTx(ctx, db, claim, outcome, at); err != nil {
			return err
		}
	}
	return nil
}
