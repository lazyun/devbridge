package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// v108 changes only the fresh confirmation producer. Existing receipts, source
// rows, bindings and journals are copied verbatim; no historical prefix gains
// a receipt, prepare fence, source or completion record during migration.
func migrateFormalConfirmationV108(ctx context.Context, tx *sql.Tx) error {
	original, revised, err := formalConfirmationV108Objects()
	if err != nil {
		return err
	}
	for name, statement := range original {
		var actual string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&actual); err != nil {
			return fmt.Errorf("v108: read %s: %w", name, err)
		}
		if runtimeSelectionSchemaText(actual) != runtimeSelectionSchemaText(statement) {
			return fmt.Errorf("v108: unsupported schema for %s (sha256=%s); database was not changed", name, runtimeSelectionSchemaHash(actual))
		}
	}
	// SQLite reparses all triggers during a table rename. Preserve every trigger,
	// including extension guards, while the two referenced tables are rebuilt.
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name IN ('formal_adoption_business','runtime_selection_sources') AND sql IS NOT NULL) ORDER BY type,name`)
	if err != nil {
		return err
	}
	var saved []runtimeSelectionSchemaObject
	for rows.Next() {
		var object runtimeSelectionSchemaObject
		if err = rows.Scan(&object.kind, &object.name, &object.statement); err != nil {
			rows.Close()
			return err
		}
		saved = append(saved, object)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	quote := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
	for _, object := range saved {
		if object.kind == "trigger" {
			if _, err = tx.ExecContext(ctx, `DROP TRIGGER `+quote(object.name)); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"formal_adoption_business", "runtime_selection_sources"} {
		temporary := name + "_v108"
		statement := strings.Replace(revised[name], "CREATE TABLE "+name+" (", "CREATE TABLE "+temporary+" (", 1)
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("v108: create %s: %w", name, err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO `+quote(temporary)+` SELECT * FROM `+quote(name)); err != nil {
			return fmt.Errorf("v108: preserve %s: %w", name, err)
		}
		if _, err = tx.ExecContext(ctx, `DROP TABLE `+quote(name)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `ALTER TABLE `+quote(temporary)+` RENAME TO `+quote(name)); err != nil {
			return err
		}
	}
	for _, object := range saved {
		statement := object.statement
		if replacement, ok := revised[object.name]; ok {
			statement = replacement
		}
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("v108: restore %s: %w", object.name, err)
		}
	}
	return nil
}

func formalConfirmationV108Objects() (map[string]string, map[string]string, error) {
	original := map[string]string{}
	wanted := map[string]bool{"formal_adoption_business": true, "runtime_selection_sources": true, "formal_adoption_fence_source": true, "formal_adoption_business_source": true, "formal_adoption_confirmation_completion": true, "runtime_selection_adoption_source_identity": true}
	for _, source := range []string{runtimeSelectionV101SQL, runtimeSelectionAdoptionV104SQL} {
		locations := runtimeSelectionDDLHeader.FindAllStringSubmatchIndex(source, -1)
		for i, match := range locations {
			name := source[match[6]:match[7]]
			if source[match[2]:match[3]] != "CREATE" || !wanted[name] {
				continue
			}
			end := len(source)
			if i+1 < len(locations) {
				end = locations[i+1][0]
			}
			original[name] = strings.TrimSpace(source[match[0]:end])
		}
	}
	if len(original) != len(wanted) {
		return nil, nil, fmt.Errorf("v108: incomplete canonical object inventory")
	}
	revised := map[string]string{}
	for name, statement := range original {
		revised[name] = statement
	}
	var buildErr error
	replace := func(name, old, next string) {
		if strings.Count(revised[name], old) != 1 {
			buildErr = fmt.Errorf("v108: unexpected canonical %s guard", name)
			return
		}
		revised[name] = strings.Replace(revised[name], old, next, 1)
	}
	replace("formal_adoption_business", "execution_id TEXT NOT NULL UNIQUE REFERENCES execution_proposals(id)", "execution_id TEXT NOT NULL REFERENCES execution_proposals(id)")
	replace("formal_adoption_business", "admission_request_id TEXT NOT NULL UNIQUE", "admission_request_id TEXT NOT NULL")
	replace("runtime_selection_sources", "CHECK((entry='RUNTIME_AUTHORIZED_REVIEW_APPROVAL_OR_CONFIRMATION' AND approval_request_id IS request_id AND adoption_request_id IS NULL) OR (entry='DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION' AND adoption_request_id IS request_id AND approval_request_id IS NULL))", "CHECK((entry='RUNTIME_AUTHORIZED_REVIEW_APPROVAL_OR_CONFIRMATION' AND approval_request_id IS request_id AND adoption_request_id IS NULL) OR (adoption_request_id IS request_id AND approval_request_id IS NULL))")
	replace("formal_adoption_fence_source", "AND NOT EXISTS(SELECT 1 FROM execution_proposals e WHERE e.id=t.current_execution_id AND e.task_definition_revision_id=r.id)", `AND (NOT EXISTS(SELECT 1 FROM execution_proposals e WHERE e.id=t.current_execution_id AND e.task_definition_revision_id=r.id)
 OR EXISTS(SELECT 1 FROM formal_execution_t1 prior
 JOIN formal_execution_t2 admitted ON admitted.ai_session_id=prior.ai_session_id AND admitted.request_id=prior.request_id
 JOIN formal_execution_receipts receipt ON receipt.ai_session_id=prior.ai_session_id AND receipt.request_id=prior.request_id
 JOIN runtime_selection_sources approved ON approved.ai_session_id=receipt.ai_session_id AND approved.request_id=receipt.request_id AND approved.approval_request_id=prior.request_id AND approved.receipt_identity=receipt.receipt_identity AND approved.receipt_sha256=receipt.receipt_sha256 AND approved.source_id=prior.source_origin_id AND approved.source_id=json_extract(prior.t1_json,'$.identity.sourceOriginId')
 JOIN execution_proposals e ON e.id=admitted.execution_id AND e.task_id=t.id AND e.project_id=t.project_id AND e.task_definition_revision_id=r.id
 JOIN execution_proposal_admissions a ON a.proposal_id=e.id AND a.admission_request_id=admitted.admission_request_id AND a.payload_sha256=admitted.admission_payload_hash
 JOIN runtime_scope_plans p ON p.plan_id=admitted.plan_id AND p.content_hash=admitted.plan_hash AND p.owner_id=e.id
 JOIN runtime_preparation_authorities auth ON auth.plan_id=receipt.preparation_authority_id AND auth.plan_id=p.plan_id AND auth.owner_id=e.id AND auth.request_id=a.admission_request_id AND auth.payload_hash=a.payload_sha256
 WHERE prior.project_id=t.project_id AND prior.task_id=t.id AND prior.revision_id=r.id
 AND e.id=t.current_execution_id AND e.status='ADMITTED' AND e.run_id IS NULL AND e.verification_run_id IS NULL
 AND auth.disposition='CURRENT' AND a.runtime_scope_plan_id=p.plan_id AND a.runtime_preparation_authority_id=p.plan_id
 AND json_extract(i.intent_json,'$.content.execution.expectedTarget.executionId')=e.id
 AND json_extract(receipt.receipt_json,'$.archive.commit.action')='APPROVE_AND_START'
 AND json_extract(receipt.receipt_json,'$.role')='CURRENT_FORMAL_COMMIT_RECEIPT'
 AND json_extract(receipt.receipt_json,'$.businessOutcome')='COMMITTED'
 AND json_extract(receipt.receipt_json,'$.consumptionDisposition')='CONSUMED'
 AND NOT EXISTS(SELECT 1 FROM runs run WHERE run.execution_proposal_id=e.id)))`)
	replace("formal_adoption_fence_source", "formal adoption prepare requires exact new selection owner", "formal adoption prepare requires exact current selection owner and prior admission")
	replace("formal_adoption_business_source", " JOIN formal_definition_approval_business prior ON prior.revision_id=NEW.revision_id AND json_extract(prior.snapshot_json,'$.fence.taskId')=t.id\n JOIN formal_definition_approval_receipts r ON r.ai_session_id=prior.ai_session_id AND r.request_id=prior.request_id\n", "")
	replace("formal_adoption_business_source", "AND json_extract(NEW.business_json,'$.priorApproval')=r.receipt_json AND json_extract(NEW.business_json,'$.priorApprovalRole')='PRIOR_APPROVAL_RECEIPT'", formalAdoptionPriorSourceV108SQL)
	replace("formal_adoption_business_source", "AND json_extract(s.value,'$.requestId')=NEW.admission_request_id", "AND (json_extract(NEW.business_json,'$.priorRuntimeApproval') IS NOT NULL OR json_extract(s.value,'$.requestId')=NEW.admission_request_id)")
	replace("formal_adoption_business_source", "AND e.status='ADMITTED' AND e.run_id IS NULL AND e.verification_run_id IS NULL", "AND e.status='ADMITTED' AND e.run_id IS NULL AND e.verification_run_id IS NULL AND NOT EXISTS(SELECT 1 FROM runs run WHERE run.execution_proposal_id=e.id)")
	replace("runtime_selection_adoption_source_identity", "AND NEW.entry='DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION'", `AND ((json_extract(r.receipt_json,'$.archive.priorRuntimeApproval') IS NULL AND NEW.entry='DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION' AND json_extract(NEW.source_json,'$.normalizedIssuanceClass')='DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION')
 OR (json_extract(r.receipt_json,'$.archive.priorRuntimeApproval') IS NOT NULL AND NEW.entry='RUNTIME_AUTHORIZED_REVIEW_APPROVAL_OR_CONFIRMATION' AND json_extract(NEW.source_json,'$.normalizedIssuanceClass')='RUNTIME_AUTHORIZED_EXECUTION_CONFIRMATION'))`)
	replace("runtime_selection_adoption_source_identity", "json_extract(r.receipt_json,'$.archive.priorApproval.receiptIdentity')=json_extract(NEW.source_json,'$.priorApprovalReceiptIdentity')", "COALESCE(json_extract(r.receipt_json,'$.archive.priorRuntimeApproval.receiptIdentity'),json_extract(r.receipt_json,'$.archive.priorApproval.receiptIdentity'))=json_extract(NEW.source_json,'$.priorApprovalReceiptIdentity')")
	revised["formal_adoption_confirmation_completion"] = formalAdoptionCompletionV108SQL
	return original, revised, buildErr
}

const formalAdoptionPriorSourceV108SQL = `AND json_extract(NEW.business_json,'$.atomicConfirmation')=1
 AND json_extract(NEW.business_json,'$.priorApprovalRole')='PRIOR_APPROVAL_RECEIPT'
 AND ((json_extract(NEW.business_json,'$.priorRuntimeApproval') IS NULL AND EXISTS(
 SELECT 1 FROM formal_definition_approval_business prior JOIN formal_definition_approval_receipts receipt ON receipt.ai_session_id=prior.ai_session_id AND receipt.request_id=prior.request_id
 WHERE prior.revision_id=NEW.revision_id AND json_extract(prior.snapshot_json,'$.fence.taskId')=t.id AND json_extract(NEW.business_json,'$.priorApproval')=receipt.receipt_json))
 OR (json_extract(NEW.business_json,'$.priorRuntimeApproval') IS NOT NULL AND EXISTS(
 SELECT 1 FROM formal_execution_t1 prior
 JOIN formal_execution_t2 admitted ON admitted.ai_session_id=prior.ai_session_id AND admitted.request_id=prior.request_id
 JOIN formal_execution_receipts receipt ON receipt.ai_session_id=prior.ai_session_id AND receipt.request_id=prior.request_id
 JOIN runtime_selection_sources approved ON approved.ai_session_id=receipt.ai_session_id AND approved.request_id=receipt.request_id AND approved.approval_request_id=prior.request_id AND approved.receipt_identity=receipt.receipt_identity AND approved.receipt_sha256=receipt.receipt_sha256 AND approved.source_id=prior.source_origin_id AND approved.source_id=json_extract(prior.t1_json,'$.identity.sourceOriginId')
 WHERE prior.project_id=NEW.project_id AND prior.task_id=NEW.task_id AND prior.revision_id=NEW.revision_id
 AND admitted.execution_id=NEW.execution_id AND admitted.admission_request_id=NEW.admission_request_id AND admitted.admission_payload_hash=a.payload_sha256
 AND admitted.plan_id=p.plan_id AND admitted.plan_hash=p.content_hash AND receipt.preparation_authority_id=auth.plan_id
 AND admitted.plan_json=json_extract(NEW.business_json,'$.plan')
 AND json_extract(NEW.business_json,'$.priorRuntimeApproval')=receipt.receipt_json
 AND json_extract(NEW.business_json,'$.admissionPayloadHash')=admitted.admission_payload_hash
 AND json_extract(NEW.business_json,'$.expectedTarget.executionId')=NEW.execution_id
 AND json_extract(NEW.business_json,'$.platformReference')=json_extract(prior.archive_json,'$.platformReference')
 AND json_extract(NEW.business_json,'$.runtime')=json_extract(prior.archive_json,'$.runtime')
 AND json_extract(NEW.business_json,'$.backend')=json_extract(prior.archive_json,'$.backend')
 AND json_extract(receipt.receipt_json,'$.archive.commit.action')='APPROVE_AND_START'
 AND json_extract(receipt.receipt_json,'$.role')='CURRENT_FORMAL_COMMIT_RECEIPT'
 AND json_extract(receipt.receipt_json,'$.businessOutcome')='COMMITTED'
 AND json_extract(receipt.receipt_json,'$.consumptionDisposition')='CONSUMED'
 AND (receipt.ai_session_id<>NEW.ai_session_id OR receipt.request_id<>NEW.request_id))))`

const formalAdoptionCompletionV108SQL = `CREATE TRIGGER formal_adoption_confirmation_completion BEFORE UPDATE OF intent_json ON confirmation_intents
WHEN EXISTS(SELECT 1 FROM formal_adoption_fences f WHERE f.intent_id=NEW.intent_id)
 AND EXISTS(SELECT 1 FROM json_each(NEW.intent_json,'$.steps') step WHERE json_extract(step.value,'$.kind')='START_IMPLEMENTATION' AND json_extract(step.value,'$.state')='COMPLETED'
 AND NOT EXISTS(SELECT 1 FROM formal_adoption_business b JOIN formal_adoption_receipts r ON r.ai_session_id=b.ai_session_id AND r.request_id=b.request_id AND r.business_sha256=b.business_sha256
 JOIN runtime_selection_sources s ON s.ai_session_id=b.ai_session_id AND s.request_id=b.request_id AND s.adoption_request_id=b.request_id AND s.receipt_identity=r.receipt_identity AND s.receipt_sha256=r.receipt_sha256
 WHERE b.ai_session_id=NEW.ai_session_id AND b.intent_id=NEW.intent_id AND b.request_id=json_extract(NEW.intent_json,'$.commit.requestId')
 AND json_extract(b.business_json,'$.atomicConfirmation')=1
 AND json_array_length(NEW.intent_json,'$.steps')=1
 AND json_extract(step.value,'$.effect')='PREPARATION_READY'
 AND COALESCE(json_extract(step.value,'$.runId'),'')='' AND COALESCE(json_extract(step.value,'$.workflowId'),'')=''
 AND EXISTS(SELECT 1 FROM json_each(OLD.intent_json,'$.steps') oldstep WHERE json_extract(oldstep.value,'$.kind')='START_IMPLEMENTATION' AND json_extract(oldstep.value,'$.requestId')=json_extract(step.value,'$.requestId'))
 AND b.task_id=json_extract(step.value,'$.taskId') AND b.revision_id=json_extract(step.value,'$.revisionId') AND b.execution_id=json_extract(step.value,'$.executionId')))
BEGIN SELECT RAISE(ABORT,'formal adoption completion requires exact atomic archive and source without Run'); END;`
