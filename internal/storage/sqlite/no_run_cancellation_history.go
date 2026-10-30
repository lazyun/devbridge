package sqlite

// Alias c is a historical Execution cancellation. After a successor replaces
// the canceled owner, current-Task eligibility no longer describes this row.
// Its exact NO_BACKEND receipt and the absence of all predecessor runtime
// authority prove settlement without inventing a terminal backend observation.
const settledNoRunExecutionCancellationSQL = `c.owner_type='EXECUTION'
 AND c.state='CANCELED' AND c.backend_disposition='NO_BACKEND'
 AND c.interrupt_state='NOT_SENT' AND c.run_id IS NULL AND c.thread_id IS NULL AND c.turn_id IS NULL AND c.resolved_at IS NOT NULL
 AND EXISTS(SELECT 1 FROM execution_proposals e
 WHERE e.id=c.owner_id AND e.id=c.execution_id AND e.project_id=c.project_id AND e.task_id=c.task_id
 AND e.status='ADMITTED' AND e.outcome='CANCELED' AND e.run_id IS NULL AND e.verification_run_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.execution_proposal_id=e.id OR (r.operation_kind='EXECUTION' AND r.operation_id=e.id))
 AND NOT EXISTS(SELECT 1 FROM runtime_preparation_attempts attempt JOIN runtime_scope_plans p ON p.plan_id=attempt.plan_id WHERE p.owner_kind IN ('IMPLEMENTATION','EXECUTION') AND p.owner_id=e.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_workset_bindings b JOIN runtime_scope_plans p ON p.plan_id=b.plan_id WHERE p.owner_kind IN ('IMPLEMENTATION','EXECUTION') AND p.owner_id=e.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_start_authorities a JOIN runtime_scope_plans p ON p.plan_id=a.plan_id WHERE p.owner_kind IN ('IMPLEMENTATION','EXECUTION') AND p.owner_id=e.id))`
