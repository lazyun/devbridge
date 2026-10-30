package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lazyun/devbridge/internal/review"
)

// ConfirmationReadEligibility is the pure owner/grant predicate from the
// original Upper file-read path. It does not open files or publish evidence.
func (s *UpperReviewAccessStore) ConfirmationReadEligibility(ctx context.Context, t review.UpperTarget) (bool, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = instructionCaller(ctx, tx, t.AISessionID); err != nil {
		return false, err
	}
	var protocol int
	if err = tx.QueryRowContext(ctx, `SELECT protocol_version FROM review_grants WHERE review_id=?`, t.ReviewID).Scan(&protocol); err != nil {
		return false, err
	}
	if protocol == 2 {
		err = requireUpperReviewCurrentTargetV2Tx(ctx, tx, t)
		if err != nil {
			return false, nil
		}
		var active bool
		err = tx.QueryRowContext(ctx, `SELECT state='ACTIVE' FROM review_grants WHERE review_id=?`, t.ReviewID).Scan(&active)
		return active, err
	}
	_, _, _, err = s.upperAccessTx(ctx, tx, t, false)
	if errors.Is(err, review.ErrTargetStale) || errors.Is(err, review.ErrConflict) {
		return false, nil
	}
	return err == nil, err
}
