package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// runtimeAuthorityState distinguishes current ownership from historical backend
// uncertainty. Busy fences new progression; RecoveryRequired tells projections
// whether the owner needs recovery rather than an ordinary wait for live work.
type runtimeAuthorityState struct {
	Busy             bool
	RecoveryRequired bool
}

func progressionActionNeedsIdle(action string) bool {
	switch action {
	case "CREATE_TASK", "START_TASK_REVIEW", "START_EXECUTION", "START_VERIFICATION", "RESOLVE_REVIEW_APPROVAL", "RESOLVE_TASK_ACCEPTANCE", "READ_TASK_EVIDENCE", "READ_DIRECT_FILE_RESULT", "RETRY_GIT_FINALIZATION":
		return true
	}
	return false
}

// Suppress decision payloads as well as nextAction: adapters derive actionable
// Approval buttons from these payloads. Immutable Task/Review facts stay visible.
func blockProgressionGuidance(g *wp.Guidance, state, action, message string) {
	g.Set(state, action, message)
	g.Approval = nil
	g.DecisionID = ""
	g.RuntimeApprovalID = ""
}

func (s *WorkflowProgressionStore) applyDirectFileGuidance(ctx context.Context, q definitionQuerier, g *wp.Guidance) error {
	// An enrolled file worker may already have advanced the Task to IMPLEMENTING.
	// Its exact stale authority must not fall through to generic runtime recovery.
	if g.TaskID != "" && g.TaskDefinitionRevisionID != "" {
		var blocked, unresolved bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_task_authorizations f JOIN direct_file_worker_claims w ON w.workflow_id=f.workflow_id WHERE f.task_id=? AND f.task_definition_revision_id=? AND f.project_id=? AND f.feature_area_id=? AND f.protocol_version=2 AND f.state<>'CLOSED' AND (f.state='RECOVERY_REQUIRED' OR f.runtime_epoch<>(SELECT epoch_id FROM runtime_authority WHERE singleton=1))), EXISTS(SELECT 1 FROM direct_file_operation_receipts r JOIN direct_file_task_authorizations f ON f.workflow_id=r.workflow_id WHERE f.task_id=? AND f.task_definition_revision_id=? AND r.state IN ('PENDING','UNKNOWN'))`, g.TaskID, g.TaskDefinitionRevisionID, g.ProjectID, g.FeatureAreaID, g.TaskID, g.TaskDefinitionRevisionID).Scan(&blocked, &unresolved); err != nil {
			return err
		}
		if blocked {
			action := "CANCEL_DIRECT_FILE"
			if unresolved {
				action = "READ_DIRECT_FILE_AUTHORIZATION"
			}
			blockProgressionGuidance(g, string(g.TaskPhase), action, fileWorkerRecoveryReason)
			g.Generation = ""
			encoded, _ := json.Marshal(g)
			g.Generation = workflowHash(string(encoded))
			return nil
		}
	}
	// Refine only the unblocked no-Execution fallback. Retirement, recovery,
	// stale-worker and Git-finalization overlays retain their existing priority.
	// This is read guidance, never an acceptance decision or new file authority.
	if g.TaskPhase == task.PhaseAcceptancePending && g.ExecutionID == "" && g.TaskID != "" && g.TaskDefinitionRevisionID != "" && g.NextAction.Code == "READ_TASK_EVIDENCE" {
		var workflowID string
		if err := q.QueryRowContext(ctx, `SELECT COALESCE((
 SELECT f.workflow_id FROM tasks t
 JOIN task_definition_revisions d ON d.id=t.current_definition_revision_id AND d.task_id=t.id
 JOIN direct_file_task_authorizations f ON f.task_id=t.id AND f.task_definition_revision_id=d.id AND f.project_id=t.project_id AND f.feature_area_id=t.feature_area_id
 JOIN direct_file_implementation_results r ON r.workflow_id=f.workflow_id AND r.task_id=t.id AND r.revision_id=d.id AND r.project_id=t.project_id AND r.ai_session_id=f.ai_session_id
 WHERE t.id=? AND t.project_id=? AND t.feature_area_id=? AND d.id=?
 AND t.status='ACTIVE' AND t.phase='ACCEPTANCE_PENDING' AND COALESCE(t.current_execution_id,'')=''
 AND json_extract(d.payload_json,'$.version')=2
 AND json_extract(d.payload_json,'$.workSpec.implementationMode')='DIRECT_FILE'
 AND json_extract(d.payload_json,'$.verificationSpec.mode')='NONE'
 AND json_extract(d.payload_json,'$.verificationSpec.waiver.source')='USER_WAIVED'
 AND f.protocol_version=2 AND f.state='CLOSED'
 AND json_extract(r.result_json,'$.protocolVersion')=2
 AND COALESCE(json_extract(r.result_json,'$.executionId'),'')=''
 AND json_extract(r.result_json,'$.verificationStatus')='NOT_RUN'
 AND NOT EXISTS(SELECT 1 FROM direct_file_implementation_acceptances a WHERE a.result_id=r.id)
 ),'')`, g.TaskID, g.ProjectID, g.FeatureAreaID, g.TaskDefinitionRevisionID).Scan(&workflowID); err != nil {
			return err
		}
		if workflowID != "" {
			g.DecisionID = ""
			g.Set(string(g.TaskPhase), "READ_DIRECT_FILE_RESULT", fmt.Sprintf("Read file_implementation_result_get for Task %s, revision %s, workflow %s. The recorded DIRECT_FILE result uses an explicit NONE verification waiver; no current Execution is recorded. Result recording is not acceptance; any acceptance requires a separate explicit user decision.", g.TaskID, g.TaskDefinitionRevisionID, workflowID))
			g.Generation = ""
			encoded, _ := json.Marshal(g)
			g.Generation = workflowHash(string(encoded))
		}
	}
	if g.TaskID != "" && g.NextAction.Code == "START_EXECUTION" {
		var mode string
		err := q.QueryRowContext(ctx, `SELECT CASE WHEN json_extract(d.payload_json,'$.workSpec.implementationMode')='DIRECT_FILE' OR EXISTS(SELECT 1 FROM direct_file_task_authorizations f WHERE f.task_id=t.id AND f.task_definition_revision_id=d.id) THEN 'DIRECT_FILE' WHEN json_extract(d.payload_json,'$.workSpec.implementationMode')='CODING_AGENT' THEN 'CODING_AGENT' WHEN COALESCE(json_extract(d.payload_json,'$.workSpec.implementationMode'),'')='' AND EXISTS(SELECT 1 FROM execution_proposals e WHERE e.id=t.current_execution_id AND e.task_id=t.id AND e.project_id=t.project_id AND e.task_definition_revision_id=d.id AND e.status IN ('CONFIRMED','CONSUMED')) THEN 'CODING_AGENT' ELSE 'UNKNOWN' END FROM tasks t JOIN task_definition_revisions d ON d.id=t.current_definition_revision_id AND d.task_id=t.id WHERE t.id=? AND t.project_id=? AND t.feature_area_id=? AND d.id=?`, g.TaskID, g.ProjectID, g.FeatureAreaID, g.TaskDefinitionRevisionID).Scan(&mode)
		if err != nil {
			return err
		}
		if mode == "DIRECT_FILE" {
			g.Set(string(g.TaskPhase), "READ_DIRECT_FILE_AUTHORIZATION", "Read this approved Task's exact file authorization.")
			var version int
			var state string
			if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(d.payload_json,'$.version'),1),COALESCE((SELECT f.state FROM direct_file_task_authorizations f WHERE f.task_definition_revision_id=d.id),'NOT_STARTED') FROM task_definition_revisions d WHERE d.id=?`, g.TaskDefinitionRevisionID).Scan(&version, &state); err != nil {
				return err
			}
			if version == 2 {
				switch state {
				case "NOT_STARTED":
					g.Set(string(g.TaskPhase), "START_DIRECT_FILE", "Confirm START_DIRECT_FILE for this exact approved revision before file access.")
				case "RECOVERY_REQUIRED", "PREPARING":
					g.Set(string(g.TaskPhase), "RECOVER_DIRECT_FILE", "Read the original workflow and confirm recovery of its exact epoch and generation.")
					var enrolled, unresolved bool
					if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_worker_claims w JOIN direct_file_task_authorizations f ON f.workflow_id=w.workflow_id WHERE f.task_id=? AND f.task_definition_revision_id=?), EXISTS(SELECT 1 FROM direct_file_operation_receipts r JOIN direct_file_task_authorizations f ON f.workflow_id=r.workflow_id WHERE f.task_id=? AND f.task_definition_revision_id=? AND r.state IN ('PENDING','UNKNOWN'))`, g.TaskID, g.TaskDefinitionRevisionID, g.TaskID, g.TaskDefinitionRevisionID).Scan(&enrolled, &unresolved); err != nil {
						return err
					}
					if enrolled {
						action := "CANCEL_DIRECT_FILE"
						if unresolved {
							action = "READ_DIRECT_FILE_AUTHORIZATION"
						}
						g.Set(string(g.TaskPhase), action, fileWorkerRecoveryReason)
					}
				case "CLOSED":
					g.Set(string(g.TaskPhase), "READ_DIRECT_FILE_AUTHORIZATION", "This file authorization is closed; historical confirmation replay cannot reopen it.")
				}
			}
		} else if mode != "CODING_AGENT" {
			g.Set(string(g.TaskPhase), "CONFIRM_IMPLEMENTATION_MODE", "The legacy approved definition has no explicit implementation channel; confirm its exact channel without guessing.")
		}
		if mode != "CODING_AGENT" {
			g.Generation = ""
			encoded, _ := json.Marshal(g)
			g.Generation = workflowHash(string(encoded))
			return nil
		}
	}
	var durable bool
	readErr := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_task_authorizations WHERE feature_area_id=? AND state IN ('ACTIVE','PREPARING','RECOVERY_REQUIRED'))`, g.FeatureAreaID).Scan(&durable)
	if readErr != nil {
		return readErr
	}
	if progressionActionNeedsIdle(g.NextAction.Code) && (durable || directFileActiveTx(s.db, g.FeatureAreaID)) {
		blockProgressionGuidance(g, "DIRECT_FILE_CAPABILITY_ACTIVE", "WAIT_FOR_DIRECT_FILE", "An existing file workflow owns this FeatureArea; close that workflow before unrelated progression.")
		g.Generation = ""
		encoded, _ := json.Marshal(g)
		g.Generation = workflowHash(string(encoded))
	}
	return nil
}

// readRuntimeAuthority is the shared durable FeatureArea progression gate. It
// must run inside the caller's write transaction when authorizing a mutation.
// Direct File grants are checked separately: Enable holds their mutex while
// calling this query, so this helper must never acquire that process-local lock.
//
// An orphan disposition is an authority fence, never backend death evidence.
// Check its exact immutable identity and the completed revocation transaction,
// including claim release. The original UNKNOWN boundary/intents remain intact.
// Cancellation may revoke authority in the same epoch; recovery requires an old
// epoch. Neither path depends on the observed epoch remaining current on reopen.
func readRuntimeAuthority(ctx context.Context, q definitionQuerier, areaID string) (state runtimeAuthorityState, err error) {
	resolved, readErr := recoveryPurposeResolvedSourceRunsTx(ctx, q, areaID)
	if readErr != nil {
		return state, readErr
	}
	independent, readErr := recoveryIndependentResolvedSourceRunsTx(ctx, q, areaID)
	if readErr != nil {
		return state, readErr
	}
	var sources []string
	if readErr = json.Unmarshal([]byte(resolved), &sources); readErr != nil {
		return state, readErr
	}
	combined, _ := json.Marshal(append(sources, independent...))
	resolved = string(combined)
	err = q.QueryRowContext(ctx, `WITH purpose_resolved AS (
 SELECT value AS id FROM json_each(?)
), area_runs AS (
 SELECT r.* FROM runs r WHERE r.feature_area_id=?
), revoked AS (
 SELECT r.id FROM area_runs r
 JOIN agent_orphan_dispositions o ON o.run_id=r.id
 JOIN agent_operation_boundaries b ON b.run_id=r.id AND b.agent_conversation_id=o.conversation_id
 JOIN agent_conversations a ON a.id=o.conversation_id AND a.feature_area_id=r.feature_area_id
 WHERE o.task_id=r.task_id AND o.feature_area_id=r.feature_area_id
 AND o.owner_type=r.operation_kind AND o.owner_id=r.operation_id
 AND o.original_runtime_epoch IS r.runtime_epoch
 AND o.thread_id IS r.thread_id AND o.turn_id IS r.turn_id AND o.workspace_id IS r.workspace_id
 AND o.disposition='UNKNOWN_ORPHANED' AND b.advance_state='UNKNOWN'
 AND a.ended_at IS NOT NULL AND r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')
 AND (o.source='CANCELLATION' OR o.original_runtime_epoch IS NOT o.observed_runtime_epoch)
 AND EXISTS(SELECT 1 FROM runtime_epochs e WHERE e.id=o.observed_runtime_epoch)
 AND (r.operation_kind<>'EXECUTION' OR EXISTS(SELECT 1 FROM execution_proposals e WHERE e.id=r.operation_id AND e.task_id=r.task_id AND e.outcome IN ('SUCCEEDED','FAILED','CANCELED')))
 AND (r.workspace_id IS NULL OR EXISTS(SELECT 1 FROM workspaces w WHERE w.id=r.workspace_id AND w.runtime_quarantined=1 AND NOT EXISTS(SELECT 1 FROM feature_areas f WHERE f.current_workspace_id=w.id)))
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=r.id)
), reconciled AS (
 SELECT r.id FROM area_runs r JOIN recovery_status_records x ON x.source_run_id=r.id
 JOIN runs child ON child.id=x.recovery_run_id
 JOIN agent_operation_boundaries b ON b.run_id=child.id
 WHERE NOT EXISTS(SELECT 1 FROM recovery_child_admissions independent WHERE independent.recovery_id=x.id) AND x.state='RESOLVED' AND json_extract(x.record_json,'$.resolution') IS NOT NULL
 AND json_extract(x.record_json,'$.assessment.consistency')='CONSISTENT'
 AND json_extract(x.record_json,'$.assessment.engineeringDisposition') IN ('RECOVERED_COMPLETE','RECOVERED_PARTIAL','REVISION_REQUIRED')
 AND json_extract(x.spec_json,'$.sourceTurnId')=r.turn_id
 AND child.thread_id=r.thread_id AND child.operation_id=r.operation_id AND child.operation_kind=r.operation_kind
 AND child.status IN ('SUCCEEDED','FAILED') AND b.quiesced=1 AND b.advance_state='KNOWN_ADVANCE'
 AND b.exit_boundary_turn_id=child.turn_id
 AND r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED')
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=r.id)
 UNION ALL
 SELECT r.id FROM area_runs r JOIN recovery_status_records x ON x.source_run_id=r.id
 JOIN runs child ON child.id=x.recovery_run_id JOIN agent_operation_boundaries b ON b.run_id=child.id
 JOIN execution_cancellations c ON c.run_id=child.id AND c.owner_id=r.operation_id AND c.owner_type=r.operation_kind
 WHERE NOT EXISTS(SELECT 1 FROM recovery_child_admissions independent WHERE independent.recovery_id=x.id) AND x.state='FAILED' AND json_extract(x.record_json,'$.failureReason')='RECOVERY_CANCELED'
 AND json_extract(x.record_json,'$.resolution') IS NOT NULL AND r.status='CANCELED' AND child.status='CANCELED'
 AND child.thread_id=r.thread_id AND json_extract(x.spec_json,'$.sourceTurnId')=r.turn_id
 AND c.state='CANCELED' AND c.terminal_observed=1 AND c.backend_disposition='TERMINATED'
 AND c.thread_id=child.thread_id AND c.turn_id=child.turn_id
 AND b.quiesced=1 AND b.advance_state='KNOWN_ADVANCE' AND b.exit_boundary_turn_id=child.turn_id
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims claim WHERE claim.run_id=r.id)
), upper_reauthorized AS (`+upperReauthorizedRunSQL+`
), unsafe AS (
 SELECT r.id,r.runtime_epoch,r.status,
 EXISTS(SELECT 1 FROM agent_operation_recovery_states s WHERE s.run_id=r.id AND s.classification IN ('UNKNOWN','CONFLICT') AND NOT EXISTS(SELECT 1 FROM upper_reauthorized u WHERE u.id=r.id)) AS classified,
 EXISTS(SELECT 1 FROM agent_finalization_intents f WHERE f.run_id=r.id AND (f.advance_state='UNKNOWN' OR f.quiesced=0)) AS finalizing
 FROM area_runs r WHERE NOT EXISTS(SELECT 1 FROM revoked o WHERE o.id=r.id) AND NOT EXISTS(SELECT 1 FROM reconciled x WHERE x.id=r.id)
 AND NOT EXISTS(SELECT 1 FROM purpose_resolved p WHERE p.id=r.id)
 AND (EXISTS(SELECT 1 FROM agent_operation_recovery_states s WHERE s.run_id=r.id AND s.classification IN ('UNKNOWN','CONFLICT') AND NOT EXISTS(SELECT 1 FROM upper_reauthorized u WHERE u.id=r.id))
 OR EXISTS(SELECT 1 FROM agent_finalization_intents f WHERE f.run_id=r.id AND (f.advance_state='UNKNOWN' OR f.quiesced=0))
 OR EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id AND (b.advance_state='UNKNOWN' OR b.quiesced=0)))
), claims AS (
 SELECT c.*,r.status FROM feature_area_agent_claims c LEFT JOIN runs r ON r.id=c.run_id WHERE c.feature_area_id=?
), cancellations AS (
 SELECT c.state FROM execution_cancellations c JOIN tasks t ON t.id=c.task_id
 WHERE t.feature_area_id=? AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')
), unsafe_context AS (
 SELECT a.id FROM agent_conversations a WHERE a.feature_area_id=? AND a.ended_at IS NULL AND a.continuation_safe=0
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b JOIN purpose_resolved p ON p.id=b.run_id
 WHERE b.agent_conversation_id=a.id AND b.rowid=(SELECT MAX(latest.rowid) FROM agent_operation_boundaries latest WHERE latest.agent_conversation_id=a.id))
 AND NOT EXISTS(SELECT 1 FROM claims c WHERE c.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND c.status IN ('CREATED','PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING'))
)
SELECT EXISTS(SELECT 1 FROM claims)
 OR EXISTS(SELECT 1 FROM area_runs WHERE status IN ('CREATED','PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING'))
 OR EXISTS(SELECT 1 FROM cancellations) OR EXISTS(SELECT 1 FROM unsafe) OR EXISTS(SELECT 1 FROM unsafe_context),
 EXISTS(SELECT 1 FROM unsafe u WHERE u.classified OR u.finalizing
 OR NOT EXISTS(SELECT 1 FROM claims c WHERE c.run_id=u.id AND c.runtime_epoch=u.runtime_epoch
 AND c.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND c.status IN ('CREATED','PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING')))
 OR EXISTS(SELECT 1 FROM claims c WHERE c.runtime_epoch IS NOT (SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 OR c.status IS NULL OR c.status IN ('FAILED','INFRASTRUCTURE_FAILED','CANCELED'))
 OR EXISTS(SELECT 1 FROM cancellations WHERE state='RECOVERY_REQUIRED') OR EXISTS(SELECT 1 FROM unsafe_context)`, resolved, areaID, areaID, areaID, areaID).Scan(&state.Busy, &state.RecoveryRequired)
	return
}

// Shared exact reauthorization proof. Used by runtime safety and read projections;
// independent UNKNOWN/CONFLICT evidence is never suppressed.
const upperReauthorizedRunSQL = ` SELECT r.id FROM runs r
 JOIN reviews v ON v.id=r.review_id AND v.linked_run_id=r.id AND v.executor_kind='UPPER_LLM' AND v.task_id=r.task_id AND v.project_id=r.project_id AND v.feature_area_id=r.feature_area_id
 JOIN review_grants g ON g.review_id=v.id AND g.run_id=r.id AND g.feature_area_id=r.feature_area_id AND g.project_id=r.project_id
 JOIN agent_operation_recovery_states s ON s.run_id=r.id
 JOIN agent_operation_recoveries x ON x.run_id=r.id AND x.operation_kind='REVIEW' AND x.operation_id=v.id AND x.feature_area_id=r.feature_area_id
 WHERE r.operation_kind='REVIEW' AND r.operation_id=v.id AND r.purpose='SCOPE_REVIEW'
 AND s.classification='UNKNOWN' AND s.detail='Upper LLM review requires explicit same-Review reauthorization after restart.'
 AND s.classified_epoch=x.authorized_epoch AND x.authorized_epoch=r.runtime_epoch AND x.authorized_epoch=g.runtime_epoch
 AND x.grant_generation=g.generation AND x.previous_epoch<>x.authorized_epoch
 AND x.old_conversation_id IS NULL AND x.new_conversation_id IS NULL
 AND x.request_payload_json IS NOT NULL AND x.result_json IS NOT NULL
 AND r.thread_id IS NULL AND r.turn_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents f WHERE f.run_id=r.id)
 AND ((g.state IN ('PREPARING','ACTIVE','AWAITING_REPORT','FINALIZING') AND r.status IN ('CREATED','PREPARING','RUNNING','COLLECTING')
 AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=r.id AND c.operation_kind='REVIEW' AND c.operation_id=v.id AND c.feature_area_id=r.feature_area_id AND c.runtime_epoch=r.runtime_epoch))
 OR (g.state='CLOSED' AND r.status='SUCCEEDED' AND r.finished_at IS NOT NULL
 AND EXISTS(SELECT 1 FROM review_reports p JOIN review_result_receipts receipt ON receipt.review_id=p.review_id AND receipt.ai_session_id=p.ai_session_id AND receipt.payload_sha256=p.payload_sha256
 WHERE p.review_id=v.id AND p.run_id=r.id AND p.ai_session_id=v.created_by_ai_session_id AND (g.protocol_version=2 OR p.baseline=g.baseline) AND p.created_at>=x.created_at
 AND receipt.state='COMPLETED' AND json_extract(receipt.payload_json,'$.runId')=r.id AND json_extract(receipt.payload_json,'$.runtimeEpoch')=g.runtime_epoch AND json_extract(receipt.payload_json,'$.grantGeneration')=g.generation)
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=r.id)))
`
