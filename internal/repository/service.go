package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/reviewaccess"
)

type Store interface {
	PutIfAbsent(context.Context, Repository) (Repository, bool, error)
	Get(context.Context, string) (Repository, error)
	List(context.Context) ([]Repository, error)
}

// HistoricalStore exposes explicitly requested historical registrations.
// Ordinary List deliberately returns only repositories available for new work.
type HistoricalStore interface {
	ListAll(context.Context) ([]Repository, error)
}

// TopologyStore is implemented by the durable registration authority. The
// revision is a current-resource fence, not a replacement ownership model.
type TopologyStore interface {
	TopologyRevision(context.Context, string) (int64, error)
}

type GitValidator interface {
	RepositoryRoot(context.Context, string) (string, error)
	ResolveHEAD(context.Context, string) (string, error)
}

type GitResolver interface {
	ResolveCommit(context.Context, string, string) (string, error)
}

type GitSearch interface {
	SearchFixedRoot(context.Context, *reviewaccess.Root, string, []string) ([]bridgegit.SearchMatch, error)
}

type IDGenerator func() (string, error)

type Service struct {
	store Store
	git   GitValidator
	newID IDGenerator
	now   func() time.Time
}

type SearchMatch struct {
	Path    string
	Line    int
	Snippet string
}

type SearchResult struct {
	Matches   []SearchMatch
	Truncated bool
}

func (s *Service) Search(ctx context.Context, id, query string, paths []string, limit int) (SearchResult, error) {
	return s.search(ctx, id, query, paths, limit)
}

// SearchReview performs deterministic fixed-string search over structurally
// authorized tracked repository content. Reviewer behavior is prompt-owned.
func (s *Service) SearchReview(ctx context.Context, id, query string, paths []string, limit int) (SearchResult, error) {
	if err := reviewaccess.RepositoryBoundary().ValidatePaths(reviewaccess.OperationSearch, paths); err != nil {
		return SearchResult{}, err
	}
	return s.search(ctx, id, query, paths, limit)
}

func (s *Service) search(ctx context.Context, id, query string, paths []string, limit int) (SearchResult, error) {
	if query == "" || len(query) > 4096 || strings.ContainsRune(query, '\x00') || limit < 1 || limit > 100 {
		return SearchResult{}, errors.New("invalid bounded repository search")
	}
	value, err := s.store.Get(ctx, id)
	if err != nil {
		return SearchResult{}, err
	}
	root, err := reviewaccess.OpenRoot(value.Path)
	if err != nil {
		return SearchResult{}, err
	}
	defer root.Close()
	validated, err := validateSearchPaths(root, paths)
	if err != nil {
		return SearchResult{}, err
	}
	searcher, ok := s.git.(GitSearch)
	if !ok {
		return SearchResult{}, errors.New("Git service does not support fixed search")
	}
	matches, err := searcher.SearchFixedRoot(ctx, root, query, validated)
	if err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{Matches: make([]SearchMatch, 0, min(limit, len(matches))), Truncated: len(matches) > limit}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	for _, match := range matches {
		snippet := match.Snippet
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		result.Matches = append(result.Matches, SearchMatch{Path: match.Path, Line: match.Line, Snippet: snippet})
	}
	return result, nil
}

func validateSearchPaths(root *reviewaccess.Root, paths []string) ([]string, error) {
	if len(paths) > 64 {
		return nil, errors.New("repository search paths exceed bounds")
	}
	result := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, supplied := range paths {
		normalized, _, err := root.Resolve(reviewaccess.OperationSearch, supplied)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[normalized]; duplicate {
			return nil, errors.New("duplicate repository search path")
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result, nil
}

func NewService(store Store, git GitValidator, newID IDGenerator) *Service {
	if newID == nil {
		newID = randomID
	}
	return &Service{store: store, git: git, newID: newID, now: time.Now}
}

func (s *Service) Register(ctx context.Context, path, name string) (Repository, error) {
	return s.RegisterInProject(ctx, "", path, name)
}

func (s *Service) RegisterInProject(ctx context.Context, projectID, path, name string) (Repository, error) {
	if s.store == nil || s.git == nil {
		return Repository{}, errors.New("repository store and Git validator are required")
	}
	canonical, err := s.ResolveRegistrationPath(ctx, path)
	if err != nil {
		return Repository{}, err
	}
	if name == "" {
		name = filepath.Base(canonical)
	}
	id, err := s.newID()
	if err != nil {
		return Repository{}, fmt.Errorf("allocate repository id: %w", err)
	}
	now := s.now().UTC()
	value, _, err := s.store.PutIfAbsent(ctx, Repository{ID: id, ProjectID: projectID, Name: name, Path: canonical, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return Repository{}, fmt.Errorf("store repository: %w", err)
	}
	return value, nil
}

// ResolveRegistrationPath validates a user-selected directory and resolves it
// to the canonical Git working-tree root used for durable repository identity.
// It is intentionally read-only and is shared by aggregate registration and
// ordinary repository registration.
func (s *Service) ResolveRegistrationPath(ctx context.Context, path string) (string, error) {
	if s.git == nil {
		return "", errors.New("Git validator is required")
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: repository path is required", ErrInvalidPath)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize repository path: %w", ErrInvalidPath, err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize repository path: %w", ErrInvalidPath, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: inspect repository path: %w", ErrInvalidPath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: repository path is not a directory", ErrInvalidPath)
	}
	directory, err := os.Open(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: repository path is not readable: %w", ErrInvalidPath, err)
	}
	if err := directory.Close(); err != nil {
		return "", fmt.Errorf("%w: close repository directory: %v", ErrInvalidPath, err)
	}
	root, err := s.git.RepositoryRoot(ctx, canonical)
	if err != nil {
		return "", fmt.Errorf("%w: resolve Git repository root: %w", ErrNotGit, err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize Git repository root: %w", ErrInvalidPath, err)
	}
	root = filepath.Clean(root)
	if _, err := s.git.ResolveHEAD(ctx, root); err != nil {
		return "", fmt.Errorf("%w: validate Git repository HEAD: %w", ErrHEADUnavailable, err)
	}
	return root, nil
}

func (s *Service) Get(ctx context.Context, id string) (Repository, error) {
	return s.store.Get(ctx, id)
}
func (s *Service) List(ctx context.Context) ([]Repository, error) { return s.store.List(ctx) }

// TopologyRevision allows Project Context bootstrap to publish only against
// the Repository inventory it inspected. Non-durable stores deliberately do
// not synthesize a revision.
func (s *Service) TopologyRevision(ctx context.Context, projectID string) (int64, error) {
	store, ok := s.store.(TopologyStore)
	if !ok {
		return 0, errors.New("repository store does not support topology revisions")
	}
	return store.TopologyRevision(ctx, projectID)
}
func (s *Service) ListAll(ctx context.Context) ([]Repository, error) {
	all, ok := s.store.(HistoricalStore)
	if !ok {
		return nil, errors.New("repository historical listing is unavailable")
	}
	return all.ListAll(ctx)
}

// CurrentHEAD resolves the repository's current HEAD through the configured
// Git service without exposing its canonical filesystem path to callers.
func (s *Service) CurrentHEAD(ctx context.Context, id string) (string, error) {
	value, err := s.store.Get(ctx, id)
	if err != nil {
		return "", err
	}
	commit, err := s.git.ResolveHEAD(ctx, value.Path)
	if err != nil {
		return "", fmt.Errorf("resolve repository HEAD: %w", err)
	}
	return commit, nil
}

// CurrentState captures the exact source-worktree facts needed by Review
// provenance. It does not imply that dirty files are inspected: Review Runs
// execute in a managed worktree created from HeadCommit.
func (s *Service) CurrentState(ctx context.Context, id string) (State, error) {
	value, err := s.store.Get(ctx, id)
	if err != nil {
		return State{}, err
	}
	inspector, ok := s.git.(interface {
		InspectRepositoryState(context.Context, string) (bridgegit.RepositoryState, error)
	})
	if !ok {
		return State{}, errors.New("repository state inspection is unavailable")
	}
	observed, err := inspector.InspectRepositoryState(ctx, value.Path)
	if err != nil {
		return State{}, fmt.Errorf("inspect repository state: %w", err)
	}
	kind := BranchBranch
	if observed.Detached {
		kind = BranchDetached
	}
	return State{HeadCommit: observed.HeadCommit, BranchKind: kind, BranchRef: observed.BranchRef, WorkingTreeDirty: observed.WorkingTreeDirty, CapturedAt: time.Now().UTC()}, nil
}

// CommitResolver adapts registered repository IDs to the task package's
// immutable base-resolution seam.
type CommitResolver struct {
	repositories interface {
		Get(context.Context, string) (Repository, error)
	}
	git GitResolver
}

func NewCommitResolver(repositories interface {
	Get(context.Context, string) (Repository, error)
}, git GitResolver) *CommitResolver {
	return &CommitResolver{repositories: repositories, git: git}
}

func (r *CommitResolver) ResolveBase(ctx context.Context, repositoryID, revision string) (string, error) {
	repository, err := r.repositories.Get(ctx, repositoryID)
	if err != nil {
		return "", fmt.Errorf("load repository: %w", err)
	}
	commit, err := r.git.ResolveCommit(ctx, repository.Path, revision)
	if err != nil {
		return "", fmt.Errorf("resolve repository commit: %w", err)
	}
	return commit, nil
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "repo_" + hex.EncodeToString(raw[:]), nil
}
