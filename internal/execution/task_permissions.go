package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepermission"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/worktree"
)

func (f DefaultCodexFactory) TaskPermissionsEnabled() bool { return true }

func (f DefaultCodexFactory) withTaskCheckpoint(cp executionCheckpoint) DefaultCodexFactory {
	if cp.MultiRuntime != nil && cp.MultiRuntime.Workset.TaskPermissions != nil {
		b := cp.MultiRuntime.Binding
		f.Config.TaskRuntimeBinding = &b
	}
	return f
}

type taskRunCodexFactory interface {
	StartForTaskRun(context.Context, string, codexconfig.Config, string, string, string, codex.AuthorizationCeiling, executionCheckpoint) (CodexSession, error)
}

func (f DefaultCodexFactory) StartForTaskRun(ctx context.Context, runID string, selected codexconfig.Config, model, reasoning, tier string, ceiling codex.AuthorizationCeiling, cp executionCheckpoint) (CodexSession, error) {
	if err := validateCheckpointTaskBinding(cp); err != nil {
		return nil, err
	}
	f = f.withTaskCheckpoint(cp)
	return f.StartForRuntimeSelection(ctx, selected, model, reasoning, tier, ceiling)
}

func validateCheckpointTaskBinding(cp executionCheckpoint) error {
	if cp.MultiRuntime == nil || cp.MultiRuntime.Workset.TaskPermissions == nil {
		return nil
	}
	cwd, binding, err := multiRuntimeClientBinding(cp.MultiRuntime.Workset)
	if err != nil {
		return err
	}
	if cwd != cp.MultiRuntime.CWD || !sameRuntimeBinding(binding, cp.MultiRuntime.Binding) {
		return errors.New("TASK_PERMISSION_CHECKPOINT_BINDING_MISMATCH")
	}
	return nil
}

// prepareTaskPermissions runs before durable workset binding and before any
// model effects. Existing bindings are read back, never upgraded or rebuilt.
func (c *Coordinator) prepareTaskPermissions(ctx context.Context, w workset.RuntimeWorkset, verification bool) (workset.RuntimeWorkset, error) {
	if err := agentpolicy.ValidateFrozenSelection(w.AgentID, w.ExecutionPolicy, w.AgentConfigFingerprint); err != nil {
		return w, err
	}
	if w.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 {
		return c.prepareCooperativeWorkset(ctx, w)
	}

	enabled, ok := c.Codex.(interface{ TaskPermissionsEnabled() bool })
	if !ok || !enabled.TaskPermissionsEnabled() || (w.TaskID == "" || w.ExecutionID == "") && w.ReviewID == "" {
		return w, nil
	}
	if w.RuntimeAdapterIdentity != "codex-app-server-v1" && w.RuntimeAdapterIdentity != executionproposal.RuntimeAdapterCodexAppServerV1 && !runtimeplatform.QualifiedExecutorIdentity(w.RuntimeAdapterIdentity, w.RuntimeProtocolIdentity) {
		return w, nil
	}
	if w.ReviewID != "" && c.Reviews != nil {
		v, err := c.Reviews.Get(ctx, w.ReviewID)
		if err != nil {
			return w, err
		}
		if v.ExecutorKind != review.ExecutorCodingAgent {
			return w, nil
		}
	}
	purpose := "IMPLEMENTATION"
	if verification {
		purpose = "VERIFICATION"
		if w.ReviewID != "" {
			purpose = "SCOPE_REVIEW"
		}
	}
	if reader, ok := c.Runs.(interface {
		RuntimeWorkset(context.Context, string) (workset.RuntimeWorkset, error)
	}); ok {
		old, err := reader.RuntimeWorkset(ctx, w.RunID)
		if err == nil {
			if old.Validate() != nil || old.ContentHash != w.ContentHash || old.RunID != w.RunID {
				return w, errors.New("TASK_PERMISSION_REPLAY_CONFLICT")
			}
			if old.TaskPermissions != nil && old.TaskPermissions.Purpose != purpose {
				return w, errors.New("TASK_PERMISSION_REPLAY_PURPOSE_MISMATCH")
			}
			return old, nil
		}
		if !errors.Is(err, run.ErrNotFound) {
			return w, err
		}
	}
	base := filepath.Join(filepath.Dir(w.Members[0].CanonicalRoot), ".task-runtime", "run-"+runtimepermission.EnvironmentID(w.RunID))
	for _, m := range w.Members {
		rel, err := filepath.Rel(m.CanonicalRoot, base)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return w, errors.New("TASK_PERMISSION_SOURCE_OVERLAP")
		}
	}
	manifest := filepath.Join(base, "permissions.json")
	if b, err := os.ReadFile(manifest); err == nil {
		var p runtimepermission.TaskPermissions
		if json.Unmarshal(b, &p) != nil {
			return w, errors.New("TASK_PERMISSION_MANIFEST_INVALID")
		}
		if p.Purpose != purpose {
			return w, errors.New("TASK_PERMISSION_REPLAY_PURPOSE_MISMATCH")
		}
		w.TaskPermissions = &p
		if err := w.Validate(); err != nil {
			return w, err
		}
		return w, validateTaskEnvironment(w)
	} else if !os.IsNotExist(err) {
		return w, err
	}
	parent := filepath.Dir(base)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return w, err
	}
	if real, err := filepath.EvalSymlinks(parent); err != nil || real != parent {
		return w, errors.New("TASK_PERMISSION_PARENT_CHANGED")
	}
	if err := os.Mkdir(base, 0700); err != nil {
		return w, fmt.Errorf("TASK_PERMISSION_PREPARATION_INCOMPLETE: %w", err)
	}
	p := runtimepermission.TaskPermissions{Version: runtimepermission.TaskPermissionVersion, TaskID: w.TaskID, RevisionID: w.TaskDefinitionRevisionID, ExecutionID: w.ExecutionID, RunID: w.RunID, WorksetHash: w.ContentHash, Purpose: "IMPLEMENTATION", ScratchRoot: filepath.Join(base, "scratch")}
	p.ReviewID = w.ReviewID
	var requestedOutputs []string
	if w.TaskID != "" && c.Tasks != nil {
		current, readErr := c.Tasks.Get(ctx, w.TaskID)
		if readErr != nil {
			return w, readErr
		}
		frozen, readErr := c.frozenTask(ctx, current, w.TaskDefinitionRevisionID)
		if readErr != nil {
			return w, readErr
		}
		if frozen.WorkSpec.CommandPermissions != nil {
			p.NetworkAccess = frozen.WorkSpec.CommandPermissions.NetworkAccess
			requestedOutputs = append([]string(nil), frozen.WorkSpec.CommandPermissions.VerificationOutputs...)
		}
	}
	// Freeze the project-level authorization once, before durable binding.
	// Replays above already returned the stored manifest and never reach here,
	// so a revoked or revised authorization only affects future new Runs. The
	// two toggles are independent: network-only grants freeze a snapshot with
	// Trusted=false that changes no filesystem semantics, and trusted-only
	// grants keep network exactly as the task definition froze it.
	var trusted *runtimepermission.TrustedExecutionSnapshot
	if c.ProjectAuthorizations != nil && w.ReviewID == "" {
		authorization, err := c.ProjectAuthorizations.Get(ctx, w.ProjectID)
		if err != nil && !errors.Is(err, project.ErrAuthorizationNotFound) {
			return w, err
		}
		if err == nil && authorization.HeadRevision > 0 && (authorization.TrustedExecution || authorization.NetworkAccess) {
			networkFromAuthorization := authorization.NetworkAccess
			trusted = &runtimepermission.TrustedExecutionSnapshot{ProjectID: w.ProjectID, AuthorizationRevision: authorization.HeadRevision, Trusted: authorization.TrustedExecution, NetworkFromAuthorization: networkFromAuthorization}
			if networkFromAuthorization {
				p.NetworkAccess = true
			}
		}
	}
	if err := os.Mkdir(p.ScratchRoot, 0700); err != nil {
		return w, err
	}
	if verification {
		p.Purpose = "VERIFICATION"
		if w.ReviewID != "" {
			p.Purpose = "SCOPE_REVIEW"
		}
		if trusted != nil && trusted.Trusted && p.Purpose == "VERIFICATION" {
			// Trusted verification runs directly in the original task
			// worktrees: no copy and no per-path output whitelist. Build
			// caches are reused where they live. End-of-run checks treat any
			// tracked or non-ignored source mutation as an invalid run.
			for _, m := range w.Members {
				before, err := c.Evidence.CaptureSnapshot(ctx, m.CanonicalRoot)
				if err != nil || before != m.StartSnapshot {
					return w, errors.Join(err, errors.New("TASK_PERMISSION_SOURCE_CHANGED"))
				}
			}
		} else {
			mapping := map[string]string{}
			for _, m := range w.Members {
				mapping[m.CanonicalRoot] = filepath.Join(base, "copy-"+runtimepermission.EnvironmentID(m.MemberID))
			}
			for _, m := range w.Members {
				copyRoot := mapping[m.CanonicalRoot]
				before, err := c.Evidence.CaptureSnapshot(ctx, m.CanonicalRoot)
				if err != nil || before != m.StartSnapshot {
					return w, errors.Join(err, errors.New("TASK_PERMISSION_SOURCE_CHANGED"))
				}
				if err = copyVerificationRepository(ctx, m.CanonicalRoot, copyRoot, mapping); err != nil {
					return w, err
				}
				after, err := c.Evidence.CaptureSnapshot(ctx, m.CanonicalRoot)
				if err != nil || after != before {
					return w, errors.Join(err, errors.New("TASK_PERMISSION_SOURCE_CHANGED"))
				}
				copied, err := c.Evidence.CaptureSnapshot(ctx, copyRoot)
				if err != nil || copied != before {
					return w, errors.Join(err, errors.New("TASK_PERMISSION_COPY_SNAPSHOT_MISMATCH"))
				}
				outputs, err := prepareVerificationOutputs(ctx, copyRoot, requestedOutputs)
				if err != nil {
					return w, err
				}
				digest, err := repositoryContentDigest(ctx, copyRoot)
				if err != nil {
					return w, err
				}
				_, identity, err := multiRuntimeRootIdentity(copyRoot)
				if err != nil {
					return w, err
				}
				p.Copies = append(p.Copies, runtimepermission.VerificationCopy{SourceRoot: m.CanonicalRoot, Root: copyRoot, SourceDigest: digest, StartSnapshot: before, RootIdentity: identity, OutputPaths: outputs})
			}
		}
	}
	if p.Purpose == "IMPLEMENTATION" {
		for _, m := range w.Members {
			if m.Write.Kind != "NONE" {
				p.Writes = append(p.Writes, runtimepermission.WriteGrant{Root: m.CanonicalRoot, Kind: m.Write.Kind, Paths: append([]string(nil), m.Write.Paths...)})
			}
		}
	}
	p.TrustedExecution = trusted
	p.ContentHash = p.Hash()
	w.TaskPermissions = &p
	if err := w.Validate(); err != nil {
		return w, err
	}
	if err := validateTaskEnvironment(w); err != nil {
		return w, err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return w, err
	}
	file, err := os.OpenFile(manifest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return w, err
	}
	_, err = file.Write(b)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	return w, errors.Join(err, closeErr)
}

func validateTaskEnvironment(w workset.RuntimeWorkset) error {
	if w.TaskPermissions == nil {
		return nil
	}
	if err := w.Validate(); err != nil {
		return err
	}
	p := w.TaskPermissions
	for _, member := range w.Members {
		root, identity, err := multiRuntimeRootIdentity(member.CanonicalRoot)
		if err != nil || root != member.CanonicalRoot || identity != member.RootFileIdentity {
			return errors.New("TASK_PERMISSION_SOURCE_ROOT_CHANGED")
		}
	}
	grants := append([]runtimepermission.WriteGrant(nil), p.Writes...)
	for _, copy := range p.Copies {
		grants = append(grants, runtimepermission.WriteGrant{Root: copy.Root, Paths: copy.OutputPaths})
	}
	for _, grant := range grants {
		for _, path := range grant.Paths {
			candidate := filepath.Join(grant.Root, filepath.FromSlash(path))
			for {
				_, err := os.Lstat(candidate)
				if os.IsNotExist(err) {
					candidate = filepath.Dir(candidate)
					continue
				}
				if err != nil {
					return err
				}
				break
			}
			actual, err := filepath.EvalSymlinks(candidate)
			if err != nil || actual != candidate {
				return errors.New("TASK_PERMISSION_WRITE_PATH_SYMLINK")
			}
		}
	}
	if root, err := filepath.EvalSymlinks(p.ScratchRoot); err != nil || root != p.ScratchRoot {
		return errors.New("TASK_PERMISSION_SCRATCH_CHANGED")
	}
	for _, copy := range p.Copies {
		root, identity, err := multiRuntimeRootIdentity(copy.Root)
		if err != nil || root != copy.Root || identity != copy.RootIdentity {
			return errors.New("TASK_PERMISSION_COPY_ROOT_CHANGED")
		}
	}
	return nil
}

func (c *Coordinator) verifyTaskCopies(ctx context.Context, w workset.RuntimeWorkset) error {
	if err := validateTaskEnvironment(w); err != nil {
		return err
	}
	if t := w.TaskPermissions.TrustedExecution; t != nil && t.Trusted {
		// Trusted verification used the original worktrees directly. Any
		// change to tracked or non-ignored source (relative to each member's
		// frozen start snapshot) invalidates the run; ignored build outputs
		// do not appear in Git snapshots and stay permitted.
		sources := map[string]string{}
		for _, m := range w.Members {
			sources[m.CanonicalRoot] = m.StartSnapshot
		}
		for _, copy := range w.TaskPermissions.Copies {
			sources[copy.SourceRoot] = copy.StartSnapshot
		}
		for root, start := range sources {
			end, err := c.Evidence.CaptureSnapshot(ctx, root)
			if err != nil || end != start {
				return errors.Join(err, fmt.Errorf("verification changed source worktree: %s", root))
			}
		}
		return nil
	}
	for _, copy := range w.TaskPermissions.Copies {
		digest, err := repositoryContentDigest(ctx, copy.Root)
		if err != nil || digest != copy.SourceDigest {
			return errors.Join(err, fmt.Errorf("verification changed copied source: %s", copy.Root))
		}
		snapshot, err := c.Evidence.CaptureSnapshot(ctx, copy.Root)
		if err != nil || snapshot != copy.StartSnapshot {
			return errors.Join(err, fmt.Errorf("verification copy snapshot changed: %s", copy.Root))
		}
	}
	return nil
}

// Each clone has independent Git metadata and ordinary copied files: no linked
// worktree, hardlink, shared index or pointer back to source repository metadata.
func copyVerificationRepository(ctx context.Context, source, destination string, mapping map[string]string) error {
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "clone", "--no-hardlinks", "--no-checkout", "--", source, destination)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("TASK_PERMISSION_COPY_FAILED: %w: %.1024s", err, out)
	}
	for _, args := range [][]string{{"read-tree", "HEAD"}, {"config", "core.hooksPath", "/dev/null"}} {
		command := exec.CommandContext(ctx, "git", append([]string{"-C", destination}, args...)...)
		command.Env = cmd.Env
		if out, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("TASK_PERMISSION_COPY_INDEX_FAILED: %w: %.1024s", err, out)
		}
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("TASK_PERMISSION_COPY_SYMLINK: %s: %w", rel, err)
			}
			for original, copyRoot := range mapping {
				r, err := filepath.Rel(original, resolved)
				if err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
					mapped := filepath.Join(copyRoot, r)
					link, err := filepath.Rel(filepath.Dir(target), mapped)
					if err != nil {
						return err
					}
					return os.Symlink(link, target)
				}
			}
			return fmt.Errorf("TASK_PERMISSION_COPY_EXTERNAL_SYMLINK: %s", rel)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("TASK_PERMISSION_COPY_SPECIAL_FILE: %s", rel)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()|0600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		return errors.Join(err, out.Close())
	})
}

// Ignore build outputs exactly as Git does, but protect tracked and untracked
// source contents, paths, modes and symlinks. Never use the copy's mutable index
// as proof of equality with the source; snapshots independently check it too.
func repositoryContentDigest(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	b, err := cmd.Output()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	names := strings.Split(string(b), "\x00")
	sort.Strings(names)
	previous := ""
	for _, name := range names {
		if name == "" {
			continue
		}
		if name == previous {
			continue
		}
		previous = name
		if filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return "", errors.New("invalid snapshot path")
		}
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			fmt.Fprintf(h, "%d:%s:DELETED\x00", len(name), name)
			continue
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%d:%s:%o:%d:", len(name), name, info.Mode(), info.Size())
		if info.Mode()&os.ModeSymlink != 0 {
			v, err := os.Readlink(path)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(h, "%d:%s", len(v), v)
		} else if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return "", err
			}
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return "", err
			}
		} else {
			return "", errors.New("invalid source file type")
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Scalar verification historically has no v90 preparation Plan. Freeze its
// exact existing managed member under the already admitted Verification Run;
// no inventory, replacement Run, engineering scope or approval is inferred.
func (c *Coordinator) prepareScalarVerificationSandbox(ctx context.Context, prepared *PreparedRun, tree worktree.Worktree, snapshot string) error {
	enabled, ok := c.Codex.(interface{ TaskPermissionsEnabled() bool })
	if !ok || !enabled.TaskPermissionsEnabled() || prepared.purpose != run.PurposeVerification || prepared.multiRuntime != nil || prepared.scalarRuntime != nil || prepared.Run.ExecutionProposalID == "" || c.Executions == nil {
		return nil
	}
	r := prepared.Run
	execution, err := c.Executions.Get(ctx, r.ExecutionProposalID)
	if err != nil {
		return err
	}
	if execution.ID != r.ExecutionProposalID || execution.VerificationRunID != r.ID || execution.TaskID != r.TaskID || execution.TaskDefinitionRevisionID != prepared.task.CurrentDefinitionRevisionID || execution.BackendSelection == nil {
		return errors.New("TASK_PERMISSION_VERIFICATION_OWNER_MISMATCH")
	}
	if execution.RuntimeConfig.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 && (execution.RunID == "" || execution.RuntimeAdapterIdentity != agentpolicy.CooperativeAdapterIdentity || execution.RuntimeProtocolIdentity != agentpolicy.CooperativeProtocolIdentity) {
		return errors.New("COOPERATIVE_IMPLEMENTATION_AUTHORITY_UNAVAILABLE")
	}

	root, identity, err := multiRuntimeRootIdentity(tree.Path)
	if err != nil {
		return err
	}
	state, err := c.Evidence.CollectRun(ctx, root, tree.BaseCommit, snapshot, snapshot)
	if err != nil {
		return err
	}
	w := workset.RuntimeWorkset{ProtocolVersion: 1, ProjectID: r.ProjectID, FeatureAreaID: r.FeatureAreaID, TaskID: r.TaskID, TaskDefinitionRevisionID: execution.TaskDefinitionRevisionID, ExecutionID: execution.ID, RunID: r.ID, WorkspaceID: r.WorkspaceID, PrimaryMemberID: prepared.repository.ID, Members: []workset.RuntimeMemberBinding{{RepositoryID: prepared.repository.ID, RepositoryAlias: prepared.repository.ID, MemberID: prepared.repository.ID, MemberVersion: 1, WorktreeID: tree.ID, CanonicalRoot: root, RootFileIdentity: identity, OriginCommit: tree.BaseCommit, StartHEAD: state.HeadCommit, StartSnapshot: snapshot, Read: true, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: identity}}, BackendConfigID: execution.BackendSelection.ConfigID, BackendConfigFingerprint: execution.BackendSelection.ConfigFingerprint, Model: execution.RuntimeConfig.Model, ReasoningEffort: execution.RuntimeConfig.ReasoningEffort, ServiceTier: execution.RuntimeConfig.ServiceTier, AgentConfigFingerprint: execution.RuntimeConfig.AgentConfigFingerprint, AgentID: execution.RuntimeConfig.AgentID, ExecutionPolicy: execution.RuntimeConfig.ExecutionPolicy, RuntimeAdapterIdentity: execution.RuntimeAdapterIdentity, RuntimeProtocolIdentity: execution.RuntimeProtocolIdentity, CreatedAt: r.CreatedAt}
	if w.RuntimeProtocolIdentity == "" {
		w.RuntimeProtocolIdentity = "codex-app-server-0.149.0"
	}
	// Historical scalar file executions did not store an adapter identifier.
	// This names the actual selected Client protocol, not a qualification or a
	// rewrite of that historical Execution. Named-profile readback is required.
	if w.RuntimeAdapterIdentity == "" {
		w.RuntimeAdapterIdentity = "codex-app-server-v1"
	}
	if execution.RunID != "" {
		source, readErr := c.Runs.Get(ctx, execution.RunID)
		if readErr != nil {
			return readErr
		}
		if execution.RuntimeConfig.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 && source.RuntimeWorkset == nil {
			return errors.New("COOPERATIVE_IMPLEMENTATION_WORKSET_UNAVAILABLE")
		}
		if frozen := source.RuntimeWorkset; frozen != nil {
			if frozen.AgentConfigFingerprint != execution.RuntimeConfig.AgentConfigFingerprint || frozen.AgentID != execution.RuntimeConfig.AgentID || frozen.ExecutionPolicy != execution.RuntimeConfig.ExecutionPolicy || frozen.Validate() != nil || frozen.ExecutionID != execution.ID || frozen.TaskID != r.TaskID || frozen.TaskDefinitionRevisionID != execution.TaskDefinitionRevisionID || len(frozen.Members) != 1 || frozen.Members[0].CanonicalRoot != root || frozen.Members[0].RootFileIdentity != identity {
				return errors.New("TASK_PERMISSION_IMPLEMENTATION_SOURCE_MISMATCH")
			}
			w = *frozen
			w.TaskPermissions = nil
			w.RunID = r.ID
			w.CreatedAt = r.CreatedAt
			w.Members = append([]workset.RuntimeMemberBinding(nil), frozen.Members...)
			w.Members[0].StartSnapshot = snapshot
			w.Members[0].StartHEAD = state.HeadCommit
		}
	}
	w, err = w.WithHash()
	if err != nil {
		return err
	}
	w, err = c.prepareTaskPermissions(ctx, w, true)
	if err != nil {
		return err
	}
	store, ok := c.Runs.(interface {
		BindRuntimeWorkset(context.Context, workset.RuntimeWorkset) (run.Run, error)
	})
	if !ok {
		return errors.New("TASK_PERMISSION_DURABLE_BINDING_UNAVAILABLE")
	}
	stored, err := store.BindRuntimeWorkset(ctx, w)
	if err != nil {
		return err
	}
	cwd, binding, err := multiRuntimeClientBinding(w)
	if err != nil {
		return err
	}
	if w.TaskPermissions == nil {
		binding.WritableRoots = nil
	}
	prepared.Run = stored
	prepared.scalarRuntime = &PreparedMultiRuntime{Workset: w, CWD: cwd, Binding: binding, ReadOnly: true}
	return nil
}

func (c *Coordinator) prepareScalarReviewSandbox(ctx context.Context, prepared *PreparedRun, tree worktree.Worktree, snapshot string) error {
	enabled, ok := c.Codex.(interface{ TaskPermissionsEnabled() bool })
	if !ok || !enabled.TaskPermissionsEnabled() || prepared.multiRuntime != nil || prepared.scalarRuntime != nil || prepared.purpose != run.PurposeScopeReview || prepared.review == nil || (prepared.review.ExecutorKind != review.ExecutorCodingAgent && prepared.review.RuntimeConfig.ExecutionPolicy != agentpolicy.PolicyCooperativeTrustedV1) {
		return nil
	}
	v, r := prepared.review, prepared.Run
	if v.ID != r.ReviewID || v.RunID != r.ID || v.BackendSelection == nil {
		return errors.New("TASK_PERMISSION_REVIEW_OWNER_MISMATCH")
	}
	root, identity, err := multiRuntimeRootIdentity(tree.Path)
	if err != nil {
		return err
	}
	state, err := c.Evidence.CollectRun(ctx, root, tree.BaseCommit, snapshot, snapshot)
	if err != nil {
		return err
	}
	w := workset.RuntimeWorkset{ProtocolVersion: 1, ProjectID: r.ProjectID, FeatureAreaID: r.FeatureAreaID, ReviewID: v.ID, RunID: r.ID, WorkspaceID: r.WorkspaceID, PrimaryMemberID: prepared.repository.ID, Members: []workset.RuntimeMemberBinding{{RepositoryID: prepared.repository.ID, RepositoryAlias: prepared.repository.ID, MemberID: prepared.repository.ID, MemberVersion: 1, WorktreeID: tree.ID, CanonicalRoot: root, RootFileIdentity: identity, OriginCommit: tree.BaseCommit, StartHEAD: state.HeadCommit, StartSnapshot: snapshot, Read: true, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: identity}}, BackendConfigID: v.BackendSelection.ConfigID, BackendConfigFingerprint: v.BackendSelection.ConfigFingerprint, Model: v.RuntimeConfig.Model, ReasoningEffort: v.RuntimeConfig.ReasoningEffort, ServiceTier: v.RuntimeConfig.ServiceTier, AgentConfigFingerprint: v.RuntimeConfig.AgentConfigFingerprint, AgentID: v.RuntimeConfig.AgentID, ExecutionPolicy: v.RuntimeConfig.ExecutionPolicy, RuntimeAdapterIdentity: executionproposal.RuntimeAdapterCodexAppServerV1, RuntimeProtocolIdentity: executionproposal.RuntimeProtocolCodex0149, CreatedAt: r.CreatedAt}
	if w.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 {
		w.RuntimeAdapterIdentity = agentpolicy.CooperativeAdapterIdentity
		w.RuntimeProtocolIdentity = agentpolicy.CooperativeProtocolIdentity
	}
	w, err = w.WithHash()
	if err != nil {
		return err
	}
	w, err = c.prepareTaskPermissions(ctx, w, true)
	if err != nil {
		return err
	}
	store, ok := c.Runs.(interface {
		BindRuntimeWorkset(context.Context, workset.RuntimeWorkset) (run.Run, error)
	})
	if !ok {
		return errors.New("TASK_PERMISSION_DURABLE_BINDING_UNAVAILABLE")
	}
	stored, err := store.BindRuntimeWorkset(ctx, w)
	if err != nil {
		return err
	}
	cwd, binding, err := multiRuntimeClientBinding(w)
	if err != nil {
		return err
	}
	if w.TaskPermissions == nil {
		binding.WritableRoots = nil
	}
	prepared.Run = stored
	prepared.scalarRuntime = &PreparedMultiRuntime{Workset: w, CWD: cwd, Binding: binding, ReadOnly: true}
	return nil
}

// Cooperative policy is a separate prospective contract. It never inherits a
// native manifest or infers a mode from a project's trusted-execution toggle.
func (c *Coordinator) prepareCooperativeWorkset(ctx context.Context, w workset.RuntimeWorkset) (workset.RuntimeWorkset, error) {
	if err := w.Validate(); err != nil {
		return w, err
	}
	if reader, ok := c.Runs.(interface {
		RuntimeWorkset(context.Context, string) (workset.RuntimeWorkset, error)
	}); ok {
		old, err := reader.RuntimeWorkset(ctx, w.RunID)
		if err == nil {
			if old.Validate() != nil || old.AgentConfigFingerprint != w.AgentConfigFingerprint || old.AgentID != w.AgentID || old.ExecutionPolicy != w.ExecutionPolicy || old.ContentHash != w.ContentHash || old.RunID != w.RunID || old.TaskPermissions != nil {
				return w, errors.New("COOPERATIVE_WORKSET_REPLAY_CONFLICT")
			}
			w = old
		} else if !errors.Is(err, run.ErrNotFound) {
			return w, err
		}
	}
	base := filepath.Join(filepath.Dir(w.Members[0].CanonicalRoot), ".task-runtime", "run-"+runtimepermission.EnvironmentID(w.RunID))
	if _, err := os.Lstat(filepath.Join(base, "permissions.json")); err == nil {
		return w, errors.New("COOPERATIVE_NATIVE_MANIFEST_CONFLICT")
	} else if !os.IsNotExist(err) {
		return w, err
	}
	return w, nil
}
