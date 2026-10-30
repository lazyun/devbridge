// Package reviewdrift derives objective Git drift facts between the
// repository state a Feature Area Review was based on and the current authoritative
// repository state. It is a factual, reusable capability: Bridge derives no
// semantic judgement (staleness, validity, importance, rerun requirements)
// from these facts — that decision belongs to GPT and the user. The same
// primitive is designed to later compare immutable Task baseline commits against the
// current repository state.
package reviewdrift

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/repository"
	productreview "github.com/lazyun/devbridge/internal/review"
)

// ReviewReader reads one durable Review, including its structured relevant
// paths (Review.Focus).
type ReviewReader interface {
	Get(context.Context, string) (productreview.Review, error)
}

// ProvenanceReader reads the immutable repository provenance of one Review.
type ProvenanceReader interface {
	RepositoryProvenance(context.Context, string) (productreview.RepositoryProvenance, error)
}

// RepositoryReader resolves one registered Repository by ID.
type RepositoryReader interface {
	Get(context.Context, string) (repository.Repository, error)
}

type Service struct {
	reviews      ReviewReader
	provenance   ProvenanceReader
	repositories RepositoryReader
	git          *git.Client
}

func New(reviews ReviewReader, provenance ProvenanceReader, repositories RepositoryReader, client *git.Client) (*Service, error) {
	if reviews == nil || provenance == nil || repositories == nil || client == nil {
		return nil, errors.New("review drift dependencies are incomplete")
	}
	return &Service{reviews: reviews, provenance: provenance, repositories: repositories, git: client}, nil
}

// RepositoryDrift computes the bounded factual drift for one Review against
// the current authoritative repository state. Unavailable facts (unknown
// provenance, unresolvable commits, repository or Git failures) are reported
// factually and never imply anything about the Review itself. limit bounds
// the returned focus-path list; counts always reflect the full change set.
func (s *Service) RepositoryDrift(ctx context.Context, reviewID string, limit int) (productreview.RepositoryDrift, error) {
	if limit <= 0 {
		limit = productreview.DefaultDriftLimit
	}
	provenance, err := s.provenance.RepositoryProvenance(ctx, reviewID)
	if errors.Is(err, productreview.ErrProvenanceNotFound) {
		return unavailableDrift("", "review repository provenance is unknown"), nil
	}
	if err != nil {
		return productreview.RepositoryDrift{}, err
	}
	reviewValue, err := s.reviews.Get(ctx, reviewID)
	if err != nil {
		return productreview.RepositoryDrift{}, err
	}
	repositoryPath, err := s.repositoryPath(ctx, provenance.RepositoryID)
	if err != nil {
		return productreview.RepositoryDrift{}, err
	}
	currentState, err := s.git.InspectRepositoryState(ctx, repositoryPath)
	if err != nil {
		return unavailableDrift(provenance.Commit, bounded("current repository HEAD is unavailable")), nil
	}
	// The recorded integration ref is independent of source checkout and of
	// legitimate commits/uncommitted work in the FeatureArea workspace.
	if provenance.BranchRef != "" {
		head, e := s.git.ResolveCommit(ctx, repositoryPath, provenance.BranchRef)
		if e != nil {
			return unavailableDrift(provenance.Commit, "recorded development branch is unavailable"), nil
		}
		currentState.HeadCommit = head
		currentState.BranchRef = provenance.BranchRef
		currentState.Detached = false
	}
	if _, err := s.git.ResolveCommit(ctx, repositoryPath, provenance.Commit); err != nil {
		return unavailableDrift(provenance.Commit, bounded("the reviewed commit is no longer resolvable in this repository")), nil
	}
	drift := productreview.RepositoryDrift{
		RepositoryID:            provenance.RepositoryID,
		ReviewCommit:            provenance.Commit,
		CurrentCommit:           currentState.HeadCommit,
		CurrentBranchKind:       productreview.ProvenanceBranch,
		CurrentBranchRef:        currentState.BranchRef,
		CurrentWorkingTreeDirty: boolPointer(currentState.WorkingTreeDirty),
		Available:               true,
	}
	if currentState.Detached {
		drift.CurrentBranchKind = productreview.ProvenanceDetached
	}
	if provenance.Commit == currentState.HeadCommit {
		drift.FocusPathsChanged = []productreview.FocusPathChange{}
		return drift, nil
	}
	drift.RepositoryChanged = true
	changes, err := s.git.ChangedFilesNamed(ctx, repositoryPath, provenance.Commit, currentState.HeadCommit)
	if err != nil {
		return unavailableDrift(provenance.Commit, bounded("Git change facts are unavailable")), nil
	}
	drift.ChangedFileCount = len(changes)
	recordedPaths := reviewValue.Focus.Paths
	drift.FocusPathsChanged = []productreview.FocusPathChange{}
	matched := 0
	for _, change := range changes {
		// A rename intersects when either the recorded old path or the new
		// path falls under the recorded relevant paths.
		matchedPath := productreview.MatchRecordedPath(change.Path, recordedPaths) ||
			(change.OldPath != "" && productreview.MatchRecordedPath(change.OldPath, recordedPaths))
		if !matchedPath {
			continue
		}
		matched++
		if len(drift.FocusPathsChanged) < limit {
			drift.FocusPathsChanged = append(drift.FocusPathsChanged, productreview.FocusPathChange{
				Change:  productreview.ChangeTypeName(change.Change),
				Path:    change.Path,
				OldPath: change.OldPath,
			})
		}
	}
	drift.ChangedFocusPathCount = matched
	drift.OtherChangedFileCount = len(changes) - matched
	drift.Truncated = matched > len(drift.FocusPathsChanged)
	sort.SliceStable(drift.FocusPathsChanged, func(i, j int) bool {
		if drift.FocusPathsChanged[i].Path != drift.FocusPathsChanged[j].Path {
			return drift.FocusPathsChanged[i].Path < drift.FocusPathsChanged[j].Path
		}
		return drift.FocusPathsChanged[i].OldPath < drift.FocusPathsChanged[j].OldPath
	})
	return drift, nil
}

func boolPointer(value bool) *bool { return &value }

func (s *Service) repositoryPath(ctx context.Context, repositoryID string) (string, error) {
	value, err := s.repositories.Get(ctx, repositoryID)
	if err != nil {
		return "", fmt.Errorf("read review repository: %w", err)
	}
	return value.Path, nil
}

func unavailableDrift(reviewCommit, reason string) productreview.RepositoryDrift {
	return productreview.RepositoryDrift{
		ReviewCommit:      reviewCommit,
		CurrentBranchKind: productreview.ProvenanceUnknown,
		Available:         false,
		UnavailableReason: reason,
	}
}

func bounded(reason string) string {
	if len(reason) > 256 {
		reason = reason[:256]
	}
	return reason
}

// Provenance returns the immutable repository provenance recorded for one
// Review, or productreview.ErrProvenanceNotFound when the Review predates
// provenance capture.
func (s *Service) Provenance(ctx context.Context, reviewID string) (productreview.RepositoryProvenance, error) {
	return s.provenance.RepositoryProvenance(ctx, reviewID)
}
