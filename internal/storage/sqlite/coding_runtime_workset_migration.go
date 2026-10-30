package sqlite

// v77 extends the existing per-Run binding authority to Task Executions. The
// table is rebuilt because SQLite cannot remove the v75 NOT NULL constraint on
// review_id in place. Historical Review rows retain identical values; new rows
// have exactly one immutable owner: Review or Execution.
const codingRuntimeWorksetV77SQL = `
ALTER TABLE execution_proposals ADD COLUMN primary_member_id TEXT;
ALTER TABLE execution_proposals ADD COLUMN approved_workset_hash TEXT;
ALTER TABLE execution_proposals ADD COLUMN runtime_adapter_identity TEXT;
ALTER TABLE execution_proposals ADD COLUMN runtime_protocol_identity TEXT;
ALTER TABLE reviews ADD COLUMN primary_member_id TEXT;
ALTER TABLE runs ADD COLUMN runtime_workset_json TEXT;
ALTER TABLE runs ADD COLUMN runtime_workset_hash TEXT;
ALTER TABLE task_commit_receipts ADD COLUMN evidence_kind TEXT NOT NULL DEFAULT 'DIRECT_FILE' CHECK(evidence_kind IN ('DIRECT_FILE','CODING'));
ALTER TABLE task_commit_receipts ADD COLUMN implementation_run_id TEXT REFERENCES runs(id);
ALTER TABLE task_commit_receipts ADD COLUMN verification_run_id TEXT REFERENCES runs(id);

-- v75 admitted exact multi-member Reviews only for the Upper executor.  The
-- same immutable read-only target set is now also the authority for a Coding
-- Review, whose backend/runtime selection is frozen on the Review row.
DROP TRIGGER reviews_current_owner;
CREATE TRIGGER reviews_current_owner BEFORE INSERT ON reviews
WHEN (NEW.executor_kind='CODING_AGENT' AND NEW.codex_config_id IS NULL)
 OR NOT EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND project_id=NEW.project_id AND legacy_read_only=0 AND status IN ('DISCOVERING','ACTIVE'))
 OR (json_array_length(NEW.repository_targets_json)=0 AND (
      NEW.repository_id IS NULL OR length(COALESCE(NEW.base_commit,'')) NOT IN (40,64)
      OR NOT EXISTS(SELECT 1 FROM repositories WHERE id=NEW.repository_id AND project_id=NEW.project_id)
      OR NOT EXISTS(SELECT 1 FROM tasks t WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id AND t.repository_id=NEW.repository_id AND t.task_type='DEVELOPMENT' AND t.status='ACTIVE' AND t.phase IN ('REVIEW_REQUIRED','REVIEWING'))
    ))
 OR (json_array_length(NEW.repository_targets_json)>0 AND (
      NEW.repository_id IS NOT NULL OR COALESCE(NEW.base_commit,'')<>'' OR COALESCE(NEW.workspace_id,'')=''
      OR NEW.executor_kind NOT IN ('CODING_AGENT','UPPER_LLM')
      OR NOT EXISTS(SELECT 1 FROM tasks t WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id AND t.repository_id IS NULL AND t.task_type='DEVELOPMENT' AND t.status='ACTIVE' AND t.phase IN ('REVIEW_REQUIRED','REVIEWING'))
    ))
BEGIN SELECT RAISE(ABORT,'Review requires current FeatureArea and repository ownership'); END;

-- v76 delivery rows were tied to Direct File result IDs.  v77 Coding uses
-- authoritative Run observations instead, so preserve the existing rows while
-- giving each new member intent one explicit evidence source.
DROP TRIGGER task_commit_member_owner;
DROP TRIGGER task_commit_member_identity;
DROP TRIGGER task_commit_member_durable;
DROP TRIGGER task_commit_v2_complete;
CREATE TABLE _v77_task_commit_members (
 task_id TEXT NOT NULL REFERENCES task_commit_receipts(task_id),repository_id TEXT NOT NULL REFERENCES repositories(id),member_id TEXT NOT NULL,member_version INTEGER NOT NULL,worktree_id TEXT NOT NULL REFERENCES worktrees(id),workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),result_id TEXT NOT NULL DEFAULT '',evidence_kind TEXT NOT NULL DEFAULT 'DIRECT_FILE' CHECK(evidence_kind IN ('DIRECT_FILE','CODING')),implementation_run_id TEXT REFERENCES runs(id),verification_run_id TEXT REFERENCES runs(id),origin_commit TEXT NOT NULL,
 start_snapshot TEXT NOT NULL,verified_snapshot TEXT NOT NULL,expected_head TEXT NOT NULL,commit_tree TEXT NOT NULL DEFAULT '',workspace_path TEXT NOT NULL,branch TEXT NOT NULL,root_identity TEXT NOT NULL,changed_paths_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('PENDING','UNKNOWN','COMPLETED','NO_CHANGE','FAILED')),resulting_commit TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',completed_at TEXT,
 PRIMARY KEY(task_id,repository_id),
 CHECK((evidence_kind='DIRECT_FILE' AND result_id<>'' AND implementation_run_id IS NULL AND verification_run_id IS NULL) OR (evidence_kind='CODING' AND result_id='' AND implementation_run_id IS NOT NULL AND verification_run_id IS NOT NULL))
);
INSERT INTO _v77_task_commit_members(task_id,repository_id,member_id,member_version,worktree_id,workspace_id,revision_id,result_id,evidence_kind,origin_commit,start_snapshot,verified_snapshot,expected_head,commit_tree,workspace_path,branch,root_identity,changed_paths_json,state,resulting_commit,last_error,completed_at)
 SELECT task_id,repository_id,member_id,member_version,worktree_id,workspace_id,revision_id,result_id,'DIRECT_FILE',origin_commit,start_snapshot,verified_snapshot,expected_head,commit_tree,workspace_path,branch,root_identity,changed_paths_json,state,resulting_commit,last_error,completed_at FROM task_commit_members;
DROP TABLE task_commit_members;
ALTER TABLE _v77_task_commit_members RENAME TO task_commit_members;

CREATE TRIGGER task_commit_member_identity BEFORE UPDATE ON task_commit_members
WHEN NEW.task_id IS NOT OLD.task_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.member_id IS NOT OLD.member_id OR NEW.member_version IS NOT OLD.member_version OR NEW.worktree_id IS NOT OLD.worktree_id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.revision_id IS NOT OLD.revision_id OR NEW.result_id IS NOT OLD.result_id OR NEW.evidence_kind IS NOT OLD.evidence_kind OR NEW.implementation_run_id IS NOT OLD.implementation_run_id OR NEW.verification_run_id IS NOT OLD.verification_run_id OR NEW.origin_commit IS NOT OLD.origin_commit OR NEW.start_snapshot IS NOT OLD.start_snapshot OR NEW.verified_snapshot IS NOT OLD.verified_snapshot OR NEW.expected_head IS NOT OLD.expected_head OR NEW.workspace_path IS NOT OLD.workspace_path OR NEW.branch IS NOT OLD.branch OR NEW.root_identity IS NOT OLD.root_identity OR NEW.changed_paths_json IS NOT OLD.changed_paths_json OR (OLD.commit_tree<>'' AND NEW.commit_tree<>OLD.commit_tree) OR (OLD.state IN ('COMPLETED','NO_CHANGE') AND (NEW.state<>OLD.state OR NEW.resulting_commit<>OLD.resulting_commit OR NEW.completed_at IS NOT OLD.completed_at))
BEGIN SELECT RAISE(ABORT,'Task member delivery intent is immutable'); END;
CREATE TRIGGER task_commit_member_durable BEFORE DELETE ON task_commit_members BEGIN SELECT RAISE(ABORT,'Task member delivery history is durable'); END;
DROP TRIGGER task_commit_v2_identity;
CREATE TRIGGER task_commit_v2_identity BEFORE UPDATE ON task_commit_receipts
WHEN NEW.protocol_version IS NOT OLD.protocol_version OR NEW.result_id IS NOT OLD.result_id OR NEW.evidence_kind IS NOT OLD.evidence_kind OR NEW.implementation_run_id IS NOT OLD.implementation_run_id OR NEW.verification_run_id IS NOT OLD.verification_run_id OR (OLD.protocol_version=2 AND (NEW.task_id IS NOT OLD.task_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.revision_id IS NOT OLD.revision_id OR NEW.verified_snapshot IS NOT OLD.verified_snapshot OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.workspace_path IS NOT OLD.workspace_path OR NEW.branch IS NOT OLD.branch))
BEGIN SELECT RAISE(ABORT,'Task delivery identity is immutable'); END;
DROP TRIGGER IF EXISTS runs_workspace_owner_guard;
DROP TRIGGER IF EXISTS runs_workspace_insert_owner_guard;
CREATE TRIGGER runs_workspace_owner_guard BEFORE UPDATE OF workspace_id ON runs
WHEN NEW.workspace_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM workspaces w WHERE w.id=NEW.workspace_id AND w.project_id=NEW.project_id AND w.feature_area_id=NEW.feature_area_id AND w.runtime_quarantined=0 AND w.id=(SELECT current_workspace_id FROM feature_areas WHERE id=NEW.feature_area_id) AND
 (w.repository_id IS (SELECT repository_id FROM tasks WHERE id=NEW.task_id) OR
  EXISTS(SELECT 1 FROM reviews v WHERE v.id=NEW.review_id AND v.workspace_id=w.id AND json_array_length(v.repository_targets_json)>0) OR
  EXISTS(SELECT 1 FROM tasks t JOIN reviews v ON v.id=t.current_review_id WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id AND v.workspace_id=w.id AND json_array_length(v.repository_targets_json)>0))
)
BEGIN SELECT RAISE(ABORT,'Run workspace must match operation environment'); END;
CREATE TRIGGER runs_workspace_insert_owner_guard BEFORE INSERT ON runs
WHEN NEW.workspace_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM workspaces w WHERE w.id=NEW.workspace_id AND w.project_id=NEW.project_id AND w.feature_area_id=NEW.feature_area_id AND w.runtime_quarantined=0 AND w.id=(SELECT current_workspace_id FROM feature_areas WHERE id=NEW.feature_area_id) AND
 (w.repository_id IS (SELECT repository_id FROM tasks WHERE id=NEW.task_id) OR
  EXISTS(SELECT 1 FROM reviews v WHERE v.id=NEW.review_id AND v.workspace_id=w.id AND json_array_length(v.repository_targets_json)>0) OR
  EXISTS(SELECT 1 FROM tasks t JOIN reviews v ON v.id=t.current_review_id WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id AND v.workspace_id=w.id AND json_array_length(v.repository_targets_json)>0))
)
BEGIN SELECT RAISE(ABORT,'Run workspace must match operation environment'); END;

DROP TRIGGER run_repository_binding_owner;
DROP TRIGGER run_repository_binding_identity;
DROP TRIGGER run_repository_binding_no_delete;
DROP TRIGGER run_repository_binding_worktree;
DROP INDEX run_repository_bindings_worktree;
CREATE TABLE _v77_run_repository_bindings (
 run_id TEXT NOT NULL REFERENCES runs(id),review_id TEXT REFERENCES reviews(id),execution_id TEXT REFERENCES execution_proposals(id),
 project_id TEXT NOT NULL REFERENCES projects(id),feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),workspace_id TEXT NOT NULL,
 repository_id TEXT NOT NULL REFERENCES repositories(id),member_id TEXT NOT NULL,member_version INTEGER NOT NULL CHECK(member_version>0),
 origin_commit TEXT NOT NULL CHECK(length(origin_commit) IN (40,64)),worktree_id TEXT REFERENCES worktrees(id),
 preparation TEXT NOT NULL CHECK(preparation IN ('PENDING','READY','FAILED')),failure TEXT NOT NULL DEFAULT '',
 start_snapshot TEXT,root_path TEXT,root_identity TEXT,last_observed_at TEXT,
 repository_alias TEXT,start_head TEXT,intended_read INTEGER CHECK(intended_read IN (0,1)),intended_write_scope_json TEXT,runtime_root_identity TEXT,
 end_head TEXT,end_snapshot TEXT,end_root_identity TEXT,changed_paths_json TEXT,diff_digest TEXT,
 observation_state TEXT CHECK(observation_state IN ('PENDING','OBSERVED','NO_CHANGE','SCOPE_CONFLICT','UNKNOWN')),observed_at TEXT,
 PRIMARY KEY(run_id,repository_id),UNIQUE(run_id,member_id),CHECK((review_id IS NOT NULL)+(execution_id IS NOT NULL)=1),
 CHECK(preparation<>'READY' OR worktree_id IS NOT NULL),CHECK(intended_write_scope_json IS NULL OR json_valid(intended_write_scope_json)),
 CHECK(changed_paths_json IS NULL OR json_valid(changed_paths_json))
);
INSERT INTO _v77_run_repository_bindings(run_id,review_id,execution_id,project_id,feature_area_id,workspace_id,repository_id,member_id,member_version,origin_commit,worktree_id,preparation,failure,start_snapshot,root_path,root_identity,last_observed_at)
 SELECT run_id,review_id,NULL,project_id,feature_area_id,workspace_id,repository_id,member_id,member_version,origin_commit,worktree_id,preparation,failure,start_snapshot,root_path,root_identity,last_observed_at FROM run_repository_bindings;
DROP TABLE run_repository_bindings;
ALTER TABLE _v77_run_repository_bindings RENAME TO run_repository_bindings;
CREATE INDEX run_repository_bindings_worktree ON run_repository_bindings(worktree_id);

CREATE TRIGGER run_repository_binding_owner BEFORE INSERT ON run_repository_bindings
WHEN NOT (
 (NEW.review_id IS NOT NULL AND NEW.execution_id IS NULL AND EXISTS(
  SELECT 1 FROM runs r JOIN reviews v ON v.id=r.review_id JOIN json_each(v.repository_targets_json) target JOIN repositories repo ON repo.id=NEW.repository_id
  WHERE r.id=NEW.run_id AND v.id=NEW.review_id AND v.project_id=NEW.project_id AND v.feature_area_id=NEW.feature_area_id AND v.workspace_id=NEW.workspace_id AND repo.project_id=NEW.project_id
  AND json_extract(target.value,'$.repositoryId')=NEW.repository_id AND json_extract(target.value,'$.memberId')=NEW.member_id AND json_extract(target.value,'$.memberVersion')=NEW.member_version AND json_extract(target.value,'$.originCommit')=NEW.origin_commit
 )) OR
 (NEW.review_id IS NULL AND NEW.execution_id IS NOT NULL AND EXISTS(
  SELECT 1 FROM runs r JOIN execution_proposals e ON e.id=r.execution_proposal_id JOIN tasks t ON t.id=e.task_id JOIN task_definition_revisions d ON d.id=e.task_definition_revision_id AND d.task_id=t.id JOIN json_each(d.payload_json,'$.repositoryTargets') target JOIN repositories repo ON repo.id=NEW.repository_id
  WHERE r.id=NEW.run_id AND r.execution_proposal_id=NEW.execution_id AND r.task_id=t.id AND r.project_id=NEW.project_id AND r.feature_area_id=NEW.feature_area_id AND r.workspace_id=NEW.workspace_id
  AND e.id=NEW.execution_id AND e.project_id=NEW.project_id AND COALESCE(e.approved_workset_hash,'')<>'' AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id AND repo.project_id=NEW.project_id
  AND json_extract(target.value,'$.repositoryId')=NEW.repository_id AND json_extract(target.value,'$.memberId')=NEW.member_id AND json_extract(target.value,'$.memberVersion')=NEW.member_version AND json_extract(target.value,'$.originCommit')=NEW.origin_commit
 ))
)
BEGIN SELECT RAISE(ABORT,'Run binding requires frozen Review or Execution target'); END;
CREATE TRIGGER run_repository_binding_identity BEFORE UPDATE ON run_repository_bindings
WHEN NEW.run_id IS NOT OLD.run_id OR NEW.review_id IS NOT OLD.review_id OR NEW.execution_id IS NOT OLD.execution_id OR NEW.project_id IS NOT OLD.project_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.workspace_id IS NOT OLD.workspace_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.member_id IS NOT OLD.member_id OR NEW.member_version IS NOT OLD.member_version OR NEW.origin_commit IS NOT OLD.origin_commit OR (OLD.worktree_id IS NOT NULL AND NEW.worktree_id IS NOT OLD.worktree_id)
BEGIN SELECT RAISE(ABORT,'Run member binding is immutable'); END;
CREATE TRIGGER run_repository_binding_no_delete BEFORE DELETE ON run_repository_bindings BEGIN SELECT RAISE(ABORT,'Run member binding is durable'); END;
CREATE TRIGGER run_repository_binding_worktree BEFORE UPDATE OF worktree_id,preparation ON run_repository_bindings
WHEN NEW.preparation='READY' AND NOT EXISTS(SELECT 1 FROM workspace_members m JOIN workspaces w ON w.id=m.workspace_id JOIN worktrees wt ON wt.id=m.worktree_id
 WHERE m.id=NEW.member_id AND m.identity_version=NEW.member_version AND m.repository_id=NEW.repository_id AND m.origin_commit=NEW.origin_commit AND w.id=NEW.workspace_id AND w.project_id=NEW.project_id AND w.feature_area_id=NEW.feature_area_id AND m.worktree_id=NEW.worktree_id AND m.preparation='READY' AND wt.status='READY')
BEGIN SELECT RAISE(ABORT,'Run binding requires exact ready member'); END;

CREATE TRIGGER task_commit_member_owner BEFORE INSERT ON task_commit_members
WHEN NOT (
 (NEW.evidence_kind='DIRECT_FILE' AND EXISTS(SELECT 1 FROM task_commit_receipts p JOIN direct_file_implementation_results r ON r.id=p.result_id JOIN json_each(r.result_json,'$.members') j ON json_extract(j.value,'$.repositoryId')=NEW.repository_id WHERE p.task_id=NEW.task_id AND p.protocol_version=2 AND p.evidence_kind='DIRECT_FILE' AND r.task_id=NEW.task_id AND r.revision_id=NEW.revision_id AND r.id=NEW.result_id AND json_extract(j.value,'$.memberId')=NEW.member_id AND json_extract(j.value,'$.memberVersion')=NEW.member_version AND json_extract(j.value,'$.worktreeId')=NEW.worktree_id AND json_extract(j.value,'$.endSnapshot')=NEW.verified_snapshot)) OR
 (NEW.evidence_kind='CODING' AND EXISTS(SELECT 1 FROM task_commit_receipts p JOIN runs i ON i.id=p.implementation_run_id JOIN runs v ON v.id=p.verification_run_id JOIN run_repository_bindings ib ON ib.run_id=i.id AND ib.repository_id=NEW.repository_id JOIN run_repository_bindings vb ON vb.run_id=v.id AND vb.repository_id=NEW.repository_id WHERE p.task_id=NEW.task_id AND p.protocol_version=2 AND p.evidence_kind='CODING' AND p.revision_id=NEW.revision_id AND p.implementation_run_id=NEW.implementation_run_id AND p.verification_run_id=NEW.verification_run_id AND i.execution_proposal_id=v.execution_proposal_id AND i.task_id=NEW.task_id AND v.task_id=NEW.task_id AND i.status='SUCCEEDED' AND v.status='SUCCEEDED' AND ib.member_id=NEW.member_id AND ib.member_version=NEW.member_version AND ib.worktree_id=NEW.worktree_id AND ib.workspace_id=NEW.workspace_id AND ib.origin_commit=NEW.origin_commit AND ib.start_snapshot=NEW.start_snapshot AND ib.start_head=NEW.expected_head AND ib.end_root_identity=ib.runtime_root_identity AND NEW.root_identity<>'' AND vb.member_id=NEW.member_id AND vb.member_version=NEW.member_version AND vb.worktree_id=NEW.worktree_id AND vb.end_snapshot=NEW.verified_snapshot AND vb.end_root_identity=vb.runtime_root_identity AND vb.observation_state='NO_CHANGE'))
)
BEGIN SELECT RAISE(ABORT,'Task delivery member requires accepted result evidence'); END;
CREATE TRIGGER task_commit_v2_complete BEFORE UPDATE OF completed_at ON task_commit_receipts
WHEN NEW.protocol_version=2 AND NEW.completed_at IS NOT NULL AND (NOT EXISTS(SELECT 1 FROM task_commit_members WHERE task_id=NEW.task_id) OR EXISTS(SELECT 1 FROM task_commit_members WHERE task_id=NEW.task_id AND state NOT IN ('COMPLETED','NO_CHANGE')) OR (SELECT count(*) FROM task_commit_members WHERE task_id=NEW.task_id) <> CASE WHEN NEW.evidence_kind='DIRECT_FILE' THEN (SELECT json_array_length(result_json,'$.members') FROM direct_file_implementation_results WHERE id=NEW.result_id) ELSE (SELECT count(*) FROM run_repository_bindings WHERE run_id=NEW.implementation_run_id) END)
BEGIN SELECT RAISE(ABORT,'All Task delivery members must complete'); END;

CREATE TRIGGER execution_runtime_layout_immutable BEFORE UPDATE OF primary_member_id,approved_workset_hash,runtime_adapter_identity,runtime_protocol_identity ON execution_proposals
WHEN NEW.primary_member_id IS NOT OLD.primary_member_id OR NEW.approved_workset_hash IS NOT OLD.approved_workset_hash OR NEW.runtime_adapter_identity IS NOT OLD.runtime_adapter_identity OR NEW.runtime_protocol_identity IS NOT OLD.runtime_protocol_identity
BEGIN SELECT RAISE(ABORT,'Execution runtime layout is immutable'); END;
CREATE TRIGGER review_primary_member_immutable BEFORE UPDATE OF primary_member_id ON reviews
WHEN NEW.primary_member_id IS NOT OLD.primary_member_id BEGIN SELECT RAISE(ABORT,'Review primary member is immutable'); END;
CREATE TRIGGER run_runtime_workset_immutable BEFORE UPDATE OF runtime_workset_json,runtime_workset_hash ON runs
WHEN OLD.runtime_workset_json IS NOT NULL AND (NEW.runtime_workset_json IS NOT OLD.runtime_workset_json OR NEW.runtime_workset_hash IS NOT OLD.runtime_workset_hash)
BEGIN SELECT RAISE(ABORT,'Run RuntimeWorkset is immutable'); END;
CREATE TRIGGER run_repository_binding_runtime_identity BEFORE UPDATE OF repository_alias,start_head,intended_read,intended_write_scope_json,runtime_root_identity ON run_repository_bindings
WHEN OLD.runtime_root_identity IS NOT NULL AND (NEW.repository_alias IS NOT OLD.repository_alias OR NEW.start_head IS NOT OLD.start_head OR NEW.intended_read IS NOT OLD.intended_read OR NEW.intended_write_scope_json IS NOT OLD.intended_write_scope_json OR NEW.runtime_root_identity IS NOT OLD.runtime_root_identity)
BEGIN SELECT RAISE(ABORT,'Run member runtime identity is immutable'); END;
CREATE TRIGGER run_repository_binding_observation_immutable BEFORE UPDATE OF end_head,end_snapshot,end_root_identity,changed_paths_json,diff_digest,observation_state,observed_at ON run_repository_bindings
WHEN OLD.observation_state IS NOT NULL AND OLD.observation_state<>'PENDING' AND (NEW.end_head IS NOT OLD.end_head OR NEW.end_snapshot IS NOT OLD.end_snapshot OR NEW.end_root_identity IS NOT OLD.end_root_identity OR NEW.changed_paths_json IS NOT OLD.changed_paths_json OR NEW.diff_digest IS NOT OLD.diff_digest OR NEW.observation_state IS NOT OLD.observation_state OR NEW.observed_at IS NOT OLD.observed_at)
BEGIN SELECT RAISE(ABORT,'Run member observation is immutable'); END;
`
