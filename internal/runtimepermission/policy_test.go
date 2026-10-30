package runtimepermission

import (
	"encoding/json"
	"strings"
	"testing"
)

func legacyManifest(t *testing.T, purpose string, mutate func(*TaskPermissions)) TaskPermissions {
	t.Helper()
	p := TaskPermissions{Version: TaskPermissionVersion, TaskID: "task", RevisionID: "rev", ExecutionID: "exec", RunID: "run-1", WorksetHash: strings.Repeat("a", 64), Purpose: purpose, ScratchRoot: "/tmp/.task-runtime/run-x/scratch"}
	if purpose == "IMPLEMENTATION" {
		p.Writes = []WriteGrant{{Root: "/repo", Kind: "REPOSITORY"}}
	}
	if mutate != nil {
		mutate(&p)
	}
	p.ContentHash = p.Hash()
	return p
}

// Legacy runs must keep their exact JSON encoding and hash: the trusted field
// is omitempty and nil on every pre-authorization manifest.
func TestTrustedFieldKeepsLegacyEncodingStable(t *testing.T) {
	p := legacyManifest(t, "IMPLEMENTATION", nil)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "trustedExecution") {
		t.Fatalf("legacy manifest gained a trusted field: %s", raw)
	}
	if p.Hash() != p.ContentHash {
		t.Fatal("legacy hash changed")
	}
	var decoded TaskPermissions
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TrustedExecution != nil {
		t.Fatal("decoded legacy manifest gained trusted snapshot")
	}
	if decoded.Validate([]string{"/repo", p.ScratchRoot}, []string{"/repo", p.ScratchRoot}) != nil {
		t.Fatal("legacy implementation manifest no longer validates")
	}
}

func trustedSnapshot(trusted, network bool) *TrustedExecutionSnapshot {
	return &TrustedExecutionSnapshot{ProjectID: "project", AuthorizationRevision: 1, Trusted: trusted, NetworkFromAuthorization: network}
}

func TestTrustedVerificationRejectsUnboundCopyAndFilesystemRoot(t *testing.T) {
	p := legacyManifest(t, "VERIFICATION", func(p *TaskPermissions) { p.TrustedExecution = trustedSnapshot(true, false) })
	roots := []string{"/repo", p.ScratchRoot}
	writes := []string{"/repo", p.ScratchRoot}
	if _, err := p.ProfileArgs(roots, writes); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProfileArgs([]string{"/", p.ScratchRoot}, []string{"/", p.ScratchRoot}); err == nil {
		t.Fatal("trusted grant installed a filesystem-root write")
	}
	p.Copies = []VerificationCopy{{SourceRoot: "/repo", Root: "/outside", OutputPaths: []string{"unexpected"}}}
	p.ContentHash = p.Hash()
	if _, err := p.ProfileArgs(roots, writes); err == nil {
		t.Fatal("trusted verification installed foreign copy output permissions")
	}
}

func TestTrustedImplementationValidation(t *testing.T) {
	p := legacyManifest(t, "IMPLEMENTATION", func(p *TaskPermissions) {
		p.TrustedExecution = trustedSnapshot(true, false)
	})
	roots := []string{"/repo", p.ScratchRoot}
	writable := []string{"/repo", p.ScratchRoot}
	if err := p.Validate(roots, writable); err != nil {
		t.Fatalf("trusted implementation manifest rejected: %v", err)
	}
	if !p.HasTrustedExecution() {
		t.Fatal("HasTrustedExecution must be true for a trusted snapshot")
	}
	// A writable root that is neither a member root nor scratch is rejected.
	if err := p.Validate(roots, []string{"/repo", p.ScratchRoot, "/other"}); err == nil {
		t.Fatal("unrelated writable root accepted")
	}
	args, err := p.ProfileArgs(roots, writable)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, `":tmpdir"="write"`) {
		t.Fatal("trusted profile must open the generic temp directory")
	}
	if !strings.Contains(joined, `":slash_tmp"="write"`) {
		t.Fatal("trusted profile must open /tmp")
	}
	for _, name := range []string{".git", ".codex", ".agents"} {
		if !strings.Contains(joined, `"/repo/`+name+`"="read"`) {
			t.Fatalf("trusted profile must keep %s read-only", name)
		}
	}
	if strings.Contains(joined, `":root"="write"`) {
		t.Fatal(":root must never be writable")
	}
	// Trusted execution must skip the legacy per-PATHS narrowing: with a PATHS
	// grant the whole worktree stays writable in the native ACL, and no
	// per-path write entries are emitted.
	p2 := legacyManifest(t, "IMPLEMENTATION", func(p *TaskPermissions) {
		p.Writes = []WriteGrant{{Root: "/repo", Kind: "PATHS", Paths: []string{"lib"}}}
		p.TrustedExecution = trustedSnapshot(true, false)
	})
	args2, err := p2.ProfileArgs(roots, writable)
	if err != nil {
		t.Fatal(err)
	}
	joined2 := strings.Join(args2, " ")
	if !strings.Contains(joined2, `"/repo"="write"`) {
		t.Fatal("trusted PATHS run must keep the whole worktree writable")
	}
	if strings.Contains(joined2, `"/repo/lib"="write"`) {
		t.Fatal("trusted run must not emit legacy per-path narrowing")
	}
}

// A network-only snapshot changes no filesystem semantics: legacy per-path
// narrowing stays, temp directories stay closed, verification keeps copies.
func TestNetworkOnlySnapshotKeepsLegacyFilesystem(t *testing.T) {
	p := legacyManifest(t, "IMPLEMENTATION", func(p *TaskPermissions) {
		p.Writes = []WriteGrant{{Root: "/repo", Kind: "PATHS", Paths: []string{"lib"}}}
		p.NetworkAccess = true
		p.TrustedExecution = trustedSnapshot(false, true)
	})
	if p.HasTrustedExecution() {
		t.Fatal("network-only snapshot must not count as trusted execution")
	}
	roots := []string{"/repo", p.ScratchRoot}
	writable := []string{"/repo", p.ScratchRoot}
	if err := p.Validate(roots, writable); err != nil {
		t.Fatalf("network-only implementation manifest rejected: %v", err)
	}
	// Legacy narrowing also requires writable to match the grants exactly.
	if err := p.Validate(roots, []string{"/repo", p.ScratchRoot}); err != nil {
		t.Fatalf("network-only run must accept legacy binding: %v", err)
	}
	args, err := p.ProfileArgs(roots, writable)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, `":tmpdir"="write"`) || strings.Contains(joined, `":slash_tmp"="write"`) {
		t.Fatal("network-only snapshot must not open temp directories")
	}
	if !strings.Contains(joined, `"/repo"="read"`) || !strings.Contains(joined, `"/repo/lib"="write"`) {
		t.Fatal("network-only snapshot must keep legacy per-path narrowing")
	}
	// Network is enabled and the instructions name the project grant.
	if !strings.Contains(joined, "network.enabled=true") {
		t.Fatal("network-only snapshot must enable network")
	}
	text := p.Instructions()
	if !strings.Contains(text, "project-level authorization") {
		t.Fatalf("network-only instructions must name the project source: %s", text)
	}
	// Network-only verification keeps the copy flow.
	v := legacyManifest(t, "VERIFICATION", func(p *TaskPermissions) {
		p.NetworkAccess = true
		p.TrustedExecution = trustedSnapshot(false, true)
		p.Copies = []VerificationCopy{{SourceRoot: "/repo", Root: "/copy", SourceDigest: strings.Repeat("d", 64), StartSnapshot: strings.Repeat("s", 40), RootIdentity: "id"}}
	})
	if err := v.Validate([]string{"/repo", "/copy", v.ScratchRoot}, []string{"/copy", v.ScratchRoot}); err != nil {
		t.Fatalf("network-only verification with copies rejected: %v", err)
	}
}

func TestTrustedVerificationValidation(t *testing.T) {
	p := legacyManifest(t, "VERIFICATION", func(p *TaskPermissions) {
		p.TrustedExecution = trustedSnapshot(true, false)
	})
	roots := []string{"/repo", p.ScratchRoot}
	writable := []string{"/repo", p.ScratchRoot}
	if err := p.Validate(roots, writable); err != nil {
		t.Fatalf("trusted verification manifest rejected: %v", err)
	}
	// Trusted verification has no copies and allows writing the original
	// worktree; a member root missing from writable is rejected.
	if err := p.Validate(roots, []string{p.ScratchRoot}); err == nil {
		t.Fatal("trusted verification without writable member worktree accepted")
	}
	// Review purpose can never be trusted.
	review := legacyManifest(t, "SCOPE_REVIEW", func(p *TaskPermissions) {
		p.ReviewID = "review"
		p.TaskID, p.RevisionID, p.ExecutionID = "", "", ""
		p.TrustedExecution = trustedSnapshot(true, false)
	})
	if err := review.Validate(roots, writable); err == nil {
		t.Fatal("trusted scope review accepted")
	}
	// A review carrying any project snapshot, even network-only, is rejected.
	reviewNetwork := legacyManifest(t, "SCOPE_REVIEW", func(p *TaskPermissions) {
		p.ReviewID = "review"
		p.TaskID, p.RevisionID, p.ExecutionID = "", "", ""
		p.NetworkAccess = true
		p.TrustedExecution = trustedSnapshot(false, true)
	})
	if err := reviewNetwork.Validate(roots, writable); err == nil {
		t.Fatal("network-only scope review accepted")
	}
	// Snapshot with revision 0 is invalid.
	bad := legacyManifest(t, "IMPLEMENTATION", func(p *TaskPermissions) {
		p.TrustedExecution = &TrustedExecutionSnapshot{ProjectID: "project", AuthorizationRevision: 0, Trusted: true}
	})
	if err := bad.Validate(roots, writable); err == nil {
		t.Fatal("trusted snapshot without durable revision accepted")
	}
	// NetworkFromAuthorization without effective network is inconsistent.
	badNetwork := legacyManifest(t, "IMPLEMENTATION", func(p *TaskPermissions) {
		p.TrustedExecution = trustedSnapshot(false, true)
	})
	if err := badNetwork.Validate(roots, writable); err == nil {
		t.Fatal("network-from-authorization without network accepted")
	}
}

func TestTrustedInstructions(t *testing.T) {
	p := legacyManifest(t, "VERIFICATION", func(p *TaskPermissions) {
		p.TrustedExecution = trustedSnapshot(true, false)
	})
	text := p.Instructions()
	if !strings.Contains(text, "trusted VERIFICATION") {
		t.Fatalf("trusted verification instructions missing: %s", text)
	}
	if !strings.Contains(text, "Git-ignored build outputs") {
		t.Fatalf("ignored-build-output guidance missing: %s", text)
	}
	// Trusted implementation must still state the original PATHS business
	// scope; the sandbox relaxation must not read as scope widening.
	impl := legacyManifest(t, "IMPLEMENTATION", func(p *TaskPermissions) {
		p.Writes = []WriteGrant{{Root: "/repo", Kind: "PATHS", Paths: []string{"lib"}}}
		p.TrustedExecution = trustedSnapshot(true, false)
	})
	implText := impl.Instructions()
	if !strings.Contains(implText, "the approved PATHS write grant still permits changes only at: lib") {
		t.Fatalf("trusted implementation must keep business PATHS scope text: %s", implText)
	}
	if !strings.Contains(implText, "does not widen the approved engineering scope") {
		t.Fatalf("trusted implementation must disclaim scope widening: %s", implText)
	}
	// A trusted run whose network came only from the task definition must not
	// imply the project authorization granted or changed it.
	definitionOnly := legacyManifest(t, "IMPLEMENTATION", func(p *TaskPermissions) {
		p.NetworkAccess = true
		p.TrustedExecution = trustedSnapshot(true, false)
	})
	defText := definitionOnly.Instructions()
	if !strings.Contains(defText, "granted by the frozen task definition") {
		t.Fatalf("definition-source network note missing: %s", defText)
	}
}
