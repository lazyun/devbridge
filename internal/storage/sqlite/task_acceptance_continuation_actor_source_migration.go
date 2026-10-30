package sqlite

// V65 distinguishes the historical predecessor Session evidence from the
// actor that supplied a newly accepted continuation. Existing rows remain
// source-unknown; no local user is backfilled.
const taskAcceptanceContinuationActorSourceMigrationSQL = `
ALTER TABLE task_plans ADD COLUMN content_actor_kind TEXT;
ALTER TABLE task_plans ADD COLUMN content_actor_id TEXT;
ALTER TABLE execution_proposals ADD COLUMN content_actor_kind TEXT;
ALTER TABLE execution_proposals ADD COLUMN content_actor_id TEXT;
ALTER TABLE task_acceptance_continuations ADD COLUMN actor_kind TEXT;
ALTER TABLE task_acceptance_continuations ADD COLUMN actor_id TEXT;
CREATE INDEX task_acceptance_continuation_actor_request ON task_acceptance_continuations(actor_kind,actor_id,request_id) WHERE actor_kind IS NOT NULL;
CREATE TRIGGER task_plan_content_actor_pair BEFORE INSERT ON task_plans
WHEN (NEW.content_actor_kind IS NULL) <> (NEW.content_actor_id IS NULL)
BEGIN SELECT RAISE(ABORT,'Task Plan content actor is incomplete'); END;
CREATE TRIGGER task_plan_content_actor_immutable BEFORE UPDATE OF content_actor_kind,content_actor_id ON task_plans
WHEN OLD.content_actor_kind IS NOT NEW.content_actor_kind OR OLD.content_actor_id IS NOT NEW.content_actor_id
BEGIN SELECT RAISE(ABORT,'Task Plan content actor is immutable'); END;
CREATE TRIGGER execution_proposal_content_actor_pair BEFORE INSERT ON execution_proposals
WHEN (NEW.content_actor_kind IS NULL) <> (NEW.content_actor_id IS NULL)
BEGIN SELECT RAISE(ABORT,'Execution content actor is incomplete'); END;
CREATE TRIGGER execution_proposal_content_actor_immutable BEFORE UPDATE OF content_actor_kind,content_actor_id ON execution_proposals
WHEN OLD.content_actor_kind IS NOT NEW.content_actor_kind OR OLD.content_actor_id IS NOT NEW.content_actor_id
BEGIN SELECT RAISE(ABORT,'Execution content actor is immutable'); END;
CREATE TRIGGER task_acceptance_continuation_actor_pair BEFORE INSERT ON task_acceptance_continuations
WHEN (NEW.actor_kind IS NULL) <> (NEW.actor_id IS NULL)
BEGIN SELECT RAISE(ABORT,'Acceptance continuation actor is incomplete'); END;
`
