package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/recovery"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
)

type agentRecoveryFacts struct {
	claim            agentconversation.Claim
	run              run.Run
	conversation     agentconversation.Conversation
	boundary         agentconversation.Boundary
	category, detail string
}

// classifyAgentClaimTx uses durable identities and dispatch observations only.
// NULL legacy epochs are deliberately not attributed to the new writer.
func classifyAgentClaimTx(ctx context.Context, tx *sql.Tx, claim agentconversation.Claim) (agentRecoveryFacts, error) {
	f := agentRecoveryFacts{claim: claim, category: "CONFLICT", detail: "Durable Agent ownership facts conflict; preserve history and reconcile the exact recorded identities before retrying recovery."}
	var err error
	f.run, err = scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, claim.RunID))
	if err != nil {
		return f, err
	}
	var proven bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_executions e WHERE e.id=? AND e.provenance_kind<>'LEGACY_UNKNOWN' AND e.task_id=? AND e.project_id=? AND COALESCE(e.feature_area_id,'')=? AND e.semantic_owner_kind=? AND e.semantic_owner_id=?)`, f.run.AgentExecutionID, f.run.TaskID, f.run.ProjectID, f.run.FeatureAreaID, f.run.OperationKind, f.run.OperationID).Scan(&proven); err != nil {
		return f, err
	}
	if !proven {
		f.detail = "Legacy unknown Agent Execution provenance is read only; recovery cannot grant authority."
		return f, nil
	}
	fa := f.run.FeatureAreaID
	kind, owner := agentconversation.OperationKind(f.run.OperationKind), f.run.OperationID
	if fa != claim.FeatureAreaID || kind != claim.Kind || owner != claim.OperationID || f.run.RuntimeEpoch != claim.RuntimeEpoch {
		return f, nil
	}
	b := &f.boundary
	err = tx.QueryRowContext(ctx, `SELECT run_id,agent_conversation_id,COALESCE(entry_boundary_turn_id,''),COALESCE(exit_boundary_turn_id,''),advance_state,quiesced,thread_start_pending FROM agent_operation_boundaries WHERE run_id=?`, claim.RunID).Scan(&b.RunID, &b.AgentConversationID, &b.EntryBoundaryTurnID, &b.ExitBoundaryTurnID, &b.Advance, &b.Quiesced, &b.ThreadStartPending)
	if errors.Is(err, sql.ErrNoRows) {
		if f.run.ThreadID == "" && f.run.TurnID == "" && (f.run.Status == run.StatusCreated || f.run.Status == run.StatusPreparing || terminalAgentRun(f.run.Status)) {
			f.category, f.detail = "NO_ADVANCE", "Run reserved without entering the backend dispatch boundary."
		}
		return f, nil
	}
	if err != nil {
		return f, err
	}
	f.conversation, err = scanAgentConversation(tx.QueryRowContext(ctx, selectAgentConversation+` WHERE id=?`, b.AgentConversationID))
	if err != nil {
		return f, err
	}
	a := f.conversation
	if a.FeatureAreaID != fa || a.EndedAt != nil {
		return f, nil
	}
	var intentAdvance, intentExit, intentEpoch, intentFA, intentKind, intentOwner, intentConversation string
	var intentQuiesced, applied bool
	err = tx.QueryRowContext(ctx, `SELECT advance_state,COALESCE(exit_boundary_turn_id,''),runtime_epoch,feature_area_id,operation_kind,operation_id,agent_conversation_id,quiesced,applied_at IS NOT NULL FROM agent_finalization_intents WHERE run_id=?`, claim.RunID).Scan(&intentAdvance, &intentExit, &intentEpoch, &intentFA, &intentKind, &intentOwner, &intentConversation, &intentQuiesced, &applied)
	hasIntent := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return f, err
	}
	if hasIntent && (intentEpoch != claim.RuntimeEpoch || intentFA != fa || intentKind != string(kind) || intentOwner != owner || intentConversation != a.ID) {
		return f, nil
	}
	if hasIntent && intentAdvance == "KNOWN_ADVANCE" {
		if !intentQuiesced || b.ThreadStartPending || f.run.TurnID != intentExit || intentExit == "" || f.run.ThreadID != a.BackendThreadID || f.run.ThreadID == "" {
			return f, nil
		}
		if applied {
			if b.Advance != agentconversation.KnownAdvance || !b.Quiesced || b.ExitBoundaryTurnID != intentExit || a.CurrentBoundaryTurnID != intentExit || !a.ContinuationSafe {
				return f, nil
			}
		} else if b.Quiesced || b.Advance != agentconversation.Unknown || a.CurrentBoundaryTurnID != b.EntryBoundaryTurnID {
			return f, nil
		}
		f.category, f.detail = "KNOWN_SAFE", "Exact completed Turn and quiescence were durably observed; finalize without replaying backend actions."
		return f, nil
	}
	if b.Advance == agentconversation.KnownAdvance {
		if b.Quiesced && !b.ThreadStartPending && a.ContinuationSafe && a.CurrentBoundaryTurnID == b.ExitBoundaryTurnID && f.run.TurnID == b.ExitBoundaryTurnID && f.run.ThreadID == a.BackendThreadID && f.run.ThreadID != "" && (!hasIntent || (applied && intentAdvance == "KNOWN_ADVANCE")) {
			f.category, f.detail = "KNOWN_SAFE", "Persisted exact safe completed boundary is available."
		}
		return f, nil
	}
	if b.Advance == agentconversation.NoAdvance && !b.ThreadStartPending {
		if f.run.TurnID != "" || a.CurrentBoundaryTurnID != b.EntryBoundaryTurnID || (hasIntent && intentAdvance != "NO_ADVANCE") {
			return f, nil
		}
		if claim.RuntimeEpoch == "" && a.BackendThreadID == "" {
			f.category, f.detail = "UNKNOWN", "Legacy unbound thread creation has no durable dispatch marker; the backend outcome cannot be proven."
		} else {
			f.category, f.detail = "NO_ADVANCE", "Dispatch marker proves no Turn was sent and thread identity was not lost."
		}
		return f, nil
	}
	if b.Advance == agentconversation.Unknown && (!hasIntent || intentAdvance == "UNKNOWN") {
		f.category, f.detail = "UNKNOWN", "Backend dispatch may have advanced; explicitly abandon this exact old operation and start a fresh context."
	}
	return f, nil
}

// interruptAgentProductsTx preserves terminal results and history. Interrupted
// nonterminal work is failed, never inferred successful from a backend boundary.
func interruptAgentProductsTx(ctx context.Context, tx *sql.Tx, f agentRecoveryFacts, now time.Time) error {
	id := f.run.ID
	// A recorded exact interrupted Turn and safe finalization intent survive
	// crashes without reclassifying cancellation as an inferred failure.
	var canceled bool
	if f.category == "KNOWN_SAFE" {
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations c JOIN agent_finalization_intents i ON i.run_id=c.run_id WHERE c.run_id=? AND c.terminal_observed=1 AND i.advance_state='KNOWN_ADVANCE' AND i.quiesced=1 AND c.turn_id=i.exit_boundary_turn_id AND c.runtime_epoch=i.runtime_epoch)`, id).Scan(&canceled); err != nil {
			return err
		}
	}
	if canceled {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status='CANCELED',failure_class='CANCELED',failure_code='EXECUTION_CANCELED',failure_stage='recovery',failure_message='Recovered exact interrupted Turn finalization',finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND status NOT IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')`, formatTime(now), formatTime(now), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_token_usage SET status='UNAVAILABLE',updated_at=? WHERE run_id=? AND status='PENDING'`, formatTime(now), id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE approvals SET status='EXPIRED' WHERE run_id=? AND status IN ('PENDING','DECIDED')`, id)
		if err != nil {
			return err
		}
		// Upper LLM work has no Codex conversation to recreate.  Marking its
		// exact grant canceled fences both a late report and any new read.
		_, err = tx.ExecContext(ctx, `UPDATE review_grants SET state='CANCELED' WHERE run_id=? AND state IN ('PREPARING','ACTIVE','AWAITING_REPORT','FINALIZING') AND EXISTS(SELECT 1 FROM reviews WHERE reviews.id=review_grants.review_id AND executor_kind='UPPER_LLM')`, id)
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status='INFRASTRUCTURE_FAILED',failure_class='INFRASTRUCTURE_FAILED',failure_code=?,failure_stage='recovery',failure_message=?,finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND status NOT IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')`, recovery.InterruptedCode, f.detail, formatTime(now), formatTime(now), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_token_usage SET status='UNAVAILABLE',updated_at=? WHERE run_id=? AND status='PENDING'`, formatTime(now), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE approvals SET status='EXPIRED' WHERE run_id=? AND status IN ('PENDING','DECIDED')`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE review_grants SET state='RECOVERY_REQUIRED' WHERE run_id=? AND state IN ('PREPARING','ACTIVE','AWAITING_REPORT','FINALIZING') AND EXISTS(SELECT 1 FROM reviews WHERE reviews.id=review_grants.review_id AND executor_kind='UPPER_LLM')`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE execution_proposals SET outcome='FAILED',failure_code=?,failure_message=?,updated_at=? WHERE id=? AND outcome='RUNNING'`, recovery.InterruptedCode, f.detail, formatTime(now), f.run.ExecutionProposalID); err != nil {
		return err
	}
	if f.run.Purpose != run.PurposeScopeReview {
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status='FAILED',updated_at=? WHERE id=? AND status IN ('READY','RUNNING','CANCELED')`, formatTime(now), f.run.TaskID); err != nil {
			return err
		}
	}
	return nil
}

// RecoverAgentOperations runs only under the new exclusive writer. This is the
// sole bounded authority to reconcile Runs owned by older (including legacy)
// epochs; ordinary callbacks cannot adopt them.
func (s *RecoveryStore) RecoverAgentOperations(ctx context.Context, now time.Time) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT feature_area_id,operation_kind,operation_id,run_id,created_at,COALESCE(runtime_epoch,'') FROM feature_area_agent_claims WHERE COALESCE(runtime_epoch,'')<>? AND NOT EXISTS(SELECT 1 FROM app_server_run_bindings b JOIN runs r ON r.id=b.run_id WHERE b.run_id=feature_area_agent_claims.run_id AND r.status='RUNNING' AND r.turn_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents f WHERE f.run_id=r.id)) ORDER BY feature_area_id`, s.db.epoch)
	if err != nil {
		return err
	}
	var claims []agentconversation.Claim
	for rows.Next() {
		v, e := scanAgentClaim(rows)
		if e != nil {
			rows.Close()
			return e
		}
		claims = append(claims, v)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	var hasRecoveryStatus bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='recovery_status_records')`).Scan(&hasRecoveryStatus); err != nil {
		return err
	}
	var classified []agentRecoveryFacts
	for _, claim := range claims {
		if hasRecoveryStatus {
			var owned bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_status_records WHERE source_run_id=?)`, claim.RunID).Scan(&owned); err != nil {
				return err
			}
			// Explicit status reconciliation owns this source even if its old Run
			// already failed. Startup must not reclassify or release its authority.
			if owned {
				continue
			}
		}
		f, e := classifyAgentClaimTx(ctx, tx, claim)
		if e != nil {
			return e
		}
		var readOnly bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_executions WHERE id=? AND provenance_kind='LEGACY_UNKNOWN')`, f.run.AgentExecutionID).Scan(&readOnly); e != nil {
			return e
		}
		if readOnly {
			continue
		}
		var upper bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM reviews WHERE id=? AND executor_kind='UPPER_LLM' AND linked_run_id=?)`, f.run.ReviewID, f.run.ID).Scan(&upper); e != nil {
			return e
		}
		if upper {
			// Accepted cancellation is continuation of its original durable
			// intent, not new recovery authorization for the current epoch.
			var pending bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE owner_type='REVIEW' AND owner_id=? AND run_id=? AND task_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))`, f.run.ReviewID, f.run.ID, f.run.TaskID).Scan(&pending); e != nil {
				return e
			}
			if pending {
				if _, e = convergePendingUpperCancellationTx(ctx, tx, f.run, s.db.epoch); e != nil {
					return e
				}
				continue
			}
			// The external reviewer has no Codex boundary to classify. Preserve
			// its exact Run and claim for the explicit Upper recovery command.
			if _, e = tx.ExecContext(ctx, `UPDATE review_grants SET state='RECOVERY_REQUIRED' WHERE review_id=? AND run_id=? AND runtime_epoch=? AND state IN ('PREPARING','ACTIVE','AWAITING_REPORT')`, f.run.ReviewID, f.run.ID, f.run.RuntimeEpoch); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET classification=excluded.classification,detail=excluded.detail,classified_epoch=excluded.classified_epoch`, f.run.ID, "UNKNOWN", "Upper LLM review requires explicit same-Review reauthorization after restart.", s.db.epoch); e != nil {
				return e
			}
			classified = append(classified, f)
			continue
		}
		var pendingCancellation bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE run_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))`, f.run.ID).Scan(&pendingCancellation); e != nil {
			return e
		}
		if pendingCancellation && (f.category == "UNKNOWN" || (f.category == "NO_ADVANCE" && f.boundary.RunID == "")) {
			if e = orphanCancellationTx(ctx, tx, f.run.ID, s.db.epoch, now); e != nil {
				return e
			}
			continue
		}

		if f.category != "CONFLICT" {
			if e = interruptAgentProductsTx(ctx, tx, f, now); e != nil {
				return e
			}
			if f.category == "NO_ADVANCE" || f.category == "KNOWN_SAFE" {
				value, e := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, claim.RunID))
				if e != nil {
					return e
				}
				if e = finalizeAgentRunTx(ctx, tx, value); e != nil {
					return e
				}
				if f.boundary.RunID != "" && f.category == "NO_ADVANCE" {
					if _, e = tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET quiesced=1 WHERE run_id=? AND quiesced=0 AND advance_state='NO_ADVANCE' AND thread_start_pending=0`, claim.RunID); e != nil {
						return e
					}
					if _, e = tx.ExecContext(ctx, `UPDATE agent_conversations SET continuation_safe=1 WHERE id=? AND ended_at IS NULL`, f.conversation.ID); e != nil {
						return e
					}
				}
				result, e := tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE feature_area_id=? AND operation_kind=? AND operation_id=? AND run_id=? AND COALESCE(runtime_epoch,'')=?`, claim.FeatureAreaID, claim.Kind, claim.OperationID, claim.RunID, claim.RuntimeEpoch)
				if e != nil {
					return e
				}
				if n, _ := result.RowsAffected(); n != 1 {
					return agentconversation.ErrConflict
				}
				if _, e = tx.ExecContext(ctx, `UPDATE agent_conversations SET ended_at=? WHERE feature_area_id=? AND ended_at IS NULL AND EXISTS(SELECT 1 FROM feature_areas WHERE id=? AND (legacy_read_only=1 OR status NOT IN ('DISCOVERING','ACTIVE')))`, formatTime(now), claim.FeatureAreaID, claim.FeatureAreaID); e != nil {
					return e
				}
			} else if f.conversation.ID != "" {
				if _, e = tx.ExecContext(ctx, `UPDATE agent_conversations SET continuation_safe=0 WHERE id=? AND ended_at IS NULL`, f.conversation.ID); e != nil {
					return e
				}
				// Legacy unbound NO_ADVANCE cannot certify thread/start never happened.
				if f.boundary.Advance == agentconversation.NoAdvance && !f.boundary.Quiesced {
					if _, e = tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET advance_state='UNKNOWN',thread_start_pending=1 WHERE run_id=? AND quiesced=0`, claim.RunID); e != nil {
						return e
					}
				}
			}
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET classification=excluded.classification,detail=excluded.detail,classified_epoch=excluded.classified_epoch`, claim.RunID, f.category, f.detail, s.db.epoch); e != nil {
			return e
		}
		if err = settleExecutionCancellationsTx(ctx, tx, f.run.ID); err != nil {
			return err
		}
		classified = append(classified, f)
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for _, f := range classified {
		observability.Info(ctx, "recovery", "agent.recovery.classified", "Agent startup recovery classified", map[string]any{"featureAreaId": f.claim.FeatureAreaID, "runId": f.run.ID, "operationKind": f.claim.Kind, "conversationId": f.conversation.ID, "runtimeEpoch": f.claim.RuntimeEpoch, "classification": f.category})
	}
	return nil
}

// State gives upper AI the exact recovery target without exposing the internal
// epoch in model arguments. Reads use a snapshot so claim/conversation agree.
func (s *AgentConversationStore) State(ctx context.Context, fa string) (agentconversation.State, error) {
	state := agentconversation.State{FeatureAreaID: fa, Status: "READY", Message: "Agent context is ready for work."}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	var currentAllowed bool
	if err = tx.QueryRowContext(ctx, `SELECT legacy_read_only=0 AND status IN ('DISCOVERING','ACTIVE') FROM feature_areas WHERE id=?`, fa).Scan(&currentAllowed); errors.Is(err, sql.ErrNoRows) {
		return state, agentconversation.ErrNotFound
	} else if err != nil {
		return state, err
	}
	claim, err := scanAgentClaim(tx.QueryRowContext(ctx, `SELECT feature_area_id,operation_kind,operation_id,run_id,created_at,COALESCE(runtime_epoch,'') FROM feature_area_agent_claims WHERE feature_area_id=?`, fa))
	if errors.Is(err, agentconversation.ErrNotFound) {
		var id string
		err = tx.QueryRowContext(ctx, `SELECT id FROM agent_conversations WHERE feature_area_id=? AND ended_at IS NULL`, fa).Scan(&id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return state, err
		}
		state.AgentConversationID = id
		if !currentAllowed {
			state.Status, state.Message = "UNAVAILABLE", "This historical FeatureArea is unavailable for new Agent work."
		}
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.Claim = &claim
	if claim.Kind == agentconversation.Review {
		executor, e := reviewClaimExecutorTx(ctx, tx, claim)
		if errors.Is(e, agentconversation.ErrConflict) {
			state.Status, state.Message = "UNAVAILABLE", "The claimed Review has a missing, unknown, or conflicting executor identity."
			return state, nil
		}
		if e != nil {
			return state, e
		}
		if executor == review.ExecutorUpperLLM {
			facts, e := upperReviewOperationFactsTx(ctx, tx, claim, s.db.epoch)
			if e != nil {
				return state, e
			}
			if facts.reason != "" || !facts.currentOwner {
				state.Status, state.Message = "UNAVAILABLE", facts.reason
				if state.Message == "" {
					state.Message = "The Upper Review is not the current active Task Review."
				}
				return state, nil
			}
			if claim.RuntimeEpoch != s.db.epoch || facts.state == "RECOVERY_REQUIRED" {
				state.Status, state.Message = "RECOVERY_REQUIRED", "Upper Review authorization belongs to an interrupted runtime; inspect the exact recovery action and its observed authorization version."
			} else if facts.state == "PREPARING" || facts.state == "ACTIVE" || facts.state == "AWAITING_REPORT" || facts.state == "FINALIZING" {
				// A same-Run recovery retains historical startup classification.
				// Current durable grant/claim epochs are the Upper authority;
				// a missing Coding boundary is not UNKNOWN context here.
				state.Status, state.Message = "BUSY", "The exact Upper Review owns this FeatureArea. File access still requires its current grant and snapshot checks."
			} else {
				state.Status, state.Message = "UNAVAILABLE", "The Upper Review grant is closed or canceled."
			}
			return state, nil
		}
	}
	f, err := classifyAgentClaimTx(ctx, tx, claim)
	if err != nil {
		return state, err
	}
	state.AgentConversationID = f.conversation.ID
	state.Status, state.Message = "BUSY", "An Agent operation owns this Feature Area. Wait for that operation to finish before starting another one."
	var interrupted bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_operation_recovery_states WHERE run_id=? AND classified_epoch=?)`, claim.RunID, s.db.epoch).Scan(&interrupted); err != nil {
		return state, err
	}
	if interrupted || claim.RuntimeEpoch != s.db.epoch || (terminalAgentRun(f.run.Status) && f.category == "UNKNOWN") || f.category == "CONFLICT" {
		state.Status = "RECOVERY_REQUIRED"
		state.Message = f.detail
		if claim.RuntimeEpoch == s.db.epoch && f.category == "UNKNOWN" {
			state.Message = "Restart Core to fence the interrupted runtime, then call agent_operation_recover with this exact operation and conversation."
		}
		if f.category == "NO_ADVANCE" || f.category == "KNOWN_SAFE" {
			state.Message = "Restart Core to deterministically finish the recorded operation finalization."
		}
	}
	return state, nil
}

func recoveryResult(input agentconversation.RecoverInput, next string) agentconversation.RecoveryResult {
	return agentconversation.RecoveryResult{OperatorPrincipalID: input.OperatorPrincipalID, BackendDisposition: "UNKNOWN_ORPHANED", FeatureAreaID: input.FeatureAreaID, RunID: input.RunID, OperationKind: input.OperationKind, OperationID: input.OperationID, OldConversationID: input.AgentConversationID, NewConversationID: next, RequestID: input.RequestID, Boundary: agentconversation.Unknown, ContextAbandoned: true, FreshWorkAllowed: next != ""}
}

// Recover is an explicitly authorized product command, not a Host Tool.
// Local operators have a separate exact-target actor; they never own the
// Upper Session grant or its file/report handoff.
// The request cannot supply authority: its exact immutable old owner must have
// been fenced by a later OS-locked writer incarnation. Workspace contents remain
// preserved, quarantined from further runtime dispatch.
func (s *AgentConversationStore) Recover(ctx context.Context, input agentconversation.RecoverInput) (agentconversation.RecoveryResult, error) {
	empty := agentconversation.RecoveryResult{}
	if strings.TrimSpace(input.RequestID) == "" || len(input.RequestID) > 128 || input.FeatureAreaID == "" || input.RunID == "" || input.OperationID == "" || (input.OperationKind != agentconversation.Review && input.OperationKind != agentconversation.Execution) {
		return empty, agentconversation.ErrInvalidArgument
	}
	observed, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, err
	}
	defer observed.Rollback()
	if err = recoveryActorTx(ctx, observed, input); err != nil {
		return empty, err
	}
	if result, found, e := recoveryReplayTx(ctx, observed, input); e != nil || found {
		return result, e
	}
	if err = observed.Rollback(); err != nil {
		return empty, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	// A receipt may have committed between the read snapshot and writer lock.
	if err = recoveryActorTx(ctx, tx, input); err != nil {
		return empty, err
	}
	if result, found, e := recoveryReplayTx(ctx, tx, input); e != nil || found {
		return result, e
	}
	claim, err := scanAgentClaim(tx.QueryRowContext(ctx, `SELECT feature_area_id,operation_kind,operation_id,run_id,created_at,COALESCE(runtime_epoch,'') FROM feature_area_agent_claims WHERE feature_area_id=?`, input.FeatureAreaID))
	if err != nil {
		return empty, err
	}
	if claim.RunID != input.RunID || claim.Kind != input.OperationKind || claim.OperationID != input.OperationID {
		return empty, agentconversation.ErrConflict
	}
	if claim.RuntimeEpoch == s.db.epoch {
		return empty, fmt.Errorf("%w: the active operation belongs to the current Core runtime; wait for it to finish", agentconversation.ErrBusy)
	}
	var upper bool
	if claim.Kind == agentconversation.Review {
		executor, e := reviewClaimExecutorTx(ctx, tx, claim)
		if e != nil {
			return empty, e
		}
		upper = executor == review.ExecutorUpperLLM
	}
	if upper {
		if input.AgentConversationID != "" || input.ProjectID == "" || !input.ExpectedUpperVersion.Valid() {
			return empty, agentconversation.ErrInvalidArgument
		}
		facts, e := upperReviewRecoveryFactsTx(ctx, tx, claim, s.db.epoch)
		if e != nil {
			return empty, e
		}
		if facts.reason != "" {
			return empty, fmt.Errorf("%w: %s", agentconversation.ErrConflict, facts.reason)
		}
		if facts.projectID != input.ProjectID || !input.ExpectedUpperVersion.Equal(facts.observedVersion()) {
			return empty, agentconversation.ErrConflict
		}
		grantEpoch, generation := facts.epoch, facts.generation
		expected := *input.ExpectedUpperVersion
		result := agentconversation.RecoveryResult{OperatorPrincipalID: input.OperatorPrincipalID, BackendDisposition: "UPPER_REAUTHORIZED", FeatureAreaID: input.FeatureAreaID, RunID: input.RunID, OperationKind: input.OperationKind, OperationID: input.OperationID, RequestID: input.RequestID, Boundary: agentconversation.NoAdvance,
			ExpectedUpperVersion:   &expected,
			AuthorizedUpperVersion: &agentconversation.UpperVersion{AuthorityEpoch: s.db.epoch, GrantEpoch: s.db.epoch, GrantGeneration: generation + 1},
			GrantState:             "PREPARING"}
		payload, e := upperRecoveryPayload(input)
		if e != nil {
			return empty, e
		}
		encodedResult, e := json.Marshal(result)
		if e != nil {
			return empty, e
		}
		now := time.Now().UTC()
		// v48's narrow Upper-only trigger exception permits this atomic
		// reauthorization. No conversation/boundary is created or rotated.
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_operation_recoveries(request_id,feature_area_id,run_id,operation_kind,operation_id,old_conversation_id,new_conversation_id,previous_epoch,grant_generation,authorized_epoch,created_at,request_payload_json,result_json,operator_principal_id) VALUES(?,?,?,?,?,?,NULL,?,?,?,?,?,?,?)`, input.RequestID, claim.FeatureAreaID, claim.RunID, claim.Kind, claim.OperationID, nil, claim.RuntimeEpoch, generation+1, s.db.epoch, formatTime(now), string(payload), string(encodedResult), nullString(input.OperatorPrincipalID)); err != nil {
			return empty, err
		}
		if changed, e := tx.ExecContext(ctx, `UPDATE runs SET runtime_epoch=?,updated_at=? WHERE id=? AND runtime_epoch=? AND review_id=? AND status IN ('CREATED','PREPARING','RUNNING','COLLECTING')`, s.db.epoch, formatTime(now), claim.RunID, claim.RuntimeEpoch, claim.OperationID); e != nil {
			return empty, e
		} else if n, _ := changed.RowsAffected(); n != 1 {
			return empty, agentconversation.ErrConflict
		}
		if changed, e := tx.ExecContext(ctx, `UPDATE feature_area_agent_claims SET runtime_epoch=? WHERE feature_area_id=? AND run_id=? AND operation_kind='REVIEW' AND operation_id=? AND runtime_epoch=?`, s.db.epoch, claim.FeatureAreaID, claim.RunID, claim.OperationID, claim.RuntimeEpoch); e != nil {
			return empty, e
		} else if n, _ := changed.RowsAffected(); n != 1 {
			return empty, agentconversation.ErrConflict
		}
		if changed, e := tx.ExecContext(ctx, `UPDATE review_grants SET runtime_epoch=?,generation=?,state='PREPARING',last_observed_at=NULL WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND state='RECOVERY_REQUIRED'`, s.db.epoch, generation+1, claim.OperationID, claim.RunID, grantEpoch, generation); e != nil {
			return empty, e
		} else if n, _ := changed.RowsAffected(); n != 1 {
			return empty, agentconversation.ErrConflict
		}
		// Recovery rotates lifecycle authority only. Preserve each original
		// observed snapshot and root identity; Prepare rejects source drift.
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		return result, nil
	}
	if input.AgentConversationID == "" || input.ExpectedUpperVersion != nil {
		return empty, agentconversation.ErrInvalidArgument
	}
	f, err := classifyAgentClaimTx(ctx, tx, claim)
	if err != nil {
		return empty, err
	}
	// Pre-v27 could label an unbound thread/start as quiesced NO_ADVANCE.
	// That immutable legacy observation cannot certify that dispatch never
	// happened. Preserve it verbatim; the explicit receipt records abandonment
	// of the UNKNOWN operation classification without inventing a safe head.
	legacyUnbound := claim.RuntimeEpoch == "" && f.boundary.Advance == agentconversation.NoAdvance && f.boundary.Quiesced && f.conversation.BackendThreadID == ""
	if f.category != "UNKNOWN" || !terminalAgentRun(f.run.Status) || f.conversation.ID != input.AgentConversationID || (f.boundary.Advance != agentconversation.Unknown && !legacyUnbound) {
		return empty, agentconversation.ErrConflict
	}
	now := time.Now().UTC()
	if !legacyUnbound && f.run.TaskID != "" {
		if err = orphanOperationTx(ctx, tx, f.run, s.db.epoch, input.RequestID, "RECOVERY", now); err != nil {
			return empty, err
		}
		var nextID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM agent_conversations WHERE feature_area_id=? AND ended_at IS NULL`, claim.FeatureAreaID).Scan(&nextID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return empty, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_operation_recoveries(request_id,feature_area_id,run_id,operation_kind,operation_id,old_conversation_id,new_conversation_id,authorized_epoch,created_at,operator_principal_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, input.RequestID, claim.FeatureAreaID, claim.RunID, claim.Kind, claim.OperationID, f.conversation.ID, nullString(nextID), s.db.epoch, formatTime(now), nullString(input.OperatorPrincipalID))
		if err != nil {
			return empty, err
		}
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		return recoveryResult(input, nextID), nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_conversations SET continuation_safe=0 WHERE id=? AND ended_at IS NULL`, f.conversation.ID); err != nil {
		return empty, err
	}
	var next agentconversation.Conversation
	var currentAllowed bool
	if err = tx.QueryRowContext(ctx, `SELECT legacy_read_only=0 AND status IN ('DISCOVERING','ACTIVE') FROM feature_areas WHERE id=?`, claim.FeatureAreaID).Scan(&currentAllowed); err != nil {
		return empty, err
	}
	if currentAllowed {
		next, err = rotateAgentTx(ctx, tx, f.conversation, agentconversation.UnsafeContextBoundary, now)
	} else {
		// Historical-only FeatureAreas may release stranded ownership, but
		// recovery must not manufacture an eligible current context for them.
		_, err = tx.ExecContext(ctx, `UPDATE agent_conversations SET ended_at=?,rotation_reason='UNSAFE_CONTEXT_BOUNDARY' WHERE id=? AND ended_at IS NULL`, formatTime(now), f.conversation.ID)
	}
	if err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workspaces SET runtime_quarantined=1,status='CONTINUITY_UNAVAILABLE',updated_at=? WHERE id=?`, formatTime(now), f.run.WorkspaceID); err != nil {
		return empty, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE feature_area_id=? AND operation_kind=? AND operation_id=? AND run_id=? AND COALESCE(runtime_epoch,'')=?`, claim.FeatureAreaID, claim.Kind, claim.OperationID, claim.RunID, claim.RuntimeEpoch)
	if err != nil {
		return empty, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return empty, agentconversation.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_operation_recoveries(request_id,feature_area_id,run_id,operation_kind,operation_id,old_conversation_id,new_conversation_id,authorized_epoch,created_at,operator_principal_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, input.RequestID, input.FeatureAreaID, input.RunID, input.OperationKind, input.OperationID, input.AgentConversationID, nullString(next.ID), s.db.epoch, formatTime(now), nullString(input.OperatorPrincipalID)); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	observability.Info(ctx, "recovery", "agent.operation.recovered", "Unknown Agent context explicitly abandoned and rotated", map[string]any{"featureAreaId": input.FeatureAreaID, "runId": input.RunID, "operationKind": input.OperationKind, "oldConversationId": input.AgentConversationID, "newConversationId": next.ID, "runtimeEpoch": s.db.epoch, "boundary": "UNKNOWN"})
	return recoveryResult(input, next.ID), nil
}

// RequireRecovery records that an owned supervisor path ended with an error,
// distinguishing a stranded aggregate from normal in-flight finalization.
func (s *AgentConversationStore) RequireRecovery(ctx context.Context, runID, detail string) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.db.checkRunTx(ctx, tx, runID); err != nil {
		return err
	}
	claim, err := scanAgentClaim(tx.QueryRowContext(ctx, `SELECT feature_area_id,operation_kind,operation_id,run_id,created_at,COALESCE(runtime_epoch,'') FROM feature_area_agent_claims WHERE run_id=?`, runID))
	if errors.Is(err, agentconversation.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	facts, err := classifyAgentClaimTx(ctx, tx, claim)
	if err != nil {
		return err
	}
	if len(detail) > 2048 {
		detail = detail[:2048]
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET classification=excluded.classification,detail=excluded.detail,classified_epoch=excluded.classified_epoch`, runID, facts.category, detail, s.db.epoch); err != nil {
		return err
	}
	return tx.Commit()
}
