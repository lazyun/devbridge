package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
)

// ContinueRecoveryReviewPreparations consumes only an already frozen purpose
// preparation authority. The purpose transaction itself never creates a Run.
// A separate writer atomically records the Run, original start receipt and
// handoff/grant; the ordinary authorized Review queue owns runtime dispatch.
func (s *ReviewStore) ContinueRecoveryReviewPreparations(ctx context.Context, after string) (string, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT p.plan_id,p.owner_id,p.preparation_request_id,p.preparation_payload_hash
 FROM runtime_scope_plans p JOIN runtime_preparation_authorities a ON a.plan_id=p.plan_id
 JOIN recovery_purpose_confirmation_consumptions c ON c.successor_review_id=p.owner_id AND c.intent_id=p.source_receipt_id
 JOIN reviews v ON v.id=p.owner_id JOIN tasks t ON t.id=v.task_id
 WHERE p.owner_kind='REVIEW' AND p.source_command='APPLY_CONFIRMED_REVIEW_PURPOSE' AND a.disposition='CURRENT'
 AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING' AND v.linked_run_id IS NULL
 AND p.owner_id>? ORDER BY p.owner_id LIMIT 32`, after)
	if err != nil {
		return after, err
	}
	type pending struct{ plan, review, request, payload string }
	var values []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.plan, &p.review, &p.request, &p.payload); err != nil {
			rows.Close()
			return after, err
		}
		values = append(values, p)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return after, err
	}
	var failures []error
	for _, p := range values {
		if ctx.Err() != nil {
			return after, errors.Join(append(failures, ctx.Err())...)
		}
		_, err = NewRunStore(s.db).CreatePreparationRun(ctx, p.plan, "run_"+p.review, p.request, p.payload, time.Now().UTC())
		if err != nil {
			failures = append(failures, fmt.Errorf("Review %s preparation: %w", p.review, err))
		}
		after = p.review
	}
	if len(values) < 32 {
		after = ""
	}
	return after, errors.Join(failures...)
}

func recoveryReviewPreparationMetadataTx(ctx context.Context, q definitionQuerier, planID, reviewID string) (runMetadata, error) {
	var empty runMetadata
	plan, err := readArchivedRuntimeSelectionPlanTx(ctx, q, planID)
	if err != nil {
		return empty, err
	}
	if plan.OwnerKind != runtimepreparation.OwnerReview || plan.OwnerID != reviewID || plan.SourceCommand != rs.ApplyConfirmedReviewPurpose {
		return empty, ErrConflict
	}
	if _, err = readRecoveryReviewPurposeEvidenceTx(ctx, q, plan.FeatureAreaID, reviewID); err != nil {
		return empty, err
	}
	var current bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM reviews v JOIN tasks t ON t.id=v.task_id JOIN feature_areas f ON f.id=v.feature_area_id WHERE v.id=? AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING' AND f.status IN ('DISCOVERING','ACTIVE') AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(v.id,t.id)))`, reviewID).Scan(&current)
	if err != nil {
		return empty, err
	}
	if !current {
		return empty, ErrConflict
	}
	var session string
	if err = q.QueryRowContext(ctx, `SELECT intent_ai_session_id FROM recovery_purpose_confirmation_consumptions WHERE successor_review_id=? AND intent_id=?`, reviewID, plan.SourceReceiptID).Scan(&session); err != nil {
		return empty, err
	}
	intent, err := formalConfirmationTx(ctx, q, session, plan.SourceReceiptID)
	if err != nil {
		return empty, err
	}
	for _, variant := range intent.Content.RecoveryPurpose.Proposal.Variants {
		if variant.ID == plan.SelectedVariantID && variant.Review != nil {
			b := variant.Review
			return runMetadata{Purpose: run.PurposeScopeReview, ReviewID: reviewID, CodexInstruction: b.ReviewInstruction, ReviewScope: run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}, ReviewPlan: b.ReviewPlan}, nil
		}
	}
	return empty, ErrConflict
}

// These records are committed with the preparation attempt, never backfilled by
// the dispatcher. A response-loss replay reads the same exact receipt.
func completeRecoveryReviewPreparationTx(ctx context.Context, tx *sql.Tx, planID string, created run.Run, request, payload string, now time.Time) error {
	var sourceCommand string
	if err := tx.QueryRowContext(ctx, `SELECT source_command FROM runtime_scope_plans WHERE plan_id=?`, planID).Scan(&sourceCommand); err != nil {
		return err
	}
	if sourceCommand != rs.ApplyConfirmedReviewPurpose {
		return nil
	}
	linked, err := tx.ExecContext(ctx, `UPDATE reviews SET linked_run_id=? WHERE id=? AND linked_run_id IS NULL`, created.ID, created.ReviewID)
	if err != nil {
		return err
	}
	if n, _ := linked.RowsAffected(); n != 1 {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO feature_area_review_start_requests(feature_area_id,request_id,payload_sha256,outcome,review_id,run_id,created_at) VALUES(?,?,?,'ACCEPTED',?,?,?)`, created.FeatureAreaID, request, payload, created.ReviewID, created.ID, formatTime(now)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE review_grants SET state='PREPARING',run_id=?,runtime_epoch=? WHERE review_id=? AND run_id IS NULL AND EXISTS(SELECT 1 FROM reviews WHERE id=? AND executor_kind='UPPER_LLM')`, created.ID, created.RuntimeEpoch, created.ReviewID, created.ReviewID)
	return err
}

// ReadReviewPreparation is an exact current preparation read. It never adopts
// another owner's latest plan, and legacy Reviews return ErrNotFound.
func (s *RunStore) ReadReviewPreparation(ctx context.Context, runID string) (run.ReviewPreparation, error) {
	var out run.ReviewPreparation
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var planID, reviewID string
	err = tx.QueryRowContext(ctx, `SELECT p.plan_id,p.owner_id FROM runtime_preparation_attempts a JOIN runtime_scope_plans p ON p.plan_id=a.plan_id WHERE a.run_id=? AND p.owner_kind='REVIEW' AND p.source_command='APPLY_CONFIRMED_REVIEW_PURPOSE'`, runID).Scan(&planID, &reviewID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, run.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if _, err = recoveryReviewPreparationMetadataTx(ctx, tx, planID, reviewID); err != nil {
		return out, err
	}
	out.Plan, err = readArchivedRuntimeSelectionPlanTx(ctx, tx, planID)
	if err != nil {
		return out, err
	}
	var current bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN reviews v ON v.id=r.review_id JOIN runtime_preparation_authorities a ON a.plan_id=? WHERE r.id=? AND v.id=? AND v.linked_run_id=r.id AND r.runtime_epoch=? AND r.status IN ('CREATED','PREPARING','RUNNING') AND a.disposition='CURRENT')`, planID, runID, reviewID, s.db.epoch).Scan(&current)
	if err != nil {
		return out, err
	}
	if !current {
		return out, ErrConflict
	}
	sourceID, err := readRecoveryReviewPurposeEvidenceTx(ctx, tx, out.Plan.FeatureAreaID, reviewID)
	if err != nil {
		return out, err
	}
	source, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, sourceID))
	if err != nil {
		return out, err
	}
	if source.RuntimeWorkset == nil {
		return out, ErrConflict
	}
	out.SourceRuntime = *source.RuntimeWorkset
	return out, tx.Commit()
}

// AdoptUnstartedRecoveryReviewPreparations runs before generic crash
// classification. It changes lifecycle epoch only when no external operation
// has been claimed; any other interrupted Review retains the normal recovery
// path and cannot silently repeat workspace or model effects.
func (s *ReviewStore) AdoptUnstartedRecoveryReviewPreparations(ctx context.Context) error {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT p.plan_id,v.id,r.id FROM runtime_scope_plans p JOIN reviews v ON v.id=p.owner_id JOIN runs r ON r.id=v.linked_run_id JOIN tasks t ON t.current_review_id=v.id JOIN feature_areas f ON f.id=v.feature_area_id JOIN runtime_preparation_authorities a ON a.plan_id=p.plan_id
 WHERE p.source_command='APPLY_CONFIRMED_REVIEW_PURPOSE' AND p.owner_kind='REVIEW' AND a.disposition='CURRENT' AND t.status='ACTIVE' AND t.phase='REVIEWING' AND f.status IN ('DISCOVERING','ACTIVE')
 AND r.status='CREATED' AND r.runtime_epoch<>? AND r.thread_id IS NULL AND r.turn_id IS NULL AND r.runtime_workset_json IS NULL
 AND NOT EXISTS(SELECT 1 FROM runtime_workspace_preparation_claims c WHERE c.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM app_server_run_bindings b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(v.id,t.id))
 AND ((v.executor_kind='CODING_AGENT' AND EXISTS(SELECT 1 FROM runtime_handoffs h WHERE h.run_id=r.id AND h.state='REGISTERED')) OR (v.executor_kind='UPPER_LLM' AND EXISTS(SELECT 1 FROM review_grants g WHERE g.review_id=v.id AND g.run_id=r.id AND g.state='PREPARING' AND g.preparation_error IS NULL)))`, s.db.epoch)
	if err != nil {
		return err
	}
	var candidates [][3]string
	for rows.Next() {
		var v [3]string
		if err = rows.Scan(&v[0], &v[1], &v[2]); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, v)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, v := range candidates {
		if err = s.adoptUnstartedRecoveryReviewPreparation(ctx, v[0], v[1], v[2]); err != nil && !errors.Is(err, errReviewPreparationContextChanged) {
			return err
		}
	}
	return nil
}

var errReviewPreparationContextChanged = errors.New("Review preparation project context changed")

func (s *ReviewStore) adoptUnstartedRecoveryReviewPreparation(ctx context.Context, planID, reviewID, runID string) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = recoveryReviewPreparationMetadataTx(ctx, tx, planID, reviewID); err != nil {
		return err
	}
	plan, err := readArchivedRuntimeSelectionPlanTx(ctx, tx, planID)
	if err != nil {
		return err
	}
	topology, development, err := purposeProjectContextTx(ctx, tx, plan.ProjectID)
	if err != nil {
		return err
	}
	if topology != plan.TopologyRevision || development != plan.DevelopmentContextHash {
		return errReviewPreparationContextChanged
	}
	var previous string
	if err = tx.QueryRowContext(ctx, `SELECT runtime_epoch FROM runs WHERE id=?`, runID).Scan(&previous); err != nil {
		return err
	}
	if previous == s.db.epoch {
		return tx.Commit()
	}
	now := formatTime(time.Now().UTC())
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_purpose_preparation_adoptions(review_id,run_id,plan_id,previous_epoch,current_epoch,created_at) VALUES(?,?,?,?,?,?)`, reviewID, runID, planID, previous, s.db.epoch, now); err != nil {
		return err
	}
	for _, statement := range []string{`UPDATE runs SET runtime_epoch=? WHERE id=? AND runtime_epoch=?`, `UPDATE feature_area_agent_claims SET runtime_epoch=? WHERE run_id=? AND runtime_epoch=?`, `UPDATE runtime_handoffs SET runtime_epoch=? WHERE run_id=? AND runtime_epoch=?`, `UPDATE review_grants SET runtime_epoch=? WHERE run_id=? AND runtime_epoch=?`} {
		if _, err = tx.ExecContext(ctx, statement, s.db.epoch, runID, previous); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// A confirmed Review purpose may retain the area's exact managed members, but
// every prior owner must have ended and every root/HEAD is observed anew. The
// immutable purpose chain resolves only its own UNKNOWN source; an unrelated
// uncertain operation continues to block reuse.
func validateRecoveryReviewWorkspaceReuseTx(ctx context.Context, tx *sql.Tx, planID, runID string, observed runtimepreparation.WorkspaceObservation, epoch string) error {
	plan, err := readArchivedRuntimeSelectionPlanTx(ctx, tx, planID)
	if err != nil {
		return err
	}
	if _, err = recoveryReviewPreparationMetadataTx(ctx, tx, planID, plan.OwnerID); err != nil {
		return err
	}
	authority, err := readRuntimeAuthority(ctx, tx, plan.FeatureAreaID)
	if err != nil {
		return err
	}
	if authority.RecoveryRequired {
		return ErrConflict
	}
	var exact bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs current JOIN reviews v ON v.id=current.review_id JOIN feature_area_agent_claims claim ON claim.run_id=current.id
 WHERE current.id=? AND v.id=? AND current.runtime_epoch=? AND claim.runtime_epoch=current.runtime_epoch AND claim.operation_kind='REVIEW' AND claim.operation_id=v.id AND v.linked_run_id=current.id
 AND NOT EXISTS(SELECT 1 FROM run_repository_bindings b JOIN runs prior ON prior.id=b.run_id WHERE b.worktree_id=? AND b.run_id<>current.id AND NOT(prior.project_id=current.project_id AND prior.feature_area_id=current.feature_area_id AND prior.status IN('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED') AND b.repository_id=? AND b.member_id=? AND b.workspace_id=?))
 AND NOT EXISTS(SELECT 1 FROM runtime_workspace_observations b JOIN runs prior ON prior.id=b.run_id WHERE b.worktree_id=? AND b.run_id<>current.id AND NOT(prior.project_id=current.project_id AND prior.feature_area_id=current.feature_area_id AND prior.status IN('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED') AND b.repository_id=? AND b.member_id=? AND b.workspace_id=?)))`, runID, plan.OwnerID, epoch, observed.WorktreeID, observed.RepositoryID, observed.MemberID, observed.WorkspaceID, observed.WorktreeID, observed.RepositoryID, observed.MemberID, observed.WorkspaceID).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		return ErrConflict
	}
	return nil
}

// canonicalUpperReviewWorkspaceTx proves the new canonical workspace slot.
// Legacy Upper v2 Runs leave that slot empty and continue through their
// original per-repository grant path.
func canonicalUpperReviewWorkspaceTx(ctx context.Context, tx *sql.Tx, reviewID, runID string, preparing bool) (bool, error) {
	return canonicalUpperReviewWorkspaceAtEpochTx(ctx, tx, reviewID, runID, preparing, false)
}

// Preparation authority proves the unbound stage; file authority additionally
// requires the complete immutable binding. Explicit Upper recoveries bridge
// lifecycle epochs without rewriting any workspace or start-authority history.
func canonicalUpperReviewWorkspaceAtEpochTx(ctx context.Context, tx *sql.Tx, reviewID, runID string, preparing, priorEpoch bool) (bool, error) {
	var workspace string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(r.workspace_id,'') FROM runs r JOIN review_grants g ON g.review_id=r.review_id AND g.protocol_version=2 WHERE r.id=? AND r.review_id=?`, runID, reviewID).Scan(&workspace)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || workspace == "" {
		return false, err
	}
	var planID, epoch string
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT p.plan_id,r.runtime_epoch,g.generation FROM runtime_scope_plans p
 JOIN runtime_preparation_attempts attempt ON attempt.plan_id=p.plan_id JOIN runs r ON r.id=attempt.run_id
 JOIN reviews v ON v.id=r.review_id JOIN review_grants g ON g.review_id=v.id
 JOIN runtime_preparation_authorities current ON current.plan_id=p.plan_id AND current.disposition='CURRENT'
 JOIN feature_area_agent_claims claim ON claim.run_id=r.id AND claim.operation_kind='REVIEW' AND claim.operation_id=v.id AND claim.runtime_epoch=r.runtime_epoch
 WHERE p.owner_kind='REVIEW' AND p.owner_id=? AND p.source_command='APPLY_CONFIRMED_REVIEW_PURPOSE' AND r.id=?
 AND v.executor_kind='UPPER_LLM' AND v.linked_run_id=r.id AND r.workspace_id=v.workspace_id
 AND g.run_id=r.id AND g.runtime_epoch=r.runtime_epoch AND r.thread_id IS NULL AND r.turn_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM app_server_run_bindings b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_preparation_revocations revoked WHERE revoked.plan_id=p.plan_id)
 AND (? OR r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1))`, reviewID, runID, priorEpoch).Scan(&planID, &epoch, &generation)
	if err != nil {
		return false, ErrConflict
	}
	if _, err = recoveryReviewPreparationMetadataTx(ctx, tx, planID, reviewID); err != nil {
		return false, err
	}
	plan, err := readArchivedRuntimeSelectionPlanTx(ctx, tx, planID)
	if err != nil {
		return false, err
	}
	topology, development, err := purposeProjectContextTx(ctx, tx, plan.ProjectID)
	if err != nil {
		return false, err
	}
	if topology != plan.TopologyRevision || development != plan.DevelopmentContextHash {
		return false, ErrConflict
	}
	var boundEpoch string
	err = tx.QueryRowContext(ctx, `SELECT b.runtime_epoch FROM runtime_workset_bindings b
 JOIN runs r ON r.id=b.run_id JOIN runtime_start_authorities a ON a.authority_id=b.start_authority_id AND a.plan_id=b.plan_id AND a.run_id=r.id
 JOIN runtime_preparation_receipts receipt ON receipt.plan_id=b.plan_id AND receipt.run_id=r.id AND receipt.outcome='BINDING_COMMITTED' AND receipt.start_authority_id=a.authority_id
 WHERE b.plan_id=? AND b.run_id=? AND b.owner_kind='REVIEW' AND b.owner_id=? AND a.owner_kind=b.owner_kind AND a.owner_id=b.owner_id
 AND b.workset_content_hash=r.runtime_workset_hash AND a.workset_content_hash=b.workset_content_hash AND receipt.workset_content_hash=b.workset_content_hash
 AND a.observation_set_hash=b.observation_set_hash AND receipt.observation_set_hash=b.observation_set_hash
 AND b.workset_json=r.runtime_workset_json AND json_extract(b.workset_json,'$.workspaceId')=r.workspace_id AND a.runtime_epoch=b.runtime_epoch`, planID, runID, reviewID).Scan(&boundEpoch)
	if errors.Is(err, sql.ErrNoRows) && preparing {
		var unbound bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r WHERE r.id=? AND r.status IN('CREATED','PREPARING') AND r.runtime_workset_json IS NULL
 AND NOT EXISTS(SELECT 1 FROM runtime_workset_bindings b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_start_authorities a WHERE a.run_id=r.id)
 AND (?=0 OR NOT EXISTS(SELECT 1 FROM runtime_workspace_preparation_claims c WHERE c.run_id=r.id)))`, runID, priorEpoch).Scan(&unbound)
		if err != nil || !unbound {
			return false, ErrConflict
		}
		return true, nil
	}
	if err != nil {
		return false, ErrConflict
	}
	if err = validateUpperPurposeEpochChainTx(ctx, tx, reviewID, runID, boundEpoch, epoch, generation); err != nil {
		return false, err
	}
	value, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, runID))
	if err != nil {
		return false, err
	}
	if value.RuntimeWorkset == nil {
		return false, ErrConflict
	}
	hash, err := value.RuntimeWorkset.ComputeContentHash()
	if err != nil || hash != value.RuntimeWorkset.ContentHash {
		return false, ErrConflict
	}
	return true, nil
}

func validateUpperPurposeEpochChainTx(ctx context.Context, tx *sql.Tx, reviewID, runID, boundEpoch, currentEpoch string, generation int64) error {
	if generation < 1 {
		return ErrConflict
	}
	var receipts int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_operation_recoveries WHERE run_id=? AND operation_kind='REVIEW' AND operation_id=?`, runID, reviewID).Scan(&receipts); err != nil {
		return err
	}
	if generation > receipts+1 {
		return ErrConflict
	}
	epoch := currentEpoch
	for g := generation; g > 1; g-- {
		var previous string
		var count int
		err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(previous_epoch),'') FROM agent_operation_recoveries
 WHERE run_id=? AND operation_kind='REVIEW' AND operation_id=? AND old_conversation_id IS NULL AND new_conversation_id IS NULL
 AND authorized_epoch=? AND grant_generation=? AND previous_epoch<>authorized_epoch
 AND json_extract(request_payload_json,'$.expectedUpperVersion.grantEpoch')=previous_epoch
 AND json_extract(request_payload_json,'$.expectedUpperVersion.grantGeneration')=grant_generation-1
 AND json_extract(request_payload_json,'$.expectedUpperVersion.authorityEpoch')=authorized_epoch
 AND json_extract(result_json,'$.authorizedUpperVersion.grantEpoch')=authorized_epoch
 AND json_extract(result_json,'$.authorizedUpperVersion.grantGeneration')=grant_generation
 AND json_extract(result_json,'$.RunID')=run_id AND json_extract(result_json,'$.OperationID')=operation_id`, runID, reviewID, epoch, g).Scan(&count, &previous)
		if err != nil || count != 1 {
			return ErrConflict
		}
		epoch = previous
	}
	if epoch != boundEpoch {
		return ErrConflict
	}
	return nil
}
