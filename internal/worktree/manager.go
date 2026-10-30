package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type GitClient interface {
	ResolveCommit(context.Context, string, string) (string, error)
	BranchExists(context.Context, string, string) (bool, error)
	WorktreeAdd(context.Context, string, string, string, string) error
	WorktreeRemove(context.Context, string, string) error
	DeleteBranch(context.Context, string, string) error
}

type Manager struct {
	root             string
	git              GitClient
	mu               sync.RWMutex
	items            map[string]Worktree
	reservedPaths    map[string]struct{}
	reservedBranches map[string]struct{}
	removing         map[string]struct{}
}

func NewManager(root string, git GitClient) (*Manager, error) {
	if root == "" {
		return nil, errors.New("worktree root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree root: %w", err)
	}
	if git == nil {
		return nil, errors.New("Git client is required")
	}
	if info, statErr := os.Lstat(absolute); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("worktree root must not be a symlink")
		}
		if !info.IsDir() {
			return nil, errors.New("worktree root is not a directory")
		}
		resolved, resolveErr := filepath.EvalSymlinks(absolute)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve worktree root: %w", resolveErr)
		}
		absolute = resolved
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect worktree root: %w", statErr)
	} else {
		resolvedParent, resolveErr := filepath.EvalSymlinks(filepath.Dir(absolute))
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve worktree root parent: %w", resolveErr)
		}
		absolute = filepath.Join(resolvedParent, filepath.Base(absolute))
	}
	return &Manager{root: filepath.Clean(absolute), git: git, items: make(map[string]Worktree), reservedPaths: make(map[string]struct{}), reservedBranches: make(map[string]struct{}), removing: make(map[string]struct{})}, nil
}

func (m *Manager) Create(ctx context.Context, input CreateInput) (Worktree, error) {
	if input.WorkspaceID == "" || input.RepositoryID == "" || input.RepositoryPath == "" || input.RunID == "" || input.BaseCommit == "" {
		return Worktree{}, errors.New("workspace, repository, owner, run, and base commit are required")
	}
	ownerKind, ownerID := input.OwnerKind, input.OwnerID
	if ownerKind == "" && ownerID == "" {
		ownerKind, ownerID = "FEATURE_AREA", input.FeatureAreaID
	}
	if ownerID == "" || (ownerKind == "FEATURE_AREA" && input.FeatureAreaID != ownerID) || (ownerKind == "PROJECT_INSPECTION" && input.FeatureAreaID != "") || (ownerKind != "FEATURE_AREA" && ownerKind != "PROJECT_INSPECTION" && ownerKind != "RECOVERY_STATUS") {
		return Worktree{}, errors.New("exact FeatureArea or Project Inspection owner is required")
	}
	if input.MemberID != "" && input.MemberVersion != 1 {
		return Worktree{}, errors.New("immutable member identity version is required")
	}
	if err := m.ensureOwnedRoot(); err != nil {
		return Worktree{}, err
	}
	rootKey := input.WorkspaceID
	if input.MemberID != "" {
		rootKey = memberWorktreeKey(input)
	}
	runPart, err := safePart(rootKey)
	if err != nil {
		return Worktree{}, err
	}
	branchOwner := ownerID
	if ownerKind == "PROJECT_INSPECTION" || ownerKind == "RECOVERY_STATUS" {
		branchOwner = ownerKind + "-" + ownerID
	}
	ownerPart, err := safePart(branchOwner)
	if err != nil {
		return Worktree{}, err
	}
	path := filepath.Join(m.root, runPart)
	// All roots are siblings, including legacy single-repository roots.
	// Never nest a new member below an existing root: that would enlarge a
	// historical os.Root grant to include the new repository.

	branch := "devbridge/" + ownerPart + "/" + runPart
	if !within(m.root, path) {
		return Worktree{}, ErrNotOwned
	}
	m.mu.Lock()
	if _, ok := m.reservedPaths[path]; ok {
		m.mu.Unlock()
		return Worktree{}, ErrCollision
	}
	if _, ok := m.reservedBranches[branch]; ok {
		m.mu.Unlock()
		return Worktree{}, ErrCollision
	}
	for _, item := range m.items {
		if item.Path == path || item.Branch == branch {
			m.mu.Unlock()
			if item.Path == path && item.Branch == branch && item.RepositoryID == input.RepositoryID && item.BaseCommit == input.BaseCommit && item.Status == StatusReady {
				return item, m.Validate(ctx, item)
			}
			return Worktree{}, ErrCollision
		}
	}
	m.reservedPaths[path] = struct{}{}
	m.reservedBranches[branch] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.reservedPaths, path)
		delete(m.reservedBranches, branch)
		m.mu.Unlock()
	}()
	info, statErr := os.Stat(input.RepositoryPath)
	if statErr != nil || !info.IsDir() {
		if statErr == nil {
			statErr = errors.New("not a directory")
		}
		return Worktree{}, fmt.Errorf("inspect source repository: %w", statErr)
	}
	resolved, err := m.git.ResolveCommit(ctx, input.RepositoryPath, input.BaseCommit)
	if err != nil {
		return Worktree{}, fmt.Errorf("resolve worktree base: %w", err)
	}
	if resolved != input.BaseCommit {
		return Worktree{}, fmt.Errorf("worktree base must be an immutable full commit: got %q resolved to %q", input.BaseCommit, resolved)
	}
	if _, err := os.Lstat(path); err == nil {
		now := time.Now().UTC()
		v := Worktree{ID: "wt_" + runPart, RepositoryID: input.RepositoryID, RepositoryPath: filepath.Clean(input.RepositoryPath), RunID: input.RunID, Path: path, Branch: branch, BaseCommit: input.BaseCommit, Status: StatusReady, CreatedAt: now, UpdatedAt: now}
		if err := m.Validate(ctx, v); err != nil {
			return Worktree{}, fmt.Errorf("%w: %v", ErrCollision, err)
		}
		m.mu.Lock()
		m.items[v.ID] = v
		m.mu.Unlock()
		return v, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Worktree{}, err
	}
	exists, err := m.git.BranchExists(ctx, input.RepositoryPath, branch)
	if err != nil {
		return Worktree{}, fmt.Errorf("inspect worktree branch: %w", err)
	}
	if exists {
		return Worktree{}, ErrCollision
	}
	if err := m.git.WorktreeAdd(ctx, input.RepositoryPath, path, branch, input.BaseCommit); err != nil {
		if input.MemberID == "" && !input.PreserveOnFailure {
			// Historical API behavior is retained only for legacy callers.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = m.git.WorktreeRemove(cleanupCtx, input.RepositoryPath, path)
			_ = os.RemoveAll(path)
			existsAfter, checkErr := m.git.BranchExists(cleanupCtx, input.RepositoryPath, branch)
			if checkErr == nil && existsAfter {
				_ = m.git.DeleteBranch(cleanupCtx, input.RepositoryPath, branch)
			}
		}
		// A transport error does not prove Git had no effect. Preserve any
		// physical directory/ref for exact identity validation on retry.
		return Worktree{}, fmt.Errorf("create Git worktree: %w", err)
	}
	now := time.Now().UTC()
	value := Worktree{ID: "wt_" + runPart, RepositoryID: input.RepositoryID, RepositoryPath: filepath.Clean(input.RepositoryPath), RunID: input.RunID, Path: path, Branch: branch, BaseCommit: input.BaseCommit, Status: StatusReady, CreatedAt: now, UpdatedAt: now}
	m.mu.Lock()
	m.items[value.ID] = value
	m.mu.Unlock()
	return value, nil
}

func (m *Manager) Get(_ context.Context, id string) (Worktree, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, ok := m.items[id]
	if !ok {
		return Worktree{}, ErrNotFound
	}
	return value, nil
}
func (m *Manager) List(_ context.Context) ([]Worktree, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	values := make([]Worktree, 0, len(m.items))
	for _, v := range m.items {
		values = append(values, v)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

func (m *Manager) Remove(ctx context.Context, id string) error {
	if err := m.ensureOwnedRoot(); err != nil {
		return err
	}
	m.mu.Lock()
	value, ok := m.items[id]
	if !ok {
		m.mu.Unlock()
		return ErrNotFound
	}
	if value.Status == StatusRemoved {
		m.mu.Unlock()
		return nil
	}
	if !within(m.root, value.Path) {
		m.mu.Unlock()
		return ErrNotOwned
	}
	if _, ok := m.removing[id]; ok {
		m.mu.Unlock()
		return ErrCollision
	}
	m.removing[id] = struct{}{}
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.removing, id); m.mu.Unlock() }()
	if err := m.git.WorktreeRemove(ctx, value.RepositoryPath, value.Path); err != nil {
		return fmt.Errorf("remove Git worktree: %w", err)
	}
	if err := m.git.DeleteBranch(ctx, value.RepositoryPath, value.Branch); err != nil {
		return fmt.Errorf("delete worktree branch: %w", err)
	}
	m.mu.Lock()
	value.Status = StatusRemoved
	value.UpdatedAt = time.Now().UTC()
	m.items[id] = value
	m.mu.Unlock()
	return nil
}

func (m *Manager) ensureOwnedRoot() error {
	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return fmt.Errorf("create worktree root: %w", err)
	}
	info, err := os.Lstat(m.root)
	if err != nil {
		return fmt.Errorf("inspect worktree root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrNotOwned
	}
	resolved, err := filepath.EvalSymlinks(m.root)
	if err != nil {
		return fmt.Errorf("resolve worktree root: %w", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(m.root) {
		return ErrNotOwned
	}
	return nil
}

var unsafePart = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func safePart(value string) (string, error) {
	result := strings.Trim(unsafePart.ReplaceAllString(value, "-"), "-_")
	if result == "" {
		return "", errors.New("id cannot form a safe worktree name")
	}
	return result, nil
}
func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (m *Manager) Validate(ctx context.Context, v Worktree) error {
	if !within(m.root, v.Path) {
		return ErrNotOwned
	}
	validator, ok := m.git.(interface {
		ValidateManagedWorktree(context.Context, string, string, string) error
	})
	if !ok {
		return errors.New("Git workspace identity validation unavailable")
	}
	return validator.ValidateManagedWorktree(ctx, v.RepositoryPath, v.Path, v.Branch)
}

func memberWorktreeKey(in CreateInput) string {
	material := in.WorkspaceID + "\x00" + in.RepositoryID + "\x00" + in.MemberID + fmt.Sprint("\x00", in.MemberVersion)
	// v1 FEATURE_AREA keys remain byte-identical. A Project Inspection gets a
	// distinct physical namespace even if a caller accidentally reuses a
	// workspace/member scalar pair from another probe.
	if in.OwnerKind == "PROJECT_INSPECTION" || in.OwnerKind == "RECOVERY_STATUS" {
		material += "\x00" + in.OwnerKind + "\x00" + in.OwnerID
	}
	sum := sha256.Sum256([]byte(material))
	return "member_" + hex.EncodeToString(sum[:16])
}
