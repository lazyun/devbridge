package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/review"
)

// PendingAuthorizedReviewStarts returns only exact, durable start admissions
// whose current Review still owns an unstarted Run.  It is a read-side worker
// queue: callers must use the returned reservation and must never create a
// substitute Review or Run.
func (s *ReviewStore) PendingAuthorizedReviewStarts(ctx context.Context, afterReviewID string) ([]review.AuthorizedStart, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT a.review_id,a.run_id,a.request_id,a.payload_sha256
FROM feature_area_review_start_requests a
JOIN reviews v ON v.id=a.review_id
JOIN tasks t ON t.id=v.task_id AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING'
JOIN runs r ON r.id=a.run_id AND r.review_id=v.id AND r.status='CREATED'
LEFT JOIN runtime_handoffs h ON h.run_id=r.id
LEFT JOIN review_grants g ON g.review_id=v.id
WHERE a.outcome='ACCEPTED' AND a.review_id>? AND (
 (v.executor_kind='CODING_AGENT' AND h.state='REGISTERED') OR
 (v.executor_kind='UPPER_LLM' AND g.run_id=r.id AND g.state='PREPARING' AND g.preparation_error IS NULL)
)
ORDER BY a.review_id LIMIT 100`, afterReviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []review.AuthorizedStart
	for rows.Next() {
		var reviewID, runID, requestID, payload string
		if err := rows.Scan(&reviewID, &runID, &requestID, &payload); err != nil {
			return nil, err
		}
		value, err := scanReview(s.db.sql.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, reviewID))
		if err != nil {
			return nil, err
		}
		reserved, err := scanRun(s.db.sql.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.review_id=?`, runID, reviewID))
		if err != nil {
			return nil, err
		}
		out = append(out, review.AuthorizedStart{Admission: review.StartAdmission{Review: value, Run: reserved, Replayed: true}, RequestID: requestID, PayloadSHA256: payload})
	}
	return out, rows.Err()
}

// ReplayAdmittedStart only reads the original durable admission and its payload
// hash. It never admits a new Review or consults today's eligibility/config.
func (s *ReviewStore) ReplayAdmittedStart(ctx context.Context, projectID, taskID, areaID, requestID, payloadHash string) (review.StartAdmission, bool, error) {
	return s.replayAdmittedStart(ctx, projectID, taskID, areaID, requestID, payloadHash, "", "")
}

// ReplayAdmittedStartForOperator returns only a receipt created by this exact
// durable local principal; it cannot expose or resume a Session-authored start.
func (s *ReviewStore) ReplayAdmittedStartForOperator(ctx context.Context, projectID, taskID, areaID, requestID, payloadHash, actorID string) (review.StartAdmission, bool, error) {
	return s.replayAdmittedStart(ctx, projectID, taskID, areaID, requestID, payloadHash, string(review.AuthorLocalOperator), actorID)
}

func (s *ReviewStore) replayAdmittedStart(ctx context.Context, projectID, taskID, areaID, requestID, payloadHash, expectedKind, expectedActor string) (review.StartAdmission, bool, error) {
	var storedHash, outcome string
	var reviewID, runID, ownerKind, ownerID, ownerRunID sql.NullString
	err := s.db.sql.QueryRowContext(ctx, `SELECT r.payload_sha256,r.outcome,r.review_id,r.run_id,r.owner_operation_kind,r.owner_operation_id,r.owner_run_id FROM feature_area_review_start_requests r JOIN feature_areas f ON f.id=r.feature_area_id WHERE f.project_id=? AND r.feature_area_id=? AND r.request_id=?`, projectID, areaID, requestID).Scan(&storedHash, &outcome, &reviewID, &runID, &ownerKind, &ownerID, &ownerRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return review.StartAdmission{}, false, nil
	}
	if err != nil {
		return review.StartAdmission{}, false, err
	}
	if storedHash != payloadHash {
		return review.StartAdmission{}, true, review.ErrRequestConflict
	}
	if outcome == "FEATURE_AREA_BUSY" {
		return review.StartAdmission{}, true, &review.AdmissionBusyError{Owner: agentconversation.Claim{FeatureAreaID: areaID, Kind: agentconversation.OperationKind(ownerKind.String), OperationID: ownerID.String, RunID: ownerRunID.String}}
	}
	value, err := s.Get(ctx, reviewID.String)
	if err != nil {
		return review.StartAdmission{}, true, err
	}
	if value.ProjectID != projectID || value.TaskID != taskID || value.FeatureAreaID != areaID || value.StartRequestID != requestID || value.RunID != runID.String {
		return review.StartAdmission{}, true, review.ErrRequestConflict
	}
	if expectedKind != "" && (value.AuthorKind != expectedKind || value.AuthorID != expectedActor || value.CreatedByAISessionID != "") {
		return review.StartAdmission{}, true, review.ErrRequestConflict
	}
	reserved, err := scanRun(s.db.sql.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.project_id=? AND r.task_id=? AND r.review_id=?`, runID.String, projectID, taskID, value.ID))
	return review.StartAdmission{Review: value, Run: reserved, Replayed: true}, true, err
}
