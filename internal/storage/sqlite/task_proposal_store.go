package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskproposal"
)

type TaskProposalStore struct{ db *DB }

func NewTaskProposalStore(db *DB) *TaskProposalStore { return &TaskProposalStore{db: db} }

// Legacy Task Proposals are read-only history after ownership closure.
func (s *TaskProposalStore) Create(context.Context, taskproposal.Proposal) error {
	return taskproposal.ErrRetired
}

func (s *TaskProposalStore) Get(ctx context.Context, id string) (taskproposal.Proposal, error) {
	value, err := scanTaskProposal(s.db.sql.QueryRowContext(ctx, selectTaskProposal+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return taskproposal.Proposal{}, taskproposal.ErrNotFound
	}
	if err != nil {
		return taskproposal.Proposal{}, fmt.Errorf("get task proposal: %w", err)
	}
	return value, nil
}

func (s *TaskProposalStore) ListBySession(ctx context.Context, aiSessionID string) ([]taskproposal.Proposal, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectTaskProposal+` WHERE ai_session_id=? ORDER BY created_at DESC,id`, aiSessionID)
	if err != nil {
		return nil, fmt.Errorf("list task proposals: %w", err)
	}
	defer rows.Close()
	values := make([]taskproposal.Proposal, 0)
	for rows.Next() {
		value, err := scanTaskProposal(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *TaskProposalStore) Confirm(context.Context, string, time.Time) (taskproposal.Proposal, error) {
	return taskproposal.Proposal{}, taskproposal.ErrRetired
}
func (s *TaskProposalStore) Reject(context.Context, string, time.Time) (taskproposal.Proposal, error) {
	return taskproposal.Proposal{}, taskproposal.ErrRetired
}
func (s *TaskProposalStore) ConsumeCreate(context.Context, string, task.Task, time.Time) (taskproposal.Proposal, error) {
	return taskproposal.Proposal{}, taskproposal.ErrRetired
}
func (s *TaskProposalStore) ConsumeContinuation(context.Context, string, time.Time) (taskproposal.Proposal, error) {
	return taskproposal.Proposal{}, taskproposal.ErrRetired
}

type taskProposalScanner interface{ Scan(...any) error }

func scanTaskProposal(scanner taskProposalScanner) (taskproposal.Proposal, error) {
	var value taskproposal.Proposal
	var target, resultTask, baseRevision, confirmed, rejected, consumed sql.NullString
	var constraints, created, updated, workSpec, verificationSpec string
	err := scanner.Scan(&value.ID, &value.AISessionID, &value.ProjectID, &value.Kind, &value.ModuleID, &value.RepositoryID, &target, &resultTask, &value.CodexConfigID, &value.Goal, &constraints, &baseRevision, &value.Status, &created, &updated, &confirmed, &rejected, &consumed, &workSpec, &verificationSpec)
	if err != nil {
		return taskproposal.Proposal{}, err
	}
	if target.Valid {
		value.TargetTaskID = &target.String
	}
	if resultTask.Valid {
		value.ResultTaskID = &resultTask.String
	}
	if baseRevision.Valid {
		value.BaseRevision = baseRevision.String
	}
	if err := decodeProposalConstraints(constraints, &value.Constraints); err != nil {
		return taskproposal.Proposal{}, err
	}
	value.WorkSpec, err = decodeWorkSpec(workSpec)
	if err != nil && workSpec == "{}" {
		value.WorkSpec, value.VerificationSpec = legacySpecs(value.Goal, value.Constraints, "Task proposal predates explicit verification planning")
		err = nil
	}
	if err != nil {
		return taskproposal.Proposal{}, err
	}
	if value.VerificationSpec.Mode == "" {
		value.VerificationSpec, err = decodeVerificationSpec(verificationSpec)
	}
	if err != nil {
		return taskproposal.Proposal{}, err
	}
	if value.Goal != value.WorkSpec.Goal || !slices.Equal(value.Constraints, value.WorkSpec.Constraints) {
		return taskproposal.Proposal{}, errors.New("task proposal compatibility fields do not match work spec")
	}
	value.CreatedAt, err = parseTime(created)
	if err != nil {
		return taskproposal.Proposal{}, err
	}
	value.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return taskproposal.Proposal{}, err
	}
	if value.ConfirmedAt, err = parseNullableTime(confirmed); err != nil {
		return taskproposal.Proposal{}, err
	}
	if value.RejectedAt, err = parseNullableTime(rejected); err != nil {
		return taskproposal.Proposal{}, err
	}
	if value.ConsumedAt, err = parseNullableTime(consumed); err != nil {
		return taskproposal.Proposal{}, err
	}
	return value, nil
}

func decodeProposalConstraints(encoded string, destination *[]string) error {
	var envelope struct {
		Version int      `json:"version"`
		Items   []string `json:"items"`
	}
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		return fmt.Errorf("decode task proposal constraints: %w", err)
	}
	if envelope.Version != 1 {
		return fmt.Errorf("unsupported task proposal constraints version %d", envelope.Version)
	}
	*destination = envelope.Items
	return nil
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func timePointer(value time.Time) *time.Time { return &value }

const selectTaskProposal = `SELECT id,ai_session_id,project_id,kind,module_id,repository_id,target_task_id,result_task_id,codex_config_id,goal,constraints_json,base_revision,status,created_at,updated_at,confirmed_at,rejected_at,consumed_at,work_spec_json,verification_spec_json FROM task_proposals`
