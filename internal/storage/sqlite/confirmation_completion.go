package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/lazyun/devbridge/internal/confirmation"
)

// completedConfirmation proves only the recorded confirmation-step lifecycle.
// It does not issue runtime platform authority or substitute for a formal
// approval archive, qualification, or a T1/T2/T3 business receipt.
func completedConfirmation(out confirmation.Intent) bool {
	if out.Commit == nil || out.Commit.RequestID == "" || !confirmationCommitIdentity(out, *out.Commit) || len(out.Steps) == 0 {
		return false
	}
	seen := make(map[[2]string]bool, len(out.Steps))
	for _, step := range out.Steps {
		key := [2]string{step.Kind, step.RequestID}
		if step.Kind == "" || step.RequestID == "" || step.State != "COMPLETED" || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func completedStepReplay(out confirmation.Intent, version int, step confirmation.Step, state string) error {
	if out.Version != version || state != "COMPLETED" || !completedConfirmation(out) {
		return ErrConflict
	}
	for _, old := range out.Steps {
		if old.Kind == step.Kind && old.RequestID == step.RequestID {
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(step)
			if string(a) == string(b) {
				return nil
			}
			break
		}
	}
	return ErrConflict
}

func (s *ConfirmationStore) readCompletedStepReplay(ctx context.Context, session, id string, version int, step confirmation.Step, state string) (out confirmation.Intent, completed bool, err error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback()
	out, err = confirmationRead(ctx, tx, session, id)
	if err != nil {
		return out, false, err
	}
	if out.State != "COMPLETED" {
		return out, false, nil
	}
	return out, true, completedStepReplay(out, version, step, state)
}
