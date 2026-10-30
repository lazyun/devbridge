package sqlite

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lazyun/devbridge/internal/workflowevents"
)

const workflowEventsV127SQL = `
CREATE TABLE workflow_event_subscriptions (
 id TEXT PRIMARY KEY, caller_scope TEXT NOT NULL, ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 project_id TEXT NOT NULL REFERENCES projects(id), task_id TEXT NOT NULL REFERENCES tasks(id),
 callback_url TEXT NOT NULL, signing_secret TEXT NOT NULL, expires_at TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'ACTIVE' CHECK(state IN ('ACTIVE','STOPPED'))
);
CREATE TABLE workflow_event_deliveries (
 subscription_id TEXT NOT NULL REFERENCES workflow_event_subscriptions(id) ON DELETE CASCADE,
 event_id TEXT NOT NULL REFERENCES events(id), state TEXT NOT NULL DEFAULT 'RETRY',
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at TEXT NOT NULL,
 PRIMARY KEY(subscription_id,event_id)
);
CREATE INDEX workflow_event_pending ON workflow_event_deliveries(state,next_attempt_at);
CREATE TRIGGER workflow_event_enqueue AFTER INSERT ON events
WHEN NEW.task_id IS NOT NULL AND NEW.type IN ('task.status_changed','run.status_changed','approval.requested','execution.completed','execution.implementation_completed','execution.verification_completed','runtime.input_required')
BEGIN
 INSERT INTO workflow_event_deliveries(subscription_id,event_id,next_attempt_at)
 SELECT s.id,NEW.id,NEW.created_at FROM workflow_event_subscriptions s
 JOIN ai_sessions a ON a.id=s.ai_session_id AND a.project_id=s.project_id
 WHERE s.state='ACTIVE' AND s.task_id=NEW.task_id AND s.project_id=NEW.project_id
 AND julianday(s.expires_at)>julianday('now');
END;
CREATE TRIGGER workflow_runtime_input_event AFTER INSERT ON codex_server_requests
WHEN json_extract(NEW.request_json,'$.state') IN ('WAITING_FOR_USER','PENDING')
 AND COALESCE(json_extract(NEW.request_json,'$.runtimeRequestId'),'')<>''
 AND json_type(NEW.request_json,'$.createdAt')='text'
BEGIN
 INSERT INTO events(id,type,project_id,task_id,run_id,entity_type,entity_id,payload_json,created_at)
 SELECT 'evt_runtime_'||lower(hex(randomblob(16))),'runtime.input_required',r.project_id,r.task_id,r.id,'runtime_request',
 json_extract(NEW.request_json,'$.runtimeRequestId'),'{"version":1}',json_extract(NEW.request_json,'$.createdAt')
 FROM runs r WHERE r.id=NEW.run_id AND r.task_id IS NOT NULL;
END;
`

type WorkflowEventStore struct{ db *DB }

func NewWorkflowEventStore(db *DB) *WorkflowEventStore { return &WorkflowEventStore{db} }
func (s *WorkflowEventStore) Status(ctx context.Context, project, task string, now time.Time) (workflowevents.Status, error) {
	var v workflowevents.Status
	e := s.db.sql.QueryRowContext(ctx, `SELECT count(DISTINCT CASE WHEN s.state='ACTIVE' AND s.expires_at>? THEN s.id END),count(DISTINCT CASE WHEN s.state='STOPPED' THEN s.id END),
 COALESCE(sum(CASE WHEN d.state='RETRY' AND s.state='ACTIVE' AND s.expires_at>? THEN 1 ELSE 0 END),0),COALESCE(sum(d.state='DELIVERED'),0),COALESCE(sum(d.state='EXHAUSTED'),0)
 FROM workflow_event_subscriptions s LEFT JOIN workflow_event_deliveries d ON d.subscription_id=s.id WHERE s.project_id=? AND s.task_id=?`, formatTime(now), formatTime(now), project, task).Scan(&v.ActiveSubscriptions, &v.StoppedSubscriptions, &v.Pending, &v.Delivered, &v.Exhausted)
	return v, e
}
func (s *WorkflowEventStore) Put(ctx context.Context, v workflowevents.Subscription) error {
	tx, e := s.db.beginWrite(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, `INSERT INTO workflow_event_subscriptions(id,caller_scope,ai_session_id,project_id,task_id,callback_url,signing_secret,expires_at) VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(id) DO UPDATE SET signing_secret=excluded.signing_secret,expires_at=excluded.expires_at,state='ACTIVE'`, v.ID, v.Scope, v.Arguments.AISessionID, v.ProjectID, v.Arguments.TaskID, v.Delivery.URL, v.Delivery.Secret, formatTime(v.ExpiresAt))
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *WorkflowEventStore) Remove(ctx context.Context, scope, id string) error {
	tx, e := s.db.beginWrite(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `DELETE FROM workflow_event_subscriptions WHERE id=? AND caller_scope=?`, id, scope); e != nil {
		return e
	}
	return tx.Commit()
}

const activeWorkflowSubscription = `s.state='ACTIVE' AND s.expires_at>? AND EXISTS(SELECT 1 FROM ai_sessions a JOIN tasks t ON t.project_id=a.project_id WHERE a.id=s.ai_session_id AND a.caller_scope=s.caller_scope AND a.project_id=s.project_id AND t.id=s.task_id)`

func (s *WorkflowEventStore) Active(ctx context.Context, id string, now time.Time) (bool, error) {
	var n int
	e := s.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM workflow_event_subscriptions s WHERE s.id=? AND `+activeWorkflowSubscription, id, formatTime(now)).Scan(&n)
	return n == 1, e
}
func (s *WorkflowEventStore) Pending(ctx context.Context, now time.Time) ([]workflowevents.Pending, error) {
	rows, e := s.db.sql.QueryContext(ctx, `SELECT s.id,s.caller_scope,s.ai_session_id,s.project_id,s.task_id,s.callback_url,s.signing_secret,s.expires_at,e.id,e.type,COALESCE(e.run_id,''),e.created_at,d.attempts
 FROM workflow_event_deliveries d JOIN workflow_event_subscriptions s ON s.id=d.subscription_id JOIN events e ON e.id=d.event_id
 WHERE d.state='RETRY' AND d.next_attempt_at<=? AND `+activeWorkflowSubscription+` ORDER BY e.sequence LIMIT 20`, formatTime(now), formatTime(now))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []workflowevents.Pending{}
	for rows.Next() {
		var v workflowevents.Pending
		var expiry, kind, run, stamp string
		if e = rows.Scan(&v.Subscription.ID, &v.Subscription.Scope, &v.Subscription.Arguments.AISessionID, &v.Subscription.ProjectID, &v.Subscription.Arguments.TaskID, &v.Subscription.Delivery.URL, &v.Subscription.Delivery.Secret, &expiry, &v.EventID, &kind, &run, &stamp, &v.Attempts); e != nil {
			return nil, e
		}
		if v.Subscription.ExpiresAt, e = parseTime(expiry); e != nil {
			return nil, e
		}
		v.Body, e = json.Marshal(map[string]any{"eventId": v.EventID, "name": workflowevents.Name, "timestamp": stamp, "cursor": nil, "data": map[string]string{"aiSessionId": v.Subscription.Arguments.AISessionID, "projectId": v.Subscription.ProjectID, "taskId": v.Subscription.Arguments.TaskID, "runId": run, "sourceType": kind}})
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *WorkflowEventStore) Finish(ctx context.Context, v workflowevents.Pending, state string, next time.Time) error {
	tx, e := s.db.beginWrite(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE workflow_event_deliveries SET state=?,attempts=attempts+1,next_attempt_at=? WHERE subscription_id=? AND event_id=? AND state='RETRY' AND attempts=?`, state, formatTime(next), v.Subscription.ID, v.EventID, v.Attempts); e != nil {
		return e
	}
	if state == "STOPPED" {
		if _, e = tx.ExecContext(ctx, `UPDATE workflow_event_subscriptions SET state='STOPPED' WHERE id=?`, v.Subscription.ID); e != nil {
			return e
		}
	}
	return tx.Commit()
}
