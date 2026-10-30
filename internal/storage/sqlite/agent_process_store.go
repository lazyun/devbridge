package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"time"
)

const schemaV41 = `
CREATE TABLE agent_process_launches (
 launch_id TEXT PRIMARY KEY,
 pid INTEGER NOT NULL CHECK(pid>0),
 executable TEXT NOT NULL,
 started_at TEXT NOT NULL,
 ended_at TEXT,
 exit_code INTEGER,
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id)
);
CREATE TABLE run_agent_processes (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 launch_id TEXT NOT NULL REFERENCES agent_process_launches(launch_id)
);
CREATE INDEX run_agent_process_launch ON run_agent_processes(launch_id);
CREATE TRIGGER agent_process_identity_immutable BEFORE UPDATE OF launch_id,pid,executable,started_at,runtime_epoch ON agent_process_launches
BEGIN SELECT RAISE(ABORT,'process launch identity is immutable'); END;
CREATE TRIGGER run_agent_process_immutable BEFORE UPDATE ON run_agent_processes
BEGIN SELECT RAISE(ABORT,'Run process binding is immutable'); END;
`

func (s *AgentRuntimeStore) RecordProcess(ctx context.Context, v agentruntime.ProcessLaunch) error {
	_, err := s.db.execWrite(ctx, `INSERT INTO agent_process_launches(launch_id,pid,executable,started_at,runtime_epoch) VALUES(?,?,?,?,?)`, v.LaunchID, v.PID, v.Executable, formatTime(v.StartedAt), s.db.epoch)
	return err
}
func (s *AgentRuntimeStore) BindProcess(ctx context.Context, runID, launchID string) error {
	fence, ok := runtimeepoch.FromContext(ctx)
	if !ok || fence.RunID != runID {
		return runtimeepoch.ErrFenced
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actual string
	err = tx.QueryRowContext(ctx, `SELECT p.launch_id FROM agent_process_launches p JOIN runs r ON r.runtime_epoch=p.runtime_epoch WHERE p.launch_id=? AND r.id=? AND p.runtime_epoch=? AND p.ended_at IS NULL`, launchID, runID, fence.Epoch).Scan(&actual)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO run_agent_processes(run_id,launch_id) VALUES(?,?) ON CONFLICT(run_id) DO NOTHING`, runID, actual)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		var existing string
		if err = tx.QueryRowContext(ctx, `SELECT launch_id FROM run_agent_processes WHERE run_id=?`, runID).Scan(&existing); err != nil {
			return err
		}
		if existing != actual {
			return ErrConflict
		}
	}
	return tx.Commit()
}
func (s *AgentRuntimeStore) EndProcess(ctx context.Context, id string, at time.Time, code int) error {
	if fence, ok := runtimeepoch.FromContext(ctx); ok && fence.Epoch != s.db.epoch {
		return runtimeepoch.ErrFenced
	}
	ctx = runtimeepoch.WithFence(ctx, s.db.epoch, "")
	_, err := s.db.execWrite(ctx, `UPDATE agent_process_launches SET ended_at=?,exit_code=? WHERE launch_id=? AND runtime_epoch=? AND ended_at IS NULL`, formatTime(at), code, id, s.db.epoch)
	return err
}
func (s *AgentRuntimeStore) processForRun(ctx context.Context, id string) (*agentruntime.ProcessLaunch, error) {
	var p agentruntime.ProcessLaunch
	var started string
	var ended sql.NullString
	var code sql.NullInt64
	err := s.db.sql.QueryRowContext(ctx, `SELECT p.launch_id,p.pid,p.executable,p.started_at,p.ended_at,p.exit_code,p.runtime_epoch FROM agent_process_launches p JOIN run_agent_processes r ON r.launch_id=p.launch_id WHERE r.run_id=?`, id).Scan(&p.LaunchID, &p.PID, &p.Executable, &started, &ended, &code, &p.RuntimeEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.StartedAt, err = parseTime(started)
	if err != nil {
		return nil, err
	}
	if ended.Valid {
		v, e := parseTime(ended.String)
		if e != nil {
			return nil, e
		}
		p.EndedAt = &v
	}
	if code.Valid {
		v := int(code.Int64)
		p.ExitCode = &v
	}
	return &p, nil
}
