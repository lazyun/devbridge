package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

func recoveryOwnerKind(source run.Run) string {
	switch source.Purpose {
	case run.PurposeScopeReview:
		return "REVIEW"
	case run.PurposeInspection:
		return "PROJECT_INSPECTION"
	default:
		return string(source.Purpose)
	}
}

func recoveryGenerationCurrentTx(ctx context.Context, q definitionQuerier, id string, generation int) (bool, error) {
	var current bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_current_generations g JOIN recovery_status_records r ON r.id=g.recovery_id JOIN runs s ON s.id=r.source_run_id WHERE g.recovery_id=? AND g.generation=? AND r.generation=g.generation AND g.owner_id=s.operation_id AND g.owner_kind=CASE s.purpose WHEN 'SCOPE_REVIEW' THEN 'REVIEW' WHEN 'INSPECTION' THEN 'PROJECT_INSPECTION' ELSE s.purpose END)`, id, generation).Scan(&current)
	return current, err
}

func appendInitialPurposeLinkTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent) error {
	p := intent.Content.RecoveryPurpose.Proposal
	current, err := recoveryGenerationCurrentTx(ctx, tx, p.RecoveryID, p.RecoveryGeneration)
	if err != nil {
		return err
	}
	if !current {
		return rs.ErrConflict
	}
	linkID := "purpose-link:" + intent.IntentID
	if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_purpose_link_history SELECT ?,1,'',l.* FROM recovery_purpose_confirmation_links l WHERE recovery_id=? AND recovery_generation=? AND intent_id=?`, linkID, p.RecoveryID, p.RecoveryGeneration, intent.IntentID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_purpose_current_links(recovery_id,recovery_generation,link_id,link_ordinal,selection_version) VALUES(?,?,?,1,1)`, p.RecoveryID, p.RecoveryGeneration, linkID)
	return err
}

type purposeLinkIdentity struct {
	ID                        string
	Ordinal, SelectionVersion int
}

func currentPurposeLinkTx(ctx context.Context, q definitionQuerier, intent confirmation.Intent) (purposeLinkIdentity, error) {
	var out purposeLinkIdentity
	err := q.QueryRowContext(ctx, `SELECT h.link_id,h.link_ordinal,c.selection_version FROM recovery_purpose_link_history h JOIN recovery_purpose_current_links c ON c.recovery_id=h.recovery_id AND c.recovery_generation=h.recovery_generation AND c.link_id=h.link_id AND c.link_ordinal=h.link_ordinal JOIN recovery_current_generations g ON g.recovery_id=h.recovery_id AND g.generation=h.recovery_generation AND g.owner_kind=h.source_owner_kind AND g.owner_id=h.source_owner_id WHERE h.intent_ai_session_id=? AND h.intent_id=? AND h.intent_version=? AND h.intent_content_hash=?`, intent.AISessionID, intent.IntentID, intent.Version, intent.ContentHash).Scan(&out.ID, &out.Ordinal, &out.SelectionVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return out, rs.ErrPurposeReceiptMismatch
	}
	return out, err
}

// Original receipt FK anchors remain in the v89 table. Only history and the
// explicit Core selectors establish current authority after v107.
func appendReplacementPurposeLinkTx(ctx context.Context, tx *sql.Tx, old, next confirmation.Intent, b confirmationReplacementBinding) error {
	const columns = `recovery_id,recovery_generation,intent_ai_session_id,intent_id,intent_version,intent_content_hash,proposal_hash,source_owner_kind,source_owner_id,task_id,feature_area_id,source_execution_id,source_review_id,task_definition_revision_id,source_operation_id,source_run_id,source_turn_id,runtime_epoch,runtime_workset_hash,assessment_hash,created_at`
	p := next.Content.RecoveryPurpose.Proposal
	_, err := tx.ExecContext(ctx, `INSERT INTO recovery_purpose_link_history(link_id,link_ordinal,predecessor_link_id,`+columns+`) SELECT ?,?,?,recovery_id,recovery_generation,?,?,?,?,?,source_owner_kind,source_owner_id,task_id,feature_area_id,source_execution_id,source_review_id,task_definition_revision_id,source_operation_id,source_run_id,source_turn_id,runtime_epoch,runtime_workset_hash,assessment_hash,? FROM recovery_purpose_link_history WHERE link_id=? AND intent_ai_session_id=? AND intent_id=? AND intent_version=? AND intent_content_hash=?`, b.ReplacementLinkID, b.LinkOrdinal, b.PredecessorLinkID, next.AISessionID, next.IntentID, next.Version, next.ContentHash, p.SubmittedHash, formatTime(next.CreatedAt), b.PredecessorLinkID, old.AISessionID, old.IntentID, old.Version, old.ContentHash)
	if err != nil {
		return err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE recovery_purpose_current_links SET link_id=?,link_ordinal=?,selection_version=selection_version+1 WHERE recovery_id=? AND recovery_generation=? AND link_id=? AND link_ordinal=? AND selection_version=?`, b.ReplacementLinkID, b.LinkOrdinal, b.RecoveryID, b.RecoveryGeneration, b.PredecessorLinkID, b.PredecessorLinkOrdinal, b.ExpectedSelectionVersion)
	if err != nil {
		return err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return fmt.Errorf("Recovery purpose link CAS: %w", confirmation.ErrRequestConflict)
	}
	return nil
}
