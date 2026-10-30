package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/confirmation"
)

// purposeProjectContextTx freezes the same Project topology and active
// development branch identity consumed by canonical Confirmation fences.
func purposeProjectContextTx(ctx context.Context, tx *sql.Tx, projectID string) (int64, string, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM project_repository_topologies WHERE project_id=?`, projectID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		revision = 0
	} else if err != nil {
		return 0, "", err
	}
	type branchIdentity struct {
		RepositoryID     string `json:"repositoryId"`
		DevelopmentRef   string `json:"developmentRef"`
		DevelopmentCycle string `json:"developmentCycleId"`
		SelectedHead     string `json:"selectedHeadSha"`
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,COALESCE(c.development_ref,''),COALESCE(c.id,''),COALESCE(c.selected_head_sha,'') FROM repositories r LEFT JOIN development_cycles c ON c.project_id=r.project_id AND c.repository_id=r.id AND c.superseded_at IS NULL WHERE r.project_id=? AND r.registration_state='ACTIVE' ORDER BY r.id`, projectID)
	if err != nil {
		return 0, "", err
	}
	defer rows.Close()
	identities := []branchIdentity{}
	for rows.Next() {
		var value branchIdentity
		if err = rows.Scan(&value.RepositoryID, &value.DevelopmentRef, &value.DevelopmentCycle, &value.SelectedHead); err != nil {
			return 0, "", err
		}
		identities = append(identities, value)
	}
	if err = rows.Err(); err != nil {
		return 0, "", err
	}
	return revision, confirmation.ValueHash(identities), nil
}
