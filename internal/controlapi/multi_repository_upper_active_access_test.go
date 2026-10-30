package controlapi_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/repository"
)

func TestMultiRepositoryUpperActiveGrantIsReadOnlyAndMemberExact(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	wantContent := map[string]string{f.repos["app"]: "app source\n", f.repos["sdk"]: "sdk source\n"}
	seenWorktrees := map[string]bool{}

	for _, target := range f.targets {
		listed, err := f.facade.UpperReviewList(f.ctx, directfile.Input{ReviewTarget: &f.access.Target, RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, Path: "."})
		if err != nil {
			t.Fatalf("list member %+v: %v", target, err)
		}
		if listed.RepositoryID != target.RepositoryID || listed.MemberID != target.MemberID || listed.MemberVersion != target.MemberVersion || listed.SourceKind != "MANAGED_WORKTREE" || listed.WorkspaceID != f.access.WorkspaceID || listed.WorktreeID == "" || listed.OriginCommit != target.OriginCommit || listed.StartSnapshot == "" {
			t.Fatalf("list lost member source identity target=%+v result=%+v", target, listed)
		}
		if seenWorktrees[listed.WorktreeID] {
			t.Fatalf("two members collapsed to worktree %s", listed.WorktreeID)
		}
		seenWorktrees[listed.WorktreeID] = true
		found := false
		for _, entry := range listed.Entries {
			if entry.Name == "same.txt" && entry.Type == "file" {
				found = true
			}
		}
		if !found {
			t.Fatalf("member root list missing same.txt: %+v", listed)
		}

		read, err := f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt"))
		if err != nil || read.Content != wantContent[target.RepositoryID] || read.RepositoryID != target.RepositoryID || read.MemberID != target.MemberID || read.WorktreeID != listed.WorktreeID {
			t.Fatalf("member read crossed root target=%+v result=%+v err=%v", target, read, err)
		}
	}
	if len(seenWorktrees) != 2 {
		t.Fatalf("distinct member worktrees=%v", seenWorktrees)
	}

	first := f.targets[0]
	base := f.inputFor(first, "same.txt")
	writes := []struct {
		name string
		call func(context.Context, directfile.Input) (directfile.Result, error)
		in   directfile.Input
	}{
		{name: "create", call: f.facade.FileCreate, in: directfile.Input{ReviewTarget: &f.access.Target, RepositoryID: first.RepositoryID, MemberID: first.MemberID, MemberVersion: first.MemberVersion, Path: "created.txt", Content: "forbidden"}},
		{name: "replace", call: f.facade.FileReplace, in: directfile.Input{ReviewTarget: &f.access.Target, RepositoryID: first.RepositoryID, MemberID: first.MemberID, MemberVersion: first.MemberVersion, Path: "same.txt", Content: "forbidden"}},
		{name: "delete", call: f.facade.FileDelete, in: base},
		{name: "move", call: f.facade.FileMove, in: directfile.Input{ReviewTarget: &f.access.Target, RepositoryID: first.RepositoryID, MemberID: first.MemberID, MemberVersion: first.MemberVersion, Path: "same.txt", Destination: "moved.txt", DestinationRepositoryID: first.RepositoryID, DestinationMemberID: first.MemberID}},
	}
	for _, tc := range writes {
		if _, err := tc.call(f.ctx, tc.in); err == nil {
			t.Fatalf("ACTIVE Upper grant authorized %s", tc.name)
		}
	}
	after, err := f.facade.UpperReviewRead(f.ctx, base)
	if err != nil || after.Content != wantContent[first.RepositoryID] {
		t.Fatalf("denied writes changed source: result=%+v err=%v", after, err)
	}
	var firstRoot string
	if err = f.plane.DB.SQL().QueryRow(`SELECT wt.path FROM run_repository_bindings b JOIN worktrees wt ON wt.id=b.worktree_id WHERE b.run_id=? AND b.repository_id=?`, f.run, first.RepositoryID).Scan(&firstRoot); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"created.txt", "moved.txt"} {
		if _, statErr := os.Stat(filepath.Join(firstRoot, name)); !os.IsNotExist(statErr) {
			t.Fatalf("denied write materialized %s: %v", name, statErr)
		}
	}

	// Generic repository reads/searches are deliberately unavailable while this
	// formal Upper Review owns the Project; callers must use exact member tools.
	for _, repositoryID := range []string{f.repos["app"], f.repos["sdk"], f.repos["backend"]} {
		if _, err = f.facade.SearchRepositoryForSession(f.ctx, f.session, repositoryID, "source", nil, 10); err == nil {
			t.Fatalf("generic search bypassed active grant for repository %s", repositoryID)
		}
		if _, err = f.facade.ReadRepositoryFileForSession(f.ctx, f.session, repositoryID, repository.FileReadInput{Path: "same.txt"}); err == nil {
			t.Fatalf("generic repository read bypassed active grant for repository %s", repositoryID)
		}
	}

	outside := t.TempDir()
	if err = os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(firstRoot, "escape")); err != nil {
		t.Fatal(err)
	}
	escape := base
	escape.Path = "escape/secret.txt"
	if _, err = f.facade.UpperReviewRead(f.ctx, escape); err == nil {
		t.Fatal("symlink child escaped the pinned member root")
	}
}
