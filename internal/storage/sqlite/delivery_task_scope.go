package sqlite

// Alias t is a development Task. A canceled status alone says nothing about
// partial edits, dispatched work or delivery outcomes. Retain those blockers.
// Closed Upper reviews are read-only history, not implementation. Require an
// exact terminal grant/Run binding for every such review; Coding history,
// missing bindings and any physical backend identity remain blockers.
const canceledReadOnlyReviewHistorySQL = `
 NOT EXISTS(SELECT 1 FROM reviews v WHERE v.task_id=t.id AND (
 v.executor_kind<>'UPPER_LLM' OR NOT EXISTS(
 SELECT 1 FROM review_grants g JOIN runs r ON r.id=g.run_id
 WHERE g.review_id=v.id AND g.project_id=t.project_id AND g.feature_area_id=t.feature_area_id
 AND g.state IN ('CLOSED','CANCELED') AND r.id=v.linked_run_id
 AND r.task_id=t.id AND r.review_id=v.id AND r.purpose='SCOPE_REVIEW'
 AND r.status IN ('SUCCEEDED','FAILED','CANCELED','INFRASTRUCTURE_FAILED')
 AND r.finished_at IS NOT NULL AND COALESCE(r.thread_id,'')='' AND COALESCE(r.turn_id,'')=''
 AND COALESCE(r.execution_proposal_id,'')='')))
 AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.task_id=t.id AND NOT EXISTS(
 SELECT 1 FROM reviews v JOIN review_grants g ON g.review_id=v.id
 WHERE v.task_id=t.id AND v.executor_kind='UPPER_LLM' AND v.linked_run_id=r.id
 AND g.run_id=r.id AND r.review_id=v.id))`

// Only a proven pre-implementation cancellation can be omitted from readiness.
// No-Run cancellation normally has terminal_observed=0; its NO_BACKEND receipt
// and absence of engineering history, not a runtime observation, prove safety.
const deliveryRelevantTaskSQL = `NOT (
 t.status='CANCELED' AND t.commit_sha IS NULL AND t.merged_commit IS NULL
 AND EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.project_id=t.project_id
 AND c.task_id=t.id AND c.owner_type='TASK' AND c.owner_id=t.id
 AND c.state='CANCELED' AND c.run_id IS NULL AND c.backend_disposition='NO_BACKEND'
 AND c.interrupt_state='NOT_SENT' AND c.resolved_at IS NOT NULL)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.task_id=t.id
 AND (c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED') OR ` + unresolvedCancellationInterruptCSQL + `
 OR c.backend_disposition='UNKNOWN_ORPHANED'))
 AND ` + canceledReadOnlyReviewHistorySQL + `
 AND NOT EXISTS(SELECT 1 FROM execution_proposals e WHERE e.task_id=t.id)
 AND NOT EXISTS(SELECT 1 FROM direct_file_task_authorizations g WHERE g.task_id=t.id)
 AND NOT EXISTS(SELECT 1 FROM task_commit_receipts q WHERE q.task_id=t.id))`

// Use the same scalar readiness predicate for presentation and the writer check.
const incompleteScalarFeatureAreaTasksSQL = `EXISTS(SELECT 1 FROM tasks t
 WHERE t.feature_area_id=? AND t.task_type='DEVELOPMENT' AND ` + deliveryRelevantTaskSQL + `
 AND (t.status<>'SUCCEEDED' OR t.commit_sha IS NULL))`
