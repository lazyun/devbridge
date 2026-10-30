package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrSnapshotDrift = errors.New("TASK_COMMIT_SNAPSHOT_DRIFT")
var ErrCommitIncomplete = errors.New("TASK_COMMIT_INCOMPLETE")
var ErrRecoveryRequired = errors.New("RECOVERY_REQUIRED")
var ErrMergeConflict = errors.New("MERGE_CONFLICT")

// BeforeEffectError is issued only by the Git adapter before it dispatches a
// mutating command. Observers never issue it: unchanged HEAD is not proof that
// an earlier process did not run. Core persists this proof against its exact
// dispatch identity before permitting cancellation or a new retry authority.
type BeforeEffectError struct{ Err error }

func (e *BeforeEffectError) Error() string { return e.Err.Error() }
func (e *BeforeEffectError) Unwrap() error { return e.Err }

// NoOperation rejects sequencers before any staging or ref mutation.
func (c *Client) NoOperation(ctx context.Context, path string) error {
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer", "index.lock"} {
		out, err := c.run(ctx, path, "inspect Git operation", "rev-parse", "--path-format=absolute", "--git-path", name)
		if err != nil {
			return err
		}
		_, err = os.Lstat(strings.TrimSpace(string(out)))
		if err == nil {
			return fmt.Errorf("GIT_OPERATION_IN_PROGRESS: %s", name)
		}
		if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (c *Client) CommitParents(ctx context.Context, path, head string) ([]string, error) {
	out, err := c.run(ctx, path, "read commit parents", "show", "-s", "--format=%P", head)
	return strings.Fields(string(out)), err
}
func (c *Client) CommitTree(ctx context.Context, path, head string) (string, error) {
	out, err := c.run(ctx, path, "read commit tree", "rev-parse", "--verify", head+"^{tree}")
	return strings.TrimSpace(string(out)), err
}
func (c *Client) RequireClean(ctx context.Context, path string) error {
	clean, err := c.IsClean(ctx, path)
	if err != nil {
		return err
	}
	if !clean {
		return errors.New("WORKSPACE_DIRTY")
	}
	return nil
}

// FinalizeTask replays only a single exact parent/tree/trailer result. The caller
// must durably record preHead and snapshot and exclude other workspace writers.
func (c *Client) FinalizeTask(ctx context.Context, path, preHead, snapshot, taskID, revision, message string) (string, error) {
	return c.finalizeTask(ctx, path, preHead, snapshot, taskID, revision, message, true)
}

// ObserveTask reads only the exact original commit result. An unchanged HEAD
// cannot prove that an admitted external effect was never dispatched.
func (c *Client) ObserveTask(ctx context.Context, path, preHead, snapshot, taskID, revision, message string) (string, error) {
	return c.finalizeTask(ctx, path, preHead, snapshot, taskID, revision, message, false)
}

func (c *Client) finalizeTask(ctx context.Context, path, preHead, snapshot, taskID, revision, message string, allowCommit bool) (result string, resultErr error) {
	dispatched := false
	defer func() {
		if allowCommit && !dispatched && resultErr != nil {
			resultErr = &BeforeEffectError{Err: resultErr}
		}
	}()
	if err := c.NoOperation(ctx, path); err != nil {
		return "", err
	}
	head, err := c.ResolveHEAD(ctx, path)
	if err != nil {
		return "", err
	}
	trailers := "Dev-Bridge-Task: " + taskID + "\nDev-Bridge-Task-Revision: " + revision
	if head != preHead {
		parents, e := c.CommitParents(ctx, path, head)
		if e != nil {
			return "", e
		}
		tree, e := c.CommitTree(ctx, path, head)
		if e != nil {
			return "", e
		}
		body, e := c.run(ctx, path, "read task commit receipt", "show", "-s", "--format=%B", head)
		if e != nil {
			return "", e
		}
		if len(parents) != 1 || parents[0] != preHead || tree != snapshot || strings.TrimSpace(string(body)) != message+"\n\n"+trailers {
			return "", ErrRecoveryRequired
		}
		if e = c.RequireClean(ctx, path); e != nil {
			return "", ErrCommitIncomplete
		}
		return head, nil
	}
	if !allowCommit {
		return "", ErrRecoveryRequired
	}
	fresh, err := c.Snapshot(ctx, path)
	if err != nil {
		return "", err
	}
	if fresh != snapshot {
		return "", ErrSnapshotDrift
	}
	// An index may be unchanged from HEAD or already fully staged by a previous
	// attempt. Partial/foreign staged content is not silently overwritten.
	index, err := c.run(ctx, path, "inspect task index", "write-tree")
	if err != nil {
		return "", err
	}
	base, err := c.CommitTree(ctx, path, preHead)
	if err != nil {
		return "", err
	}
	if got := strings.TrimSpace(string(index)); got != base && got != snapshot {
		return "", errors.New("TASK_COMMIT_INDEX_UNEXPECTED")
	}
	dispatched = true // v1 staging changes the real index, so later errors remain unknown.
	if _, err = c.run(ctx, path, "stage accepted Task", "add", "-A"); err != nil {
		return "", err
	}
	if _, err = c.run(ctx, path, "check accepted Task", "diff", "--cached", "--check"); err != nil {
		return "", err
	}
	index, err = c.run(ctx, path, "verify staged Task", "write-tree")
	if err != nil {
		return "", err
	}
	fresh, err = c.Snapshot(ctx, path)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(index)) != snapshot || fresh != snapshot {
		return "", ErrSnapshotDrift
	}
	// Disable hooks: they are unverified repository programs that could mutate the
	// accepted tree between the fence and commit, or invoke external tools.
	indexFile, e := os.CreateTemp("", "devbridge-accepted-index-*")
	if e != nil {
		return "", e
	}
	indexPath := indexFile.Name()
	if e = indexFile.Close(); e != nil {
		return "", e
	}
	defer os.Remove(indexPath)
	if e = os.Remove(indexPath); e != nil {
		return "", e
	}
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, e = c.runEnv(ctx, path, "freeze accepted commit tree", env, "read-tree", snapshot); e != nil {
		return "", e
	}
	_, err = c.runEnv(ctx, path, "commit accepted Task", env, "-c", "core.hooksPath="+os.DevNull, "commit", "--allow-empty", "--no-gpg-sign", "-m", message+"\n\n"+trailers)
	if err != nil {
		return "", err
	}
	return c.ObserveTask(ctx, path, preHead, snapshot, taskID, revision, message)
}

// FinalizeTaskPaths commits exactly the supplied repository-relative paths from
// the current worktree.  It deliberately builds a private index from preHead:
// pre-existing staging and unrelated dirty files in the real worktree are never
// included or modified.  Callers persist paths and preHead before invoking it.
//
// A completed call is recognized only by the exact parent, tree and trailers;
// therefore a lost response can be recovered without replaying a mutation.
func (c *Client) FinalizeTaskPaths(ctx context.Context, path, preHead, verifiedSnapshot, taskID, revision, message string, paths []string) (commit, tree string, changed bool, err error) {
	return c.finalizeTaskPaths(ctx, path, preHead, verifiedSnapshot, taskID, revision, message, paths, true)
}

// ObserveTaskPaths never starts another Git commit for an existing dispatch.
func (c *Client) ObserveTaskPaths(ctx context.Context, path, preHead, verifiedSnapshot, taskID, revision, message string, paths []string) (commit, tree string, changed bool, err error) {
	return c.finalizeTaskPaths(ctx, path, preHead, verifiedSnapshot, taskID, revision, message, paths, false)
}

// ValidateTaskPathsSnapshot is a read-only preflight for a new retry authority.
// It compares only the accepted paths; unrelated worktree bytes stay outside
// the Task's delivery permission and are neither staged nor overwritten.
func (c *Client) ValidateTaskPathsSnapshot(ctx context.Context, path, preHead, verifiedSnapshot string, paths []string) error {
	current, err := c.Snapshot(ctx, path)
	if err != nil {
		return err
	}
	want, err := c.selectiveTaskTree(ctx, path, preHead, verifiedSnapshot, paths)
	if err != nil {
		return err
	}
	got, err := c.selectiveTaskTree(ctx, path, preHead, current, paths)
	if err != nil {
		return err
	}
	if got != want {
		return ErrSnapshotDrift
	}
	return nil
}

func (c *Client) finalizeTaskPaths(ctx context.Context, path, preHead, verifiedSnapshot, taskID, revision, message string, paths []string, allowCommit bool) (commit, tree string, changed bool, err error) {
	dispatched := false
	defer func() {
		if allowCommit && !dispatched && err != nil {
			err = &BeforeEffectError{Err: err}
		}
	}()
	if err = c.NoOperation(ctx, path); err != nil {
		return "", "", false, err
	}
	if _, err = c.ResolveCommit(ctx, path, preHead); err != nil {
		return "", "", false, err
	}
	if err = c.verifyTree(ctx, path, verifiedSnapshot); err != nil {
		return "", "", false, err
	}
	paths = uniqueSorted(paths)
	if len(paths) == 0 {
		return "", "", false, nil
	}
	for _, value := range paths {
		if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") || value == "." || value == ".." || strings.HasPrefix(value, "../") || filepath.ToSlash(filepath.Clean(value)) != value {
			return "", "", false, fmt.Errorf("invalid task delivery path %q", value)
		}
	}
	expectedTree, err := c.selectiveTaskTree(ctx, path, preHead, verifiedSnapshot, paths)
	if err != nil {
		return "", "", false, err
	}
	head, err := c.ResolveHEAD(ctx, path)
	if err != nil {
		return "", "", false, err
	}
	trailers := "Dev-Bridge-Task: " + taskID + "\nDev-Bridge-Task-Revision: " + revision
	if head != preHead {
		parents, e := c.CommitParents(ctx, path, head)
		if e != nil {
			return "", "", false, e
		}
		body, e := c.run(ctx, path, "read selective task commit receipt", "show", "-s", "--format=%B", head)
		if e != nil || len(parents) != 1 || parents[0] != preHead || strings.TrimSpace(string(body)) != message+"\n\n"+trailers {
			if e != nil {
				return "", "", false, e
			}
			return "", "", false, ErrRecoveryRequired
		}
		tree, err = c.CommitTree(ctx, path, head)
		if err != nil {
			return "", "", false, err
		}
		if tree != expectedTree {
			return "", "", false, ErrRecoveryRequired
		}
		return head, tree, true, nil
	}

	if !allowCommit {
		return "", "", false, ErrRecoveryRequired
	}
	file, e := os.CreateTemp("", "devbridge-selective-index-*")
	if e != nil {
		return "", "", false, e
	}
	indexPath := file.Name()
	if e = file.Close(); e != nil {
		_ = os.Remove(indexPath)
		return "", "", false, e
	}
	if e = os.Remove(indexPath); e != nil {
		return "", "", false, e
	}
	defer os.Remove(indexPath)
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err = c.runEnv(ctx, path, "initialize selective task index", env, "read-tree", preHead); err != nil {
		return "", "", false, err
	}
	args := append([]string{"add", "-A", "--"}, paths...)
	if _, err = c.runEnv(ctx, path, "stage exact accepted Task paths", env, args...); err != nil {
		return "", "", false, err
	}
	var out []byte
	out, err = c.runEnv(ctx, path, "write selective task tree", env, "write-tree")
	if err != nil {
		return "", "", false, err
	}
	tree = strings.TrimSpace(string(out))
	if tree != expectedTree {
		return "", "", false, ErrSnapshotDrift
	}
	base, e := c.CommitTree(ctx, path, preHead)
	if e != nil {
		return "", "", false, e
	}
	if tree == base {
		return "", tree, false, nil
	}
	checkArgs := append([]string{"diff", "--quiet", verifiedSnapshot, tree, "--"}, paths...)
	if _, e = c.runEnv(ctx, path, "verify exact accepted Task path bytes", env, checkArgs...); e != nil {
		var command *CommandError
		if errors.As(e, &command) {
			return "", "", false, ErrSnapshotDrift
		}
		return "", "", false, e
	}
	if _, err = c.runEnv(ctx, path, "check exact accepted Task paths", env, "diff", "--cached", "--check"); err != nil {
		return "", "", false, err
	}
	dispatched = true
	if _, err = c.runEnv(ctx, path, "commit exact accepted Task paths", env, "-c", "core.hooksPath="+os.DevNull, "commit", "--allow-empty", "--no-gpg-sign", "-m", message+"\n\n"+trailers); err != nil {
		return "", "", false, err
	}
	commit, tree, changed, err = c.ObserveTaskPaths(ctx, path, preHead, verifiedSnapshot, taskID, revision, message, paths)
	return commit, tree, changed, err
}

// selectiveTaskTree reconstructs the one tree an accepted selective delivery
// is allowed to create: every path starts at preHead, then only the frozen
// accepted paths are restored from verifiedSnapshot. It never reads or changes
// the caller's real index or worktree.
func (c *Client) selectiveTaskTree(ctx context.Context, path, preHead, verifiedSnapshot string, paths []string) (string, error) {
	file, err := os.CreateTemp("", "devbridge-selective-proof-index-*")
	if err != nil {
		return "", err
	}
	indexPath := file.Name()
	if err = file.Close(); err != nil {
		_ = os.Remove(indexPath)
		return "", err
	}
	if err = os.Remove(indexPath); err != nil {
		return "", err
	}
	defer os.Remove(indexPath)
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err = c.runEnv(ctx, path, "initialize selective task proof", env, "read-tree", preHead); err != nil {
		return "", err
	}
	args := append([]string{"restore", "--source=" + verifiedSnapshot, "--staged", "--"}, paths...)
	if _, err = c.runEnv(ctx, path, "restore accepted task paths", env, args...); err != nil {
		return "", err
	}
	out, err := c.runEnv(ctx, path, "write selective task proof", env, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(out))
	if !isObjectID(tree) {
		return "", fmt.Errorf("selective task proof returned invalid object id %q", tree)
	}
	return tree, nil
}

func (c *Client) IsAncestor(ctx context.Context, path, ancestor, head string) (bool, error) {
	_, err := c.run(ctx, path, "check commit ancestry", "merge-base", "--is-ancestor", ancestor, head)
	if err == nil {
		return true, nil
	}
	var ce *CommandError
	if errors.As(err, &ce) {
		type exitCoder interface{ ExitCode() int }
		var ex exitCoder
		if errors.As(ce.Err, &ex) && ex.ExitCode() == 1 {
			return false, nil
		}
	}
	return false, err
}

// CommitsExclusive returns commits reachable from head but not base, oldest
// first. Delivery uses it to prove a frozen source branch contains only
// durable Task commits, never an incidental workspace commit.
func (c *Client) CommitsExclusive(ctx context.Context, path, base, head string) ([]string, error) {
	out, err := c.run(ctx, path, "list source-only commits", "rev-list", "--reverse", base+".."+head)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}
func (c *Client) DevelopmentCheckout(ctx context.Context, path, ref string) (string, error) {
	out, err := c.run(ctx, path, "find development checkout", "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", err
	}
	current := ""
	for _, field := range strings.Split(string(out), "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			current = strings.TrimPrefix(field, "worktree ")
		}
		if field == "branch "+ref && current != "" {
			return current, nil
		}
	}
	return "", errors.New("DEVELOPMENT_CHECKOUT_REQUIRED")
}
func (c *Client) CommonDirectory(ctx context.Context, path string) (string, error) {
	out, err := c.run(ctx, path, "repository identity", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(out)))
}

// MergeFeatureArea performs full branch integration, including recovery after
// Git success and before the durable receipt was finalized.
func (c *Client) MergeFeatureArea(ctx context.Context, sourcePath, sourceBranch, targetPath, ref, source, expected string) (string, error) {
	return c.mergeFeatureArea(ctx, sourcePath, sourceBranch, targetPath, ref, source, expected, true, true)
}

// MergeFeatureAreaOnce executes a newly reserved legacy command. Replay must
// use its durable outcome; current Git topology cannot prove command completion.
func (c *Client) MergeFeatureAreaOnce(ctx context.Context, sourcePath, sourceBranch, targetPath, ref, source, expected string) (string, error) {
	return c.mergeFeatureArea(ctx, sourcePath, sourceBranch, targetPath, ref, source, expected, true, false)
}

// MergeFeatureAreaFrozen merges a previously committed member into its
// development checkout without touching the source worktree. This is used by
// v2 delivery because its real index and unrelated dirty bytes intentionally
// survive a selective Task commit. The target remains clean and exact-head
// fenced; recovery still recognizes only the expected fast-forward/merge.
func (c *Client) MergeFeatureAreaFrozen(ctx context.Context, sourcePath, sourceBranch, targetPath, ref, source, expected string) (string, error) {
	return c.mergeFeatureArea(ctx, sourcePath, sourceBranch, targetPath, ref, source, expected, false, true)
}

// MergeFeatureAreaFrozenOnce is used after a durable Core dispatch. Existing
// Git history cannot stand in for this operation's missing outcome receipt.
func (c *Client) MergeFeatureAreaFrozenOnce(ctx context.Context, sourcePath, sourceBranch, targetPath, ref, source, expected string) (string, error) {
	return c.mergeFeatureArea(ctx, sourcePath, sourceBranch, targetPath, ref, source, expected, false, false)
}

func (c *Client) mergeFeatureArea(ctx context.Context, sourcePath, sourceBranch, targetPath, ref, source, expected string, syncSource, allowRecovery bool) (result string, resultErr error) {
	dispatched := false
	defer func() {
		if !dispatched && resultErr != nil {
			resultErr = &BeforeEffectError{Err: resultErr}
		}
	}()
	if err := c.ValidateManagedWorktree(ctx, targetPath, sourcePath, sourceBranch); err != nil {
		return "", err
	}
	state, err := c.InspectRepositoryState(ctx, targetPath)
	if err != nil {
		return "", err
	}
	if state.BranchRef != ref || state.Detached {
		return "", ErrRecoveryRequired
	}
	paths := []string{targetPath}
	if syncSource {
		paths = append([]string{sourcePath}, paths...)
	}
	for _, p := range paths {
		if err = c.NoOperation(ctx, p); err != nil {
			return "", err
		}
		if err = c.RequireClean(ctx, p); err != nil {
			return "", err
		}
	}
	current, err := c.ResolveHEAD(ctx, sourcePath)
	if err != nil {
		return "", err
	}
	head := state.HeadCommit
	if head != expected {
		if !allowRecovery {
			return "", ErrRecoveryRequired
		}
		parents, e := c.CommitParents(ctx, targetPath, head)
		if e != nil {
			return "", e
		}
		ff, e := c.IsAncestor(ctx, targetPath, expected, source)
		if e != nil {
			return "", e
		}
		mergeResult := len(parents) == 2 && parents[0] == expected && parents[1] == source
		if !(head == source && ff) && !mergeResult {
			return "", ErrRecoveryRequired
		}
		if mergeResult {
			want, e := c.mergeResultTree(ctx, targetPath, expected, source)
			if e != nil {
				return "", e
			}
			got, e := c.CommitTree(ctx, targetPath, head)
			if e != nil {
				return "", e
			}
			if got != want {
				return "", ErrRecoveryRequired
			}
		}
		if current != source && current != head {
			return "", ErrRecoveryRequired
		}
	} else {
		if current != source {
			included, e := c.IsAncestor(ctx, targetPath, source, expected)
			if e != nil {
				return "", e
			}
			if current != expected || !included {
				return "", ErrRecoveryRequired
			}
		}
		if source != head && current == source {
			if _, err = c.run(ctx, targetPath, "require common ancestor", "merge-base", expected, source); err != nil {
				return "", err
			}
			ff, e := c.IsAncestor(ctx, targetPath, expected, source)
			if e != nil {
				return "", e
			}
			mode := "--no-edit"
			if ff {
				mode = "--ff-only"
			}
			dispatched = true
			_, err = c.run(ctx, targetPath, "merge FeatureArea", "-c", "core.hooksPath="+os.DevNull, "-c", "commit.gpgSign=false", "merge", mode, source)
			if err != nil {
				// Abort only an actual merge, leaving no dirty target on conflict.
				marker, e := c.run(ctx, targetPath, "inspect failed merge", "rev-parse", "--verify", "MERGE_HEAD")
				if e == nil && len(marker) > 0 {
					if _, e = c.run(context.WithoutCancel(ctx), targetPath, "abort conflicted merge", "merge", "--abort"); e != nil {
						return "", ErrRecoveryRequired
					}
					restored, e := c.ResolveHEAD(ctx, targetPath)
					if e != nil || restored != expected {
						return "", ErrRecoveryRequired
					}
					if e = c.RequireClean(ctx, targetPath); e != nil {
						return "", ErrRecoveryRequired
					}
					return "", ErrMergeConflict
				}
				return "", err
			}
			head, err = c.ResolveHEAD(ctx, targetPath)
			if err != nil {
				return "", err
			}
		}
	}
	if syncSource {
		dispatched = true
		if _, err = c.run(ctx, sourcePath, "sync FeatureArea workspace", "-c", "core.hooksPath="+os.DevNull, "merge", "--ff-only", head); err != nil {
			return "", err
		}
	}
	paths = []string{targetPath}
	if syncSource {
		paths = append([]string{sourcePath}, paths...)
	}
	for _, p := range paths {
		h, e := c.ResolveHEAD(ctx, p)
		if e != nil || h != head {
			return "", ErrRecoveryRequired
		}
		if e = c.RequireClean(ctx, p); e != nil {
			return "", ErrRecoveryRequired
		}
	}
	return head, nil
}

// mergeResultTree computes the tree produced by Git's default recursive merge
// for the two frozen heads. Recovery compares this proof with the observed
// merge commit so matching parents alone cannot authorize arbitrary bytes.
func (c *Client) mergeResultTree(ctx context.Context, path, expected, source string) (string, error) {
	out, err := c.run(ctx, path, "compute frozen merge proof", "merge-tree", "--write-tree", expected, source)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || !isObjectID(fields[0]) {
		return "", fmt.Errorf("frozen merge proof returned invalid tree %q", strings.TrimSpace(string(out)))
	}
	return fields[0], nil
}
