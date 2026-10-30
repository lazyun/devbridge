package sqlite

const executionIdentityBackfill = `INSERT INTO agent_executions
 SELECT e.id,e.project_id,e.task_id,t.feature_area_id,'DEVELOPMENT',
 CASE WHEN t.project_id=e.project_id AND t.task_type='DEVELOPMENT' AND t.feature_area_id IS NOT NULL AND e.task_definition_revision_id IS NOT NULL THEN 'EXECUTION' END,
 CASE WHEN t.project_id=e.project_id AND t.task_type='DEVELOPMENT' AND t.feature_area_id IS NOT NULL AND e.task_definition_revision_id IS NOT NULL THEN e.id END,
 e.id,CASE WHEN t.project_id=e.project_id AND t.task_type='DEVELOPMENT' AND t.feature_area_id IS NOT NULL AND e.task_definition_revision_id IS NOT NULL THEN 'BACKFILLED' ELSE 'LEGACY_UNKNOWN' END,e.created_at
 FROM execution_proposals e JOIN tasks t ON t.id=e.task_id;
INSERT INTO agent_executions
 SELECT 'review:'||v.id,v.project_id,v.task_id,v.feature_area_id,'REVIEW','REVIEW',v.id,NULL,'BACKFILLED',v.created_at
 FROM reviews v JOIN tasks t ON t.id=v.task_id AND t.project_id=v.project_id AND t.feature_area_id=v.feature_area_id AND t.task_type='REVIEW' AND t.work_owner_id=v.id
 WHERE EXISTS(SELECT 1 FROM runs r WHERE r.review_id=v.id AND r.operation_kind='REVIEW' AND r.operation_id=v.id AND r.task_id=v.task_id AND r.project_id=v.project_id AND r.feature_area_id=v.feature_area_id);
INSERT INTO agent_executions
 SELECT 'inspection:'||p.id,p.project_id,p.task_id,NULL,'INSPECTION','INSPECTION',p.id,NULL,'BACKFILLED',p.created_at
 FROM context_probes p JOIN tasks t ON t.id=p.task_id AND t.project_id=p.project_id AND t.task_type='INSPECTION' AND t.task_scope='PROJECT' AND t.work_owner_id=p.id
 WHERE EXISTS(SELECT 1 FROM runs r WHERE r.context_probe_id=p.id AND r.operation_kind='INSPECTION' AND r.operation_id=p.id AND r.task_id=p.task_id AND r.project_id=p.project_id AND r.feature_area_id IS NULL);

-- This migration changes only the new identity column, including fenced history.
-- Restore the identical permanent fence before the migration transaction commits.
UPDATE runs SET agent_execution_id=(SELECT e.id FROM agent_executions e
 WHERE e.provenance_kind='BACKFILLED' AND e.task_id=runs.task_id AND e.project_id=runs.project_id AND e.feature_area_id IS runs.feature_area_id AND e.semantic_owner_kind=runs.operation_kind AND e.semantic_owner_id=runs.operation_id
 AND ((e.kind='REVIEW' AND runs.review_id=e.semantic_owner_id AND runs.purpose='SCOPE_REVIEW') OR
 (e.kind='DEVELOPMENT' AND runs.execution_proposal_id=e.id AND runs.purpose IN ('IMPLEMENTATION','VERIFICATION')) OR
 (e.kind='INSPECTION' AND runs.context_probe_id=e.semantic_owner_id AND runs.purpose='INSPECTION')));
INSERT INTO agent_executions
 SELECT 'legacy-run:'||r.id,r.project_id,r.task_id,r.feature_area_id,
 CASE WHEN r.purpose='INSPECTION' THEN 'INSPECTION' WHEN r.purpose='SCOPE_REVIEW' THEN 'REVIEW' ELSE 'DEVELOPMENT' END,
 NULL,NULL,NULL,'LEGACY_UNKNOWN',r.created_at FROM runs r WHERE r.agent_execution_id IS NULL;
UPDATE runs SET agent_execution_id='legacy-run:'||id WHERE agent_execution_id IS NULL;

`
