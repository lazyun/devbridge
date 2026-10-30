package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// One Review disposition Decision has exactly one durable status. Every read
// surface (workflow guidance, TaskView, FeatureArea projection, options) and
// every write entry (prepare preflight, commit fence) derives from this reducer,
// so "read says PENDING while the writer says not pending" cannot happen without
// an actual concurrent version change.
const (
	// reviewDecisionPending: the durable Decision row exists, is owned by the
	// Task's current Review, and carries no resolution receipt.
	reviewDecisionPending = "PENDING"
	// reviewDecisionRecoverable is the orphan shape: the exact raw result IS
	// retained as an INVALID/SCOPE_CONFLICT materialization for the Task's
	// current Review, but the Decision row was never written. The commit writer
	// repairs it from that same durable proof before consuming it, so a read
	// surface may offer the resolution action.
	reviewDecisionRecoverable = "RECOVERABLE_PENDING"
	// reviewDecisionSuperseded: the Decision was resolved by a successor Review.
	reviewDecisionSuperseded = "SUPERSEDED"
	// reviewDecisionConsumed: the Decision carries a receipt that created no
	// successor, i.e. its Task gate was abandoned.
	reviewDecisionConsumed = "CONSUMED"
	// reviewDecisionOwnerChanged: a Decision row exists but names another Review
	// or Task, or the Task no longer holds the Review it belongs to.
	reviewDecisionOwnerChanged = "OWNER_CHANGED"
	// reviewDecisionNoAuthority: nothing durable backs this Decision identity.
	reviewDecisionNoAuthority = "NO_AUTHORITY"
)

// ReviewDecisionSnapshot resolves one Decision identity against the durable
// store. Version is the token a confirmation freezes at prepare time and the
// writer re-derives inside its own transaction at commit time.
type ReviewDecisionSnapshot struct {
	AvailableActions  []string
	SuccessorReviewID string
	ResolutionAction  string
	DecisionID        string
	ReviewID          string
	TaskID            string
	ProjectID         string
	FeatureAreaID     string
	CurrentReviewID   string
	TaskPhase         string
	TaskStatus        string
	Materialization   string
	RawResultSHA256   string
	ProvenanceSHA256  string
	ResultSHA256      string
	Version           string
	Status            string
	RequiredAction    string
	ReasonCode        string
	ReasonMessage     string
	DecisionKind      string
	ExpectedPhase     string
	OwnerMatches      bool
	Repairable        bool
	Consumed          bool
	Durable           bool
}

func (s ReviewDecisionSnapshot) actionable() bool {
	return s.Status == reviewDecisionPending || s.Status == reviewDecisionRecoverable
}

// ProjectedStatus is what every read surface shows. The recoverable shape is
// deliberately projected as PENDING: the writer accepts it with a proof-backed
// repair, so a surface that refused it would contradict the same reducer.
func (s ReviewDecisionSnapshot) ProjectedStatus() string {
	if s.actionable() {
		return reviewDecisionPending
	}
	return s.Status
}

func (s ReviewDecisionSnapshot) Message() string { return s.ReasonMessage }

// CanPrepare, CanCommit and CanReprepare are deliberately the same predicate: a
// Decision the writer would refuse must never be offered by a read surface.
func (s ReviewDecisionSnapshot) CanPrepare() bool   { return s.actionable() }
func (s ReviewDecisionSnapshot) CanCommit() bool    { return s.actionable() }
func (s ReviewDecisionSnapshot) CanReprepare() bool { return s.actionable() }

func reviewDecisionKind(decisionID string) string {
	kind, reviewID, ok := strings.Cut(decisionID, ":")
	if !ok || reviewID == "" || (kind != wp.KindReviewApproval && kind != wp.KindInvalidReviewResultResolution) {
		return ""
	}
	return kind
}

// reviewDecisionVersionToken hashes durable identity facts plus the status facts
// a confirmation must notice. It is never the Decision ID, is stable for
// identical state, and changes when the Decision's status or ownership does.
func reviewDecisionVersionToken(s ReviewDecisionSnapshot) string {
	return confirmation.ValueHash([]any{
		s.DecisionID, s.ReviewID, s.TaskID, s.CurrentReviewID, s.TaskPhase, s.TaskStatus,
		s.Materialization, s.RawResultSHA256, s.ProvenanceSHA256, s.ResultSHA256,
		s.Durable, s.Consumed, s.ResolutionAction, s.SuccessorReviewID,
	})
}

func reviewDecisionSnapshotTx(ctx context.Context, q definitionQuerier, decisionID string) (out ReviewDecisionSnapshot, err error) {
	out = ReviewDecisionSnapshot{DecisionID: decisionID, DecisionKind: reviewDecisionKind(decisionID)}
	if out.DecisionKind == "" {
		out.Status = reviewDecisionNoAuthority
		out.ReasonCode = "DECISION_NOT_PENDING"
		out.ReasonMessage = "The frozen Review disposition Decision identity is not recognized."
		out.Version = reviewDecisionVersionToken(out)
		return out, nil
	}
	out.ReviewID = decisionID[len(out.DecisionKind)+1:]
	out.ExpectedPhase = string(task.PhaseReviewApprovalPending)
	if out.DecisionKind == wp.KindInvalidReviewResultResolution {
		out.ExpectedPhase = string(task.PhaseReviewing)
	}

	var reviewTask sql.NullString
	err = q.QueryRowContext(ctx, `SELECT COALESCE(task_id,'') FROM reviews WHERE id=?`, out.ReviewID).Scan(&reviewTask)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		out.TaskID = reviewTask.String
	}
	if out.TaskID != "" {
		var currentReview, phase, status, project, area sql.NullString
		if err = q.QueryRowContext(ctx, `SELECT COALESCE(current_review_id,''),phase,status,project_id,feature_area_id FROM tasks WHERE id=?`, out.TaskID).Scan(&currentReview, &phase, &status, &project, &area); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if err == nil {
			out.CurrentReviewID, out.TaskPhase, out.TaskStatus, out.ProjectID, out.FeatureAreaID = currentReview.String, phase.String, status.String, project.String, area.String
		}
	}

	var outcome, rawHash string
	err = q.QueryRowContext(ctx, `SELECT outcome,raw_result_sha256 FROM review_result_materializations WHERE review_id=?`, out.ReviewID).Scan(&outcome, &rawHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		out.Materialization, out.RawResultSHA256 = outcome, rawHash
	}

	var decisionReview, decisionTask, provenance, resultHash sql.NullString
	err = q.QueryRowContext(ctx, `SELECT COALESCE(review_id,''),COALESCE(task_id,''),COALESCE(provenance_sha256,''),COALESCE(result_sha256,'') FROM review_approval_decisions WHERE decision_id=?`, decisionID).Scan(&decisionReview, &decisionTask, &provenance, &resultHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err == nil {
		out.Durable = true
		out.ProvenanceSHA256, out.ResultSHA256 = provenance.String, resultHash.String
		out.OwnerMatches = decisionReview.String == out.ReviewID && decisionTask.String == out.TaskID
	}

	var receiptOption string
	err = q.QueryRowContext(ctx, `SELECT option FROM decision_resolution_receipts WHERE decision_id=?`, decisionID).Scan(&receiptOption)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	out.Consumed = err == nil
	if out.Consumed {
		table := "review_re_review_requests"
		if out.DecisionKind == wp.KindInvalidReviewResultResolution {
			table = "review_invalid_result_resolutions"
		}
		var action, successor sql.NullString
		err = q.QueryRowContext(ctx, `SELECT action,COALESCE(successor_review_id,'') FROM `+table+` WHERE decision_id=?`, decisionID).Scan(&action, &successor)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		if err == nil {
			out.ResolutionAction, out.SuccessorReviewID = action.String, successor.String
		}
	}

	out.Repairable = !out.Durable && out.DecisionKind == wp.KindInvalidReviewResultResolution
	if out.Repairable {
		if out.Repairable, err = invalidResolutionRepairProofTx(ctx, q, out); err != nil {
			return out, err
		}
	}
	out.finish(out.Repairable)
	return out, nil
}

// finish is the single status, reason, version and action derivation. It reads
// no state: every fact it needs was already loaded.
func (s *ReviewDecisionSnapshot) finish(repairable bool) {
	switch {
	case s.Consumed:
		if s.SuccessorReviewID != "" {
			s.Status = reviewDecisionSuperseded
			s.ReasonCode = "DECISION_SUPERSEDED"
			s.ReasonMessage = "The frozen Review Decision was already resolved by a successor Review."
		} else {
			s.Status = reviewDecisionConsumed
			s.ReasonCode = "DECISION_NOT_PENDING"
			s.ReasonMessage = "The frozen Review Decision was already resolved and is no longer pending."
		}
	case s.Durable && !s.OwnerMatches:
		s.Status = reviewDecisionOwnerChanged
		s.ReasonCode = "DECISION_OWNER_CHANGED"
		s.ReasonMessage = "The frozen Review Decision belongs to another Review or Task."
	case s.Durable && s.OwnerMatches && s.TaskStatus == "ACTIVE" && s.TaskPhase == s.ExpectedPhase && s.CurrentReviewID == s.ReviewID:
		s.Status = reviewDecisionPending
	case s.Durable:
		s.Status = reviewDecisionOwnerChanged
		s.ReasonCode = "DECISION_OWNER_CHANGED"
		s.ReasonMessage = "The Task no longer holds the Review this frozen Decision belongs to."
	case repairable:
		s.Status = reviewDecisionRecoverable
	default:
		s.Status = reviewDecisionNoAuthority
		s.ReasonCode = "DECISION_NOT_PENDING"
		s.ReasonMessage = "No durable Review Decision backs this frozen Decision identity."
	}
	s.Version = reviewDecisionVersionToken(*s)
	if s.DecisionKind != wp.KindInvalidReviewResultResolution {
		// A formal Review approval keeps its own durable action set; this reducer
		// owns only its status and version.
		return
	}
	if s.actionable() {
		s.RequiredAction = "REVIEW_RESULT_RESOLUTION_REQUIRED"
		// The workspace offers REQUEST_RE_REVIEW only as a freshly prepared
		// confirmation, so the copy has to name that path: a Decision can stay
		// actionable while the intent that targeted it is terminally rejected, and
		// "re-review" must never be read as "resubmit the old Card".
		s.ReasonMessage = "The retained Review result is invalid or outside the authorized scope. Request another Review by preparing a new confirmation against this current Decision state; a rejected confirmation is replaced with confirmation_reprepare and never resubmitted."
		s.AvailableActions = []string{"REQUEST_RE_REVIEW", "OPEN_PROJECT_REPOSITORIES", "ABANDON_TASK"}
		return
	}
	s.RequiredAction = "REVIEW_RESULT_RESOLVED"
	s.AvailableActions = []string{"OPEN_PROJECT_REPOSITORIES", "ABANDON_TASK"}
	if s.ReasonMessage == "" {
		s.ReasonMessage = "The retained Review result has already been resolved."
	}
}

// invalidResolutionRepairProofTx answers the only question the repair may rely
// on: is this Review the Task's current gate, in the phase the invalid-result
// Decision governs, with exactly one retained non-VALID materialization over a
// SUCCEEDED Review Run whose raw result still hashes to what was retained, an
// accepted start request, and no resolution of its own? That is the same proof
// the completion writer required when it recorded the materialization, so the
// repaired Decision row is the row that proof should have created.
func invalidResolutionRepairProofTx(ctx context.Context, q definitionQuerier, s ReviewDecisionSnapshot) (bool, error) {
	var proof bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM reviews v JOIN tasks t ON t.id=v.task_id JOIN runs r ON r.id=v.linked_run_id JOIN feature_area_review_start_requests a ON a.review_id=v.id AND a.outcome='ACCEPTED' WHERE v.id=? AND v.task_id=? AND t.id=v.task_id AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING' AND r.status='SUCCEEDED' AND length(coalesce(r.result_text,''))>0 AND (SELECT raw_result_sha256 FROM review_result_materializations WHERE review_id=v.id)=? AND (SELECT outcome FROM review_result_materializations WHERE review_id=v.id) IN ('INVALID','SCOPE_CONFLICT') AND NOT EXISTS(SELECT 1 FROM review_invalid_result_resolutions x WHERE x.previous_review_id=v.id))`, s.ReviewID, s.TaskID, s.RawResultSHA256).Scan(&proof)
	if err != nil {
		return false, err
	}
	return proof, nil
}

// repairInvalidReviewResolutionDecisionTx materializes the Decision row the
// completion writer should have created for a proven retained invalid result.
// It is only ever called by the commit writer, inside the transaction that
// consumes the Decision, and it reuses the completion writer's own builder, so
// there is exactly one Decision constructor and one Decision authority.
func repairInvalidReviewResolutionDecisionTx(ctx context.Context, tx *sql.Tx, s ReviewDecisionSnapshot) error {
	var raw, payload, cycle, ref, recorded string
	err := tx.QueryRowContext(ctx, `SELECT r.result_text,a.payload_sha256,COALESCE(s.development_cycle_id,''),s.development_ref,m.created_at FROM reviews v JOIN runs r ON r.id=v.linked_run_id JOIN feature_area_review_start_requests a ON a.review_id=v.id AND a.outcome='ACCEPTED' JOIN task_review_sources s ON s.review_id=v.id JOIN review_result_materializations m ON m.review_id=v.id WHERE v.id=? AND v.task_id=?`, s.ReviewID, s.TaskID).Scan(&raw, &payload, &cycle, &ref, &recorded)
	if err != nil {
		return err
	}
	v, err := scanReview(tx.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, s.ReviewID))
	if err != nil {
		return err
	}
	at, err := parseTime(recorded)
	if err != nil {
		return err
	}
	return recordInvalidReviewResolutionDecisionTx(ctx, tx, v, raw, payload, cycle, ref, at)
}
