package workset

import (
	"strings"
	"testing"
	"time"
)

func runtimeFixture(t *testing.T) RuntimeWorkset {
	t.Helper()
	commit := strings.Repeat("a", 40)
	value := RuntimeWorkset{
		ProtocolVersion:          RuntimeProtocolVersion,
		ProjectID:                "project-1",
		FeatureAreaID:            "area-1",
		TaskID:                   "task-1",
		TaskDefinitionRevisionID: "revision-1",
		ExecutionID:              "execution-1",
		RunID:                    "run-implementation",
		WorkspaceID:              "workspace-1",
		PrimaryMemberID:          "member-app",
		Members: []RuntimeMemberBinding{
			{RepositoryID: "repository-app", RepositoryAlias: "app", MemberID: "member-app", MemberVersion: 3, WorktreeID: "worktree-app", CanonicalRoot: "/managed/app", RootFileIdentity: "root-app", OriginCommit: commit, StartHEAD: commit, StartSnapshot: strings.Repeat("b", 64), Read: true, Write: WriteScope{Kind: "PATHS", Paths: []string{"lib"}}, RuntimeRootIdentity: "runtime-app"},
			{RepositoryID: "repository-sdk", RepositoryAlias: "sdk", MemberID: "member-sdk", MemberVersion: 5, WorktreeID: "worktree-sdk", CanonicalRoot: "/managed/sdk", RootFileIdentity: "root-sdk", OriginCommit: commit, StartHEAD: commit, StartSnapshot: strings.Repeat("c", 64), Read: true, Write: WriteScope{Kind: "NONE"}, RuntimeRootIdentity: "runtime-sdk"},
		},
		BackendConfigID:          "config-1",
		BackendConfigFingerprint: strings.Repeat("d", 64),
		Model:                    "model-1",
		ReasoningEffort:          "high",
		RuntimeAdapterIdentity:   "codex-app-server",
		RuntimeProtocolIdentity:  "0.149.0",
		CreatedAt:                time.Unix(1_800_000_000, 0).UTC(),
	}
	value, err := value.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestRuntimeWorksetContentIdentitySpansImplementationAndVerificationRuns(t *testing.T) {
	implementation := runtimeFixture(t)
	verification := implementation
	verification.RunID = "run-verification"
	verification.CreatedAt = implementation.CreatedAt.Add(time.Minute)
	verification.Members = cloneRuntimeMembers(implementation.Members)
	verification.Members[0].StartHEAD = strings.Repeat("e", 40)
	verification.Members[0].StartSnapshot = strings.Repeat("f", 64)
	verification.ContentHash = ""
	verification, err := verification.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if verification.ContentHash != implementation.ContentHash {
		t.Fatalf("same frozen layout changed identity: implementation=%s verification=%s", implementation.ContentHash, verification.ContentHash)
	}
	verification.Members[1].CanonicalRoot = "/managed/replacement-sdk"
	if err := verification.Validate(); err == nil || !strings.Contains(err.Error(), "content hash mismatch") {
		t.Fatalf("root drift was not fenced: %v", err)
	}
}

func TestRuntimeWorksetRequiresExplicitPrimaryInsideExactSet(t *testing.T) {
	value := runtimeFixture(t)
	value.PrimaryMemberID = ""
	if _, err := value.WithHash(); err == nil {
		t.Fatal("missing primary member was accepted")
	}
	value = runtimeFixture(t)
	value.PrimaryMemberID = "member-foreign"
	if _, err := value.WithHash(); err == nil {
		t.Fatal("foreign primary member was accepted")
	}
}

func TestRuntimeWorksetHashPreservesMemberOrderAndWriteScope(t *testing.T) {
	value := runtimeFixture(t)
	reversed := value
	reversed.Members = append([]RuntimeMemberBinding(nil), value.Members...)
	reversed.Members[0], reversed.Members[1] = reversed.Members[1], reversed.Members[0]
	reversed.ContentHash = ""
	reversed, err := reversed.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if reversed.ContentHash == value.ContentHash {
		t.Fatal("ordered member layout was normalized away")
	}

	changedScope := value
	changedScope.Members = cloneRuntimeMembers(value.Members)
	changedScope.Members[0].Write.Paths = []string{"cmd"}
	changedScope.ContentHash = ""
	changedScope, err = changedScope.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if changedScope.ContentHash == value.ContentHash {
		t.Fatal("write scope was omitted from workset identity")
	}
}

func TestProjectInspectionRuntimeWorksetUsesV2OwnerWithoutV1Fields(t *testing.T) {
	commit := strings.Repeat("a", 40)
	value := RuntimeWorkset{
		ProtocolVersion: RuntimeProtocolVersionProjectInspection,
		OwnerKind:       RuntimeOwnerProjectInspection, ProjectID: "project", InspectionOwnerTaskID: "inspection-task", InspectionOperationID: "probe-1",
		RunID: "inspection-run", WorkspaceID: "workspace-inspection", PrimaryMemberID: "member-app",
		Members:         []RuntimeMemberBinding{{RepositoryID: "repo-app", RepositoryAlias: "app", MemberID: "member-app", MemberVersion: 1, WorktreeID: "wt-app", CanonicalRoot: "/managed/inspection/app", RootFileIdentity: "root", OriginCommit: commit, StartHEAD: commit, StartSnapshot: strings.Repeat("b", 64), Read: true, IntendedReadScope: ReadScope{Kind: "REPOSITORY"}, Write: WriteScope{Kind: "NONE"}, RuntimeRootIdentity: "identity"}},
		BackendConfigID: "config", BackendConfigFingerprint: strings.Repeat("c", 64), Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: "codex", RuntimeProtocolIdentity: "0.149.0", CreatedAt: time.Now().UTC(),
	}
	value, err := value.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if value.ContentHash == "" || value.Validate() != nil {
		t.Fatalf("v2 did not validate: %+v", value)
	}
	changed := value
	changed.RunID = "inspection-run-replay"
	changed.CreatedAt = changed.CreatedAt.Add(time.Minute)
	changed.ContentHash = ""
	changed, err = changed.WithHash()
	if err != nil || changed.ContentHash != value.ContentHash {
		t.Fatalf("per-run facts changed frozen v2 authority: %v %#v", err, changed)
	}
	value.FeatureAreaID = "forbidden"
	value.ContentHash = ""
	if _, err = value.WithHash(); err == nil {
		t.Fatal("v2 accepted FeatureArea identity")
	}
	value = changed
	value.Members[0].Write = WriteScope{Kind: "PATHS", Paths: []string{"x"}}
	value.ContentHash = ""
	if _, err = value.WithHash(); err == nil {
		t.Fatal("v2 accepted write scope")
	}
}

func TestProjectInspectionReadScopeIsRequiredAndFrozenInV2Hash(t *testing.T) {
	base := projectInspectionRuntimeFixture(t)
	paths := base
	paths.Members = cloneRuntimeMembers(base.Members)
	paths.Members[0].IntendedReadScope = ReadScope{Kind: "PATHS", Paths: []string{"cmd", "internal/api"}}
	paths.ContentHash = ""
	paths, err := paths.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if paths.ContentHash == base.ContentHash {
		t.Fatal("v2 hash omitted confirmed read scope")
	}
	invalid := paths
	invalid.Members = cloneRuntimeMembers(paths.Members)
	invalid.Members[0].IntendedReadScope = ReadScope{Kind: "PATHS", Paths: []string{"../escape"}}
	invalid.ContentHash = ""
	if _, err = invalid.WithHash(); err == nil {
		t.Fatal("v2 accepted escaping read path")
	}
	missing := base
	missing.Members = cloneRuntimeMembers(base.Members)
	missing.Members[0].IntendedReadScope = ReadScope{}
	missing.ContentHash = ""
	if _, err = missing.WithHash(); err == nil {
		t.Fatal("v2 accepted missing confirmed read scope")
	}
	// v1 deliberately ignores v2 scope material so its frozen encoding remains
	// byte-identical for persisted v77 rows.
	v1 := runtimeFixture(t)
	v1WithV2Field := v1
	v1WithV2Field.Members = cloneRuntimeMembers(v1.Members)
	v1WithV2Field.Members[0].IntendedReadScope = ReadScope{Kind: "PATHS", Paths: []string{"new-v2-only"}}
	v1WithV2Field.ContentHash = ""
	v1WithV2Field, err = v1WithV2Field.WithHash()
	if err != nil || v1WithV2Field.ContentHash != v1.ContentHash {
		t.Fatalf("v1 hash changed for v2-only field: %v", err)
	}
}

func projectInspectionRuntimeFixture(t *testing.T) RuntimeWorkset {
	t.Helper()
	commit := strings.Repeat("a", 40)
	value := RuntimeWorkset{ProtocolVersion: RuntimeProtocolVersionProjectInspection, OwnerKind: RuntimeOwnerProjectInspection, ProjectID: "project", InspectionOwnerTaskID: "inspection-task", InspectionOperationID: "probe", RunID: "run", WorkspaceID: "workspace", PrimaryMemberID: "member", Members: []RuntimeMemberBinding{{RepositoryID: "repo", RepositoryAlias: "repo", MemberID: "member", MemberVersion: 1, WorktreeID: "wt", CanonicalRoot: "/managed/inspection", RootFileIdentity: "root", OriginCommit: commit, StartHEAD: commit, StartSnapshot: strings.Repeat("b", 64), Read: true, IntendedReadScope: ReadScope{Kind: "REPOSITORY"}, Write: WriteScope{Kind: "NONE"}, RuntimeRootIdentity: "identity"}}, BackendConfigID: "config", BackendConfigFingerprint: strings.Repeat("c", 64), Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: "codex", RuntimeProtocolIdentity: "0.149.0", CreatedAt: time.Now().UTC()}
	value, err := value.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
