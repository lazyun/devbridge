package workset

import (
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimepermission"
)

func verificationBindingFixture(t *testing.T, trusted bool) RuntimeWorkset {
	t.Helper()
	value := runtimeFixture(t)
	base := "/managed/.task-runtime/run-" + runtimepermission.EnvironmentID("run-implementation")
	value.TaskPermissions = &runtimepermission.TaskPermissions{
		Version:     runtimepermission.TaskPermissionVersion,
		TaskID:      value.TaskID,
		RevisionID:  value.TaskDefinitionRevisionID,
		ExecutionID: value.ExecutionID,
		RunID:       value.RunID,
		WorksetHash: value.ContentHash,
		Purpose:     "VERIFICATION",
		ScratchRoot: "/managed/.task-runtime/run-" + runtimepermission.EnvironmentID("run-implementation") + "/scratch",
		TrustedExecution: &runtimepermission.TrustedExecutionSnapshot{
			ProjectID:             value.ProjectID,
			AuthorizationRevision: 1,
			Trusted:               trusted,
		},
	}
	if !trusted {
		// Network-only verification keeps the legacy copy flow.
		value.TaskPermissions.Copies = []runtimepermission.VerificationCopy{
			{SourceRoot: "/managed/app", Root: base + "/copy-" + runtimepermission.EnvironmentID("member-app"), SourceDigest: strings.Repeat("d", 64), StartSnapshot: strings.Repeat("b", 64), RootIdentity: "runtime-app"},
			{SourceRoot: "/managed/sdk", Root: base + "/copy-" + runtimepermission.EnvironmentID("member-sdk"), SourceDigest: strings.Repeat("e", 64), StartSnapshot: strings.Repeat("c", 64), RootIdentity: "runtime-sdk"},
		}
	}
	value.TaskPermissions.ContentHash = value.TaskPermissions.Hash()
	return value
}

// Trusted verification keeps the original member worktrees as cwd and writable
// roots instead of swapping in read-only copies.
func TestTrustedVerificationBindingUsesOriginalWorktrees(t *testing.T) {
	value := verificationBindingFixture(t, true)
	cwd, roots, writes := value.TaskPermissionBinding()
	if cwd != "/managed/app" {
		t.Fatalf("trusted verification cwd must stay the original worktree, got %s", cwd)
	}
	for _, root := range []string{"/managed/app", "/managed/sdk", value.TaskPermissions.ScratchRoot} {
		if !contains(roots, root) {
			t.Fatalf("missing root %s in %v", root, roots)
		}
		if !contains(writes, root) {
			t.Fatalf("trusted verification must write %s, got %v", root, writes)
		}
	}
	if len(writes) != 3 {
		t.Fatalf("trusted verification writes must be member roots + scratch only, got %v", writes)
	}
}

// A network-only snapshot must not widen verification: copies stay mandatory
// and the CWD moves to the primary member's copy exactly like legacy runs.
func TestNetworkOnlyVerificationKeepsCopies(t *testing.T) {
	value := verificationBindingFixture(t, false)
	base := "/managed/.task-runtime/run-" + runtimepermission.EnvironmentID("run-implementation")
	if value.TaskPermissions.HasTrustedExecution() {
		t.Fatal("network-only snapshot must not count as trusted execution")
	}
	cwd, roots, writes := value.TaskPermissionBinding()
	if cwd != base+"/copy-"+runtimepermission.EnvironmentID("member-app") {
		t.Fatalf("network-only verification cwd must stay the copy, got %s", cwd)
	}
	for _, root := range []string{"/managed/app", "/managed/sdk"} {
		if contains(writes, root) {
			t.Fatalf("network-only verification must not write source worktree %s", root)
		}
	}
	for _, copy := range value.TaskPermissions.Copies {
		if !contains(roots, copy.Root) || !contains(writes, copy.Root) {
			t.Fatalf("network-only verification must keep copy binding for %s", copy.Root)
		}
	}
	if err := value.validateTaskPermissions(); err != nil {
		t.Fatalf("network-only verification must validate with copies: %v", err)
	}
}

// The trusted binding must remain stable when the frozen manifest is replayed.
func TestTrustedVerificationBindingRoundTrip(t *testing.T) {
	value := verificationBindingFixture(t, true)
	_, roots, writes := value.TaskPermissionBinding()
	if err := value.TaskPermissions.Validate(roots, writes); err != nil {
		t.Fatalf("trusted binding fails policy validation: %v", err)
	}
	if err := value.validateTaskPermissions(); err != nil {
		t.Fatalf("trusted binding fails workset validation: %v", err)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}
