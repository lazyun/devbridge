package workspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"github.com/lazyun/devbridge/internal/workset"
)

type Service struct {
	store Store
	newID func() (string, error)
	now   func() time.Time
}

// PlanMembers freezes the member identities a proposed operation will use
// without allocating a workspace or changing an existing one.  The returned
// set is safe to persist in an admission receipt and must be handed back to
// EnsurePlannedMembers after confirmation.
func (s *Service) PlanMembers(ctx context.Context, projectID, areaID string, targets []workset.Target) (workset.Set, error) {
	if s == nil || s.store == nil || projectID == "" || areaID == "" || len(targets) == 0 {
		return workset.Set{}, ErrConflict
	}
	value, err := s.store.GetByFeatureArea(ctx, areaID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return workset.Set{}, err
	}
	workspaceID := plannedWorkspaceID(projectID, areaID)
	if err == nil {
		if value.ProjectID != projectID || value.RuntimeQuarantined {
			return workset.Set{}, ErrConflict
		}
		workspaceID = value.ID
	}
	normalized := workset.CloneTargets(targets)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].RepositoryID < normalized[j].RepositoryID })
	seen := make(map[string]bool, len(normalized))
	for i := range normalized {
		t := &normalized[i]
		if t.RepositoryID == "" || !workset.ValidCommit(t.OriginCommit) || !t.Read || seen[t.RepositoryID] {
			return workset.Set{}, ErrConflict
		}
		seen[t.RepositoryID] = true
		if err == nil {
			member, memberErr := value.MemberForRepository(t.RepositoryID)
			if memberErr == nil {
				if (t.MemberID != "" && t.MemberID != member.ID) || (t.MemberVersion != 0 && t.MemberVersion != member.Version) || t.OriginCommit != member.OriginCommit || t.OriginRef != member.OriginRef || t.DevelopmentCycleID != member.DevelopmentCycleID {
					return workset.Set{}, ErrConflict
				}
				t.MemberID, t.MemberVersion = member.ID, member.Version
				continue
			}
			if !errors.Is(memberErr, ErrNotFound) {
				return workset.Set{}, memberErr
			}
		}
		expected := memberIdentity(workspaceID, t.RepositoryID)
		if (t.MemberID != "" && t.MemberID != expected) || (t.MemberVersion != 0 && t.MemberVersion != 1) {
			return workset.Set{}, ErrConflict
		}
		t.MemberID, t.MemberVersion = expected, 1
	}
	set := workset.Set{Version: workset.Version, WorkspaceID: workspaceID, Targets: normalized}
	if err := set.Validate(); err != nil {
		return workset.Set{}, err
	}
	return set, nil
}

// PlanInspectionMembers has the same no-side-effect admission semantics as
// PlanMembers, but uses the concrete ContextProbe as its owner namespace.
// A Project alone is intentionally insufficient: two inspections in the same
// Project must not share members, worktrees, or a future conversation.
func (s *Service) PlanInspectionMembers(ctx context.Context, projectID, inspectionTaskID, inspectionID string, targets []workset.Target) (workset.Set, error) {
	if s == nil || s.store == nil || projectID == "" || inspectionTaskID == "" || inspectionID == "" || len(targets) == 0 {
		return workset.Set{}, ErrConflict
	}
	value, err := s.store.GetByOwner(ctx, OwnerProjectInspection, inspectionID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return workset.Set{}, err
	}
	workspaceID := plannedInspectionWorkspaceID(projectID, inspectionID)
	if err == nil {
		if value.ProjectID != projectID || value.OwnerKind != OwnerProjectInspection || value.OwnerID != inspectionID || value.InspectionTaskID != inspectionTaskID || value.InspectionID != inspectionID || value.RuntimeQuarantined {
			return workset.Set{}, ErrConflict
		}
		workspaceID = value.ID
	}
	normalized := workset.CloneTargets(targets)
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].RepositoryID < normalized[j].RepositoryID })
	seen := make(map[string]bool, len(normalized))
	for i := range normalized {
		t := &normalized[i]
		if t.RepositoryID == "" || !workset.ValidCommit(t.OriginCommit) || !t.Read || t.Write.Kind != "NONE" || len(t.Write.Paths) != 0 || workset.ValidateProjectInspectionReadScope(t.ReadScope) != nil || seen[t.RepositoryID] {
			return workset.Set{}, ErrConflict
		}
		seen[t.RepositoryID] = true
		if err == nil {
			member, memberErr := value.MemberForRepository(t.RepositoryID)
			if memberErr == nil {
				if (t.MemberID != "" && t.MemberID != member.ID) || (t.MemberVersion != 0 && t.MemberVersion != member.Version) || t.OriginCommit != member.OriginCommit || t.OriginRef != member.OriginRef || t.DevelopmentCycleID != member.DevelopmentCycleID {
					return workset.Set{}, ErrConflict
				}
				t.MemberID, t.MemberVersion = member.ID, member.Version
				continue
			}
			if !errors.Is(memberErr, ErrNotFound) {
				return workset.Set{}, memberErr
			}
		}
		expected := memberIdentity(workspaceID, t.RepositoryID)
		// A confirmation receipt may already carry its deterministic member ID
		// (derived with the inspection ID). Preserve that exact identity; only a
		// missing ID is filled by this planner.
		if (t.MemberID == "" && t.MemberVersion != 0) || (t.MemberID != "" && t.MemberVersion != 1) {
			return workset.Set{}, ErrConflict
		}
		if t.MemberID == "" {
			t.MemberID = expected
		}
		t.MemberVersion = 1
	}
	set := workset.Set{Version: workset.Version, WorkspaceID: workspaceID, Targets: normalized}
	if err := set.Validate(); err != nil {
		return workset.Set{}, err
	}
	return set, nil
}

// EnsurePlannedMembers materializes exactly a previously planned set. It is
// intentionally separate from PlanMembers so options and confirmation reads
// do not create a workspace or append members.
func (s *Service) EnsurePlannedMembers(ctx context.Context, projectID, areaID, workspaceID string, targets []workset.Target) (Workspace, error) {
	planned, err := s.PlanMembers(ctx, projectID, areaID, targets)
	if err != nil {
		return Workspace{}, err
	}
	if planned.WorkspaceID != workspaceID {
		return Workspace{}, ErrConflict
	}
	value, err := s.store.GetByFeatureArea(ctx, areaID)
	if errors.Is(err, ErrNotFound) {
		now := s.now().UTC()
		// V2 workspaces deliberately leave scalar repository/base projections
		// empty. A member list is the sole authority for a multi-repository Run.
		value = Workspace{ID: workspaceID, ProjectID: projectID, FeatureAreaID: areaID, OwnerKind: OwnerFeatureArea, OwnerID: areaID, Status: StatusReady, TokenBaselineAvailable: true, CreatedAt: now, UpdatedAt: now}
		value.Members = membersFromTargets(value.ID, planned.Targets, now)
		if err = s.store.Create(ctx, value); errors.Is(err, ErrConflict) {
			value, err = s.store.GetByFeatureArea(ctx, areaID)
		}
	}
	if err != nil {
		return Workspace{}, err
	}
	if value.ID != workspaceID || value.ProjectID != projectID || value.FeatureAreaID != areaID || value.RuntimeQuarantined {
		return Workspace{}, ErrConflict
	}
	for _, target := range planned.Targets {
		member, memberErr := value.MemberForRepository(target.RepositoryID)
		if memberErr == nil {
			if member.ID != target.MemberID || member.Version != target.MemberVersion || member.OriginCommit != target.OriginCommit || member.OriginRef != target.OriginRef || member.DevelopmentCycleID != target.DevelopmentCycleID {
				return Workspace{}, ErrConflict
			}
			continue
		}
		if !errors.Is(memberErr, ErrNotFound) {
			return Workspace{}, memberErr
		}
		add := target
		value, err = s.store.Update(ctx, value.ID, func(w *Workspace) error {
			if existing, e := w.MemberForRepository(add.RepositoryID); e == nil {
				if existing.ID != add.MemberID || existing.Version != add.MemberVersion || existing.OriginCommit != add.OriginCommit || existing.OriginRef != add.OriginRef || existing.DevelopmentCycleID != add.DevelopmentCycleID {
					return ErrConflict
				}
				return nil
			}
			now := s.now().UTC()
			w.Members = append(w.Members, Member{ID: add.MemberID, WorkspaceID: w.ID, RepositoryID: add.RepositoryID, Version: add.MemberVersion, Alias: add.MemberID, OriginCommit: add.OriginCommit, OriginRef: add.OriginRef, DevelopmentCycleID: add.DevelopmentCycleID, Preparation: "PENDING", CreatedAt: now, UpdatedAt: now})
			w.UpdatedAt = now
			return nil
		})
		if err != nil {
			return Workspace{}, err
		}
	}
	return value, nil
}

// EnsurePlannedInspectionMembers materializes only the exact receipt-derived
// target set. It never appends a later Project repository to an old probe.
func (s *Service) EnsurePlannedInspectionMembers(ctx context.Context, projectID, inspectionTaskID, inspectionID, workspaceID string, targets []workset.Target) (Workspace, error) {
	planned, err := s.PlanInspectionMembers(ctx, projectID, inspectionTaskID, inspectionID, targets)
	if err != nil || planned.WorkspaceID != workspaceID {
		return Workspace{}, ErrConflict
	}
	value, err := s.store.GetByOwner(ctx, OwnerProjectInspection, inspectionID)
	if errors.Is(err, ErrNotFound) {
		now := s.now().UTC()
		value = Workspace{ID: workspaceID, ProjectID: projectID, OwnerKind: OwnerProjectInspection, OwnerID: inspectionID, InspectionTaskID: inspectionTaskID, InspectionID: inspectionID, Status: StatusReady, TokenBaselineAvailable: true, CreatedAt: now, UpdatedAt: now, Members: membersFromTargets(workspaceID, planned.Targets, now)}
		if err = s.store.Create(ctx, value); errors.Is(err, ErrConflict) {
			value, err = s.store.GetByOwner(ctx, OwnerProjectInspection, inspectionID)
		}
	}
	if err != nil {
		return Workspace{}, err
	}
	if value.ID != workspaceID || value.ProjectID != projectID || value.OwnerKind != OwnerProjectInspection || value.OwnerID != inspectionID || value.InspectionTaskID != inspectionTaskID || value.InspectionID != inspectionID || value.FeatureAreaID != "" || value.RuntimeQuarantined {
		return Workspace{}, ErrConflict
	}
	for _, target := range planned.Targets {
		member, memberErr := value.MemberForRepository(target.RepositoryID)
		if memberErr != nil || member.ID != target.MemberID || member.Version != target.MemberVersion || member.OriginCommit != target.OriginCommit || member.OriginRef != target.OriginRef || member.DevelopmentCycleID != target.DevelopmentCycleID {
			return Workspace{}, ErrConflict
		}
	}
	return value, nil
}

// EnsureRecoveryMembers gives one admitted Recovery child its own managed
// workspace container. Source workspace and member identities remain read-only
// provenance and are never consulted to fill missing child members.
func (s *Service) EnsureRecoveryMembers(ctx context.Context, projectID, featureAreaID, recoveryID string, targets []workset.Target) (Workspace, error) {
	if s == nil || s.store == nil || projectID == "" || recoveryID == "" || workset.ValidateTargets(targets) != nil {
		return Workspace{}, ErrConflict
	}
	for _, target := range targets {
		if target.Write.Kind != "NONE" || len(target.Write.Paths) != 0 {
			return Workspace{}, ErrConflict
		}
	}
	id := plannedRecoveryWorkspaceID(projectID, recoveryID)
	value, err := s.store.GetByOwner(ctx, OwnerRecoveryStatus, recoveryID)
	if errors.Is(err, ErrNotFound) {
		now := s.now().UTC()
		value = Workspace{ID: id, ProjectID: projectID, OwnerKind: OwnerRecoveryStatus, OwnerID: recoveryID, Status: StatusReady, TokenBaselineAvailable: true, CreatedAt: now, UpdatedAt: now, Members: membersFromTargets(id, targets, now)}
		if err = s.store.Create(ctx, value); errors.Is(err, ErrConflict) {
			createErr := err
			value, err = s.store.GetByOwner(ctx, OwnerRecoveryStatus, recoveryID)
			if err != nil {
				return Workspace{}, errors.Join(createErr, err)
			}
		}
	}
	if err != nil {
		return Workspace{}, err
	}
	if value.ID != id || value.ProjectID != projectID || value.FeatureAreaID != "" || value.OwnerKind != OwnerRecoveryStatus || value.OwnerID != recoveryID || value.RuntimeQuarantined || len(value.Members) != len(targets) {
		return Workspace{}, ErrConflict
	}
	for _, target := range targets {
		member, memberErr := value.MemberForRepository(target.RepositoryID)
		if memberErr != nil || member.ID != target.MemberID || member.Version != target.MemberVersion || member.OriginCommit != target.OriginCommit {
			return Workspace{}, ErrConflict
		}
	}
	return value, nil
}

func plannedRecoveryWorkspaceID(projectID, recoveryID string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + string(OwnerRecoveryStatus) + "\x00" + recoveryID))
	return "workspace_recovery_" + hex.EncodeToString(sum[:16])
}

func plannedWorkspaceID(projectID, areaID string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + areaID))
	return "workspace_plan_" + hex.EncodeToString(sum[:16])
}

func plannedInspectionWorkspaceID(projectID, inspectionID string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + string(OwnerProjectInspection) + "\x00" + inspectionID))
	return "workspace_inspection_" + hex.EncodeToString(sum[:16])
}

func membersFromTargets(workspaceID string, targets []workset.Target, now time.Time) []Member {
	members := make([]Member, 0, len(targets))
	for _, target := range targets {
		members = append(members, Member{ID: target.MemberID, WorkspaceID: workspaceID, RepositoryID: target.RepositoryID, Version: target.MemberVersion, Alias: target.MemberID, OriginCommit: target.OriginCommit, OriginRef: target.OriginRef, DevelopmentCycleID: target.DevelopmentCycleID, Preparation: "PENDING", CreatedAt: now, UpdatedAt: now})
	}
	return members
}

func NewService(store Store, newID func() (string, error), now func() time.Time) *Service {
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, newID: newID, now: now}
}

// EnsureForFeatureArea creates or reads the exact canonical owner. Later Task
// baselines do not replace the workspace's immutable origin commit.
func (s *Service) EnsureForFeatureArea(ctx context.Context, projectID, repositoryID, areaID, baseCommit string) (Workspace, error) {
	if s == nil || s.store == nil || projectID == "" || repositoryID == "" || areaID == "" || baseCommit == "" {
		return Workspace{}, errors.New("exact FeatureArea workspace ownership and baseline are required")
	}
	value, err := s.store.GetByFeatureArea(ctx, areaID)
	if errors.Is(err, ErrNotFound) {
		id, e := s.newID()
		if e != nil {
			return Workspace{}, e
		}
		now := s.now().UTC()
		value = Workspace{ID: id, ProjectID: projectID, RepositoryID: repositoryID, FeatureAreaID: areaID, OwnerKind: OwnerFeatureArea, OwnerID: areaID, BaseCommit: baseCommit, Status: StatusReady, TokenBaselineAvailable: true, CreatedAt: now, UpdatedAt: now}
		value.Members = []Member{LegacyMember(value)}
		err = s.store.Create(ctx, value)
		if errors.Is(err, ErrConflict) {
			value, err = s.store.GetByFeatureArea(ctx, areaID)
		}
	}
	if err != nil {
		return Workspace{}, err
	}
	if value.ProjectID != projectID || value.FeatureAreaID != areaID || value.RuntimeQuarantined {
		return Workspace{}, ErrConflict
	}
	if _, e := value.MemberForRepository(repositoryID); errors.Is(e, ErrNotFound) {
		value, err = s.store.Update(ctx, value.ID, func(w *Workspace) error {
			if _, e := w.MemberForRepository(repositoryID); e == nil {
				return nil
			}
			now := s.now().UTC()
			id := memberIdentity(w.ID, repositoryID)
			w.Members = append(w.Members, Member{ID: id, WorkspaceID: w.ID, RepositoryID: repositoryID, Version: 1, Alias: id, OriginCommit: baseCommit, Preparation: "PENDING", CreatedAt: now, UpdatedAt: now})
			w.UpdatedAt = now
			return nil
		})
	}
	return value, err
}

func (s *Service) AttachWorktree(ctx context.Context, id, worktreeID string) (Workspace, error) {
	if worktreeID == "" {
		return Workspace{}, errors.New("workspace worktree id is required")
	}
	return s.store.Update(ctx, id, func(value *Workspace) error {
		if value.WorktreeID != "" && value.WorktreeID != worktreeID {
			return ErrConflict
		}
		if value.RuntimeQuarantined {
			return ErrConflict
		}
		if len(value.Members) != 1 {
			return ErrConflict
		}
		value.Members[0].WorktreeID = worktreeID
		value.Members[0].Preparation = "READY"
		value.Members[0].Failure = ""
		value.Members[0].UpdatedAt = s.now().UTC()
		value.WorktreeID = worktreeID
		value.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Service) RecordTokenBaseline(ctx context.Context, id string, counts *TokenCounts) (Workspace, error) {
	return s.store.Update(ctx, id, func(value *Workspace) error {
		value.TokenBaselineAvailable = counts != nil
		value.TokenBaseline = TokenCounts{}
		if counts != nil {
			if !validCounts(*counts) {
				return errors.New("workspace token baseline is invalid")
			}
			value.TokenBaseline = *counts
		}
		value.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Service) MarkContinuityUnavailable(ctx context.Context, id string) (Workspace, error) {
	return s.store.Update(ctx, id, func(value *Workspace) error {
		value.Status = StatusContinuityUnavailable
		value.TokenBaselineAvailable = false
		value.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Service) Get(ctx context.Context, id string) (Workspace, error) {
	return s.store.Get(ctx, id)
}

func (s *Service) GetByFeatureArea(ctx context.Context, areaID string) (Workspace, error) {
	return s.store.GetByFeatureArea(ctx, areaID)
}

func (s *Service) GetByOwner(ctx context.Context, kind OwnerKind, ownerID string) (Workspace, error) {
	return s.store.GetByOwner(ctx, kind, ownerID)
}

func (s *Service) List(ctx context.Context) ([]Workspace, error) { return s.store.List(ctx) }

func validCounts(value TokenCounts) bool {
	return value.InputTokens >= 0 && value.CachedInputTokens >= 0 && value.CacheWriteInputTokens >= 0 && value.OutputTokens >= 0 && value.ReasoningOutputTokens >= 0 && value.TotalTokens >= 0
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "workspace_" + hex.EncodeToString(raw[:]), nil
}

// AttachMemberWorktree binds one exact member without selecting a default root.
func (s *Service) AttachMemberWorktree(ctx context.Context, workspaceID, memberID, worktreeID string) (Workspace, error) {
	if memberID == "" || worktreeID == "" {
		return Workspace{}, ErrConflict
	}
	return s.store.Update(ctx, workspaceID, func(v *Workspace) error {
		if v.RuntimeQuarantined {
			return ErrConflict
		}
		for i := range v.Members {
			m := &v.Members[i]
			if m.ID != memberID {
				continue
			}
			if m.WorktreeID != "" && m.WorktreeID != worktreeID {
				return ErrConflict
			}
			m.WorktreeID = worktreeID
			m.Preparation = "READY"
			m.Failure = ""
			m.UpdatedAt = s.now().UTC()
			v.UpdatedAt = m.UpdatedAt
			// Historical scalar projection is never used to pick another repository.
			if m.RepositoryID == v.RepositoryID {
				v.WorktreeID = worktreeID
			}
			return nil
		}
		return ErrNotFound
	})
}
func (s *Service) RecordMemberFailure(ctx context.Context, workspaceID, memberID, failure string) (Workspace, error) {
	return s.store.Update(ctx, workspaceID, func(v *Workspace) error {
		for i := range v.Members {
			m := &v.Members[i]
			if m.ID == memberID {
				m.Preparation = "FAILED"
				m.Failure = failure
				m.UpdatedAt = s.now().UTC()
				v.UpdatedAt = m.UpdatedAt
				return nil
			}
		}
		return ErrNotFound
	})
}
