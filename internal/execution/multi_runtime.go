package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

// MultiRuntimeOwner identifies the one durable operation that owns a runtime
// workset. Task and Review ownership are mutually exclusive.
type MultiRuntimeOwner struct {
	ProjectID                string
	FeatureAreaID            string
	TaskID                   string
	ReviewID                 string
	TaskDefinitionRevisionID string
	ExecutionID              string
	RunID                    string
}

// MultiRuntimeIdentity freezes the selected backend facts alongside the exact
// workspace layout. None of these values are inferred from a configuration
// default by this helper.
type MultiRuntimeIdentity struct {
	BackendConfigID          string
	BackendConfigFingerprint string
	Model                    string
	ReasoningEffort          string
	ServiceTier              string
	RuntimeAdapterIdentity   string
	RuntimeProtocolIdentity  string
	CreatedAt                time.Time
}

// MultiRuntimePrepareInput contains the complete reviewed target set and the
// caller-selected primary member. The helper never reads a Project inventory or
// chooses a first workspace member.
type MultiRuntimePrepareInput struct {
	// UpperReview reserves repository-binding snapshots for the scoped file grant.
	// Git snapshots remain in the canonical observations and RuntimeWorkset.
	UpperReview     bool
	Owner           MultiRuntimeOwner
	WorkspaceID     string
	PrimaryMemberID string
	Targets         []workset.Target
	Runtime         MultiRuntimeIdentity
	ReadOnly        bool
}

// PreparedMultiRuntime is the immutable handoff to the Codex client. CWD is
// the caller-selected primary member's root and Binding contains every exact
// prepared root.
type PreparedMultiRuntime struct {
	Workset  workset.RuntimeWorkset
	CWD      string
	Binding  codex.RuntimeBinding
	ReadOnly bool
}

type canonicalRuntimePreparer interface {
	PreparationIdentity(context.Context, string) (string, string, string, error)
	ClaimWorkspacePreparation(context.Context, string, string, string, string, time.Time) (bool, error)
	ReadPreparationReceipt(context.Context, string, string) (string, string, string, error)
	RecordWorkspaceObservations(context.Context, string, string, string, string, []runtimepreparation.WorkspaceObservation, time.Time) (string, error)
	BindPreparedRuntimeWorkset(context.Context, string, string, string, string, workset.RuntimeWorkset, time.Time) (run.Run, error)
}

// MultiRuntimeObservationError says that durable observations were recorded
// but one or more members could not satisfy the frozen workset. State is only
// SCOPE_CONFLICT or UNKNOWN.
type MultiRuntimeObservationError struct {
	State string
	Cause error
}

func (e *MultiRuntimeObservationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return e.State
	}
	return e.State + ": " + e.Cause.Error()
}
func (e *MultiRuntimeObservationError) Unwrap() error { return e.Cause }

// PrepareMultiRuntime materializes exactly input.Targets, creates or reloads
// each named member worktree, freezes its start facts, records READY bindings,
// and binds a RuntimeWorkset before a backend may use its RuntimeBinding.
func (c *Coordinator) PrepareMultiRuntime(ctx context.Context, in MultiRuntimePrepareInput) (out PreparedMultiRuntime, err error) {
	if c == nil || c.Repositories == nil || c.Workspaces == nil || c.Worktrees == nil || c.Runs == nil || c.Evidence == nil {
		return PreparedMultiRuntime{}, errors.New("multi-runtime preparation dependencies are required")
	}
	if err := validateMultiRuntimeInput(in); err != nil {
		return PreparedMultiRuntime{}, err
	}
	if in.UpperReview {
		if c.Reviews == nil {
			return PreparedMultiRuntime{}, errors.New("Upper Review authority unavailable")
		}
		value, err := c.Reviews.Get(ctx, in.Owner.ReviewID)
		if err != nil || value.ExecutorKind != review.ExecutorUpperLLM || value.RunID != in.Owner.RunID || value.ProjectID != in.Owner.ProjectID || value.FeatureAreaID != in.Owner.FeatureAreaID {
			return PreparedMultiRuntime{}, errors.Join(err, errors.New("Upper Review preparation owner mismatch"))
		}
	}
	planner, ok := c.Workspaces.(plannedWorkspaceService)
	if !ok {
		return PreparedMultiRuntime{}, errors.New("exact planned workspace preparation is unavailable")
	}
	bindings, ok := c.Runs.(repositoryBindingStore)
	if !ok {
		return PreparedMultiRuntime{}, errors.New("durable repository bindings are unavailable")
	}
	runtimeStore, ok := c.Runs.(interface {
		BindRuntimeWorkset(context.Context, workset.RuntimeWorkset) (run.Run, error)
	})
	if !ok {
		return PreparedMultiRuntime{}, errors.New("durable RuntimeWorkset storage is unavailable")
	}
	var canonical canonicalRuntimePreparer
	var planID, workspaceRequest, workspacePayload string
	if in.Owner.ExecutionID != "" || in.Owner.ReviewID != "" {
		if candidate, available := c.Runs.(canonicalRuntimePreparer); available {
			var prepErr error
			planID, _, _, prepErr = candidate.PreparationIdentity(ctx, in.Owner.RunID)
			if prepErr != nil && !errors.Is(prepErr, run.ErrNotFound) {
				return PreparedMultiRuntime{}, prepErr
			}
			if prepErr == nil {
				canonical = candidate
				sum := sha256.Sum256([]byte(planID + "\x00" + in.Owner.RunID + "\x00WORKSPACE_PREPARATION"))
				workspaceRequest = "workspace_" + in.Owner.RunID
				workspacePayload = hex.EncodeToString(sum[:])
				replay, claimErr := canonical.ClaimWorkspacePreparation(ctx, planID, in.Owner.RunID, workspaceRequest, workspacePayload, time.Now().UTC())
				if claimErr != nil {
					return PreparedMultiRuntime{}, claimErr
				}
				if replay {
					return readCanonicalMultiRuntime(ctx, c.Runs, canonical, in, planID, workspaceRequest, workspacePayload)
				}
			}
		}
	}

	// Only a returned synchronous member-observation failure can be settled.
	// A failed/aborted worktree call or ambiguous observation/binding commit keeps
	// its claim UNKNOWN; replay never performs the external preparation again.
	var settledArtifacts []runtimepreparation.FailureArtifact
	definiteMemberFailure := false
	settlementStage := "MEMBER_OBSERVATION"
	defer func() {
		if err == nil || canonical == nil || !in.UpperReview || !definiteMemberFailure || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		recorder, ok := c.Runs.(interface {
			SettleUpperPreparationFailure(context.Context, runtimepreparation.FailureSettlement) error
		})
		if !ok {
			return
		}
		current, readErr := c.Runs.Get(context.WithoutCancel(ctx), in.Owner.RunID)
		if readErr != nil {
			err = errors.Join(err, readErr)
			return
		}
		settleErr := recorder.SettleUpperPreparationFailure(context.WithoutCancel(ctx), runtimepreparation.FailureSettlement{PlanID: planID, RunID: in.Owner.RunID, RequestID: workspaceRequest, PayloadHash: workspacePayload, RuntimeEpoch: current.RuntimeEpoch, Stage: settlementStage, Code: "RUNTIME_WORKSPACE_PREPARATION_FAILED", Message: err.Error(), Artifacts: settledArtifacts, At: time.Now().UTC()})
		err = errors.Join(err, settleErr)
	}()

	set := workset.Set{Version: workset.Version, WorkspaceID: in.WorkspaceID, Targets: workset.CloneTargets(in.Targets)}
	if err := set.Validate(); err != nil {
		return PreparedMultiRuntime{}, err
	}
	value, err := planner.EnsurePlannedMembers(ctx, in.Owner.ProjectID, in.Owner.FeatureAreaID, in.WorkspaceID, set.Targets)
	if err != nil {
		return PreparedMultiRuntime{}, fmt.Errorf("ensure planned workspace members: %w", err)
	}
	if value.ID != in.WorkspaceID || value.ProjectID != in.Owner.ProjectID || value.FeatureAreaID != in.Owner.FeatureAreaID {
		return PreparedMultiRuntime{}, errors.New("prepared workspace identity conflicts with supplied owner")
	}

	members := make([]workset.RuntimeMemberBinding, 0, len(set.Targets))
	observations := make([]runtimepreparation.WorkspaceObservation, 0, len(set.Targets))
	for _, target := range set.Targets {
		member, memberErr := value.MemberForRepository(target.RepositoryID)
		if memberErr != nil || member.ID != target.MemberID || member.Version != target.MemberVersion || member.OriginCommit != target.OriginCommit {
			return PreparedMultiRuntime{}, errors.New("prepared workspace member conflicts with supplied target")
		}
		definiteMemberFailure = false
		tree, updated, treeErr := c.multiRuntimeWorktree(ctx, value, member, target, in.Owner)
		if treeErr != nil {
			return PreparedMultiRuntime{}, fmt.Errorf("prepare member worktree %s: %w", target.MemberID, treeErr)
		}
		value = updated
		settledArtifacts = append(settledArtifacts, runtimepreparation.FailureArtifact{MemberID: member.ID, WorktreeID: tree.ID, Root: tree.Path})
		definiteMemberFailure = true
		root, rootIdentity, rootErr := multiRuntimeRootIdentity(tree.Path)
		if rootErr != nil {
			return PreparedMultiRuntime{}, rootErr
		}
		startSnapshot, snapshotErr := c.Evidence.CaptureSnapshot(ctx, root)
		if snapshotErr != nil || !workset.ValidCommit(startSnapshot) {
			if snapshotErr == nil {
				snapshotErr = errors.New("invalid start snapshot")
			}
			return PreparedMultiRuntime{}, fmt.Errorf("capture member start snapshot: %w", snapshotErr)
		}
		startEvidence, collectErr := c.Evidence.CollectRun(ctx, root, target.OriginCommit, startSnapshot, startSnapshot)
		if collectErr != nil || !workset.ValidCommit(startEvidence.HeadCommit) {
			if collectErr == nil {
				collectErr = errors.New("invalid member start head")
			}
			return PreparedMultiRuntime{}, fmt.Errorf("capture member start head: %w", collectErr)
		}
		binding := run.RepositoryBinding{RunID: in.Owner.RunID, ReviewID: in.Owner.ReviewID, ExecutionID: in.Owner.ExecutionID, ProjectID: in.Owner.ProjectID, FeatureAreaID: in.Owner.FeatureAreaID, WorkspaceID: in.WorkspaceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, OriginCommit: target.OriginCommit, WorktreeID: tree.ID, Preparation: "READY", RepositoryAlias: member.Alias, StartHEAD: startEvidence.HeadCommit, StartSnapshot: startSnapshot, IntendedRead: target.Read, IntendedWrite: target.Write, RuntimeRootIdentity: rootIdentity}
		if in.UpperReview {
			binding.RepositoryAlias, binding.StartHEAD, binding.StartSnapshot, binding.RuntimeRootIdentity = "", "", "", ""
			binding.IntendedRead = false
			binding.IntendedWrite = workset.WriteScope{}
		}
		if err = bindings.PutRepositoryBinding(ctx, binding); err != nil {
			return PreparedMultiRuntime{}, fmt.Errorf("record repository binding %s: %w", target.MemberID, err)
		}
		members = append(members, workset.RuntimeMemberBinding{RepositoryID: target.RepositoryID, RepositoryAlias: member.Alias, MemberID: target.MemberID, MemberVersion: target.MemberVersion, WorktreeID: tree.ID, CanonicalRoot: root, RootFileIdentity: rootIdentity, OriginCommit: target.OriginCommit, StartHEAD: startEvidence.HeadCommit, StartSnapshot: startSnapshot, Read: target.Read, Write: target.Write, RuntimeRootIdentity: rootIdentity})
		if canonical != nil {
			metadata := sha256.Sum256([]byte(tree.ID + "\x00" + root + "\x00" + tree.Branch + "\x00" + tree.BaseCommit + "\x00" + rootIdentity + "\x00" + startEvidence.HeadCommit + "\x00" + startSnapshot))
			observationID := sha256.Sum256([]byte(planID + "\x00" + in.Owner.RunID + "\x00" + target.MemberID))
			observations = append(observations, runtimepreparation.WorkspaceObservation{ID: "observation_" + hex.EncodeToString(observationID[:16]), PlanID: planID, RunID: in.Owner.RunID, WorkspaceID: in.WorkspaceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, WorktreeID: tree.ID, CanonicalRoot: root, RootIdentity: rootIdentity, StartHEAD: startEvidence.HeadCommit, StartSnapshot: startSnapshot, Branch: tree.Branch, MetadataDigest: hex.EncodeToString(metadata[:]), Generation: 1, ObservedAt: time.Now().UTC()})
		}
	}
	// ValidateWorkset is deliberately after exact member attachment. It proves
	// the caller's target set, not an inventory projection, is now READY.
	if err := value.ValidateWorkset(set); err != nil {
		return PreparedMultiRuntime{}, err
	}
	frozen := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersion, ProjectID: in.Owner.ProjectID, FeatureAreaID: in.Owner.FeatureAreaID, TaskID: in.Owner.TaskID, ReviewID: in.Owner.ReviewID, TaskDefinitionRevisionID: in.Owner.TaskDefinitionRevisionID, ExecutionID: in.Owner.ExecutionID, RunID: in.Owner.RunID, WorkspaceID: in.WorkspaceID, PrimaryMemberID: in.PrimaryMemberID, Members: members, BackendConfigID: in.Runtime.BackendConfigID, BackendConfigFingerprint: in.Runtime.BackendConfigFingerprint, Model: in.Runtime.Model, ReasoningEffort: in.Runtime.ReasoningEffort, ServiceTier: in.Runtime.ServiceTier, RuntimeAdapterIdentity: in.Runtime.RuntimeAdapterIdentity, RuntimeProtocolIdentity: in.Runtime.RuntimeProtocolIdentity, CreatedAt: in.Runtime.CreatedAt.UTC()}
	frozen, err = frozen.WithHash()
	if err != nil {
		return PreparedMultiRuntime{}, err
	}
	if !in.UpperReview {
		frozen, err = c.prepareTaskPermissions(ctx, frozen, in.ReadOnly)
		if err != nil {
			return PreparedMultiRuntime{}, err
		}
	}
	if canonical != nil {
		if _, err = canonical.RecordWorkspaceObservations(ctx, planID, in.Owner.RunID, workspaceRequest, workspacePayload, observations, time.Now().UTC()); err != nil {
			return PreparedMultiRuntime{}, fmt.Errorf("record workspace observations: %w", err)
		}
		settlementStage = "WORKSET_BINDING"
		bindingRequest := "binding_" + in.Owner.RunID
		bindingSum := sha256.Sum256([]byte(planID + "\x00" + in.Owner.RunID + "\x00" + frozen.ContentHash))
		if _, err = canonical.BindPreparedRuntimeWorkset(ctx, planID, bindingRequest, hex.EncodeToString(bindingSum[:]), "start_authority_"+in.Owner.RunID, frozen, time.Now().UTC()); err != nil {
			return PreparedMultiRuntime{}, fmt.Errorf("bind prepared RuntimeWorkset: %w", err)
		}
	} else if _, err = runtimeStore.BindRuntimeWorkset(ctx, frozen); err != nil {
		return PreparedMultiRuntime{}, err
	}
	definiteMemberFailure = false
	cwd, binding, err := multiRuntimeClientBinding(frozen)
	if err != nil {
		return PreparedMultiRuntime{}, err
	}
	if in.ReadOnly && frozen.TaskPermissions == nil {
		binding.WritableRoots = nil
	}
	return PreparedMultiRuntime{Workset: frozen, CWD: cwd, Binding: binding, ReadOnly: in.ReadOnly}, nil
}

func readCanonicalMultiRuntime(ctx context.Context, runs RunService, canonical canonicalRuntimePreparer, in MultiRuntimePrepareInput, planID, requestID, payloadHash string) (PreparedMultiRuntime, error) {
	if reader, ok := runs.(interface {
		ReadSettledPreparationFailure(context.Context, string) (string, error)
	}); ok {
		message, readErr := reader.ReadSettledPreparationFailure(ctx, in.Owner.RunID)
		if readErr != nil {
			return PreparedMultiRuntime{}, readErr
		}
		if message != "" {
			return PreparedMultiRuntime{}, fmt.Errorf("%w: %s; explicitly cancel the exact Task to abandon the settled failed attempt", review.ErrPreparationFailed, message)
		}
	}

	gotPlan, gotRun, outcome, err := canonical.ReadPreparationReceipt(ctx, requestID, payloadHash)
	if err == nil && gotPlan == planID && gotRun == in.Owner.RunID && outcome == "FAILED" {
		return PreparedMultiRuntime{}, fmt.Errorf("%w: exact workspace preparation failed and is settled; cancel the exact Task to abandon it", review.ErrPreparationFailed)
	}
	if err != nil || gotPlan != planID || gotRun != in.Owner.RunID || outcome != "OBSERVATION_RECORDED" {
		return PreparedMultiRuntime{}, errors.Join(err, errors.New("RUNTIME_WORKSPACE_PREPARATION_OUTCOME_UNKNOWN"))
	}
	reader, ok := runs.(interface {
		RuntimeWorkset(context.Context, string) (workset.RuntimeWorkset, error)
	})
	if !ok {
		return PreparedMultiRuntime{}, errors.New("durable RuntimeWorkset readback unavailable")
	}
	frozen, err := reader.RuntimeWorkset(ctx, in.Owner.RunID)
	if err != nil || frozen.RunID != in.Owner.RunID || frozen.ExecutionID != in.Owner.ExecutionID {
		return PreparedMultiRuntime{}, errors.Join(err, errors.New("RUNTIME_WORKSET_BINDING_OUTCOME_UNKNOWN"))
	}
	cwd, binding, err := multiRuntimeClientBinding(frozen)
	if err != nil {
		return PreparedMultiRuntime{}, err
	}
	if in.ReadOnly && frozen.TaskPermissions == nil {
		binding.WritableRoots = nil
	}
	return PreparedMultiRuntime{Workset: frozen, CWD: cwd, Binding: binding, ReadOnly: in.ReadOnly}, nil
}

func (c *Coordinator) multiRuntimeWorktree(ctx context.Context, value workspace.Workspace, member workspace.Member, target workset.Target, owner MultiRuntimeOwner) (worktree.Worktree, workspace.Workspace, error) {
	repo, err := c.Repositories.Get(ctx, target.RepositoryID)
	if err != nil {
		return worktree.Worktree{}, value, err
	}
	if repo.ID != target.RepositoryID || repo.ProjectID != owner.ProjectID || repo.Path == "" {
		return worktree.Worktree{}, value, errors.New("repository conflicts with supplied multi-runtime owner")
	}
	if member.WorktreeID != "" {
		getter, ok := c.Worktrees.(worktreeGetter)
		if !ok {
			return worktree.Worktree{}, value, errors.New("worktree manager cannot reload planned member")
		}
		tree, getErr := getter.Get(ctx, member.WorktreeID)
		if getErr != nil {
			return worktree.Worktree{}, value, getErr
		}
		if tree.ID != member.WorktreeID || tree.RepositoryID != target.RepositoryID || tree.BaseCommit != target.OriginCommit || tree.Status != worktree.StatusReady {
			return worktree.Worktree{}, value, errors.New("managed worktree conflicts with supplied target")
		}
		return tree, value, nil
	}
	tree, err := c.Worktrees.Create(ctx, worktree.CreateInput{PreserveOnFailure: true, MemberID: member.ID, MemberVersion: member.Version, MemberAlias: member.Alias, RepositoryID: target.RepositoryID, RepositoryPath: repo.Path, FeatureAreaID: owner.FeatureAreaID, RunID: owner.RunID, WorkspaceID: value.ID, BaseCommit: target.OriginCommit})
	if err != nil {
		return worktree.Worktree{}, value, err
	}
	if tree.RepositoryID != target.RepositoryID || tree.BaseCommit != target.OriginCommit || tree.Status != worktree.StatusReady || tree.Path == "" {
		return worktree.Worktree{}, value, errors.New("created worktree conflicts with supplied target")
	}
	attacher, ok := c.Workspaces.(interface {
		AttachMemberWorktree(context.Context, string, string, string) (workspace.Workspace, error)
	})
	if !ok {
		return worktree.Worktree{}, value, errors.New("exact workspace member attachment unavailable")
	}
	updated, err := attacher.AttachMemberWorktree(ctx, value.ID, member.ID, tree.ID)
	if err != nil {
		return worktree.Worktree{}, value, err
	}
	return tree, updated, nil
}

// ObserveMultiRuntime records one terminal observation for every frozen member.
// Inspection failures become durable UNKNOWN observations; root/head/scope
// failures become SCOPE_CONFLICT observations and are returned to the caller.
func (c *Coordinator) ObserveMultiRuntime(ctx context.Context, prepared PreparedMultiRuntime) error {
	if c == nil || c.Runs == nil || c.Evidence == nil {
		return errors.New("multi-runtime observation dependencies are required")
	}
	frozen := prepared.Workset
	if err := frozen.Validate(); err != nil {
		return err
	}
	cwd, binding, err := multiRuntimeClientBinding(frozen)
	if prepared.ReadOnly && frozen.TaskPermissions == nil {
		binding.WritableRoots = nil
	}
	if err != nil || cwd != prepared.CWD || !sameRuntimeBinding(binding, prepared.Binding) {
		return errors.New("prepared multi-runtime binding is inconsistent with frozen workset")
	}
	observer, ok := c.Runs.(interface {
		ObserveRepositoryBinding(context.Context, run.RepositoryObservation) error
	})
	if !ok {
		return errors.New("durable repository observation storage is unavailable")
	}
	loader, ok := c.Runs.(interface {
		RuntimeWorkset(context.Context, string) (workset.RuntimeWorkset, error)
	})
	if !ok {
		return errors.New("durable RuntimeWorkset lookup is unavailable")
	}
	stored, err := loader.RuntimeWorkset(ctx, frozen.RunID)
	if err != nil || !reflect.DeepEqual(stored, frozen) {
		if err != nil {
			return err
		}
		return errors.New("prepared multi-runtime workset differs from durable authority")
	}
	var state, cause error
	for _, member := range frozen.Members {
		observation, observedState, observedErr := c.observeMultiRuntimeMember(ctx, frozen, member, prepared.ReadOnly)
		if observedErr != nil && observation.ObservationState == "" {
			observation = run.RepositoryObservation{RunID: frozen.RunID, RepositoryID: member.RepositoryID, ObservationState: observedState, ObservedAt: time.Now().UTC()}
		}
		if putErr := observer.ObserveRepositoryBinding(context.WithoutCancel(ctx), observation); putErr != nil {
			return putErr
		}
		if observedErr != nil && state == nil {
			state, cause = errors.New(observedState), observedErr
		}
	}
	if state != nil {
		return &MultiRuntimeObservationError{State: state.Error(), Cause: cause}
	}
	return nil
}

func (c *Coordinator) observeMultiRuntimeMember(ctx context.Context, frozen workset.RuntimeWorkset, member workset.RuntimeMemberBinding, readOnly bool) (run.RepositoryObservation, string, error) {
	root, identity, err := multiRuntimeRootIdentity(member.CanonicalRoot)
	if err != nil {
		return run.RepositoryObservation{}, "UNKNOWN", err
	}
	rootConflict := root != member.CanonicalRoot || identity != member.RootFileIdentity || identity != member.RuntimeRootIdentity
	endSnapshot, err := c.Evidence.CaptureSnapshot(ctx, root)
	if err != nil || !workset.ValidCommit(endSnapshot) {
		if err == nil {
			err = errors.New("invalid end snapshot")
		}
		return run.RepositoryObservation{}, "UNKNOWN", err
	}
	gitEvidence, err := c.Evidence.CollectRun(ctx, root, member.OriginCommit, member.StartSnapshot, endSnapshot)
	if err != nil || !workset.ValidCommit(gitEvidence.HeadCommit) {
		if err == nil {
			err = errors.New("invalid end head")
		}
		return run.RepositoryObservation{}, "UNKNOWN", err
	}
	state := "OBSERVED"
	if rootConflict || gitEvidence.HeadCommit != member.StartHEAD || (readOnly && len(gitEvidence.ChangedFiles) != 0) || (!multiRuntimeWriteAllowsChanges(member.Write) && len(gitEvidence.ChangedFiles) != 0) || !multiRuntimePathsAllowed(member.Write, gitEvidence.ChangedFiles) {
		state = "SCOPE_CONFLICT"
	} else if len(gitEvidence.ChangedFiles) == 0 {
		state = "NO_CHANGE"
	}
	out := observationFor(member, frozen.RunID, gitEvidence.HeadCommit, identity, state)
	out.EndSnapshot = endSnapshot
	out.ChangedPaths = append([]string(nil), gitEvidence.ChangedFiles...)
	sum := sha256.Sum256([]byte(gitEvidence.Diff))
	out.DiffDigest = hex.EncodeToString(sum[:])
	if state == "SCOPE_CONFLICT" {
		return out, state, errors.New("runtime changes exceed frozen scope")
	}
	return out, state, nil
}

func observationFor(member workset.RuntimeMemberBinding, runID, head, identity, state string) run.RepositoryObservation {
	return run.RepositoryObservation{RunID: runID, RepositoryID: member.RepositoryID, EndHEAD: head, EndRootIdentity: identity, ObservationState: state, ObservedAt: time.Now().UTC()}
}

func multiRuntimeWriteAllowsChanges(scope workset.WriteScope) bool {
	return scope.Kind == "PATHS" || scope.Kind == "REPOSITORY"
}

func multiRuntimePathsAllowed(scope workset.WriteScope, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	if scope.Kind == "REPOSITORY" {
		return true
	}
	if scope.Kind != "PATHS" {
		return false
	}
	for _, changed := range paths {
		if changed == "" || filepath.IsAbs(changed) || filepath.Clean(changed) != changed || changed == "." || changed == ".." || strings.HasPrefix(changed, ".."+string(filepath.Separator)) {
			return false
		}
		allowed := false
		for _, prefix := range scope.Paths {
			if changed == prefix || strings.HasPrefix(changed, prefix+"/") {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

func multiRuntimeRootIdentity(path string) (string, string, error) {
	if path == "" {
		return "", "", errors.New("managed root is required")
	}
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", fmt.Errorf("resolve managed root: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	root = filepath.Clean(root)
	if root == string(filepath.Separator) {
		return "", "", errors.New("filesystem root is not a managed root")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("managed root is not a directory")
		}
		return "", "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", errors.New("managed root filesystem identity is unavailable")
	}
	// Dev+Ino are stable across normal file edits. Including canonical root
	// detects a substitution to a different managed path without incorporating
	// mutable timestamps, size, or directory contents.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", root, stat.Dev, stat.Ino)))
	return root, hex.EncodeToString(sum[:]), nil
}

func multiRuntimeClientBinding(frozen workset.RuntimeWorkset) (string, codex.RuntimeBinding, error) {
	if frozen.TaskPermissions != nil {
		if err := validateTaskEnvironment(frozen); err != nil {
			return "", codex.RuntimeBinding{}, err
		}
		cwd, roots, writes := frozen.TaskPermissionBinding()
		binding := codex.RuntimeBinding{RuntimeWorkspaceRoots: roots, WritableRoots: writes, TaskPermissions: frozen.TaskPermissions}
		return cwd, binding, validateMultiRuntimeClientBinding(cwd, binding)
	}
	roots := make([]string, 0, len(frozen.Members))
	writes := make([]string, 0, len(frozen.Members))
	cwd := ""
	for _, member := range frozen.Members {
		roots = append(roots, member.CanonicalRoot)
		if member.Write.Kind != "NONE" {
			writes = append(writes, member.CanonicalRoot)
		}
		if member.MemberID == frozen.PrimaryMemberID {
			cwd = member.CanonicalRoot
		}
	}
	sort.Strings(roots)
	sort.Strings(writes)
	binding := codex.RuntimeBinding{RuntimeWorkspaceRoots: roots, WritableRoots: writes}
	if err := validateMultiRuntimeClientBinding(cwd, binding); err != nil {
		return "", codex.RuntimeBinding{}, err
	}
	return cwd, binding, nil
}

// Keep the cross-package boundary explicit: codex validates this again at its
// thread boundary, while this helper rejects an invalid handoff before it can
// be persisted or returned to a caller.
func validateMultiRuntimeClientBinding(cwd string, binding codex.RuntimeBinding) error {
	if cwd == "" || len(binding.RuntimeWorkspaceRoots) == 0 {
		return errors.New("primary cwd and runtime roots are required")
	}
	roots := make(map[string]struct{}, len(binding.RuntimeWorkspaceRoots))
	for _, root := range binding.RuntimeWorkspaceRoots {
		if root == "" || root == string(filepath.Separator) || !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return errors.New("runtime root is invalid")
		}
		if _, exists := roots[root]; exists {
			return errors.New("runtime roots are not unique")
		}
		roots[root] = struct{}{}
	}
	if _, ok := roots[cwd]; !ok {
		return errors.New("primary cwd is not an exact runtime root")
	}
	seenWrites := map[string]struct{}{}
	for _, root := range binding.WritableRoots {
		if _, exists := roots[root]; !exists {
			return errors.New("writable root is outside runtime roots")
		}
		if _, exists := seenWrites[root]; exists {
			return errors.New("writable roots are not unique")
		}
		seenWrites[root] = struct{}{}
	}
	return nil
}

func sameRuntimeBinding(a, b codex.RuntimeBinding) bool {
	return strings.Join(a.RuntimeWorkspaceRoots, "\x00") == strings.Join(b.RuntimeWorkspaceRoots, "\x00") && strings.Join(a.WritableRoots, "\x00") == strings.Join(b.WritableRoots, "\x00") && reflect.DeepEqual(a.TaskPermissions, b.TaskPermissions)
}

func validateMultiRuntimeInput(in MultiRuntimePrepareInput) error {
	if in.UpperReview && (in.Owner.ReviewID == "" || !in.ReadOnly) {
		return errors.New("Upper Review preparation requires a read-only Review owner")
	}
	if in.WorkspaceID == "" || in.PrimaryMemberID == "" || in.Owner.ProjectID == "" || in.Owner.FeatureAreaID == "" || in.Owner.RunID == "" || (in.Owner.TaskID == "") == (in.Owner.ReviewID == "") {
		return errors.New("exact multi-runtime owner and workspace are required")
	}
	if in.Owner.TaskID != "" && (in.Owner.TaskDefinitionRevisionID == "" || in.Owner.ExecutionID == "") {
		return errors.New("task runtime requires definition revision and execution identity")
	}
	if in.Owner.ReviewID != "" && (in.Owner.TaskDefinitionRevisionID != "" || in.Owner.ExecutionID != "") {
		return errors.New("review runtime cannot carry task execution identity")
	}
	if in.Runtime.BackendConfigID == "" || in.Runtime.BackendConfigFingerprint == "" || in.Runtime.Model == "" || in.Runtime.ReasoningEffort == "" || in.Runtime.RuntimeAdapterIdentity == "" || in.Runtime.RuntimeProtocolIdentity == "" || in.Runtime.CreatedAt.IsZero() {
		return errors.New("frozen backend and runtime identities are required")
	}
	return workset.ValidateTargets(in.Targets)
}
