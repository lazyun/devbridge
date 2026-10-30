package sqlite

// Upgrade only the merge authority guard. Existing Task and merge receipts
// remain unchanged, including incomplete or historically partial plans.
const mixedMergeV122SQL = `
DROP TRIGGER merge_operation_initial_exact;
CREATE TRIGGER merge_operation_initial_exact BEFORE INSERT ON merge_delivery_operations WHEN NEW.predecessor_operation_id IS NULL AND NOT EXISTS(
 SELECT 1 FROM feature_area_commit_members m JOIN feature_area_commit_receipts p ON p.request_id=m.request_id JOIN tasks t ON t.id=NEW.task_id JOIN task_commit_receipts q ON q.task_id=t.id JOIN (` + acceptedDeliveryCommitsSQL + `) tm ON tm.task_id=t.id AND tm.repository_id=m.repository_id
 WHERE m.request_id=NEW.merge_request_id AND m.repository_id=NEW.repository_id AND p.feature_area_id=NEW.feature_area_id AND p.completed_at IS NULL
 AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id AND t.status='SUCCEEDED' AND q.terminal_outcome='APPLIED' AND q.completed_at IS NOT NULL
 AND q.revision_id=t.current_definition_revision_id AND (q.protocol_version=2 OR q.resulting_commit=tm.resulting_commit)
 AND tm.member_id=m.member_id AND tm.member_version=m.member_version AND tm.worktree_id=m.worktree_id
 AND tm.workspace_path=m.source_path AND tm.branch=m.source_branch
 AND tm.state='COMPLETED' AND EXISTS(SELECT 1 FROM json_each(m.task_commits_json) j WHERE j.value=tm.resulting_commit)
 AND NEW.execution_id=COALESCE(NULLIF(t.current_execution_id,''),'direct/'||q.result_id)
 AND m.member_id=NEW.member_id AND q.workspace_id=NEW.workset_id AND m.development_cycle_id=NEW.development_context_id
 AND json_extract(NEW.payload_json,'$.PlanSHA256')=p.plan_sha256 AND json_extract(NEW.payload_json,'$.ProjectID')=NEW.project_id AND json_extract(NEW.payload_json,'$.FeatureAreaID')=p.feature_area_id
 AND json_extract(NEW.payload_json,'$.WorkspaceID')=NEW.workset_id AND json_extract(NEW.payload_json,'$.Member.repositoryId')=m.repository_id
 AND json_extract(NEW.payload_json,'$.Member.memberId')=m.member_id AND json_extract(NEW.payload_json,'$.Member.memberVersion')=m.member_version
 AND json_extract(NEW.payload_json,'$.Member.sourceHead')=m.source_head AND json_extract(NEW.payload_json,'$.Member.expectedDevHead')=m.expected_dev_head
 AND json_extract(NEW.payload_json,'$.Member.developmentCycleId')=m.development_cycle_id)
BEGIN SELECT RAISE(ABORT,'Merge operation requires exact accepted member'); END;
`
