package workset

import (
	"strings"
	"testing"
	"time"
)

func TestRecoveryStatusRuntimeWorksetHasIndependentReadOnlyOwner(t *testing.T) {
	commit := strings.Repeat("a", 40)
	w := RuntimeWorkset{ProtocolVersion: RuntimeProtocolVersionRecoveryStatus, OwnerKind: RuntimeOwnerRecoveryStatus, ProjectID: "project", TaskID: "task", RecoveryStatusID: "recovery", RecoverySourceRunID: "source-run", RunID: "child-run", WorkspaceID: "workspace", PrimaryMemberID: "member", Members: []RuntimeMemberBinding{{RepositoryID: "repository", RepositoryAlias: "app", MemberID: "member", MemberVersion: 1, WorktreeID: "fresh-worktree", CanonicalRoot: "/managed/recovery", RootFileIdentity: "fresh-root", OriginCommit: commit, StartHEAD: commit, StartSnapshot: commit, Read: true, Write: WriteScope{Kind: "NONE"}, RuntimeRootIdentity: "runtime-root"}}, BackendConfigID: "backend", BackendConfigFingerprint: strings.Repeat("b", 64), Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: "adapter", RuntimeProtocolIdentity: "protocol", CreatedAt: time.Now().UTC()}
	var err error
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	changed := w
	changed.Members = append([]RuntimeMemberBinding(nil), w.Members...)
	changed.Members[0].Write = WriteScope{Kind: "REPOSITORY"}
	if _, err = changed.WithHash(); err == nil {
		t.Fatal("Recovery Workset gained write authority")
	}
	changed = w
	changed.ExecutionID = "source-execution"
	if _, err = changed.WithHash(); err == nil {
		t.Fatal("Recovery Workset impersonated source Execution")
	}
	changed = w
	changed.RecoverySourceRunID = "other-source"
	if err = changed.Validate(); err == nil {
		t.Fatal("Recovery source identity changed without hash mismatch")
	}
}
