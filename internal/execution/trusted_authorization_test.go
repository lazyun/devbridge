package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/project"
)

type stubAuthorizations struct {
	project.ExecutionAuthorization
	err error
}

func (s stubAuthorizations) Get(context.Context, string) (project.ExecutionAuthorization, error) {
	if s.err != nil {
		return project.ExecutionAuthorization{}, s.err
	}
	return s.ExecutionAuthorization, nil
}

func trustedAuthorization(trusted, network bool) stubAuthorizations {
	return stubAuthorizations{ExecutionAuthorization: project.ExecutionAuthorization{ProjectID: "project", TrustedExecution: trusted, NetworkAccess: network, HeadRevision: 3}}
}

// Trusted verification runs directly in the original worktree: no copy is
// created, the worktree is writable, ignored build outputs are allowed, and
// any tracked or non-ignored source change invalidates the run. The evidence
// stays in place — invalidation is reported, not reverted.
func TestTrustedVerificationRunsInOriginalWorktreeWithoutCopy(t *testing.T) {
	c, w := taskPermissionFixture(t)
	c.ProjectAuthorizations = trustedAuthorization(true, false)
	ctx := context.Background()
	originalHash := w.ContentHash
	source := w.Members[0].CanonicalRoot
	w, err := c.prepareTaskPermissions(ctx, w, true)
	if err != nil {
		t.Fatal(err)
	}
	if w.ContentHash != originalHash || w.TaskPermissions == nil || w.Validate() != nil {
		t.Fatal("engineering authority changed")
	}
	if !w.TaskPermissions.HasTrustedExecution() {
		t.Fatal("trusted snapshot missing")
	}
	if len(w.TaskPermissions.Copies) != 0 {
		t.Fatal("trusted verification must not create copies")
	}
	cwd, binding, err := multiRuntimeClientBinding(w)
	if err != nil {
		t.Fatal(err)
	}
	if cwd != source {
		t.Fatalf("trusted verification cwd must be the original worktree, got %s", cwd)
	}
	// The original worktree is writable alongside scratch.
	writable := false
	for _, root := range binding.WritableRoots {
		if root == source {
			writable = true
		}
	}
	if !writable {
		t.Fatalf("trusted verification must write the original worktree, got %v", binding.WritableRoots)
	}
	// Ignored build outputs are permitted and do not invalidate the run.
	if err = os.MkdirAll(filepath.Join(source, "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "dist", "bundle.js"), []byte("build"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = c.verifyTaskCopies(ctx, w); err != nil {
		t.Fatalf("ignored build output invalidated trusted run: %v", err)
	}
	if _, err = os.Stat(filepath.Join(source, "dist", "bundle.js")); err != nil {
		t.Fatal("ignored build output evidence disappeared")
	}
	// Modifying tracked source invalidates the run and the evidence stays.
	if err = os.WriteFile(filepath.Join(source, "source.txt"), []byte("secret fix"), 0644); err != nil {
		t.Fatal(err)
	}
	if c.verifyTaskCopies(ctx, w) == nil {
		t.Fatal("tracked source mutation accepted in trusted verification")
	}
	if b, _ := os.ReadFile(filepath.Join(source, "source.txt")); string(b) != "secret fix" {
		t.Fatal("invalidating evidence must stay in place, not revert")
	}
	// A remount/restart replays the same manifest without a fresh clone.
	again, err := c.prepareTaskPermissions(ctx, w, true)
	if err != nil || again.TaskPermissions.ContentHash != w.TaskPermissions.ContentHash {
		t.Fatalf("replay: %v", err)
	}
}

// Non-ignored untracked additions and tracked deletions also invalidate
// trusted verification; ignored untracked files do not.
func TestTrustedVerificationUntrackedAndDeletionInvalidates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(string) error
		wantErr bool
	}{
		{"untracked source file", func(root string) error {
			return os.WriteFile(filepath.Join(root, "sneaky.go"), []byte("x"), 0644)
		}, true},
		{"tracked deletion", func(root string) error {
			return os.Remove(filepath.Join(root, "source.txt"))
		}, true},
		{"ignored untracked file", func(root string) error {
			return os.WriteFile(filepath.Join(root, ".cache", "hit"), []byte("x"), 0600)
		}, false},
	} {
		c, w := taskPermissionFixture(t)
		c.ProjectAuthorizations = trustedAuthorization(true, false)
		ctx := context.Background()
		w, err := c.prepareTaskPermissions(ctx, w, true)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.MkdirAll(filepath.Join(w.Members[0].CanonicalRoot, ".cache"), 0700); err != nil {
			t.Fatal(err)
		}
		if err = tc.mutate(w.Members[0].CanonicalRoot); err != nil {
			t.Fatal(err)
		}
		err = c.verifyTaskCopies(ctx, w)
		if tc.wantErr && err == nil {
			t.Fatalf("%s accepted", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s rejected: %v", tc.name, err)
		}
	}
}

// Network-only authorization must not relax the filesystem: verification
// still copies, the source stays read-only, and the network flag is frozen
// with the project as its source.
func TestNetworkOnlyAuthorizationKeepsCopyFlow(t *testing.T) {
	c, w := taskPermissionFixture(t)
	c.ProjectAuthorizations = trustedAuthorization(false, true)
	ctx := context.Background()
	source := w.Members[0].CanonicalRoot
	w, err := c.prepareTaskPermissions(ctx, w, true)
	if err != nil {
		t.Fatal(err)
	}
	p := w.TaskPermissions
	if p.HasTrustedExecution() || p.TrustedExecution == nil {
		t.Fatal("network-only snapshot must exist and not be trusted")
	}
	if !p.NetworkAccess || !p.TrustedExecution.NetworkFromAuthorization {
		t.Fatal("project network grant not frozen")
	}
	if len(p.Copies) != 1 {
		t.Fatal("network-only verification must keep exactly one copy")
	}
	cwd, binding, err := multiRuntimeClientBinding(w)
	if err != nil {
		t.Fatal(err)
	}
	if cwd == source {
		t.Fatal("network-only verification must still run in the copy")
	}
	for _, root := range binding.WritableRoots {
		if root == source {
			t.Fatal("network-only verification must not write the source worktree")
		}
	}
}

// A trusted snapshot can never smuggle network in on its own; network needs
// the explicit project toggle.
func TestTrustedOnlyAuthorizationDoesNotGrantNetwork(t *testing.T) {
	c, w := taskPermissionFixture(t)
	c.ProjectAuthorizations = trustedAuthorization(true, false)
	w, err := c.prepareTaskPermissions(context.Background(), w, true)
	if err != nil {
		t.Fatal(err)
	}
	if w.TaskPermissions.NetworkAccess {
		t.Fatal("trusted-only grant must not enable network")
	}
}

// Store failures surface instead of silently running unauthorized.
func TestAuthorizationStoreErrorFailsClosed(t *testing.T) {
	c, w := taskPermissionFixture(t)
	c.ProjectAuthorizations = stubAuthorizations{err: errors.New("boom")}
	if _, err := c.prepareTaskPermissions(context.Background(), w, true); err == nil {
		t.Fatal("authorization store error must fail closed")
	}
}

// Reviews can never pick up project authority.
func TestReviewNeverGainsProjectAuthorization(t *testing.T) {
	c, w := taskPermissionFixture(t)
	c.ProjectAuthorizations = trustedAuthorization(true, true)
	w.TaskID, w.ExecutionID, w.TaskDefinitionRevisionID = "", "", ""
	w.ReviewID = "review"
	w, err := w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	w, err = c.prepareTaskPermissions(context.Background(), w, true)
	if err != nil {
		t.Fatal(err)
	}
	if w.TaskPermissions.TrustedExecution != nil || w.TaskPermissions.NetworkAccess {
		t.Fatal("scope review gained project authorization")
	}
}

func TestTrustedImplementationFreezesSnapshotAndBusinessScope(t *testing.T) {
	c, w := taskPermissionFixture(t)
	c.ProjectAuthorizations = trustedAuthorization(true, true)
	w.RunID = "implementation-run"
	w, err := w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	w, err = c.prepareTaskPermissions(context.Background(), w, false)
	if err != nil {
		t.Fatal(err)
	}
	p := w.TaskPermissions
	if p.TrustedExecution == nil || !p.TrustedExecution.Trusted || p.TrustedExecution.AuthorizationRevision != 3 {
		t.Fatalf("trusted implementation snapshot wrong: %+v", p.TrustedExecution)
	}
	if !p.NetworkAccess || !p.TrustedExecution.NetworkFromAuthorization {
		// The fixture definition carries no explicit network grant, so the
		// project must be recorded as the network source.
		t.Fatal("project network grant not frozen with correct attribution")
	}
	for _, grant := range p.Writes {
		if grant.Root == w.Members[0].CanonicalRoot && grant.Kind != "REPOSITORY" {
			t.Fatal("trusted run changed the approved business write scope")
		}
	}
}
