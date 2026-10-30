package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/git"
)

func TestMergeCommitCoverageChecksEveryAcceptedCommit(t *testing.T) {
	repo := t.TempDir()
	commitTestGit(t, repo, "init", "-b", "develop")
	commitTestGit(t, repo, "config", "user.name", "Test")
	commitTestGit(t, repo, "config", "user.email", "test@example.invalid")
	commitTestWrite(t, filepath.Join(repo, "base"), "base")
	commitTestGit(t, repo, "add", ".")
	commitTestGit(t, repo, "commit", "-m", "base")
	base := commitTestGit(t, repo, "rev-parse", "HEAD")
	commitTestWrite(t, filepath.Join(repo, "first"), "first task")
	commitTestGit(t, repo, "add", ".")
	commitTestGit(t, repo, "commit", "-m", "first")
	first := commitTestGit(t, repo, "rev-parse", "HEAD")
	commitTestWrite(t, filepath.Join(repo, "second"), "second task")
	commitTestGit(t, repo, "add", ".")
	commitTestGit(t, repo, "commit", "-m", "second")
	second := commitTestGit(t, repo, "rev-parse", "HEAD")
	g := git.NewClient(git.Config{})
	for _, tc := range []struct {
		name, source, target string
		refused              bool
	}{
		{"partial-source", first, base, true},
		{"complete-source", second, base, false},
		{"already-in-target", first, second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := delivery.FeatureAreaMember{SourcePath: repo, TargetPath: repo, SourceHead: tc.source, ExpectedDevHead: tc.target, TaskCommits: []string{first, second}}
			err := validateMergeCommitCoverage(context.Background(), g, m)
			if tc.refused {
				if !errors.Is(err, git.ErrRecoveryRequired) {
					t.Fatalf("missing accepted commit not refused: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
