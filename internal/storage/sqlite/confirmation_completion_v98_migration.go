package sqlite

var confirmationCompletionV98SQL = `
CREATE TRIGGER confirmation_json_identity_insert BEFORE INSERT ON confirmation_intents
WHEN ` + confirmationAmbiguousJSON("NEW.intent_json") + `
BEGIN SELECT RAISE(ABORT,'confirmation JSON identity must be unambiguous'); END;
CREATE TRIGGER confirmation_json_identity_update BEFORE UPDATE OF intent_json ON confirmation_intents
WHEN ` + confirmationAmbiguousJSON("NEW.intent_json") + ` OR ` + confirmationAmbiguousJSON("OLD.intent_json") + `
BEGIN SELECT RAISE(ABORT,'confirmation JSON identity must be unambiguous'); END;
CREATE TRIGGER confirmation_step_identity_immutable BEFORE UPDATE OF intent_json ON confirmation_intents
WHEN json_type(OLD.intent_json,'$.commit')='object' AND (
 json_type(NEW.intent_json,'$.steps') IS NOT 'array'
 OR json_array_length(NEW.intent_json,'$.steps') IS NOT json_array_length(OLD.intent_json,'$.steps')
 OR EXISTS(SELECT 1 FROM json_each(OLD.intent_json,'$.steps') old_step
   LEFT JOIN json_each(NEW.intent_json,'$.steps') new_step ON new_step.key=old_step.key
   WHERE new_step.type IS NOT 'object'
   OR json_extract(new_step.value,'$.kind') IS NOT json_extract(old_step.value,'$.kind')
   OR json_extract(new_step.value,'$.requestId') IS NOT json_extract(old_step.value,'$.requestId'))
)
BEGIN SELECT RAISE(ABORT,'consumed confirmation step identities are immutable'); END;
CREATE TRIGGER confirmation_completed_immutable BEFORE UPDATE ON confirmation_intents
WHEN json_extract(OLD.intent_json,'$.state')='COMPLETED'
 AND (NEW.intent_json IS NOT OLD.intent_json OR NEW.worker_epoch IS NOT OLD.worker_epoch
 OR NEW.intent_id IS NOT OLD.intent_id OR NEW.ai_session_id IS NOT OLD.ai_session_id
 OR NEW.project_id IS NOT OLD.project_id OR NEW.prepare_request_id IS NOT OLD.prepare_request_id
 OR NEW.prepare_hash IS NOT OLD.prepare_hash OR NEW.submitted_sha256 IS NOT OLD.submitted_sha256)
BEGIN SELECT RAISE(ABORT,'completed confirmation is immutable'); END;
CREATE TRIGGER confirmation_completed_insert_guard BEFORE INSERT ON confirmation_intents
WHEN json_extract(NEW.intent_json,'$.state')='COMPLETED'
BEGIN SELECT RAISE(ABORT,'confirmation must complete through an existing consumed intent'); END;
CREATE TRIGGER confirmation_completed_update_guard BEFORE UPDATE OF intent_json ON confirmation_intents
WHEN json_extract(NEW.intent_json,'$.state')='COMPLETED' AND (
 json_type(NEW.intent_json,'$.commit') IS NOT 'object'
 OR json_type(NEW.intent_json,'$.steps') IS NOT 'array'
 OR COALESCE(json_array_length(NEW.intent_json,'$.steps'),0)=0
 OR EXISTS(SELECT 1 FROM json_each(NEW.intent_json,'$.steps') step
   WHERE step.type IS NOT 'object' OR json_extract(step.value,'$.state') IS NOT 'COMPLETED'
   OR json_type(step.value,'$.kind') IS NOT 'text' OR length(json_extract(step.value,'$.kind'))=0
   OR json_type(step.value,'$.requestId') IS NOT 'text' OR length(json_extract(step.value,'$.requestId'))=0)
 OR EXISTS(SELECT 1 FROM json_each(NEW.intent_json,'$.steps') step
   GROUP BY json_extract(step.value,'$.kind'),json_extract(step.value,'$.requestId') HAVING COUNT(*)>1)
 OR json_extract(NEW.intent_json,'$.commit.aiSessionId') IS NOT NEW.ai_session_id
 OR json_extract(NEW.intent_json,'$.commit.intentId') IS NOT NEW.intent_id
 OR json_extract(NEW.intent_json,'$.commit.version') IS NOT json_extract(NEW.intent_json,'$.version')
 OR json_extract(NEW.intent_json,'$.commit.purpose') IS NOT json_extract(NEW.intent_json,'$.purpose')
 OR json_extract(NEW.intent_json,'$.commit.action') IS NOT json_extract(NEW.intent_json,'$.action')
 OR json_extract(NEW.intent_json,'$.commit.contentHash') IS NOT json_extract(NEW.intent_json,'$.contentHash')
 OR NOT EXISTS(SELECT 1 FROM confirmation_commit_requests r WHERE r.ai_session_id=NEW.ai_session_id
   AND r.intent_id=NEW.intent_id AND r.request_id=json_extract(NEW.intent_json,'$.commit.requestId')
   AND (r.outcome='CONSUMED' OR (r.outcome IS NULL AND r.request_json IS NULL AND r.submitted_sha256 IS NULL AND r.failure_json IS NULL
     AND length(r.payload_sha256)=64 AND r.payload_sha256 NOT GLOB '*[^0-9a-f]*')))
)
BEGIN SELECT RAISE(ABORT,'completed confirmation requires all original steps and consumed request'); END;
`
