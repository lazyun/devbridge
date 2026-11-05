package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
)

// Exercise only the production read methods against synthetic rows in a real
// SQLite table. Do not open a Core DataRoot, run migrations, or create tasks,
// sessions, plans, events, or workflow transitions through application APIs.
func TestTaskPlanReadOrderChronologicalNanoseconds(t *testing.T) {
	base := time.Date(2026, 11, 4, 12, 30, 0, 0, time.UTC)
	cases := []struct {
		name    string
		offsets []time.Duration
	}{
		{"whole_second_and_one_nanosecond", []time.Duration{0, 1}},
		{"fractional_prefix", []time.Duration{100 * time.Millisecond, 100*time.Millisecond + 1}},
		{"unequal_fraction_widths", []time.Duration{10 * time.Millisecond, 12 * time.Millisecond, 120000001, 123450000, 123456789, 123456790}},
		{"adjacent_seconds", []time.Duration{-1, 0, time.Second - 1, time.Second, time.Second + 1}},
		{"equal_timestamp_id_ties", []time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond}},
	}
	var fixtures []taskplan.Plan
	for _, tc := range cases {
		for i, offset := range tc.offsets {
			fixtures = append(fixtures, taskPlanReadOrderFixture(fmt.Sprintf("%s-%02d", tc.name, i), tc.name, tc.name+"-task", base.Add(offset)))
		}
	}
	// A newer plan for another task in the same project must be listed, but
	// must not replace the requested task's latest plan. A newer other-project
	// plan must not leak into the requested project's list.
	fixtures = append(fixtures,
		taskPlanReadOrderFixture("isolation-target-old", "isolation", "target", base),
		taskPlanReadOrderFixture("isolation-target-new", "isolation", "target", base.Add(1)),
		taskPlanReadOrderFixture("isolation-peer", "isolation", "peer", base.Add(time.Second)),
		taskPlanReadOrderFixture("isolation-other", "other-project", "other-task", base.Add(2*time.Second)),
	)
	for _, mode := range []string{"warm", "reopened_read_only"} {
		t.Run(mode, func(t *testing.T) {
			store := taskPlanReadOrderStore(t, fixtures, mode == "reopened_read_only")
			ctx := context.Background()
			t.Run("query_plan_diagnostics", func(t *testing.T) { taskPlanReadOrderQueryPlans(t, store.db.sql) })
			t.Run("get_round_trip", func(t *testing.T) {
				for _, want := range fixtures {
					got, err := store.Get(ctx, want.ID)
					if err != nil {
						t.Fatalf("Get(%q): %v", want.ID, err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("Get(%q) = %#v, want %#v", want.ID, got, want)
					}
				}
			})
			projects, tasks := map[string]bool{}, map[string]bool{}
			for _, value := range fixtures {
				projects[value.ProjectID], tasks[value.TaskID] = true, true
			}
			for projectID := range projects {
				t.Run("list/"+projectID, func(t *testing.T) {
					want := taskPlanReadOrderExpected(fixtures, func(value taskplan.Plan) bool { return value.ProjectID == projectID })
					got, err := store.ListByProject(ctx, projectID)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("ListByProject(%q) IDs = %v, want %v", projectID, taskPlanReadOrderIDs(got), taskPlanReadOrderIDs(want))
					}
				})
			}
			for taskID := range tasks {
				t.Run("latest/"+taskID, func(t *testing.T) {
					want := taskPlanReadOrderExpected(fixtures, func(value taskplan.Plan) bool { return value.TaskID == taskID })[0]
					got, err := store.LatestByTask(ctx, taskID)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("LatestByTask(%q) = %s at %s, want %s at %s", taskID, got.ID, got.CreatedAt, want.ID, want.CreatedAt)
					}
				})
			}
			t.Run("missing", func(t *testing.T) {
				if got, err := store.Get(ctx, "missing-plan"); !errors.Is(err, taskplan.ErrNotFound) || !reflect.DeepEqual(got, taskplan.Plan{}) {
					t.Errorf("Get missing = %#v, %v", got, err)
				}
				if got, err := store.LatestByTask(ctx, "missing-task"); !errors.Is(err, taskplan.ErrNotFound) || !reflect.DeepEqual(got, taskplan.Plan{}) {
					t.Errorf("LatestByTask missing = %#v, %v", got, err)
				}
				if got, err := store.ListByProject(ctx, "missing-project"); err != nil || got == nil || len(got) != 0 {
					t.Errorf("ListByProject missing = %#v, %v", got, err)
				}
			})
		})
	}
}

func taskPlanReadOrderFixture(id, projectID, taskID string, created time.Time) taskplan.Plan {
	return taskplan.Plan{
		ID: id, ProjectID: projectID, TaskID: taskID, Title: "Synthetic read fixture " + id,
		WorkSpec:         task.WorkSpec{Goal: "Read ordering fixture", CodexInstruction: "No workflow execution", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}},
		VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Synthetic read fixture"}},
		Status:           taskplan.StatusPendingConfirmation, CreatedAt: created, UpdatedAt: created.Add(time.Minute),
	}
}

func taskPlanReadOrderStore(t *testing.T, fixtures []taskplan.Plan, reopen bool) *TaskPlanStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "task-plan-read-order.db")
	dsn := &url.URL{Scheme: "file", Path: path}
	raw, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	raw.SetMaxOpenConns(1)
	// Only the columns read by selectTaskPlan are present. This deliberately
	// excludes migrations, foreign application tables, triggers, and services.
	_, err = raw.Exec(`CREATE TABLE task_plans (
		id TEXT PRIMARY KEY, ai_session_id TEXT, project_id TEXT NOT NULL,
		task_id TEXT NOT NULL, title TEXT NOT NULL, work_spec_json TEXT NOT NULL,
		verification_spec_json TEXT NOT NULL, status TEXT NOT NULL,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		confirmed_at TEXT, rejected_at TEXT, applied_at TEXT
	)`)
	if err != nil {
		t.Fatal(err)
	}
	// Mirror only the existing task_plans indexes, without applying migrations.
	for _, query := range []string{
		`CREATE INDEX task_plans_project_created ON task_plans(project_id,created_at DESC,id)`,
		`CREATE INDEX task_plans_task_status ON task_plans(task_id,status,created_at,id)`,
	} {
		if _, err := raw.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	// Reverse insertion order so the ID tie-break cannot pass by accident.
	for i := len(fixtures) - 1; i >= 0; i-- {
		value := fixtures[i]
		work, err := encodeWorkSpec(value.WorkSpec)
		if err != nil {
			t.Fatal(err)
		}
		verification, err := encodeVerificationSpec(value.VerificationSpec)
		if err != nil {
			t.Fatal(err)
		}
		// Use the production writer's canonical UTC RFC3339Nano encoding,
		// including normalization from a non-UTC input zone.
		inputTime := value.CreatedAt.In(time.FixedZone("fixture-offset", 5*60*60+30*60))
		_, err = raw.Exec(`INSERT INTO task_plans(id,ai_session_id,project_id,task_id,title,work_spec_json,verification_spec_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			value.ID, nil, value.ProjectID, value.TaskID, value.Title, work, verification, string(value.Status), formatTime(inputTime), formatTime(value.UpdatedAt))
		if err != nil {
			t.Fatal(err)
		}
	}
	if reopen {
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		dsn.RawQuery = "mode=ro"
		raw, err = sql.Open("sqlite", dsn.String())
		if err != nil {
			t.Fatal(err)
		}
		raw.SetMaxOpenConns(1)
		if err := raw.Ping(); err != nil {
			t.Fatal(err)
		}
	}
	return NewTaskPlanStore(&DB{sql: raw})
}

// The oracle compares time.Time values, never encoded timestamps or SQL.
func taskPlanReadOrderExpected(fixtures []taskplan.Plan, include func(taskplan.Plan) bool) []taskplan.Plan {
	values := make([]taskplan.Plan, 0)
	for _, value := range fixtures {
		if include(value) {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
	return values
}

func taskPlanReadOrderIDs(values []taskplan.Plan) []string {
	ids := make([]string, len(values))
	for i, value := range values {
		ids[i] = value.ID
	}
	return ids
}

// Diagnostics only: capture existing-index behavior, not a performance claim.
func taskPlanReadOrderQueryPlans(t *testing.T, raw *sql.DB) {
	t.Helper()
	for _, query := range []struct {
		name, tail, argument string
	}{
		{"list_before", " WHERE project_id=? ORDER BY created_at DESC,id", "isolation"},
		{"list_after", " WHERE project_id=? ORDER BY rtrim(created_at,'Z') DESC,id", "isolation"},
		{"latest_before", " WHERE task_id=? ORDER BY created_at DESC,id LIMIT 1", "target"},
		{"latest_after", " WHERE task_id=? ORDER BY rtrim(created_at,'Z') DESC,id LIMIT 1", "target"},
	} {
		rows, err := raw.Query("EXPLAIN QUERY PLAN "+selectTaskPlan+query.tail, query.argument)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			t.Logf("%s: %s", query.name, detail)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
