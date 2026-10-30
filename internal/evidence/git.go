package evidence

import (
	"context"
	"errors"
	"fmt"

	bridgegit "github.com/lazyun/devbridge/internal/git"
)

type GitClient interface {
	ResolveHEAD(context.Context, string) (string, error)
	Status(context.Context, string) (bridgegit.Status, error)
	ChangedFiles(context.Context, string, string) ([]string, error)
	Diff(context.Context, string, string) (string, error)
	Snapshot(context.Context, string) (string, error)
	ChangedFilesBetween(context.Context, string, string, string) ([]string, error)
	DiffSnapshots(context.Context, string, string, string) (string, error)
}

func (c *Collector) CaptureSnapshot(ctx context.Context, worktreePath string) (string, error) {
	if c == nil || c.git == nil {
		return "", errors.New("Git evidence client is required")
	}
	value, err := c.git.Snapshot(ctx, worktreePath)
	if err != nil {
		return "", fmt.Errorf("capture Git worktree snapshot: %w", err)
	}
	return value, nil
}

// CollectRun keeps cumulative Task-base Git truth in BaseCommit/HeadCommit and
// Dirty, while ChangedFiles and Diff are strictly the immutable start-to-end
// delta for this Run.
func (c *Collector) CollectRun(ctx context.Context, worktreePath, baseCommit, startSnapshot, endSnapshot string) (GitEvidence, error) {
	if c == nil || c.git == nil {
		return GitEvidence{}, errors.New("Git evidence client is required")
	}
	head, err := c.git.ResolveHEAD(ctx, worktreePath)
	if err != nil {
		return GitEvidence{}, fmt.Errorf("collect Git evidence head: %w", err)
	}
	status, err := c.git.Status(ctx, worktreePath)
	if err != nil {
		return GitEvidence{}, fmt.Errorf("collect Git evidence status: %w", err)
	}
	files, err := c.git.ChangedFilesBetween(ctx, worktreePath, startSnapshot, endSnapshot)
	if err != nil {
		return GitEvidence{}, fmt.Errorf("collect per-Run changed files: %w", err)
	}
	diff, err := c.git.DiffSnapshots(ctx, worktreePath, startSnapshot, endSnapshot)
	if err != nil {
		return GitEvidence{}, fmt.Errorf("collect per-Run diff: %w", err)
	}
	return GitEvidence{BaseCommit: baseCommit, HeadCommit: head, StartSnapshot: startSnapshot, EndSnapshot: endSnapshot, Dirty: status.Dirty(), ChangedFiles: files, Diff: diff}, nil
}

type Collector struct{ git GitClient }

func NewCollector(git GitClient) *Collector { return &Collector{git: git} }

func (c *Collector) Collect(ctx context.Context, worktreePath, baseCommit string) (GitEvidence, error) {
	if c == nil || c.git == nil {
		return GitEvidence{}, errors.New("Git evidence client is required")
	}
	head, err := c.git.ResolveHEAD(ctx, worktreePath)
	if err != nil {
		return GitEvidence{}, fmt.Errorf("collect Git evidence head: %w", err)
	}
	status, err := c.git.Status(ctx, worktreePath)
	if err != nil {
		return GitEvidence{}, fmt.Errorf("collect Git evidence status: %w", err)
	}
	files, err := c.git.ChangedFiles(ctx, worktreePath, baseCommit)
	if err != nil {
		return GitEvidence{}, fmt.Errorf("collect Git evidence changed files: %w", err)
	}
	diff, err := c.git.Diff(ctx, worktreePath, baseCommit)
	if err != nil {
		return GitEvidence{}, fmt.Errorf("collect Git evidence diff: %w", err)
	}
	return GitEvidence{BaseCommit: baseCommit, HeadCommit: head, Dirty: status.Dirty(), ChangedFiles: files, Diff: diff}, nil
}
