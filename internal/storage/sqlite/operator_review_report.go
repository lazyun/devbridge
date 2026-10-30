package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/review"
)

// GetReviewReportForOperator reads a committed Project result, never the Upper
// grant, submission receipt, file access target, or original caller scope.
func (s *UpperReviewResultStore) GetReviewReportForOperator(ctx context.Context, project, reviewID string) (review.StoredReport, error) {
	var value review.StoredReport
	if err := localoperator.RequireProject(ctx, project); err != nil {
		return value, err
	}
	var raw, created string
	err := s.db.sql.QueryRowContext(ctx, `SELECT p.id,p.review_id,p.run_id,p.ai_session_id,p.baseline,p.payload_sha256,p.report_json,p.created_at,COALESCE(r.start_snapshot,'')
 FROM review_reports p JOIN reviews v ON v.id=p.review_id AND v.linked_run_id=p.run_id
 JOIN runs r ON r.id=p.run_id AND r.review_id=v.id AND r.project_id=v.project_id AND r.task_id IS v.task_id AND r.feature_area_id=v.feature_area_id
 WHERE v.project_id=? AND v.id=? AND v.executor_kind='UPPER_LLM'`, project, reviewID).Scan(&value.ReportID, &value.ReviewID, &value.RunID, &value.CreatedByAISessionID, &value.Baseline, &value.PayloadSHA256, &raw, &created, &value.StartSnapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return value, review.ErrNotFound
	}
	if err != nil {
		return value, err
	}
	if err = json.Unmarshal([]byte(raw), &value.Report); err != nil {
		return value, err
	}
	value.CreatedAt, err = parseTime(created)
	return value, err
}
