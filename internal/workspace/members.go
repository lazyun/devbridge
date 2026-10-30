package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"

	"github.com/lazyun/devbridge/internal/workset"
)

// Clone detaches mutable resource facts from the store's canonical value.
func Clone(v Workspace) Workspace { v.Members = append([]Member(nil), v.Members...); return v }

// MemberForRepository is exact: absence never falls back to another member.
func (v Workspace) MemberForRepository(repositoryID string) (Member, error) {
	for _, m := range v.Members {
		if m.RepositoryID == repositoryID {
			return m, nil
		}
	}
	return Member{}, ErrNotFound
}

// LegacyMember projects only a historical single-repository resource. Its ID
// matches the v73 backfill, preserving existing worktree and receipt identity.
func LegacyMember(v Workspace) Member {
	state := "PENDING"
	if v.WorktreeID != "" {
		state = "READY"
	}
	return Member{ID: "member_" + v.ID, WorkspaceID: v.ID, RepositoryID: v.RepositoryID, Version: 1, Alias: v.RepositoryID, OriginCommit: v.BaseCommit, WorktreeID: v.WorktreeID, Preparation: state, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}
func memberIdentity(workspaceID, repositoryID string) string {
	sum := sha256.Sum256([]byte(workspaceID + "\x00" + repositoryID))
	return "member_" + hex.EncodeToString(sum[:16])
}

func ValidateMembers(v Workspace) error {
	ids, repos, aliases := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, m := range v.Members {
		if m.ID == "" || m.WorkspaceID != v.ID || m.RepositoryID == "" || m.Version != 1 || m.Alias == "" || strings.ContainsAny(m.Alias, "/\\\x00") || m.Alias == "." || m.Alias == ".." || !workset.ValidCommit(m.OriginCommit) || ids[m.ID] || repos[m.RepositoryID] || aliases[m.Alias] {
			return ErrConflict
		}
		if m.Preparation != "PENDING" && m.Preparation != "READY" && m.Preparation != "FAILED" && m.Preparation != "UNKNOWN" {
			return ErrConflict
		}
		if m.Preparation == "READY" && m.WorktreeID == "" {
			return ErrConflict
		}
		ids[m.ID] = true
		repos[m.RepositoryID] = true
		aliases[m.Alias] = true
	}
	return nil
}
func ValidateMemberUpdate(previous, next Workspace) error {
	if err := ValidateMembers(next); err != nil {
		return err
	}
	for _, old := range previous.Members {
		m, err := next.MemberForRepository(old.RepositoryID)
		if err != nil {
			return ErrConflict
		}
		oldState, newState := old, m
		oldState.Preparation, newState.Preparation = "", ""
		oldState.Failure, newState.Failure = "", ""
		oldState.UpdatedAt, newState.UpdatedAt = old.CreatedAt, old.CreatedAt
		if old.WorktreeID != "" && old.WorktreeID != m.WorktreeID {
			return ErrConflict
		}
		oldState.WorktreeID, newState.WorktreeID = "", ""
		if !reflect.DeepEqual(oldState, newState) {
			return ErrConflict
		}
	}
	return nil
}

// ValidateWorkset resolves only the frozen identities. New Workspace members
// do not participate and cannot enlarge an already-approved operation.
func (v Workspace) ValidateWorkset(set workset.Set) error {
	if err := set.Validate(); err != nil {
		return err
	}
	if set.WorkspaceID != v.ID || v.RuntimeQuarantined {
		return ErrConflict
	}
	for _, target := range set.Targets {
		m, err := v.MemberForRepository(target.RepositoryID)
		if err != nil || m.ID != target.MemberID || m.Version != target.MemberVersion || m.OriginCommit != target.OriginCommit || m.OriginRef != target.OriginRef || m.DevelopmentCycleID != target.DevelopmentCycleID {
			return ErrConflict
		}
		if m.Preparation != "READY" || m.WorktreeID == "" {
			return errors.New("required workspace member is unavailable: " + target.RepositoryID)
		}
	}
	return nil
}

// SyncLegacyWorktreeUpdate interprets the historical scalar attach command only
// when its exact repository member is still unbound. It cannot select a member.
func SyncLegacyWorktreeUpdate(previous Workspace, next *Workspace) error {
	if previous.WorktreeID == next.WorktreeID {
		return nil
	}
	for i := range next.Members {
		m := &next.Members[i]
		if m.RepositoryID != previous.RepositoryID {
			continue
		}
		if m.WorktreeID != "" && m.WorktreeID != next.WorktreeID {
			return ErrConflict
		}
		m.WorktreeID = next.WorktreeID
		m.Preparation = "READY"
		m.Failure = ""
		m.UpdatedAt = next.UpdatedAt
		return nil
	}
	return ErrConflict
}
