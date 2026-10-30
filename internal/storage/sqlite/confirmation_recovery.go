package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
)

// Recovery only observes original canonical receipts. It never retries a
// dispatcher, prepares a workspace, or substitutes the Task's latest owner.
func recoverConfirmationTx(ctx context.Context, q definitionQuerier, out confirmation.Intent, epoch string) (confirmation.Intent, error) {
	if out.State == "COMPLETED" {
		if !completedConfirmation(out) {
			return out, ErrConflict
		}
		for _, step := range out.Steps {
			if step.Kind == "APPROVE" {
				if _, err := recoverApprovalTx(ctx, q, out, &step); err != nil {
					return out, err
				}
			}
		}
		out.CanResume = false
		return out, nil
	}
	if out.Commit == nil {
		return out, nil
	}
	if len(out.Steps) == 0 {
		return out, ErrConflict
	}
	var worker string
	if err := q.QueryRowContext(ctx, `SELECT worker_epoch FROM confirmation_intents WHERE intent_id=?`, out.IntentID).Scan(&worker); err != nil {
		return out, err
	}
	if rejected, proven, err := recoverRejectedContinuationTx(ctx, q, out, worker, epoch); err != nil || proven {
		return rejected, err
	}
	project := out.ProjectID
	area, taskID, revision, executionID := "", "", "", ""
	if b := out.Content.Task; b != nil {
		area = b.FeatureAreaID
	}
	if b := out.Content.Review; b != nil {
		area = b.FeatureAreaID
		taskID = b.TaskID
	}
	if b := out.Content.Execution; b != nil {
		area = b.FeatureAreaID
		taskID = b.TaskID
		revision = b.TaskDefinitionRevisionID
		executionID = b.ExecutionID
	}
	allDone, unresolved := true, false
	for i, step := range out.Steps {
		if step.State == "COMPLETED" && step.Kind == "APPROVE" {
			if _, err := recoverApprovalTx(ctx, q, out, &step); err != nil {
				return out, err
			}
		}
		if step.State == "UNKNOWN" {
			var err error
			switch step.Kind {
			case "BIND_PROJECT":
				err = q.QueryRowContext(ctx, `SELECT r.project_id FROM ai_session_requests r JOIN ai_sessions s ON s.id=r.ai_session_id WHERE r.ai_session_id=? AND r.request_id=? AND r.operation_kind='BIND_PROJECT' AND r.project_id=? AND (s.caller_scope IS NULL OR s.caller_scope=r.caller_scope)`, out.AISessionID, step.RequestID, out.Commit.Selection.ProjectID).Scan(&step.ProjectID)
			case "SELECT_BRANCH":
				repositoryID, developmentRef := out.Commit.Selection.RepositoryID, out.Commit.Selection.DevelopmentRef
				if out.Action == "CONFIGURE_DEVELOPMENT_CONTEXT" {
					repositoryID, developmentRef = step.RepositoryID, step.DevelopmentRef
				}
				err = q.QueryRowContext(ctx, `SELECT json_extract(result_json,'$.projectId') FROM development_branch_requests WHERE request_id=? AND json_extract(result_json,'$.projectId')=? AND json_extract(result_json,'$.repositoryId')=? AND json_extract(result_json,'$.developmentRef')=?`, step.RequestID, out.Commit.Selection.ProjectID, repositoryID, developmentRef).Scan(&step.ProjectID)
			case "CREATE_AREA":
				err = q.QueryRowContext(ctx, `SELECT feature_area_id FROM feature_area_creation_receipts WHERE ai_session_id=? AND project_id=? AND request_id=?`, out.AISessionID, project, step.RequestID).Scan(&step.FeatureAreaID)
			case "CREATE_TASK":
				err = q.QueryRowContext(ctx, `SELECT r.task_id FROM task_create_requests r JOIN tasks t ON t.id=r.task_id WHERE r.ai_session_id=? AND r.project_id=? AND r.request_id=? AND t.feature_area_id=?`, out.AISessionID, project, step.RequestID, area).Scan(&step.TaskID)
			case "REQUEST_REVIEW":
				err = q.QueryRowContext(ctx, `SELECT task_id FROM task_supplemental_review_requests WHERE ai_session_id=? AND ((actor_kind='AI_SESSION' AND actor_id=?) OR (actor_kind IS NULL AND actor_id IS NULL)) AND project_id=? AND task_id=? AND request_id=?`, out.AISessionID, out.AISessionID, project, taskID, step.RequestID).Scan(&step.TaskID)
			case "START_REVIEW":
				var status, executor string
				err = q.QueryRowContext(ctx, `SELECT v.id,r.id,r.status,v.executor_kind FROM feature_area_review_start_requests a JOIN reviews v ON v.id=a.review_id JOIN runs r ON r.id=a.run_id WHERE a.feature_area_id=? AND a.request_id=? AND a.outcome='ACCEPTED' AND v.created_by_ai_session_id=? AND v.project_id=? AND v.task_id=?`, area, step.RequestID, out.AISessionID, project, taskID).Scan(&step.ReviewID, &step.RunID, &status, &executor)
				if err == nil && executor == "UPPER_LLM" {
					var active bool
					err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_grants g WHERE g.review_id=? AND g.run_id=? AND (g.state IN ('ACTIVE','AWAITING_REPORT','FINALIZING') OR (g.state='CLOSED' AND EXISTS(SELECT 1 FROM review_result_receipts p JOIN review_reports report ON report.review_id=p.review_id AND report.run_id=g.run_id WHERE p.review_id=g.review_id AND p.state='COMPLETED'))))`, step.ReviewID, step.RunID).Scan(&active)
					if err == nil && !active {
						var retryable bool
						err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_grants g JOIN runs r ON r.id=g.run_id JOIN runtime_authority ra ON ra.epoch_id=g.runtime_epoch WHERE g.review_id=? AND g.run_id=? AND g.protocol_version=2 AND g.state='PREPARING' AND g.generation=1 AND r.runtime_epoch=g.runtime_epoch AND r.status IN ('CREATED','PREPARING','RUNNING') AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.run_id=r.id))`, step.ReviewID, step.RunID).Scan(&retryable)
						if err == nil && retryable {
							step.State = "PENDING"
							step.Effect = "RESERVED"
							step.Failure = nil
							step.Code = ""
							step.Message = ""
						}
						if err == nil {
							err = sql.ErrNoRows
						}
					}
				} else if err == nil {
					receipt, readErr := readRuntimeHandoff(ctx, q, project, step.RunID)
					if readErr != nil {
						return out, readErr
					}
					if !receipt.Accepted() {
						step.Code = receipt.Code
						step.Effect = "RESERVED"
						err = sql.ErrNoRows
					} else {
						step.Effect = "RUNTIME_HANDOFF_ACCEPTED"
					}
				}
			case "CONFIRM_IMPLEMENTATION":
				err = q.QueryRowContext(ctx, `SELECT s.task_id,s.approved_revision_id FROM task_review_skips s JOIN task_definition_revisions d ON d.id=s.approved_revision_id AND d.task_id=s.task_id WHERE s.task_id=? AND s.project_id=? AND s.confirmation_intent_id=? AND s.approved_at IS NOT NULL`, taskID, project, out.IntentID).Scan(&step.TaskID, &step.RevisionID)
				if err == nil {
					step.Effect = "COMMAND_COMPLETED"
				}
			case "APPROVE":
				var armed bool
				armed, err = recoverApprovalTx(ctx, q, out, &step)
				if armed {
					break
				}
				err = q.QueryRowContext(ctx, `SELECT m.initial_revision_id FROM decision_resolution_receipts r JOIN review_approval_materializations m ON m.decision_id=r.decision_id JOIN confirmation_approval_sources c ON c.decision_id=r.decision_id WHERE r.project_id=? AND r.request_id=? AND r.result_id=? AND r.option='APPROVE' AND c.intent_id=?`, project, step.RequestID, taskID, out.IntentID).Scan(&step.RevisionID)
			case "RESOLVE_CONTINUATION":
				err = q.QueryRowContext(ctx, `SELECT e.id,e.task_definition_revision_id FROM decision_resolution_receipts r JOIN execution_proposals e ON e.id=r.result_id WHERE r.project_id=? AND r.request_id=? AND e.task_id=? AND r.option=?`, project, step.RequestID, taskID, out.Action).Scan(&step.ExecutionID, &step.RevisionID)
			case "RECORD_TASK_CHOICE":
				b := out.Content.Execution
				if b == nil || (out.Action != "DEFER_TASK" && out.Action != "CONTINUE_TASK") || b.Action != out.Action || step.RequestID != "confirmation_"+confirmation.ValueHash([]string{out.IntentID, step.Kind})[:32] {
					err = ErrConflict
					break
				}
				payload, _ := json.Marshal(task.HoldInput{ProjectID: out.ProjectID, TaskID: b.TaskID, FeatureAreaID: b.FeatureAreaID, ExecutionID: b.ExecutionID, RevisionID: b.TaskDefinitionRevisionID, Action: out.Action, RequestID: step.RequestID, Note: b.Note})
				err = q.QueryRowContext(ctx, `SELECT r.task_id,r.execution_id,r.revision_id FROM task_hold_receipts r JOIN tasks t ON t.id=r.task_id WHERE r.actor=? AND r.request_id=? AND r.task_id=? AND r.execution_id=? AND r.revision_id=? AND r.action=? AND r.payload_sha256=? AND t.project_id=? AND t.feature_area_id=?`, "session:"+out.AISessionID, step.RequestID, b.TaskID, b.ExecutionID, b.TaskDefinitionRevisionID, out.Action, workflowHash(string(payload)), out.ProjectID, b.FeatureAreaID).Scan(&step.TaskID, &step.ExecutionID, &step.RevisionID)
				if err == nil {
					step.Effect = "COMMAND_COMPLETED"
				}
			case "RESOLVE_ACCEPTANCE_DECISION":
				err = recoverConfirmationAcceptanceTx(ctx, q, out, &step)
			case "START_DIRECT_FILE", "RECOVER_DIRECT_FILE", "CANCEL_DIRECT_FILE":
				err = q.QueryRowContext(ctx, `SELECT r.workflow_id,g.task_definition_revision_id FROM direct_file_start_receipts r JOIN direct_file_task_authorizations g ON g.workflow_id=r.workflow_id WHERE r.ai_session_id=? AND r.request_id=? AND r.action=? AND g.task_id=? AND g.task_definition_revision_id=? AND g.project_id=? AND json_extract(r.receipt_json,'$.state') IN ('ACTIVE','CLOSED')`, out.AISessionID, step.RequestID, step.Kind, taskID, revision, project).Scan(&step.WorkflowID, &step.RevisionID)
			case "MERGE_FEATURE_AREA":
				var state string
				b := out.Content.FeatureArea
				if b == nil || b.DeliveryPlan == nil {
					err = ErrConflict
					break
				}
				err = q.QueryRowContext(ctx, `SELECT r.feature_area_id,CASE WHEN r.released_at IS NOT NULL THEN 'RELEASED' WHEN r.completed_at IS NOT NULL THEN 'COMPLETED' ELSE 'PENDING' END FROM feature_area_commit_receipts r JOIN feature_areas a ON a.id=r.feature_area_id WHERE r.request_id=? AND a.project_id=? AND r.feature_area_id=? AND r.protocol_version=2 AND r.plan_sha256=?`, step.RequestID, project, b.FeatureAreaID, b.DeliveryPlan.SHA256).Scan(&step.FeatureAreaID, &state)
				if err == nil && state == "RELEASED" {
					step.State, step.Effect, step.Code, step.Message = "FAILED", "PLAN_RELEASED", "MERGE_PLAN_RELEASED", "旧合入计划已解除，请重新确认合入"
					step.Failure = nil
					out.State = "PARTIAL"
					err = sql.ErrNoRows
				}
				if err == nil && state != "COMPLETED" {
					step.State = "PENDING"
					step.Effect = "RESERVED"
					step.Failure = nil
					step.Code = ""
					step.Message = ""
					err = sql.ErrNoRows
				}
			case "START_IMPLEMENTATION":
				direct := false
				for _, v := range out.Content.Variants {
					if v.ID == out.Commit.VariantID && v.ImplementationMode == "DIRECT_FILE" {
						direct = true
					}
				}
				if direct {
					err = q.QueryRowContext(ctx, `SELECT workflow_id,task_definition_revision_id FROM direct_file_task_authorizations WHERE task_id=? AND task_definition_revision_id=? AND ai_session_id=? AND project_id=? AND state='ACTIVE'`, taskID, revision, out.AISessionID, project).Scan(&step.WorkflowID, &step.RevisionID)
				} else {
					var status string
					if out.Action == "START_APPROVED" && out.Content.Execution != nil && out.Content.Execution.ExpectedTarget != nil && out.Content.Execution.ExpectedTarget.ExecutionID != "" {
						owner, readErr := scanTask(q.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, taskID, project))
						if readErr != nil {
							return out, readErr
						}
						if owner.CurrentExecutionID != out.Content.Execution.ExpectedTarget.ExecutionID && owner.CurrentDefinitionRevisionID == out.Content.Execution.ExpectedTarget.TaskDefinitionRevisionID {
							candidate, _, found, readErr := existingTaskExecutionReservationCommand(ctx, q, owner, *out.Content.Execution.ExpectedTarget, executionStartActor{Kind: "AI_SESSION", ID: out.AISessionID, Action: "START_EXECUTION", RequestID: step.RequestID})
							if readErr != nil {
								return out, readErr
							}
							if found {
								executionID = candidate.ID
							}
						}
					}
					if executionID == "" {
						var approval string
						if e := q.QueryRowContext(ctx, `SELECT json_extract(payload_json,'$.reviewApprovalId') FROM task_definition_revisions WHERE id=? AND task_id=?`, revision, taskID).Scan(&approval); e != nil {
							return out, e
						}
						if approval == "" {
							approved, readErr := unreviewedDefinitionApprovedTx(ctx, q, task.Task{ID: taskID, ProjectID: project, CurrentDefinitionRevisionID: revision})
							if readErr != nil {
								return out, readErr
							}
							if !approved {
								return out, ErrConflict
							}
						}
						executionID = approvedExecutionID(task.Task{ID: taskID, ReviewApprovalID: approval})
					}
					expectedPhase := task.PhaseImplementationReady
					if out.Action == "START_APPROVED" && out.Content.Execution != nil && out.Content.Execution.ExpectedTarget != nil {
						expectedPhase = out.Content.Execution.ExpectedTarget.Phase
					}
					recordedRunID := step.RunID
					err = q.QueryRowContext(ctx, `SELECT e.id,r.id,r.status FROM execution_proposals e JOIN runs r ON r.id=CASE WHEN ?='VERIFICATION_READY' THEN e.verification_run_id ELSE e.run_id END WHERE e.task_id=? AND e.project_id=? AND e.task_definition_revision_id=? AND e.id=? AND r.task_id=e.task_id AND r.execution_proposal_id=e.id AND (?='' OR r.id=?)`, string(expectedPhase), taskID, project, revision, executionID, recordedRunID, recordedRunID).Scan(&step.ExecutionID, &step.RunID, &status)
					if err == nil {
						receipt, readErr := readRuntimeHandoff(ctx, q, project, step.RunID)
						if readErr != nil {
							return out, readErr
						}
						if !receipt.Accepted() {
							step.Code = receipt.Code
							step.Effect = "RESERVED"
							err = sql.ErrNoRows
						} else {
							step.Effect = "RUNTIME_HANDOFF_ACCEPTED"
						}
					}
				}
			default:
				err = sql.ErrNoRows
			}
			if err == nil && step.Failure != nil && step.Failure.Code == "PROJECT_PREPARATION_INCOMPLETE" {
				// The binding receipt proves only binding, not subsequent workspace
				// preparation. Keep the known Project and block automatic continuation.
				err = sql.ErrNoRows
			}
			if err == nil {
				step.State = "COMPLETED"
				step.Failure = nil
				step.Code = ""
				step.Message = ""
			} else if !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
			out.Steps[i] = step
		}
		if step.State != "COMPLETED" {
			allDone = false
			if step.State != "PENDING" {
				unresolved = true
			}
		}
		if step.State == "COMPLETED" {
			if step.ProjectID != "" {
				project = step.ProjectID
			}
			if step.FeatureAreaID != "" {
				area = step.FeatureAreaID
			}
			if step.TaskID != "" {
				taskID = step.TaskID
			}
			if step.RevisionID != "" {
				revision = step.RevisionID
			}
			if step.ExecutionID != "" {
				executionID = step.ExecutionID
			}
		}
	}
	out.CanResume = !allDone && !unresolved && (out.State == "PARTIAL" || out.State == "UNKNOWN" || (out.State == "CONFIRMING" && worker != epoch))
	if allDone {
		if !completedConfirmation(out) {
			return out, ErrConflict
		}
		out.State = "COMPLETED"
	}
	return out, nil
}
func (s *ConfirmationStore) Resume(ctx context.Context, session, id string, version int) (out confirmation.Intent, fresh bool, err error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback()
	out, err = confirmationRead(ctx, tx, session, id)
	if err != nil {
		return
	}
	if out.Version != version {
		return out, false, ErrConflict
	}
	out, err = recoverConfirmationTx(ctx, tx, out, s.db.epoch)
	if err != nil || !out.CanResume {
		return
	}
	out.CanResume = false
	out.State = "CONFIRMING"
	if err = confirmationSave(ctx, tx, out); err != nil {
		return
	}
	if _, err = tx.ExecContext(ctx, `UPDATE confirmation_intents SET worker_epoch=? WHERE intent_id=?`, s.db.epoch, id); err != nil {
		return
	}
	err = tx.Commit()
	return out, err == nil, err
}
func (s *ConfirmationStore) ApprovedRuntimeConfig(ctx context.Context, session, taskID, revision string) (codexruntime.NextRunRuntimeConfig, error) {
	var out codexruntime.NextRunRuntimeConfig
	var raw string
	if err := instructionCaller(ctx, s.db.sql, session); err != nil {
		return out, err
	}
	err := s.db.sql.QueryRowContext(ctx, `SELECT m.runtime_config_json FROM tasks t JOIN ai_sessions s ON s.id=? AND s.project_id=t.project_id JOIN review_approval_materializations m ON m.decision_id=t.review_approval_id AND m.task_id=t.id JOIN task_definition_revisions r ON r.id=t.current_definition_revision_id WHERE t.id=? AND t.current_definition_revision_id=? AND r.task_id=t.id AND json_extract(r.payload_json,'$.reviewApprovalId')=m.decision_id`, session, taskID, revision).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &out)
	}
	return out, err
}
