// Package worktreeinventory joins durable ownership with live, read-only Git
// and directory observations. It never changes workflow or performs cleanup.
package worktreeinventory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

type Operation struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	RunID string `json:"runId,omitempty"`
}
type Item struct {
	WorktreeID              string     `json:"worktreeId,omitempty"`
	Path                    string     `json:"path"`
	Kind                    string     `json:"kind"`
	RepositoryID            string     `json:"repositoryId"`
	FeatureAreaID           string     `json:"featureAreaId,omitempty"`
	TaskID                  string     `json:"taskId,omitempty"`
	ReviewID                string     `json:"reviewId,omitempty"`
	OwnerType               string     `json:"ownerType,omitempty"`
	OwnerID                 string     `json:"ownerId,omitempty"`
	BranchRef               string     `json:"branchRef,omitempty"`
	Detached                bool       `json:"detached"`
	Head                    string     `json:"head,omitempty"`
	GitRegistered           bool       `json:"gitRegistered"`
	GitRegistrationKnown    bool       `json:"gitRegistrationKnown"`
	DirectoryExists         bool       `json:"directoryExists"`
	DirectoryExistenceKnown bool       `json:"directoryExistenceKnown"`
	WorkingTreeState        string     `json:"workingTreeState"`
	ActivityState           string     `json:"activityState"`
	ActiveOperation         *Operation `json:"activeOperation,omitempty"`
	LastActivityAt          *time.Time `json:"lastActivityAt,omitempty"`
	IntegrationState        string     `json:"integrationState"`
	IntegrationTargetHead   string     `json:"integrationTargetHead,omitempty"`
	HasUniqueCommits        *bool      `json:"hasUniqueCommits,omitempty"`
	CleanupState            string     `json:"cleanupState"`
	Locked                  bool       `json:"locked"`
	Prunable                bool       `json:"prunable"`
	ReadErrors              []string   `json:"readErrors,omitempty"`
}
type Inventory struct {
	SchemaVersion string    `json:"schemaVersion"`
	ProjectID     string    `json:"projectId"`
	Items         []Item    `json:"items"`
	RefreshedAt   time.Time `json:"refreshedAt"`
}
type Dependencies struct {
	Repositories interface {
		List(context.Context) ([]repository.Repository, error)
	}
	Worktrees interface {
		List(context.Context) ([]worktree.Worktree, error)
	}
	Workspaces interface {
		List(context.Context) ([]workspace.Workspace, error)
	}
	Runs interface {
		List(context.Context) ([]run.Run, error)
	}
	Conversations interface {
		State(context.Context, string) (agentconversation.State, error)
	}
	DevelopmentCycles interface {
		Current(context.Context, string, string) (developmentcycle.Cycle, error)
	}
	Git interface {
		RegisteredWorktrees(context.Context, string) ([]bridgegit.RegisteredWorktree, error)
		InventoryStatus(context.Context, string) (string, bridgegit.Status, error)
		InventoryCommit(context.Context, string, string) (string, error)
		InventoryAncestor(context.Context, string, string, string) (bool, error)
	}
}
type Service struct{ Dependencies }

func canonical(path string) string {
	path = filepath.Clean(path)
	ancestor := path
	for {
		if resolved, err := filepath.EvalSymlinks(ancestor); err == nil {
			suffix, _ := filepath.Rel(ancestor, path)
			return filepath.Clean(filepath.Join(resolved, suffix))
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return path
		}
		ancestor = parent
	}
}

func (s *Service) Read(ctx context.Context, projectID string) (Inventory, error) {
	out := Inventory{SchemaVersion: "1", ProjectID: projectID, Items: []Item{}, RefreshedAt: time.Now().UTC()}
	if projectID == "" || s.Repositories == nil || s.Worktrees == nil || s.Workspaces == nil || s.Runs == nil || s.Git == nil {
		return out, errors.New("worktree inventory dependencies unavailable")
	}
	repos, err := s.Repositories.List(ctx)
	if err != nil {
		return out, err
	}
	stored, err := s.Worktrees.List(ctx)
	if err != nil {
		return out, err
	}
	spaces, err := s.Workspaces.List(ctx)
	if err != nil {
		return out, err
	}
	runs, err := s.Runs.List(ctx)
	if err != nil {
		return out, err
	}
	for _, repo := range repos {
		if repo.ProjectID != projectID {
			continue
		}
		registered, readErr := s.Git.RegisteredWorktrees(ctx, repo.Path)
		items := map[string]*Item{}
		ensure := func(path string) *Item {
			key := canonical(path)
			if v := items[key]; v != nil {
				return v
			}
			v := &Item{Path: path, Kind: "MANAGED", RepositoryID: repo.ID, GitRegistrationKnown: readErr == nil, WorkingTreeState: "UNKNOWN", ActivityState: "UNKNOWN", IntegrationState: "UNKNOWN"}
			if readErr != nil {
				v.ReadErrors = append(v.ReadErrors, "Git registration unavailable")
			}
			items[key] = v
			return v
		}
		formal := ensure(repo.Path)
		formal.Kind = "FORMAL"
		formal.OwnerType = "REPOSITORY"
		formal.OwnerID = repo.ID
		for _, wt := range stored {
			if wt.RepositoryID != repo.ID || wt.Path == "" {
				continue
			}
			v := ensure(wt.Path)
			v.WorktreeID = wt.ID
			if !wt.UpdatedAt.IsZero() {
				at := wt.UpdatedAt
				v.LastActivityAt = &at
			}
			// Stored branch/base are provenance, never fresh Git head facts.
			ownedSpaces := make(map[string]bool)
			for _, w := range spaces {
				if w.ProjectID == projectID && w.WorktreeID == wt.ID {
					ownedSpaces[w.ID] = true
					if v.Kind != "FORMAL" {
						v.Kind = "FEATURE_AREA"
						v.OwnerType = "FEATURE_AREA"
						v.OwnerID = w.FeatureAreaID
					}
					v.FeatureAreaID = w.FeatureAreaID
				}
			}
			var latestRun, latestActive *run.Run
			for _, r := range runs {
				associated := r.WorktreeID == wt.ID || r.ID == wt.RunID || r.WorkspaceID != "" && ownedSpaces[r.WorkspaceID]
				// Historical Runs may lack ProjectID. An exact worktree or
				// current-project Workspace link establishes their repository
				// ownership; an explicit foreign Project always fails closed.
				if !associated || r.ProjectID != "" && r.ProjectID != projectID {
					continue
				}
				if newerRun(r, latestRun) {
					copy := r
					latestRun = &copy
				}
				if !terminal(r.Status) && newerRun(r, latestActive) {
					copy := r
					latestActive = &copy
				}
			}
			if latestRun != nil {
				r := latestRun
				v.TaskID, v.ReviewID = r.TaskID, r.ReviewID
				if v.LastActivityAt == nil || r.UpdatedAt.After(*v.LastActivityAt) {
					at := r.UpdatedAt
					v.LastActivityAt = &at
				}
				if v.FeatureAreaID == "" {
					v.FeatureAreaID = r.FeatureAreaID
				}
				if v.OwnerType == "" {
					v.OwnerType, v.OwnerID = string(r.OperationKind), r.OperationID
					if r.ReviewID != "" {
						v.Kind = "REVIEW"
					}
				}
			}
			if latestActive != nil {
				v.ActiveOperation = &Operation{Kind: string(latestActive.OperationKind), ID: latestActive.OperationID, RunID: latestActive.ID}
				v.ActivityState = "ACTIVE"
			}
		}
		for _, registration := range registered {
			v := ensure(registration.Path)
			v.GitRegistered = true
			v.BranchRef = registration.BranchRef
			v.Head = registration.Head
			v.Detached = registration.Detached
			v.Locked = registration.Locked
			v.Prunable = registration.Prunable
		}
		target := ""
		if s.DevelopmentCycles != nil {
			if cycle, e := s.DevelopmentCycles.Current(ctx, projectID, repo.ID); e == nil {
				target, _ = s.Git.InventoryCommit(ctx, repo.Path, cycle.DevelopmentRef)
			}
		}
		states := map[string]agentconversation.State{}
		stateErrors := map[string]error{}
		for _, v := range items {
			info, e := os.Stat(v.Path)
			v.DirectoryExistenceKnown = e == nil || os.IsNotExist(e)
			v.DirectoryExists = e == nil && info.IsDir()
			if e != nil && !os.IsNotExist(e) {
				v.ReadErrors = append(v.ReadErrors, "Directory existence unavailable")
			}
			if e == nil && !info.IsDir() {
				v.DirectoryExistenceKnown = false
				v.ReadErrors = append(v.ReadErrors, "Path is not a directory")
			}
			if v.DirectoryExists {
				root, status, statusErr := s.Git.InventoryStatus(ctx, v.Path)
				if statusErr == nil && canonical(root) == canonical(v.Path) {
					v.WorkingTreeState = "CLEAN"
					if status.Dirty() {
						v.WorkingTreeState = "DIRTY"
					}
				} else {
					v.ReadErrors = append(v.ReadErrors, "Working tree status unavailable")
				}
			}
			if v.FeatureAreaID != "" && s.Conversations == nil {
				v.ReadErrors = append(v.ReadErrors, "Owner activity unavailable")
			} else if v.FeatureAreaID != "" {
				state, ok := states[v.FeatureAreaID]
				if !ok {
					state, stateErrors[v.FeatureAreaID] = s.Conversations.State(ctx, v.FeatureAreaID)
					states[v.FeatureAreaID] = state
				}
				if stateErrors[v.FeatureAreaID] != nil {
					v.ReadErrors = append(v.ReadErrors, "Owner activity unavailable")
				} else if state.Claim != nil {
					v.ActiveOperation = &Operation{Kind: string(state.Claim.Kind), ID: state.Claim.OperationID, RunID: state.Claim.RunID}
					v.ActivityState = "ACTIVE"
				} else if v.ActiveOperation == nil && (state.Status == "IDLE" || state.Status == "READY" || state.Status == "UNAVAILABLE") {
					// Core UNAVAILABLE with a successful read and no claim means
					// a historical area closed to new work, not a failed activity read.
					v.ActivityState = "IDLE"
				} else if v.ActiveOperation == nil && state.Status == "" {
					v.ReadErrors = append(v.ReadErrors, "Owner activity unavailable")
				} else if v.ActiveOperation == nil {
					v.ReadErrors = append(v.ReadErrors, "Owner requires recovery")
				}
			} else if v.OwnerType != "" && v.ActiveOperation == nil {
				v.ActivityState = "IDLE"
			}
			if target != "" && v.Head != "" {
				if integrated, e := s.Git.InventoryAncestor(ctx, repo.Path, v.Head, target); e == nil {
					v.IntegrationTargetHead = target
					v.IntegrationState = "NOT_INTEGRATED"
					if integrated {
						v.IntegrationState = "INTEGRATED"
					}
					unique := !integrated
					v.HasUniqueCommits = &unique
				} else {
					v.ReadErrors = append(v.ReadErrors, "Commit ancestry unavailable")
				}
			}
			v.CleanupState = CleanupState(*v)
			out.Items = append(out.Items, *v)
		}
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		if (a.Kind == "FORMAL") != (b.Kind == "FORMAL") {
			return a.Kind == "FORMAL"
		}
		if a.RepositoryID != b.RepositoryID {
			return a.RepositoryID < b.RepositoryID
		}
		return a.Path < b.Path
	})
	return out, nil
}

// CleanupState is an explanation of observations, never cleanup permission.
func CleanupState(v Item) string {
	if !v.GitRegistrationKnown || !v.DirectoryExistenceKnown {
		return "UNKNOWN"
	}
	if v.GitRegistered && !v.DirectoryExists {
		return "MISSING_DIRECTORY"
	}
	if !v.GitRegistered && !v.DirectoryExists {
		return "REMOVED"
	}
	if v.Kind == "FORMAL" {
		return "PROTECTED_FORMAL"
	}
	if v.ActiveOperation != nil || v.ActivityState == "ACTIVE" {
		return "ACTIVE"
	}
	if !v.GitRegistered {
		return "CLEANUP_PENDING"
	}
	if v.WorkingTreeState == "DIRTY" {
		return "PROTECTED_DIRTY"
	}
	if v.Locked {
		return "PROTECTED_LOCKED"
	}
	if v.HasUniqueCommits != nil && *v.HasUniqueCommits {
		return "PROTECTED_UNMERGED"
	}
	if v.WorkingTreeState == "CLEAN" && v.IntegrationState == "INTEGRATED" && v.ActivityState == "IDLE" {
		return "SAFE_TO_CLEAN"
	}
	return "UNKNOWN"
}

func RenderText(v Inventory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Worktrees · Project %s · %s\n", v.ProjectID, v.RefreshedAt.Format(time.RFC3339))
	for _, item := range v.Items {
		fmt.Fprintf(&b, "%s · %s\n  owner=%s:%s task=%s review=%s\n  branch=%s HEAD=%s\n  gitRegistered=%t (known=%t) directoryExists=%t (known=%t)\n  workingTree=%s activity=%s integrated=%s target=%s cleanup=%s\n", item.Kind, item.Path, item.OwnerType, item.OwnerID, item.TaskID, item.ReviewID, item.BranchRef, item.Head, item.GitRegistered, item.GitRegistrationKnown, item.DirectoryExists, item.DirectoryExistenceKnown, item.WorkingTreeState, item.ActivityState, item.IntegrationState, item.IntegrationTargetHead, item.CleanupState)
		fmt.Fprintf(&b, "  worktreeId=%s repositoryId=%s featureAreaId=%s detached=%t locked=%t prunable=%t\n", item.WorktreeID, item.RepositoryID, item.FeatureAreaID, item.Detached, item.Locked, item.Prunable)
		if item.ActiveOperation != nil {
			fmt.Fprintf(&b, "  activeOperation=%s:%s runId=%s\n", item.ActiveOperation.Kind, item.ActiveOperation.ID, item.ActiveOperation.RunID)
		} else {
			fmt.Fprintln(&b, "  activeOperation=none")
		}
		if item.LastActivityAt != nil {
			fmt.Fprintf(&b, "  lastActivityAt=%s\n", item.LastActivityAt.UTC().Format(time.RFC3339Nano))
		} else {
			fmt.Fprintln(&b, "  lastActivityAt=unknown")
		}
		if item.HasUniqueCommits != nil {
			fmt.Fprintf(&b, "  hasUniqueCommits=%t\n", *item.HasUniqueCommits)
		} else {
			fmt.Fprintln(&b, "  hasUniqueCommits=unknown")
		}
		if len(item.ReadErrors) > 0 {
			fmt.Fprintf(&b, "  readErrors=%s\n", strings.Join(item.ReadErrors, "; "))
		}
	}
	return b.String()
}

func terminal(status run.Status) bool {
	switch status {
	case run.StatusSucceeded, run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled:
		return true
	default:
		return false
	}
}

// Deterministic selection independent of storage iteration order.
func newerRun(candidate run.Run, current *run.Run) bool {
	return current == nil || candidate.UpdatedAt.After(current.UpdatedAt) || candidate.UpdatedAt.Equal(current.UpdatedAt) && candidate.ID > current.ID
}
