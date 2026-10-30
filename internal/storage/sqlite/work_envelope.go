package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// insertWorkEnvelopeTx creates a visible non-Development Task before any Run
// can be admitted. Its deterministic identity also binds legacy backfills.
func insertWorkEnvelopeTx(ctx context.Context, tx *sql.Tx, kind, owner, project, area, repository, config, title, base, model, effort string, now time.Time) (string, error) {
	if kind != "INSPECTION" {
		return "", errors.New("only project Inspection uses an internal envelope")
	}
	id := "work_" + owner
	scope := "FEATURE_AREA"
	if kind == "INSPECTION" {
		scope = "PROJECT"
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at,problem_statement,feature_area_id,task_type,task_scope,work_owner_id,model,reasoning_effort)
 VALUES(?,?,(SELECT id FROM modules WHERE project_id=? ORDER BY system_owned DESC,id LIMIT 1),?,?,?,'{"version":1,"items":[]}',?,'PLANNING',?,?,?,?,?,?,?,?,?)`, id, project, project, repository, config, title, base, formatTime(now), formatTime(now), title, nullString(area), kind, scope, owner, nullString(model), nullString(effort))
	return id, err
}

// Work facts are a low-level snapshot read in the same SELECT as Task. The
// Task-wide cancellation fields retain legacy context, not exact Execution
// facts. Product readers must use Core EnrichTaskExecution before presenting
// Task.Work; it overwrites Execution fields from the exact owner projection.

// Operation facts are selected through the Task's durable pointers. They are
// activity details; phase itself is read directly from tasks.phase.
const taskCurrentRunSQL = `CASE WHEN tasks.phase IN ('REVIEW_REQUIRED','REVIEWING','REVIEW_APPROVAL_PENDING') AND tasks.task_type='DEVELOPMENT' THEN (SELECT linked_run_id FROM reviews WHERE id=tasks.current_review_id)
 WHEN tasks.task_type='INSPECTION' THEN (SELECT run_id FROM context_probes WHERE task_id=tasks.id)
 ELSE (SELECT COALESCE(verification_run_id,run_id) FROM execution_proposals WHERE id=tasks.current_execution_id) END`
const taskWorkColumns = `,task_type,task_scope,COALESCE(work_owner_id,''),
 CASE WHEN task_type='INSPECTION' THEN 'INSPECTION' WHEN phase IN ('REVIEW_REQUIRED','REVIEWING','REVIEW_APPROVAL_PENDING') AND current_review_id IS NOT NULL THEN 'REVIEW' WHEN current_execution_id IS NOT NULL THEN 'EXECUTION' ELSE 'TASK' END,
 COALESCE(CASE WHEN phase IN ('REVIEW_REQUIRED','REVIEWING','REVIEW_APPROVAL_PENDING') THEN current_review_id ELSE current_execution_id END,work_owner_id,id),
 COALESCE((` + taskCurrentRunSQL + `),''),
 COALESCE((SELECT status FROM runs WHERE id=(` + taskCurrentRunSQL + `)),''),
 COALESCE((SELECT state FROM execution_cancellations WHERE task_id=tasks.id ORDER BY requested_at DESC,request_id DESC LIMIT 1),''),
 COALESCE((SELECT disposition FROM agent_orphan_dispositions WHERE run_id=(` + taskCurrentRunSQL + `)),''),
 COALESCE((SELECT status FROM run_token_usage WHERE run_id=(` + taskCurrentRunSQL + `)),'UNAVAILABLE'),
 MAX(updated_at,COALESCE((SELECT updated_at FROM runs WHERE id=(` + taskCurrentRunSQL + `)),updated_at)),
 COALESCE((SELECT purpose FROM runs WHERE id=(` + taskCurrentRunSQL + `)),''),
 legacy_read_only=1 OR status IN ('CANCELED','SUCCEEDED','FAILED') OR EXISTS(SELECT 1 FROM feature_areas WHERE id=tasks.feature_area_id AND status IN ('RETIRING','RETIRED','DECOMPOSED')),
 'DURABLE',
 COALESCE((SELECT id FROM execution_proposals WHERE id=tasks.current_execution_id AND run_id IS NULL AND verification_run_id IS NULL),''),
 COALESCE((SELECT json_group_array(s.development_ref) FROM task_review_sources s JOIN reviews r ON r.id=s.review_id WHERE r.id=tasks.current_review_id AND s.development_ref<>''),'[]'),
 COALESCE((SELECT json_group_array(json_object('runId',COALESCE(r.id,''),'id',e.id,'kind',e.kind,'provenance',e.provenance_kind,'proposal',COALESCE(e.execution_proposal_id,''))) FROM agent_executions e LEFT JOIN runs r ON r.agent_execution_id=e.id WHERE e.task_id=tasks.id AND e.project_id=tasks.project_id),'[]')`
