package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/run"
)

type ContextProbeStore struct{ db *DB }

func NewContextProbeStore(db *DB) *ContextProbeStore { return &ContextProbeStore{db: db} }

type probePaths struct {
	Version int      `json:"version"`
	Paths   []string `json:"paths"`
}

func encodeProbePaths(paths []string) (string, error) {
	data, err := json.Marshal(probePaths{Version: 1, Paths: paths})
	return string(data), err
}

func (s *ContextProbeStore) Create(ctx context.Context, value contextprobe.Probe) error {
	paths, err := encodeProbePaths(value.Scope.Paths)
	if err != nil {
		return err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	taskID, err := insertWorkEnvelopeTx(ctx, tx, "INSPECTION", value.ID, value.ProjectID, "", value.RepositoryID, value.CodexConfigID, value.CodexInstruction, "", "", "", value.CreatedAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO context_probes(id,ai_session_id,project_id,repository_id,codex_config_id,scope_kind,scope_paths_json,codex_instruction,status,token_usage_status,created_at,updated_at,task_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.AISessionID, value.ProjectID, value.RepositoryID, value.CodexConfigID, value.Scope.Kind, paths, value.CodexInstruction, value.Status, value.TokenUsage.Status, formatTime(value.CreatedAt), formatTime(value.UpdatedAt), taskID)
	if err != nil {
		return conflict(fmt.Errorf("insert context probe: %w", err))
	}
	payload, _ := event.Payload(map[string]any{"ai_session_id": value.AISessionID, "repository_id": value.RepositoryID, "scope_kind": value.Scope.Kind, "scope_paths": value.Scope.Paths})
	if err := appendEvent(ctx, tx, event.Event{Type: event.ContextProbeCreated, ProjectID: value.ProjectID, EntityType: "context_probe", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit()
}

const selectContextProbe = `SELECT id,ai_session_id,project_id,repository_id,codex_config_id,scope_kind,scope_paths_json,codex_instruction,status,COALESCE(thread_id,''),COALESCE(turn_id,''),started_at,finished_at,token_usage_status,input_tokens,cached_input_tokens,cache_write_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,model_context_window,COALESCE(result,''),COALESCE(result_artifact_ref,''),COALESCE(failure_message,''),COALESCE(final_codex_prompt,''),COALESCE(prompt_sha256,''),COALESCE(prompt_bytes,0),prompt_sent_at,created_at,updated_at,COALESCE(task_id,''),COALESCE(run_id,''),COALESCE(runtime_epoch,''),COALESCE((SELECT r.agent_execution_id FROM runs r WHERE r.id=context_probes.run_id AND r.context_probe_id=context_probes.id),''),canonical_target_receipt_json,COALESCE(canonical_request_id,''),COALESCE(canonical_payload_hash,''),COALESCE(primary_member_id,''),COALESCE(workspace_id,''),COALESCE(runtime_workset_json,''),COALESCE(runtime_workset_hash,''),COALESCE(report_hash,''),COALESCE(result_hash,''),COALESCE(runtime_limitation,''),COALESCE(recovery_successor_run_id,'') FROM context_probes`

type probeScanner interface{ Scan(...any) error }

func scanContextProbe(scanner probeScanner) (contextprobe.Probe, error) {
	var value contextprobe.Probe
	var pathsJSON, created, updated string
	var started, finished, promptSent sql.NullString
	var input, cached, cacheWrite, output, reasoning, total, window sql.NullInt64
	var canonical sql.NullString
	err := scanner.Scan(&value.ID, &value.AISessionID, &value.ProjectID, &value.RepositoryID, &value.CodexConfigID, &value.Scope.Kind, &pathsJSON, &value.CodexInstruction, &value.Status, &value.ThreadID, &value.TurnID, &started, &finished, &value.TokenUsage.Status, &input, &cached, &cacheWrite, &output, &reasoning, &total, &window, &value.Result, &value.ResultArtifactRef, &value.FailureMessage, &value.FinalCodexPrompt, &value.PromptSHA256, &value.PromptBytes, &promptSent, &created, &updated, &value.TaskID, &value.RunID, &value.RuntimeEpoch, &value.AgentExecutionID, &canonical, &value.CanonicalRequestID, &value.CanonicalPayloadSHA256, &value.PrimaryMemberID, &value.WorkspaceID, &value.RuntimeWorksetJSON, &value.RuntimeWorksetSHA256, &value.ReportSHA256, &value.ResultSHA256, &value.RuntimeLimitation, &value.RecoverySuccessorRunID)
	if err != nil {
		return value, err
	}
	var paths probePaths
	if err := json.Unmarshal([]byte(pathsJSON), &paths); err != nil {
		return value, fmt.Errorf("decode context probe paths: %w", err)
	}
	if paths.Version != 1 {
		return value, fmt.Errorf("decode context probe paths: unsupported version %d", paths.Version)
	}
	value.Scope.Paths = paths.Paths
	if canonical.Valid {
		var plan contextprobe.InspectionConfirmationPlan
		if err := json.Unmarshal([]byte(canonical.String), &plan); err != nil {
			return value, fmt.Errorf("decode canonical inspection receipt: %w", err)
		}
		value.Canonical = &plan
	}
	if started.Valid {
		parsed, err := parseTime(started.String)
		if err != nil {
			return value, err
		}
		value.StartedAt = &parsed
	}
	if finished.Valid {
		parsed, err := parseTime(finished.String)
		if err != nil {
			return value, err
		}
		value.FinishedAt = &parsed
	}
	if promptSent.Valid {
		parsed, err := parseTime(promptSent.String)
		if err != nil {
			return value, err
		}
		value.PromptSentAt = &parsed
	}
	if input.Valid && cached.Valid && cacheWrite.Valid && output.Valid && reasoning.Valid && total.Valid {
		value.TokenUsage.Usage = &run.TokenCounts{InputTokens: input.Int64, CachedInputTokens: cached.Int64, CacheWriteInputTokens: cacheWrite.Int64, OutputTokens: output.Int64, ReasoningOutputTokens: reasoning.Int64, TotalTokens: total.Int64}
	}
	if window.Valid {
		value.TokenUsage.ModelContextWindow = &window.Int64
	}
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return value, err
	}
	value.UpdatedAt, err = parseTime(updated)
	return value, err
}

func (s *ContextProbeStore) RecordPrompt(ctx context.Context, id, prompt string, sentAt time.Time) (contextprobe.Probe, error) {
	value, readErr := s.Get(ctx, id)
	if readErr != nil {
		return contextprobe.Probe{}, readErr
	}
	if value.Canonical != nil {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	sum := sha256.Sum256([]byte(prompt))
	result, err := s.db.execWrite(ctx, `UPDATE context_probes SET final_codex_prompt=?,prompt_sha256=?,prompt_bytes=?,prompt_sent_at=?,updated_at=? WHERE id=? AND status='RUNNING' AND final_codex_prompt IS NULL`, prompt, hex.EncodeToString(sum[:]), len([]byte(prompt)), formatTime(sentAt), formatTime(sentAt), id)
	if err != nil {
		return contextprobe.Probe{}, conflict(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		existing, getErr := s.Get(ctx, id)
		if getErr == nil && existing.FinalCodexPrompt == prompt {
			return existing, nil
		}
		if getErr != nil {
			return contextprobe.Probe{}, getErr
		}
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	return s.Get(ctx, id)
}

func (s *ContextProbeStore) Get(ctx context.Context, id string) (contextprobe.Probe, error) {
	value, err := scanContextProbe(s.db.sql.QueryRowContext(ctx, selectContextProbe+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, contextprobe.ErrNotFound
	}
	if err != nil {
		return value, fmt.Errorf("get context probe: %w", err)
	}
	return value, nil
}

func (s *ContextProbeStore) List(ctx context.Context) ([]contextprobe.Probe, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectContextProbe+` ORDER BY created_at DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []contextprobe.Probe
	for rows.Next() {
		value, err := scanContextProbe(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *ContextProbeStore) Update(ctx context.Context, id string, update func(*contextprobe.Probe) error) (contextprobe.Probe, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	defer tx.Rollback()
	value, err := scanContextProbe(tx.QueryRowContext(ctx, selectContextProbe+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, contextprobe.ErrNotFound
	}
	if err != nil {
		return value, err
	}
	if value.Canonical != nil {
		return contextprobe.Probe{}, fmt.Errorf("%w: canonical Inspection transitions require its runtime reducer", contextprobe.ErrConflict)
	}
	if value.RunID != "" {
		if err := s.db.checkRunTx(ctx, tx, value.RunID); err != nil {
			return value, err
		}
	}
	previous := value
	if err := update(&value); err != nil {
		return contextprobe.Probe{}, err
	}
	if value.Status != previous.Status && value.Status != contextprobe.StatusCanceled {
		if err := requireResolvedResponsesTx(ctx, tx, value.RunID); err != nil {
			return contextprobe.Probe{}, err
		}
	}
	if value.AgentExecutionID != previous.AgentExecutionID || value.TaskID != previous.TaskID || value.RunID != previous.RunID || value.RuntimeEpoch != previous.RuntimeEpoch || value.AISessionID != previous.AISessionID || value.ProjectID != previous.ProjectID || value.RepositoryID != previous.RepositoryID || value.CodexConfigID != previous.CodexConfigID || value.Scope.Kind != previous.Scope.Kind || !sameProbePaths(value.Scope.Paths, previous.Scope.Paths) || value.CodexInstruction != previous.CodexInstruction {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	usage := value.TokenUsage.Usage
	var input, cached, cacheWrite, output, reasoning, total, window any
	if usage != nil {
		input, cached, cacheWrite, output, reasoning, total = usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteInputTokens, usage.OutputTokens, usage.ReasoningOutputTokens, usage.TotalTokens
	}
	if value.TokenUsage.ModelContextWindow != nil {
		window = *value.TokenUsage.ModelContextWindow
	}
	result, err := tx.ExecContext(ctx, `UPDATE context_probes SET status=?,thread_id=?,turn_id=?,started_at=?,finished_at=?,token_usage_status=?,input_tokens=?,cached_input_tokens=?,cache_write_input_tokens=?,output_tokens=?,reasoning_output_tokens=?,total_tokens=?,model_context_window=?,result=?,result_artifact_ref=?,failure_message=?,updated_at=? WHERE id=? AND status=? AND updated_at=?`, value.Status, nullString(value.ThreadID), nullString(value.TurnID), nullableTime(value.StartedAt), nullableTime(value.FinishedAt), value.TokenUsage.Status, input, cached, cacheWrite, output, reasoning, total, window, nullString(value.Result), nullString(value.ResultArtifactRef), nullString(value.FailureMessage), formatTime(value.UpdatedAt), id, previous.Status, formatTime(previous.UpdatedAt))
	if err != nil {
		return contextprobe.Probe{}, conflict(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	if err := s.syncInspectionRunTx(ctx, tx, &value); err != nil {
		return value, err
	}
	if value.Status != previous.Status {
		payload, _ := event.Payload(map[string]any{"from": previous.Status, "to": value.Status})
		if err := appendEvent(ctx, tx, event.Event{Type: event.ContextProbeStatusChanged, ProjectID: value.ProjectID, EntityType: "context_probe", EntityID: value.ID, Payload: payload, CreatedAt: value.UpdatedAt}); err != nil {
			return contextprobe.Probe{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return contextprobe.Probe{}, err
	}
	return value, nil
}

func sameProbePaths(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *ContextProbeStore) RecoverRunning(ctx context.Context, now time.Time) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(run_id,'') FROM context_probes WHERE status='RUNNING' AND canonical_target_receipt_json IS NULL AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.run_id=context_probes.run_id AND c.owner_type='INSPECTION' AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')) AND NOT EXISTS(SELECT 1 FROM runs r JOIN agent_executions e ON e.id=r.agent_execution_id WHERE r.id=context_probes.run_id AND e.provenance_kind='LEGACY_UNKNOWN')`)
	if err != nil {
		return err
	}
	type candidate struct{ id, runID string }
	var candidates []candidate
	for rows.Next() {
		var v candidate
		if err = rows.Scan(&v.id, &v.runID); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range candidates {
		if err = requireResolvedResponsesTx(ctx, tx, v.runID); errors.Is(err, agentruntime.ErrResponseDispositionUnknown) {
			continue
		} else if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE context_probes SET status='FAILED',finished_at=?,failure_message='Bridge restarted during context probe',token_usage_status='UNAVAILABLE',updated_at=? WHERE id=? AND status='RUNNING'`, formatTime(now), formatTime(now), v.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
