package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

type RunStore struct{ db *DB }

func NewRunStore(db *DB) *RunStore { return &RunStore{db: db} }

// ListCanceledApprovalIDs exposes bounded post-commit status facts used only
// for operational diagnostics after a Run reaches terminal state.
func (s *RunStore) ListCanceledApprovalIDs(ctx context.Context, runID string) ([]string, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT id FROM approvals WHERE run_id=? AND status=? ORDER BY created_at,id`, runID, string(approval.StatusCanceled))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *RunStore) CreateAttempt(ctx context.Context, taskID, runID string, now time.Time) (run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	value, err := createRunAttempt(ctx, s.db, tx, taskID, runID, now)
	if err != nil {
		return run.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return value, nil
}

func (s *RunStore) CreateExecutionAttempt(ctx context.Context, taskID, runID string, purpose run.Purpose, executionProposalID string, now time.Time) (run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	value, err := createRunAttempt(ctx, s.db, tx, taskID, runID, now, runMetadata{Purpose: purpose, ExecutionProposalID: executionProposalID})
	if err != nil {
		return run.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return value, nil
}

func (s *RunStore) CreateScopeReviewAttempt(ctx context.Context, taskID, runID, instruction string, scope run.ReviewScope, now time.Time) (run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	value, err := createRunAttempt(ctx, s.db, tx, taskID, runID, now, runMetadata{Purpose: run.PurposeScopeReview, CodexInstruction: instruction, ReviewScope: scope})
	if err != nil {
		return run.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return value, nil
}

func (s *RunStore) CreateFocusedScopeReviewAttempt(ctx context.Context, taskID, runID, instruction string, scope run.ReviewScope, plan run.ReviewPlan, now time.Time) (run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	value, err := createRunAttempt(ctx, s.db, tx, taskID, runID, now, runMetadata{Purpose: run.PurposeScopeReview, CodexInstruction: instruction, ReviewScope: scope, ReviewPlan: plan})
	if err != nil {
		return run.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return value, nil
}

func (s *RunStore) CreateProductScopeReviewAttempt(ctx context.Context, reviewID, runID, instruction string, scope run.ReviewScope, plan run.ReviewPlan, now time.Time) (run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	value, err := createRunAttempt(ctx, s.db, tx, "", runID, now, runMetadata{ReviewID: reviewID, Purpose: run.PurposeScopeReview, CodexInstruction: instruction, ReviewScope: scope, ReviewPlan: plan})
	if err != nil {
		return run.Run{}, err
	}
	linked, err := tx.ExecContext(ctx, `UPDATE reviews SET linked_run_id=? WHERE id=? AND linked_run_id IS NULL`, runID, reviewID)
	if err != nil {
		return run.Run{}, conflict(err)
	}
	if count, _ := linked.RowsAffected(); count != 1 {
		return run.Run{}, review.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return value, nil
}

func (s *RunStore) RecordPrompt(ctx context.Context, id, prompt string, sentAt time.Time) (run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	if err = s.db.checkRunTx(ctx, tx, id); err != nil {
		return run.Run{}, err
	}
	sum := sha256.Sum256([]byte(prompt))
	result, err := tx.ExecContext(ctx, `UPDATE runs SET final_codex_prompt=?,prompt_sha256=?,prompt_bytes=?,prompt_sent_at=?,updated_at=? WHERE id=? AND status='RUNNING' AND final_codex_prompt IS NULL`, prompt, hex.EncodeToString(sum[:]), len([]byte(prompt)), formatTime(sentAt), formatTime(sentAt), id)
	if err != nil {
		return run.Run{}, conflict(err)
	}
	existing, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, id))
	if err != nil {
		return existing, err
	}
	if count, _ := result.RowsAffected(); count != 1 && existing.FinalCodexPrompt != prompt {
		return existing, ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return existing, nil
}

type runMetadata struct {
	ReviewID            string
	Purpose             run.Purpose
	ExecutionProposalID string
	CodexInstruction    string
	ReviewScope         run.ReviewScope
	ReviewPlan          run.ReviewPlan
}

func createRunAttempt(ctx context.Context, db *DB, tx *sql.Tx, taskID, runID string, now time.Time, metadata ...runMetadata) (run.Run, error) {
	if taskID != "" {
		t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, taskID))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return run.Run{}, err
		}
		if err == nil {
			if t.WorkSpec.ImplementationMode == task.ImplementationDirectFile && len(metadata) == 1 && metadata[0].Purpose == run.PurposeVerification && metadata[0].ExecutionProposalID == t.CurrentExecutionID {
				p, e := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, t.CurrentExecutionID))
				if e != nil {
					return run.Run{}, e
				}
				if err = requireDirectFileVerificationSourceTx(ctx, tx, p); err != nil {
					return run.Run{}, err
				}
			} else if err = validateApprovedTaskTx(ctx, tx, t, false); err != nil {
				return run.Run{}, err
			}
		}
	}
	purpose := run.PurposeImplementation
	executionProposalID := ""
	reviewID := ""
	codexInstruction := ""
	reviewScope := run.ReviewScope{}
	if len(metadata) != 0 {
		reviewID = metadata[0].ReviewID
		purpose = metadata[0].Purpose
		executionProposalID = metadata[0].ExecutionProposalID
		codexInstruction = metadata[0].CodexInstruction
		reviewScope = metadata[0].ReviewScope
	}
	var attempt int
	var projectID, featureAreaID string
	var operationKind run.OperationKind
	var operationID string
	var err error
	if reviewID != "" {
		if taskID != "" || executionProposalID != "" {
			return run.Run{}, review.ErrConflict
		}
		operationKind, operationID = run.OperationReview, reviewID
		err = tx.QueryRowContext(ctx, `UPDATE tasks SET next_attempt=next_attempt+1 WHERE id=(SELECT task_id FROM reviews WHERE id=?) AND current_review_id=? RETURNING next_attempt,project_id,feature_area_id,id`, reviewID, reviewID).Scan(&attempt, &projectID, &featureAreaID, &taskID)
	} else {
		err = tx.QueryRowContext(ctx, `UPDATE tasks SET next_attempt=next_attempt+1 WHERE id=? RETURNING next_attempt,project_id,COALESCE(feature_area_id,'')`, taskID).Scan(&attempt, &projectID, &featureAreaID)
		if err == nil {
			switch {
			case executionProposalID != "":
				operationKind, operationID = run.OperationExecution, executionProposalID
				var matches bool
				err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposals WHERE id=? AND task_id=?)`, executionProposalID, taskID).Scan(&matches)
				if err == nil && !matches {
					return run.Run{}, ErrConflict
				}
			default:
				return run.Run{}, errors.New("current engineering Run requires a Review or Execution owner")
			}
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, run.ErrNotFound
	}
	if err != nil {
		return run.Run{}, fmt.Errorf("allocate owned Run attempt: %w", err)
	}
	if projectID == "" || featureAreaID == "" {
		return run.Run{}, errors.New("current engineering Run requires Project and FeatureArea ownership")
	}
	if taskID != "" {
		var repositoryID string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(repository_id,'') FROM tasks WHERE id=? AND project_id=?`, taskID, projectID).Scan(&repositoryID); err != nil {
			return run.Run{}, err
		}
		if repositoryID != "" {
			if err := ensureRepositoryActiveTx(ctx, tx, projectID, repositoryID); err != nil {
				return run.Run{}, err
			}
		}
	}
	if err := directFileAdmissionTx(ctx, tx, db, featureAreaID); err != nil {
		return run.Run{}, err
	}
	paths, _ := json.Marshal(map[string]any{"version": 1, "paths": append([]string(nil), reviewScope.Paths...)})
	plan := run.ReviewPlan{}
	if len(metadata) != 0 {
		plan = metadata[0].ReviewPlan
	}
	selected, _ := json.Marshal(map[string]any{"version": 1, "initialScope": plan.InitialScope, "items": append([]run.ReviewModuleSelection(nil), plan.SelectedModules...)})
	value := run.Run{ID: runID, ProjectID: projectID, FeatureAreaID: featureAreaID, OperationKind: operationKind, OperationID: operationID, ReviewID: reviewID, TaskID: taskID, Purpose: purpose, ExecutionProposalID: executionProposalID, CodexInstruction: codexInstruction, ReviewScope: reviewScope, ReviewPlan: plan, Attempt: attempt, Status: run.StatusCreated, CreatedAt: now.UTC(), UpdatedAt: now.UTC(), TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}}
	if err := tx.QueryRowContext(ctx, `SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&value.RuntimeEpoch); err != nil {
		return run.Run{}, err
	}
	if err := insertRunAgentExecutionTx(ctx, tx, &value); err != nil {
		return run.Run{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs(id,project_id,feature_area_id,operation_kind,operation_id,task_id,attempt,status,created_at,updated_at,purpose,execution_proposal_id,codex_instruction,review_scope_kind,review_scope_paths_json,review_mode,selected_modules_json,review_id,runtime_epoch,agent_execution_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, runID, projectID, featureAreaID, operationKind, operationID, nullString(taskID), attempt, string(value.Status), formatTime(now), formatTime(now), string(purpose), nullString(executionProposalID), codexInstruction, nullString(string(reviewScope.Kind)), string(paths), nullString(string(plan.Mode)), string(selected), nullString(reviewID), value.RuntimeEpoch, value.AgentExecutionID)
	if err != nil {
		return run.Run{}, conflict(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_handoffs(run_id,runtime_epoch,state,code,created_at,updated_at) SELECT ?,?,'REGISTERED','RUN_RESERVED',?,? WHERE NOT EXISTS(SELECT 1 FROM reviews WHERE id=? AND executor_kind='UPPER_LLM')`, value.ID, value.RuntimeEpoch, formatTime(now), formatTime(now), value.ReviewID); err != nil {
		return run.Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_token_usage(run_id,status,updated_at) VALUES(?,?,?)`, runID, string(run.TokenUsagePending), formatTime(now)); err != nil {
		return run.Run{}, conflict(err)
	}
	if err := startTaskRunPhaseTx(ctx, tx, value, now); err != nil {
		return run.Run{}, err
	}
	if err := acquireAgentRunTx(ctx, tx, value, now); err != nil {
		return run.Run{}, err
	}
	payload, _ := event.Payload(map[string]any{"attempt": attempt, "status": value.Status, "purpose": purpose, "execution_proposal_id": executionProposalID, "review_id": reviewID, "agent_execution_id": value.AgentExecutionID})
	if err := appendEvent(ctx, tx, event.Event{Type: event.RunCreated, ProjectID: projectID, TaskID: taskID, RunID: runID, EntityType: "run", EntityID: runID, Payload: payload, CreatedAt: now}); err != nil {
		return run.Run{}, err
	}
	return value, nil
}

type runScanner interface{ Scan(...any) error }

func scanRun(scanner runScanner) (run.Run, error) {
	var value run.Run
	var status, purpose, usageStatus, created, updated string
	var reviewID, executionProposalID, started, finished, workspaceID, worktreeID, threadID, turnID, startSnapshot, endSnapshot, fclass, fcode, fstage, fmessage, verificationResult, reviewScopeKind, reviewMode, resultText, promptSentAt, runtimeWorksetJSON, runtimeWorksetHash sql.NullString
	var reviewScopePaths, selectedModules string
	var endInput, endCached, endCacheWrite, endOutput, endReasoning, endTotal sql.NullInt64
	var usageInput, usageCached, usageCacheWrite, usageOutput, usageReasoning, usageTotal sql.NullInt64
	var modelContextWindow sql.NullInt64
	err := scanner.Scan(
		&value.ID, &value.ProjectID, &value.FeatureAreaID, &value.OperationKind, &value.OperationID, &reviewID, &value.TaskID, &purpose, &executionProposalID, &value.Attempt, &status, &workspaceID, &worktreeID, &threadID, &turnID, &startSnapshot, &endSnapshot, &started, &finished,
		&fclass, &fcode, &fstage, &fmessage, &created, &updated,
		&usageStatus,
		&value.TokenUsage.StartCumulative.InputTokens,
		&value.TokenUsage.StartCumulative.CachedInputTokens,
		&value.TokenUsage.StartCumulative.CacheWriteInputTokens,
		&value.TokenUsage.StartCumulative.OutputTokens,
		&value.TokenUsage.StartCumulative.ReasoningOutputTokens,
		&value.TokenUsage.StartCumulative.TotalTokens,
		&endInput, &endCached, &endCacheWrite, &endOutput, &endReasoning, &endTotal,
		&usageInput, &usageCached, &usageCacheWrite, &usageOutput, &usageReasoning, &usageTotal,
		&modelContextWindow, &verificationResult, &value.CodexInstruction, &reviewScopeKind, &reviewScopePaths, &reviewMode, &selectedModules, &resultText, &value.FinalCodexPrompt, &value.PromptSHA256, &value.PromptBytes, &promptSentAt, &value.ContextProbeID, &value.RuntimeEpoch, &value.AgentExecutionID, &runtimeWorksetJSON, &runtimeWorksetHash, &value.ResultProtocol, &value.AnalysisState, &value.NativeResultState,
	)
	if err != nil {
		return run.Run{}, err
	}
	value.Status = run.Status(status)
	value.ReviewID = reviewID.String
	value.Purpose = run.Purpose(purpose)
	value.ExecutionProposalID = executionProposalID.String
	value.ReviewScope.Kind = run.ReviewScopeKind(reviewScopeKind.String)
	value.ReviewPlan.Mode = run.ReviewMode(reviewMode.String)
	if reviewScopePaths != "" {
		var envelope struct {
			Version int      `json:"version"`
			Paths   []string `json:"paths"`
		}
		if err := json.Unmarshal([]byte(reviewScopePaths), &envelope); err != nil || envelope.Version != 1 {
			return run.Run{}, errors.New("decode run review scope paths")
		}
		value.ReviewScope.Paths = envelope.Paths
	}
	if selectedModules != "" {
		var envelope struct {
			Version      int                         `json:"version"`
			InitialScope run.ReviewScope             `json:"initialScope"`
			Items        []run.ReviewModuleSelection `json:"items"`
		}
		if err := json.Unmarshal([]byte(selectedModules), &envelope); err != nil || envelope.Version != 1 {
			return run.Run{}, errors.New("decode run selected modules")
		}
		value.ReviewPlan.InitialScope = envelope.InitialScope
		value.ReviewPlan.SelectedModules = envelope.Items
	}
	value.TokenUsage.Status = run.TokenUsageStatus(usageStatus)
	value.WorkspaceID = workspaceID.String
	value.WorktreeID = worktreeID.String
	value.ThreadID = threadID.String
	value.TurnID = turnID.String
	value.StartSnapshot = startSnapshot.String
	value.EndSnapshot = endSnapshot.String
	if started.Valid {
		v, e := parseTime(started.String)
		if e != nil {
			return run.Run{}, e
		}
		value.StartedAt = &v
	}
	if finished.Valid {
		v, e := parseTime(finished.String)
		if e != nil {
			return run.Run{}, e
		}
		value.FinishedAt = &v
	}
	if fclass.Valid {
		value.Failure = &run.Failure{Kind: run.FailureKind(fclass.String), Code: fcode.String, Stage: fstage.String, Message: fmessage.String}
	}
	if endInput.Valid && endCached.Valid && endCacheWrite.Valid && endOutput.Valid && endReasoning.Valid && endTotal.Valid {
		value.TokenUsage.EndCumulative = &run.TokenCounts{InputTokens: endInput.Int64, CachedInputTokens: endCached.Int64, CacheWriteInputTokens: endCacheWrite.Int64, OutputTokens: endOutput.Int64, ReasoningOutputTokens: endReasoning.Int64, TotalTokens: endTotal.Int64}
	}
	if usageInput.Valid && usageCached.Valid && usageCacheWrite.Valid && usageOutput.Valid && usageReasoning.Valid && usageTotal.Valid {
		value.TokenUsage.Usage = &run.TokenCounts{InputTokens: usageInput.Int64, CachedInputTokens: usageCached.Int64, CacheWriteInputTokens: usageCacheWrite.Int64, OutputTokens: usageOutput.Int64, ReasoningOutputTokens: usageReasoning.Int64, TotalTokens: usageTotal.Int64}
	}
	if modelContextWindow.Valid {
		value.TokenUsage.ModelContextWindow = &modelContextWindow.Int64
	}
	if verificationResult.Valid {
		value.VerificationReport, err = decodeVerificationReportJSON(verificationResult.String)
		if err != nil {
			return run.Run{}, err
		}
	}
	if resultText.Valid {
		value.Result = &run.Result{AgentClaim: evidence.AgentClaim{Message: resultText.String}}
	}
	if promptSentAt.Valid {
		parsed, parseErr := parseTime(promptSentAt.String)
		if parseErr != nil {
			return run.Run{}, parseErr
		}
		value.PromptSentAt = &parsed
	}
	if runtimeWorksetJSON.Valid || runtimeWorksetHash.Valid {
		if !runtimeWorksetJSON.Valid || !runtimeWorksetHash.Valid {
			return run.Run{}, errors.New("incomplete RuntimeWorkset persistence")
		}
		var runtimeValue workset.RuntimeWorkset
		if err := json.Unmarshal([]byte(runtimeWorksetJSON.String), &runtimeValue); err != nil || runtimeValue.ContentHash != runtimeWorksetHash.String || runtimeValue.Validate() != nil {
			return run.Run{}, errors.New("decode persisted RuntimeWorkset")
		}
		value.RuntimeWorkset = &runtimeValue
	}
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return run.Run{}, err
	}
	value.UpdatedAt, err = parseTime(updated)
	return value, err
}

const selectRun = `SELECT r.id,COALESCE(r.project_id,''),COALESCE(r.feature_area_id,''),COALESCE(r.operation_kind,''),COALESCE(r.operation_id,''),r.review_id,COALESCE(r.task_id,''),r.purpose,r.execution_proposal_id,r.attempt,r.status,r.workspace_id,r.worktree_id,r.thread_id,r.turn_id,r.start_snapshot,r.end_snapshot,r.started_at,r.finished_at,r.failure_class,r.failure_code,r.failure_stage,r.failure_message,r.created_at,r.updated_at,
 u.status,u.start_input_tokens,u.start_cached_input_tokens,u.start_cache_write_input_tokens,u.start_output_tokens,u.start_reasoning_output_tokens,u.start_total_tokens,
 u.end_input_tokens,u.end_cached_input_tokens,u.end_cache_write_input_tokens,u.end_output_tokens,u.end_reasoning_output_tokens,u.end_total_tokens,
 u.input_tokens,u.cached_input_tokens,u.cache_write_input_tokens,u.output_tokens,u.reasoning_output_tokens,u.total_tokens,u.model_context_window,r.verification_result_json,r.codex_instruction,r.review_scope_kind,r.review_scope_paths_json,r.review_mode,r.selected_modules_json,r.result_text,COALESCE(r.final_codex_prompt,''),COALESCE(r.prompt_sha256,''),COALESCE(r.prompt_bytes,0),r.prompt_sent_at
 ,COALESCE(r.context_probe_id,''),COALESCE(r.runtime_epoch,''),COALESCE(r.agent_execution_id,''),r.runtime_workset_json,r.runtime_workset_hash,CASE WHEN EXISTS(SELECT 1 FROM run_execution_records er WHERE er.run_id=r.id) THEN 'NATURAL_V1' ELSE 'LEGACY_STRUCTURED' END,CASE WHEN NOT EXISTS(SELECT 1 FROM run_execution_records er WHERE er.run_id=r.id) THEN 'NOT_REQUIRED' WHEN EXISTS(SELECT 1 FROM run_analyses an WHERE an.run_id=r.id) THEN 'RECORDED' WHEN r.status='SUCCEEDED' AND r.purpose<>'IMPLEMENTATION' THEN 'REQUIRED' WHEN r.status='SUCCEEDED' THEN 'OPTIONAL' WHEN r.status IN ('FAILED','INFRASTRUCTURE_FAILED','CANCELED') THEN 'NOT_REQUIRED' ELSE 'WAITING_EXECUTION' END,` + nativeResultStateSQL + ` FROM runs r JOIN run_token_usage u ON u.run_id=r.id`

// Native completion and workflow admission are independent. In particular,
// local shutdown failure is not evidence that Codex returned a terminal result.
const nativeResultStateSQL = `CASE
 WHEN EXISTS(SELECT 1 FROM agent_operation_boundaries b JOIN agent_conversations a ON a.id=b.agent_conversation_id
  WHERE b.run_id=r.id AND COALESCE(r.turn_id,'')<>'' AND r.thread_id=a.backend_thread_id AND b.thread_start_pending=0
  AND ((b.advance_state='KNOWN_ADVANCE' AND b.quiesced=1 AND b.exit_boundary_turn_id=r.turn_id)
   OR EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id AND i.agent_conversation_id=a.id
    AND i.runtime_epoch=r.runtime_epoch AND i.feature_area_id=r.feature_area_id AND i.operation_kind=r.operation_kind AND i.operation_id=r.operation_id
    AND i.advance_state='KNOWN_ADVANCE' AND i.quiesced=1 AND i.exit_boundary_turn_id=r.turn_id))) THEN 'RECEIVED'
 WHEN EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id AND b.advance_state='NO_ADVANCE'
  AND b.thread_start_pending=0 AND COALESCE(r.turn_id,'')='' AND (b.quiesced=1 OR EXISTS(
   SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id AND i.agent_conversation_id=b.agent_conversation_id
    AND i.runtime_epoch=r.runtime_epoch AND i.feature_area_id=r.feature_area_id AND i.operation_kind=r.operation_kind AND i.operation_id=r.operation_id
    AND i.advance_state='NO_ADVANCE' AND i.quiesced=1))) THEN 'NOT_STARTED'
 WHEN EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id AND b.advance_state='UNKNOWN') THEN 'UNKNOWN'
 ELSE 'UNAVAILABLE' END`

func (s *RunStore) Get(ctx context.Context, id string) (run.Run, error) {
	v, err := scanRun(s.db.sql.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, run.ErrNotFound
	}
	if err != nil {
		return run.Run{}, fmt.Errorf("get run: %w", err)
	}
	return v, nil
}
func (s *RunStore) GetByInteraction(ctx context.Context, threadID, turnID string) (run.Run, error) {
	value, err := scanRun(s.db.sql.QueryRowContext(ctx, selectRun+` WHERE r.thread_id=? AND r.turn_id=?`, threadID, turnID))
	if errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, run.ErrNotFound
	}
	if err != nil {
		return run.Run{}, fmt.Errorf("get run by Codex interaction: %w", err)
	}
	return value, nil
}

func encodeVerificationReportJSON(report run.VerificationReport) (string, error) {
	if err := run.ValidateVerificationReportV2(report); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		Version int                    `json:"version"`
		Report  run.VerificationReport `json:"report"`
	}{Version: 2, Report: report})
	return string(encoded), err
}

func decodeVerificationReportJSON(encoded string) (*run.VerificationReport, error) {
	var version struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal([]byte(encoded), &version); err != nil {
		return nil, fmt.Errorf("decode verification result: %w", err)
	}
	switch version.Version {
	case 1:
		var legacy struct {
			Version int                                `json:"version"`
			Status  run.LegacyVerificationResultStatus `json:"status"`
			Checks  []run.VerificationCheckOutcome     `json:"checks"`
		}
		if err := json.Unmarshal([]byte(encoded), &legacy); err != nil {
			return nil, fmt.Errorf("decode legacy verification result: %w", err)
		}
		for index := range legacy.Checks {
			if legacy.Checks[index].Status == run.LegacyVerificationCheckSemanticResult {
				legacy.Checks[index].Status = run.VerificationCheckUnknown
			}
			if legacy.Checks[index].Reported.Status == "" {
				legacy.Checks[index].Reported = run.VerificationReportedFact{Status: run.VerificationCheckUnknown, Summary: "legacy v1 data did not preserve source-separated reported facts"}
			}
		}
		availability := run.VerificationReportAvailable
		if legacy.Status == run.LegacyVerificationMutatedWorktree {
			availability = run.VerificationReportUnavailable
		}
		return &run.VerificationReport{Version: 1, Availability: availability, Summary: "Legacy VerificationReport v1", LegacyResultStatus: legacy.Status, Status: legacy.Status, Checks: legacy.Checks}, nil
	case 2:
		var envelope struct {
			Version int                    `json:"version"`
			Report  run.VerificationReport `json:"report"`
		}
		if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
			return nil, fmt.Errorf("decode VerificationReport v2: %w", err)
		}
		if err := run.ValidateVerificationReportV2(envelope.Report); err != nil {
			return nil, fmt.Errorf("invalid VerificationReport v2 envelope: %w", err)
		}
		return &envelope.Report, nil
	default:
		return nil, fmt.Errorf("unsupported verification result version %d", version.Version)
	}
}
func (s *RunStore) List(ctx context.Context) ([]run.Run, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectRun+` ORDER BY r.task_id,r.attempt`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []run.Run
	for rows.Next() {
		v, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
func (s *RunStore) ListByTask(ctx context.Context, taskID string) ([]run.Run, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectRun+` WHERE r.task_id=? ORDER BY r.created_at,r.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []run.Run
	for rows.Next() {
		v, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
func (s *RunStore) LatestByTaskPurpose(ctx context.Context, taskID string, purpose run.Purpose, executionProposalID string) (run.Run, error) {
	query := selectRun + ` WHERE r.task_id=? AND r.purpose=?`
	args := []any{taskID, string(purpose)}
	if executionProposalID != "" {
		query += ` AND r.execution_proposal_id=?`
		args = append(args, executionProposalID)
	}
	query += ` ORDER BY r.created_at DESC,r.id LIMIT 1`
	value, err := scanRun(s.db.sql.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, run.ErrNotFound
	}
	if err != nil {
		return run.Run{}, fmt.Errorf("get latest run for task phase: %w", err)
	}
	return value, nil
}
func (s *RunStore) Update(ctx context.Context, id string, update func(*run.Run) error) (run.Run, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	if err := s.db.checkRunTx(ctx, tx, id); err != nil {
		return run.Run{}, err
	}
	value, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, run.ErrNotFound
	}
	if err != nil {
		return run.Run{}, err
	}
	previous := value.Status
	if previous == run.StatusCanceled {
		var canceled bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE run_id=? AND state='CANCELED')`, id).Scan(&canceled); err != nil {
			return run.Run{}, err
		}
		if canceled {
			return run.Run{}, s.db.fenced(ctx, id)
		}
	}
	previousOwner := value
	previousUpdatedAt := value.UpdatedAt
	if err := update(&value); err != nil {
		return run.Run{}, err
	}
	if !value.SameOwner(previousOwner) {
		return run.Run{}, run.ErrOwnerImmutable
	}
	if value.Status != previous && value.Status != run.StatusCanceled {
		if err := requireResolvedResponsesTx(ctx, tx, id); err != nil {
			return run.Run{}, err
		}
	}
	if value.Status == run.StatusCanceled && value.WorkflowOwned() {
		if err := requireExecutionCancellationIntentTx(ctx, tx, value); err != nil {
			return run.Run{}, err
		}
	}
	var fclass, fcode, fstage, fmessage any
	if value.Failure != nil {
		fclass = value.Failure.Kind
		fcode = value.Failure.Code
		fstage = value.Failure.Stage
		fmessage = value.Failure.Message
	}
	var verificationResult any
	if previousOwner.VerificationReport != nil && previousOwner.VerificationReport.Version == 1 {
		if !reflect.DeepEqual(previousOwner.VerificationReport, value.VerificationReport) {
			return run.Run{}, errors.New("legacy verification report is immutable")
		}
		var original string
		if err := tx.QueryRowContext(ctx, `SELECT verification_result_json FROM runs WHERE id=?`, id).Scan(&original); err != nil {
			return run.Run{}, err
		}
		verificationResult = original
	} else if value.VerificationReport != nil {
		encoded, encodeErr := encodeVerificationReportJSON(*value.VerificationReport)
		if encodeErr != nil {
			return run.Run{}, encodeErr
		}
		verificationResult = encoded
	}
	var resultText any
	if value.Result != nil {
		resultText = value.Result.AgentClaim.Message
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?,workspace_id=?,worktree_id=?,thread_id=?,turn_id=?,start_snapshot=?,end_snapshot=?,started_at=?,finished_at=?,failure_class=?,failure_code=?,failure_stage=?,failure_message=?,verification_result_json=?,result_text=?,updated_at=? WHERE id=? AND status=? AND updated_at=?`, string(value.Status), nullString(value.WorkspaceID), nullString(value.WorktreeID), nullString(value.ThreadID), nullString(value.TurnID), nullString(value.StartSnapshot), nullString(value.EndSnapshot), nullableTime(value.StartedAt), nullableTime(value.FinishedAt), fclass, fcode, fstage, fmessage, verificationResult, resultText, formatTime(value.UpdatedAt), id, string(previous), formatTime(previousUpdatedAt))
	if err != nil {
		return run.Run{}, conflict(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return run.Run{}, err
	}
	if affected != 1 {
		return run.Run{}, ErrConflict
	}
	if err := completeTaskRunPhaseTx(ctx, tx, value); err != nil {
		return run.Run{}, err
	}
	if taskRecoveryRequiredFromRunFailure(value) {
		if err := markTaskRecoveryRequiredTx(ctx, tx, value.ProjectID, value.TaskID, "TASK_RUN_FINALIZATION", value.UpdatedAt); err != nil {
			return run.Run{}, err
		}
	}
	if err := updateRunTokenUsage(ctx, tx, value); err != nil {
		return run.Run{}, err
	}
	if err := finalizeAgentRunTx(ctx, tx, value); err != nil {
		return run.Run{}, err
	}
	if value.Status != previous {
		projectID := value.ProjectID
		payload, _ := event.Payload(map[string]any{"from": previous, "to": value.Status, "purpose": value.Purpose, "execution_proposal_id": value.ExecutionProposalID})
		if err := appendEvent(ctx, tx, event.Event{Type: event.RunStatusChanged, ProjectID: projectID, TaskID: value.TaskID, RunID: value.ID, EntityType: "run", EntityID: value.ID, Payload: payload, CreatedAt: value.UpdatedAt}); err != nil {
			return run.Run{}, err
		}
		if executionEvent := purposeExecutionEvent(value.Purpose, value.Status); executionEvent != "" && value.ExecutionProposalID != "" {
			if err := appendEvent(ctx, tx, event.Event{Type: executionEvent, ProjectID: projectID, TaskID: value.TaskID, RunID: value.ID, EntityType: "execution", EntityID: value.ExecutionProposalID, Payload: payload, CreatedAt: value.UpdatedAt}); err != nil {
				return run.Run{}, err
			}
		}
		if value.Status == run.StatusSucceeded || value.Status == run.StatusFailed || value.Status == run.StatusInfrastructureFailed || value.Status == run.StatusCanceled {
			if err := cancelOpenApprovalsExcept(ctx, tx, value.ID, "", "run_terminal", projectID, value.TaskID, value.UpdatedAt); err != nil {
				return run.Run{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return run.Run{}, err
	}
	// The durable materialization protocol runs after the terminal Review fact
	// is committed. A crash here leaves no gap to repair: the next sweep or
	// boot Reconcile claims the same facts deterministically.
	if value.ReviewID != "" && value.Status == run.StatusSucceeded {
		_ = materializeReviewResults(ctx, s.db, value.UpdatedAt)
	}
	return value, nil
}

func purposeExecutionEvent(purpose run.Purpose, status run.Status) string {
	if status == run.StatusRunning {
		if purpose == run.PurposeVerification {
			return event.ExecutionVerificationStarted
		}
		if purpose == run.PurposeImplementation {
			return event.ExecutionImplementationStarted
		}
		return ""
	}
	if status == run.StatusSucceeded || status == run.StatusFailed || status == run.StatusInfrastructureFailed || status == run.StatusCanceled {
		if purpose == run.PurposeVerification {
			return event.ExecutionVerificationCompleted
		}
		if purpose == run.PurposeImplementation {
			return event.ExecutionImplementationCompleted
		}
	}
	return ""
}

func updateRunTokenUsage(ctx context.Context, tx *sql.Tx, value run.Run) error {
	var endInput, endCached, endCacheWrite, endOutput, endReasoning, endTotal any
	if value.TokenUsage.EndCumulative != nil {
		endInput = value.TokenUsage.EndCumulative.InputTokens
		endCached = value.TokenUsage.EndCumulative.CachedInputTokens
		endCacheWrite = value.TokenUsage.EndCumulative.CacheWriteInputTokens
		endOutput = value.TokenUsage.EndCumulative.OutputTokens
		endReasoning = value.TokenUsage.EndCumulative.ReasoningOutputTokens
		endTotal = value.TokenUsage.EndCumulative.TotalTokens
	}
	var usageInput, usageCached, usageCacheWrite, usageOutput, usageReasoning, usageTotal any
	if value.TokenUsage.Usage != nil {
		usageInput = value.TokenUsage.Usage.InputTokens
		usageCached = value.TokenUsage.Usage.CachedInputTokens
		usageCacheWrite = value.TokenUsage.Usage.CacheWriteInputTokens
		usageOutput = value.TokenUsage.Usage.OutputTokens
		usageReasoning = value.TokenUsage.Usage.ReasoningOutputTokens
		usageTotal = value.TokenUsage.Usage.TotalTokens
	}
	var modelContextWindow any
	if value.TokenUsage.ModelContextWindow != nil {
		modelContextWindow = *value.TokenUsage.ModelContextWindow
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_token_usage SET status=?,start_input_tokens=?,start_cached_input_tokens=?,start_cache_write_input_tokens=?,start_output_tokens=?,start_reasoning_output_tokens=?,start_total_tokens=?,end_input_tokens=?,end_cached_input_tokens=?,end_cache_write_input_tokens=?,end_output_tokens=?,end_reasoning_output_tokens=?,end_total_tokens=?,input_tokens=?,cached_input_tokens=?,cache_write_input_tokens=?,output_tokens=?,reasoning_output_tokens=?,total_tokens=?,model_context_window=?,updated_at=? WHERE run_id=?`,
		string(value.TokenUsage.Status), value.TokenUsage.StartCumulative.InputTokens, value.TokenUsage.StartCumulative.CachedInputTokens, value.TokenUsage.StartCumulative.CacheWriteInputTokens, value.TokenUsage.StartCumulative.OutputTokens, value.TokenUsage.StartCumulative.ReasoningOutputTokens, value.TokenUsage.StartCumulative.TotalTokens,
		endInput, endCached, endCacheWrite, endOutput, endReasoning, endTotal,
		usageInput, usageCached, usageCacheWrite, usageOutput, usageReasoning, usageTotal,
		modelContextWindow, formatTime(value.UpdatedAt), value.ID)
	if err != nil {
		return conflict(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrConflict
	}
	return nil
}
