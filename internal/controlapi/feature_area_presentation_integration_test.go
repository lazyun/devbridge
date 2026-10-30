package controlapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"

	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/featurearea"
)

func TestFeatureAreaPresentationForSessionRejectsForeignProject(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	ctx := f.ctx
	foreignProject, err := f.plane.Projects.Create(ctx, "Foreign project")
	if err != nil {
		t.Fatal(err)
	}
	foreignSessionID := f.bindProject(t, foreignProject.ID, "foreign-presentation")
	_, err = f.facade.FeatureAreaPresentationForSession(ctx, foreignSessionID, f.areaID)
	var apiErr *controlapi.Error
	if !errors.As(err, &apiErr) || apiErr.Code != controlapi.CodeNotFound {
		t.Fatalf("foreign project error=%v, want not found", err)
	}
}

func initializePresentationRepository(t *testing.T, f ownershipWorkflowFixture) {
	t.Helper()
	ctx := context.Background()
	repository, err := f.plane.Repositories.Get(ctx, f.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repository.Path+"/tracked.txt", []byte("presentation fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"add", "tracked.txt"},
		{"-c", "user.name=Dev Bridge Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixture"},
	} {
		command := exec.Command("git", args...)
		command.Dir = repository.Path
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
}

type featureAreaPresentationCount struct {
	reviews, tasks, plans, executions, runs, decisions, cancellations, recoveries, developmentCycles int
	fingerprint                                                                                      string
}

func featureAreaPresentationCounts(t *testing.T, ctx context.Context, f ownershipWorkflowFixture) featureAreaPresentationCount {
	t.Helper()
	var counts featureAreaPresentationCount
	err := f.plane.DB.SQL().QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM reviews),
  (SELECT COUNT(*) FROM tasks),
  (SELECT COUNT(*) FROM task_plans),
  (SELECT COUNT(*) FROM execution_proposals),
  (SELECT COUNT(*) FROM runs),
  (SELECT COUNT(*) FROM decision_resolution_receipts),
	  (SELECT COUNT(*) FROM execution_cancellations),
	  (SELECT COUNT(*) FROM agent_operation_recoveries),
  (SELECT COUNT(*) FROM development_cycles)`).Scan(
		&counts.reviews, &counts.tasks, &counts.plans,
		&counts.executions, &counts.runs, &counts.decisions, &counts.cancellations, &counts.recoveries, &counts.developmentCycles,
	)
	if err != nil {
		t.Fatal(err)
	}
	// Compare contents as well as counts: a pure Refresh must not alter an
	// existing receipt, lifecycle, epoch, or provenance field in place.
	hash := sha256.New()
	for _, table := range []string{"reviews", "tasks", "task_plans", "execution_proposals", "runs", "decision_resolution_receipts", "execution_cancellations", "agent_operation_recoveries", "development_cycles"} {
		rows, err := f.plane.DB.SQL().QueryContext(ctx, "SELECT * FROM "+table+" ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			hash.Write([]byte(table))
			hash.Write(encoded)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	counts.fingerprint = hex.EncodeToString(hash.Sum(nil))
	return counts
}

func assertFeatureAreaPresentationCounts(t *testing.T, ctx context.Context, f ownershipWorkflowFixture, want featureAreaPresentationCount) {
	t.Helper()
	if got := featureAreaPresentationCounts(t, ctx, f); got != want {
		t.Fatalf("FeatureArea presentation mutated durable workflow records: got=%+v want=%+v", got, want)
	}
}
