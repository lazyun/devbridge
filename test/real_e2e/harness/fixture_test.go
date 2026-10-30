package harness

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFixtureIsDeterministicCleanAndInspectable(t *testing.T) {
	ctx := context.Background()
	first, err := NewFixture(ctx, filepath.Join(t.TempDir(), "one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFixture(ctx, filepath.Join(t.TempDir(), "two"))
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseCommit != second.BaseCommit {
		t.Fatalf("base commits differ: %s != %s", first.BaseCommit, second.BaseCommit)
	}
	source, err := first.SourceStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if source.Status != "" || source.HEAD != first.BaseCommit || len(source.ChangedFiles) != 0 || source.Diff != "" {
		t.Fatalf("unexpected source snapshot: %+v", source)
	}
	target, err := first.NewExecutionTarget(ctx, "task-1-run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target.Path, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target.Path, "new.txt"), []byte("new evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := first.WorktreeStatus(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.ChangedFiles, []string{"new.txt", "tracked.txt"}) {
		t.Fatalf("changed files: %v", snapshot.ChangedFiles)
	}
	for _, want := range []string{"tracked.txt", "new.txt", "new evidence"} {
		if !strings.Contains(snapshot.Diff, want) {
			t.Fatalf("diff does not contain %q: %s", want, snapshot.Diff)
		}
	}
}

func TestFixturePreparesTwoIndependentExecutionTargets(t *testing.T) {
	ctx := context.Background()
	fixture, err := NewFixture(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	targets, err := fixture.TwoExecutionTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if targets[0].Path == targets[1].Path || targets[0].Branch == targets[1].Branch {
		t.Fatalf("targets are not isolated: %+v", targets)
	}
	if err := os.WriteFile(filepath.Join(targets[0].Path, "only-a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(targets[1].Path, "only-a.txt")); !os.IsNotExist(err) {
		t.Fatalf("target A change leaked to target B: %v", err)
	}
}

func TestFixtureRejectsUnsafeTargetNames(t *testing.T) {
	fixture, err := NewFixture(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.NewExecutionTarget(context.Background(), "../escape"); err == nil {
		t.Fatal("unsafe target name accepted")
	}
}

func TestFixtureRefusesToReuseExistingSourcePath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "source"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFixture(context.Background(), root); err == nil {
		t.Fatal("existing source path was reused")
	}
}
