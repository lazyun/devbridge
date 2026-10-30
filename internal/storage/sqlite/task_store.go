package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
	"reflect"
)

type TaskStore struct{ db *DB }

func NewTaskStore(db *DB) *TaskStore { return &TaskStore{db: db} }

func (s *TaskStore) Create(ctx context.Context, value task.Task) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertTask(ctx, tx, value); err != nil {
		return err
	}
	return tx.Commit()
}

func insertTask(ctx context.Context, target *sql.Tx, value task.Task) error {
	if len(value.RepositoryTargets) != 0 {
		return task.ErrRepositoryTargetsUnsupported
	}
	if value.RepositoryID != "" {
		if err := ensureRepositoryActiveTx(ctx, target, value.ProjectID, value.RepositoryID); err != nil {
			return err
		}
	}
	constraints, err := json.Marshal(map[string]any{"version": 1, "items": value.Constraints})
	if err != nil {
		return err
	}
	workSpec, verificationSpec := "{}", "{}"
	if !value.Phase.ReviewStage() {
		workSpec, err = encodeWorkSpec(value.WorkSpec)
		if err != nil {
			return err
		}
		verificationSpec, err = encodeVerificationSpec(value.VerificationSpec)
		if err != nil {
			return err
		}
	}
	problem := value.ProblemStatement
	if problem == "" {
		problem = value.Goal
	}
	value.ProblemStatement = problem
	if value.Phase == task.PhaseReviewRequired && value.Status == task.StatusActive {
		value.RequiredReviewDepth = task.ReviewDepthPointer(task.ReviewDepthFast)
	}
	_, err = target.ExecContext(ctx, `INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,work_kind,model,reasoning_effort,goal,constraints_json,base_commit,status,created_at,updated_at,work_spec_json,verification_spec_json,problem_statement,confirmed_task_plan_id,feature_area_id,review_approval_id,phase,title,required_review_depth) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.ProjectID, value.ModuleID, nullString(value.RepositoryID), nullString(value.CodexConfigID), nullString(string(value.WorkKind)), nullString(value.Model), nullString(value.ReasoningEffort), value.Goal, string(constraints), value.BaseCommit, string(value.Status), formatTime(value.CreatedAt), formatTime(value.UpdatedAt), workSpec, verificationSpec, problem, nullString(value.ConfirmedTaskPlanID), nullString(value.FeatureAreaID), nullString(value.ReviewApprovalID), string(value.Phase), value.Title, value.RequiredReviewDepth)
	if err != nil {
		return conflict(fmt.Errorf("insert task: %w", err))
	}
	// Preserve the authored requirement before any Review. This is a draft
	// definition, with no approval, runtime selection or execution authority.
	if value.Type == task.TypeDevelopment && value.FeatureAreaID != "" && value.Phase.ReviewStage() {
		value.WorkSpec = task.WorkSpec{Goal: value.Goal, Constraints: append([]string(nil), value.Constraints...)}
		value.VerificationSpec = task.VerificationSpec{}
		if _, err := ensureTaskDefinitionTx(ctx, target, &value, value.CreatedAt); err != nil {
			return err
		}
	}

	if err := bindCreatedTaskActionTx(ctx, target, value.ID, value.ProjectID); err != nil {
		return err
	}
	payload, err := event.Payload(map[string]any{"status": value.Status, "module_id": value.ModuleID, "codex_config_id": value.CodexConfigID,
		"originalRequirement": map[string]any{"version": 1, "goal": value.Goal, "constraints": value.Constraints}})
	if err != nil {
		return err
	}
	return appendEvent(ctx, target, event.Event{Type: event.TaskCreated, ProjectID: value.ProjectID, TaskID: value.ID, EntityType: "task", EntityID: value.ID, Payload: payload, CreatedAt: value.CreatedAt})
}

// decodeOriginalRequirement reads only the exact creation event. Missing legacy
// snapshots are explicitly untraceable; malformed sources are factual read errors.
func decodeOriginalRequirement(raw string) (task.OriginalRequirement, error) {
	value := task.OriginalRequirement{Status: "UNTRACEABLE"}
	if raw == "" {
		return value, nil
	}
	var source struct {
		EventID string `json:"eventId"`
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		return value, fmt.Errorf("decode task creation source: %w", err)
	}
	if source.EventID == "" {
		return value, errors.New("task creation source event identity missing")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(source.Payload), &payload); err != nil {
		return value, fmt.Errorf("decode task creation payload: %w", err)
	}
	if payload == nil {
		return value, errors.New("task creation payload missing")
	}
	var eventVersion int
	if err := json.Unmarshal(payload["version"], &eventVersion); err != nil || eventVersion != 1 {
		return value, errors.New("unsupported task creation payload version")
	}
	snapshot, ok := payload["originalRequirement"]
	if !ok {
		return value, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &fields); err != nil {
		return value, fmt.Errorf("decode original requirement: %w", err)
	}
	var version int
	var goal *string
	var constraints []string
	if err := json.Unmarshal(fields["version"], &version); err != nil || version != 1 {
		return value, errors.New("unsupported original requirement version")
	}
	if err := json.Unmarshal(fields["goal"], &goal); err != nil || goal == nil {
		return value, errors.New("invalid original requirement goal")
	}
	if err := json.Unmarshal(fields["constraints"], &constraints); err != nil {
		return value, fmt.Errorf("decode original requirement constraints: %w", err)
	}
	return task.OriginalRequirement{Status: "SOURCE_AVAILABLE", SourceEventID: source.EventID, Goal: *goal, Constraints: constraints}, nil
}

type taskScanner interface{ Scan(...any) error }

func scanTask(scanner taskScanner) (task.Task, error) {
	var value task.Task
	var constraints, created, updated, workSpec, verificationSpec, workUpdated string
	var confirmedPlan, featureAreaID, workKind, model, reasoningEffort sql.NullString
	var revisionID sql.NullString
	var committedAt, mergedAt sql.NullString
	var purpose, preRunExecution, branches, executions, reviewExecutor, originalRequirement string
	var definitionPayload, definitionHash, recoveryOrigin string
	var exactReviewRecovery bool
	var targetDefinition *task.Definition
	var authoredDefinition *task.Definition
	err := scanner.Scan(&value.ID, &value.ProjectID, &value.ModuleID, &value.RepositoryID, &value.CodexConfigID, &workKind, &model, &reasoningEffort, &value.Goal, &constraints, &value.BaseCommit, &value.Status, &created, &updated, &workSpec, &verificationSpec, &value.ProblemStatement, &confirmedPlan, &featureAreaID, &revisionID, &value.LegacyReadOnly, &value.ReviewApprovalID, &value.Type, &value.Scope, &value.WorkOwnerID, &value.Work.OwnerType, &value.Work.OwnerID, &value.Work.RunID, &value.Work.RunStatus, &value.Work.CancellationState, &value.Work.BackendDisposition, &value.Work.TokenAvailability, &workUpdated, &purpose, &value.Work.ReadOnly, &value.Work.Provenance, &preRunExecution, &branches, &executions, &value.CommitSHA, &committedAt, &value.MergedCommit, &mergedAt, &value.Phase, &value.CurrentReviewID, &value.CurrentExecutionID, &value.Title, &value.RequiredReviewDepth, &reviewExecutor, &originalRequirement, &definitionPayload, &definitionHash, &exactReviewRecovery, &recoveryOrigin)
	if err != nil {
		return task.Task{}, err
	}
	if definitionPayload != "" {
		definition, decodeErr := task.DecodeDefinition(definitionPayload, definitionHash)
		if decodeErr != nil {
			return task.Task{}, decodeErr
		}
		authoredDefinition = &definition
		if definition.Version == 2 {
			if value.RepositoryID != "" || value.BaseCommit != "" {
				return task.Task{}, errors.New("ambiguous v2 task repository authority")
			}
			value.RepositoryTargets = definition.RepositoryTargets
			value.PrimaryMemberID = definition.PrimaryMemberID
			targetDefinition = &definition
		}
	}
	if value.Phase == task.PhaseRecoveryRequired && recoveryOrigin != "" {
		var origin struct {
			EventID string     `json:"eventId"`
			From    task.Phase `json:"from"`
			To      task.Phase `json:"to"`
		}
		if json.Unmarshal([]byte(recoveryOrigin), &origin) == nil && origin.EventID != "" && origin.To == task.PhaseRecoveryRequired && origin.From.Valid() && origin.From != task.PhaseRecoveryRequired {
			value.RecoveryOrigin = &task.RecoveryOrigin{Phase: origin.From, SourceEventID: origin.EventID}
		}
	}
	value.OriginalRequirement, err = decodeOriginalRequirement(originalRequirement)
	if err != nil {
		return task.Task{}, err
	}
	value.CommittedAt, err = parseOptionalTime(committedAt)
	if err != nil {
		return value, err
	}
	value.MergedAt, err = parseOptionalTime(mergedAt)
	if err != nil {
		return value, err
	}
	var envelope struct {
		Version int      `json:"version"`
		Items   []string `json:"items"`
	}
	if err := json.Unmarshal([]byte(constraints), &envelope); err != nil {
		return task.Task{}, fmt.Errorf("decode task constraints: %w", err)
	}
	if envelope.Version != 1 {
		return task.Task{}, fmt.Errorf("unsupported task constraints version %d", envelope.Version)
	}
	value.Constraints = envelope.Items
	value.ConfirmedTaskPlanID = confirmedPlan.String
	value.FeatureAreaID = featureAreaID.String
	value.CurrentDefinitionRevisionID = revisionID.String
	value.WorkKind = task.WorkKind(workKind.String)
	value.Model = model.String
	value.ReasoningEffort = reasoningEffort.String
	// A pre-approval Review can enter Recovery before any execution definition
	// exists. Its exact current Review/source and original authored requirement
	// preserve this intent-only shape; an Execution with missing specs still fails.
	reviewOnlyDefinition := authoredDefinition != nil && authoredDefinition.Version == 1 && authoredDefinition.ProjectID == value.ProjectID && authoredDefinition.FeatureAreaID == value.FeatureAreaID && authoredDefinition.ReviewApprovalID == "" && authoredDefinition.WorkSpec.CodexInstruction == "" && authoredDefinition.WorkSpec.ExecutionScope.Kind == "" && authoredDefinition.VerificationSpec.Mode == "" && authoredDefinition.WorkSpec.Goal == value.Goal && slices.Equal(authoredDefinition.WorkSpec.Constraints, value.Constraints)
	reviewRecoveryIntent := reviewOnlyDefinition && exactReviewRecovery && value.Phase == task.PhaseRecoveryRequired && value.CurrentReviewID != "" && value.CurrentExecutionID == "" && value.ReviewApprovalID == "" && value.CurrentDefinitionRevisionID != "" && value.OriginalRequirement.Status == "SOURCE_AVAILABLE" && workSpec == "{}" && verificationSpec == "{}"
	reviewSkippedIntent := reviewOnlyDefinition && value.Phase == task.PhaseImplementationReady && value.ReviewApprovalID == "" && value.CurrentReviewID == "" && value.CurrentExecutionID == "" && value.RequiredReviewDepth == nil && workSpec == "{}" && verificationSpec == "{}"
	if targetDefinition != nil {
		value = targetDefinition.ApplyTo(value)
	} else if (value.Type != task.TypeDevelopment || value.Phase.ReviewStage() || reviewRecoveryIntent || value.Phase == task.PhaseImplementationReady && value.ReviewApprovalID == "" && value.CurrentReviewID == "" && value.RequiredReviewDepth == nil) && workSpec == "{}" && verificationSpec == "{}" {
		value.WorkSpec, value.VerificationSpec = task.WorkSpec{}, task.VerificationSpec{}
		err = nil
	} else {
		value.WorkSpec, err = decodeWorkSpec(workSpec)
	}

	if err != nil {
		return task.Task{}, err
	}
	if value.VerificationSpec.Mode == "" && verificationSpec != "{}" {
		value.VerificationSpec, err = decodeVerificationSpec(verificationSpec)
	}
	if err != nil {
		return task.Task{}, err
	}
	if value.Type == task.TypeDevelopment && !value.Phase.ReviewStage() && !reviewRecoveryIntent && !reviewSkippedIntent && (value.Goal != value.WorkSpec.Goal || !slices.Equal(value.Constraints, value.WorkSpec.Constraints)) {
		return task.Task{}, errors.New("task compatibility fields do not match work spec")
	}
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return task.Task{}, err
	}
	value.UpdatedAt, err = parseTime(updated)
	value.Work.UpdatedAt, err = parseTime(workUpdated)
	if err != nil {
		return value, err
	}
	if err = json.Unmarshal([]byte(branches), &value.Work.DevelopmentBranches); err != nil {
		return value, err
	}
	value.Work.DevelopmentBranchProvenance = "NOT_REVIEWED"
	if len(value.Work.DevelopmentBranches) > 0 {
		switch {
		case value.ReviewApprovalID != "":
			value.Work.DevelopmentBranchProvenance = "REVIEW_APPROVAL_INITIAL_REVISION"
		default:
			value.Work.DevelopmentBranchProvenance = "SOURCE_REVIEW"
		}
	}
	value.Work.Type, value.Work.Scope, value.Work.Agent = value.Type, value.Scope, "Codex"
	if value.Work.OwnerType == "REVIEW" && reviewExecutor == "UPPER_LLM" {
		value.Work.Agent = "UPPER_LLM"
		value.Work.TokenAvailability = "UNAVAILABLE"
		value.Work.ReadOnly = true
	}
	value.Work.Phase = value.Work.OwnerType
	if value.Work.OwnerType == "EXECUTION" && purpose != "" {
		value.Work.Phase = purpose
	}
	value.Work.Status = string(value.Status)
	switch value.Work.RunStatus {
	case "CREATED", "PREPARING", "RUNNING", "WAITING_APPROVAL", "COLLECTING":
		if value.Status != task.StatusCanceled && value.Status != task.StatusSucceeded && value.Status != task.StatusFailed {
			value.Work.Status = "RUNNING"
		}
	}
	if value.Work.CancellationState == "CANCEL_REQUESTED" || value.Work.CancellationState == "RECOVERY_REQUIRED" {
		value.Work.Status = "CANCELING"
	}
	if value.Type == task.TypeDevelopment && value.Work.RunID == "" && value.Work.OwnerType == "TASK" {
		value.Work.Phase = "CONFIRMATION"
	}
	if preRunExecution != "" {
		value.Work.OwnerType = "EXECUTION"
		value.Work.OwnerID = preRunExecution
		value.Work.RunID = ""
		value.Work.RunStatus = ""
		value.Work.Phase = "AUTHORIZATION"
		value.Work.Status = string(value.Status)
	}
	var identities []struct {
		RunID      string `json:"runId"`
		ID         string `json:"id"`
		Kind       string `json:"kind"`
		Provenance string `json:"provenance"`
		Proposal   string `json:"proposal"`
	}
	if err := json.Unmarshal([]byte(executions), &identities); err != nil {
		return task.Task{}, err
	}
	for _, e := range identities {
		if (value.Work.RunID != "" && e.RunID == value.Work.RunID) || (preRunExecution != "" && e.ID == preRunExecution) {
			value.Work.ExecutionID = e.ID
			value.Work.ExecutionKind = e.Kind
			value.Work.ExecutionProposalID = e.Proposal
			if e.Provenance == "LEGACY_UNKNOWN" {
				value.Work.ReadOnly = true
				value.Work.Provenance = e.Provenance
			}
			break
		}
	}

	if value.Type == task.TypeDevelopment {
		value.Work.Phase = string(value.Phase)
		value.Work.Status = string(value.Status)
	}
	return value, err
}
func (s *TaskStore) Get(ctx context.Context, id string) (task.Task, error) {
	v, err := scanTask(s.db.sql.QueryRowContext(ctx, selectTask+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Task{}, task.ErrNotFound
	}
	if err != nil {
		return task.Task{}, fmt.Errorf("get task: %w", err)
	}
	return v, nil
}
func (s *TaskStore) List(ctx context.Context) ([]task.Task, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectTask+` WHERE legacy_read_only=0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []task.Task
	for rows.Next() {
		v, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
func (s *TaskStore) LatestByFeatureArea(ctx context.Context, featureAreaID string) (task.Task, error) {
	v, err := scanTask(s.db.sql.QueryRowContext(ctx, selectTask+` WHERE feature_area_id=? AND legacy_read_only=0 AND task_type='DEVELOPMENT' ORDER BY created_at DESC,id LIMIT 1`, featureAreaID))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Task{}, task.ErrNotFound
	}
	if err != nil {
		return task.Task{}, fmt.Errorf("get latest task for featureArea: %w", err)
	}
	return v, nil
}

func (s *TaskStore) ActiveFeatureAreaIDsByProject(ctx context.Context, projectID string, limit int) ([]string, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT DISTINCT tasks.feature_area_id FROM tasks JOIN feature_areas ON feature_areas.id=tasks.feature_area_id AND feature_areas.project_id=tasks.project_id WHERE tasks.project_id=? AND tasks.legacy_read_only=0 AND feature_areas.status IN ('DISCOVERING','ACTIVE') AND tasks.status='ACTIVE' AND tasks.task_type='DEVELOPMENT' ORDER BY tasks.feature_area_id LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list active Feature Area lineage: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0, limit)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// CountByFeatureArea returns the durable number of Tasks owned by the Feature
// Area. It backs the structural split gate (splitting is allowed only before
// the parent Feature Area owns Tasks).
func (s *TaskStore) CountByFeatureArea(ctx context.Context, featureAreaID string) (int, error) {
	var count int
	if err := s.db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE feature_area_id=? AND legacy_read_only=0 AND task_type='DEVELOPMENT'`, featureAreaID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count tasks by feature area: %w", err)
	}
	return count, nil
}
func (s *TaskStore) Update(ctx context.Context, id string, update func(*task.Task) error) (task.Task, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback()
	value, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return task.Task{}, task.ErrNotFound
	}
	if err != nil {
		return task.Task{}, err
	}
	if value.LegacyReadOnly || value.Type != task.TypeDevelopment {
		return task.Task{}, task.ErrLegacyReadOnly
	}
	original := value
	original.RepositoryTargets = workset.CloneTargets(value.RepositoryTargets)
	previousRevisionID := value.CurrentDefinitionRevisionID
	previous := value.Status
	previousConfigID := value.CodexConfigID
	previousBaseCommit := value.BaseCommit
	previousFeatureAreaID := value.FeatureAreaID
	previousUpdatedAt := value.UpdatedAt
	if err := update(&value); err != nil {
		return task.Task{}, err
	}
	// Generic updates are organization metadata only. Gate/acceptance stores own
	// all semantic mutations and immutable definition creation.
	if !reflect.DeepEqual(value.RepositoryTargets, original.RepositoryTargets) || value.Status != original.Status || value.Phase != original.Phase || !reflect.DeepEqual(value.RequiredReviewDepth, original.RequiredReviewDepth) || value.Title != original.Title || value.ProjectID != original.ProjectID || value.RepositoryID != original.RepositoryID || value.ProblemStatement != original.ProblemStatement || value.Goal != original.Goal || !slices.Equal(value.Constraints, original.Constraints) || !reflect.DeepEqual(value.WorkSpec, original.WorkSpec) || !reflect.DeepEqual(value.VerificationSpec, original.VerificationSpec) || value.CurrentReviewID != original.CurrentReviewID || value.CurrentExecutionID != original.CurrentExecutionID || value.CurrentDefinitionRevisionID != original.CurrentDefinitionRevisionID || value.ReviewApprovalID != original.ReviewApprovalID {
		return task.Task{}, ErrConflict
	}
	if original.ReviewApprovalID != "" && (value.Model != original.Model || value.ReasoningEffort != original.ReasoningEffort) {
		return task.Task{}, ErrConflict
	}
	if value.CodexConfigID != previousConfigID {
		return task.Task{}, task.ErrCodexConfigImmutable
	}
	if previousBaseCommit != "" && value.BaseCommit != previousBaseCommit {
		return task.Task{}, task.ErrBaseCommitImmutable
	}
	if value.FeatureAreaID != previousFeatureAreaID {
		return task.Task{}, task.ErrTopicImmutable
	}
	if value.Status == task.StatusCanceled && previous != task.StatusCanceled {
		var executionOwned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposals e WHERE e.task_id=? AND
 (e.outcome='RUNNING' OR (e.outcome='PENDING' AND e.status IN ('PENDING_CONFIRMATION','CONFIRMED'))
 OR EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.operation_kind='EXECUTION' AND c.operation_id=e.id)))`, id).Scan(&executionOwned); err != nil {
			return task.Task{}, err
		}
		if executionOwned {
			// Execution cancellation publishes the Task terminal in the same
			// authoritative transaction as its exact Run and Execution.
			return task.Task{}, ErrConflict
		}
	}
	value.CurrentDefinitionRevisionID = previousRevisionID
	workJSON, verificationJSON := "{}", "{}"
	if value.WorkSpec.Goal != "" {
		workJSON, err = encodeWorkSpec(value.WorkSpec)
		if err != nil {
			return task.Task{}, err
		}
		verificationJSON, err = encodeVerificationSpec(value.VerificationSpec)
		if err != nil {
			return task.Task{}, err
		}
	}
	constraints, _ := json.Marshal(map[string]any{"version": 1, "items": value.Constraints})
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET project_id=?,module_id=?,repository_id=?,codex_config_id=?,work_kind=?,model=?,reasoning_effort=?,problem_statement=?,goal=?,constraints_json=?,base_commit=?,status=?,updated_at=?,work_spec_json=?,verification_spec_json=? WHERE id=? AND status=? AND updated_at=?`, value.ProjectID, value.ModuleID, nullString(value.RepositoryID), nullString(value.CodexConfigID), nullString(string(value.WorkKind)), nullString(value.Model), nullString(value.ReasoningEffort), value.ProblemStatement, value.Goal, string(constraints), value.BaseCommit, string(value.Status), formatTime(value.UpdatedAt), workJSON, verificationJSON, id, string(previous), formatTime(previousUpdatedAt))
	if err != nil {
		return task.Task{}, conflict(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return task.Task{}, err
	}
	if affected != 1 {
		return task.Task{}, ErrConflict
	}
	if value.Status != previous {
		payload, _ := event.Payload(map[string]any{"from": previous, "to": value.Status})
		if err := appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: value.ProjectID, TaskID: value.ID, EntityType: "task", EntityID: value.ID, Payload: payload, CreatedAt: value.UpdatedAt}); err != nil {
			return task.Task{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return task.Task{}, err
	}
	return value, nil
}

const selectTask = `SELECT id,project_id,COALESCE(module_id,''),COALESCE(repository_id,''),COALESCE(codex_config_id,''),work_kind,model,reasoning_effort,goal,constraints_json,base_commit,status,created_at,updated_at,work_spec_json,verification_spec_json,problem_statement,confirmed_task_plan_id,feature_area_id,current_definition_revision_id,legacy_read_only,COALESCE(review_approval_id,'')` + taskWorkColumns + `,COALESCE(commit_sha,''),committed_at,COALESCE(merged_commit,''),merged_at,phase,COALESCE(current_review_id,''),COALESCE(current_execution_id,''),title,required_review_depth,COALESCE((SELECT executor_kind FROM reviews v WHERE v.id=tasks.current_review_id),'CODING_AGENT'),COALESCE((SELECT json_object('eventId',e.id,'payload',e.payload_json) FROM events e WHERE e.type='task.created' AND e.project_id=tasks.project_id AND e.task_id=tasks.id AND e.entity_type='task' AND e.entity_id=tasks.id ORDER BY e.sequence LIMIT 1),''),COALESCE((SELECT d.payload_json FROM task_definition_revisions d WHERE d.id=tasks.current_definition_revision_id AND d.task_id=tasks.id),''),COALESCE((SELECT d.payload_sha256 FROM task_definition_revisions d WHERE d.id=tasks.current_definition_revision_id AND d.task_id=tasks.id),''),EXISTS(SELECT 1 FROM reviews v JOIN runs source ON source.id=v.linked_run_id AND source.review_id=v.id AND source.task_id=tasks.id AND source.operation_kind='REVIEW' AND source.operation_id=v.id JOIN recovery_status_records recovery ON recovery.source_run_id=source.id WHERE v.id=tasks.current_review_id AND v.task_id=tasks.id AND v.project_id=tasks.project_id AND v.feature_area_id=tasks.feature_area_id) ,CASE WHEN tasks.phase='RECOVERY_REQUIRED' THEN COALESCE((SELECT json_object('eventId',e.id,'from',json_extract(e.payload_json,'$.phase_from'),'to',json_extract(e.payload_json,'$.phase_to')) FROM events e WHERE e.project_id=tasks.project_id AND e.task_id=tasks.id AND e.entity_type='task' AND e.entity_id=tasks.id AND e.type='task.status_changed' AND json_type(e.payload_json,'$.phase_to')='text' ORDER BY e.sequence DESC LIMIT 1),'') ELSE '' END FROM tasks`
