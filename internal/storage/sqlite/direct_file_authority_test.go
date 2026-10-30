package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

func TestDirectFileAuthorityPinsCurrentWorkspaceAndGuardsRunAdmission(t *testing.T) {
	assertLegacyDirectFileScopeClosed(t, "authority-pin")
}

func directFileAuthorityFixture(t *testing.T, suffix string) (agentStorageFixture, *DirectFileAuthority, directfile.Scope, string) {
	t.Helper()
	f := newAgentStorageFixture(t, suffix)
	ctx := context.Background()
	op := prepareAgentOperation(t, f, agentconversation.Execution, suffix)
	r, err := op.make(ctx, "run-direct-file-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, r.ID, run.StatusSucceeded, "", "")
	if _, err = f.db.sql.Exec(`DELETE FROM feature_area_agent_claims WHERE feature_area_id=?`, f.area.ID); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "worktree")
	if err = os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(f.root, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	w := workspace.Workspace{ID: "direct-file-workspace-" + suffix, ProjectID: f.project.ID, RepositoryID: f.task.RepositoryID, FeatureAreaID: f.area.ID, BaseCommit: f.task.BaseCommit, Status: workspace.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}
	if err = NewWorkspaceStore(f.db).Create(ctx, w); err != nil {
		t.Fatal(err)
	}
	wt := worktree.Worktree{ID: "direct-file-worktree-" + suffix, RepositoryID: f.task.RepositoryID, RunID: r.ID, Path: path, Branch: "area", BaseCommit: f.task.BaseCommit, Status: worktree.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}
	if err = NewWorktreeStore(f.db).Create(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if _, err = NewWorkspaceStore(f.db).Update(ctx, w.ID, func(v *workspace.Workspace) error { v.WorktreeID = wt.ID; return nil }); err != nil {
		t.Fatal(err)
	}
	return f, NewDirectFileAuthority(f.db), directfile.Scope{AISessionID: f.session.ID, FeatureAreaID: f.area.ID, WorkflowID: "workflow-" + suffix}, path
}

func TestDirectFileAuthorityBlocksActualRunAndDisableRestoresIt(t *testing.T) {
	assertLegacyDirectFileScopeClosed(t, "run-guard")
}

func TestDirectFileAuthorityLifecycleFailuresAndDefaults(t *testing.T) {
	assertLegacyDirectFileScopeClosed(t, "lifecycle")
}

func TestDirectFileAuthorityActiveRunRejectsEnableAndConcurrentXOR(t *testing.T) {
	assertLegacyDirectFileScopeClosed(t, "xor")
}

func TestDirectFileAuthorityRejectsActiveRunAndCloseClearsEphemeralGrant(t *testing.T) {
	assertLegacyDirectFileScopeClosed(t, "active-and-close")
}
