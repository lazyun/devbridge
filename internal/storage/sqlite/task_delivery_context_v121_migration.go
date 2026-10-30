package sqlite

// Keep the context stable from acceptance through parent completion, including
// members whose commit operation has not yet been reserved or has completed.
const pendingTaskDeliveryRepositorySQL = `EXISTS(
 SELECT 1 FROM task_commit_receipts q JOIN tasks t ON t.id=q.task_id
 WHERE q.completed_at IS NULL AND (
 (q.protocol_version=1 AND t.repository_id=?)
 OR EXISTS(SELECT 1 FROM task_commit_members m WHERE m.task_id=q.task_id AND m.repository_id=?)))`

const taskDeliveryContextV121SQL = `
CREATE TRIGGER development_selection_task_delivery_fence BEFORE UPDATE ON development_cycles
WHEN EXISTS(SELECT 1 FROM task_commit_receipts q JOIN tasks t ON t.id=q.task_id
 WHERE q.completed_at IS NULL AND (
 (q.protocol_version=1 AND t.repository_id=OLD.repository_id)
 OR EXISTS(SELECT 1 FROM task_commit_members m WHERE m.task_id=q.task_id AND m.repository_id=OLD.repository_id)))
BEGIN SELECT RAISE(ABORT,'GIT_FINALIZATION_PENDING'); END;
`
