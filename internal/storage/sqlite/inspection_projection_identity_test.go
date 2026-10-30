package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/run"
)

func TestInspectionSyncFailsClosedOnIdentityMismatch(t *testing.T) {
	f := newAgentStorageFixture(t, "inspection-projection-identity")
	ctx := context.Background()
	probes := NewContextProbeStore(f.db)
	create := func(id string) contextprobe.Probe {
		t.Helper()
		p := contextprobe.Probe{ID: id, AISessionID: f.session.ID, ProjectID: f.project.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository, Paths: []string{}}, CodexInstruction: "inspect", Status: contextprobe.StatusCreated, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}, CreatedAt: testNow, UpdatedAt: testNow}
		if err := probes.Create(ctx, p); err != nil {
			t.Fatal(err)
		}
		p, err := probes.Update(ctx, id, func(v *contextprobe.Probe) error {
			v.Status = contextprobe.StatusRunning
			v.StartedAt = &testNow
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := create("inspection-projection-first")
	if _, err := probes.Update(ctx, first.ID, func(v *contextprobe.Probe) error {
		v.Status = contextprobe.StatusSucceeded
		v.FinishedAt = &testNow
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second := create("inspection-projection-second")
	first.RunID = second.RunID
	first.TaskID = second.TaskID
	tx, err := f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = probes.syncInspectionRunTx(ctx, tx, &first)
	if !errors.Is(err, contextprobe.ErrConflict) {
		t.Fatalf("cross-owner projection error = %v, want conflict", err)
	}
}
