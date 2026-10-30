package execution

import (
	"context"
	"errors"
	"fmt"

	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/run"
)

type cooperativeReviewSource struct{ Root, RootIdentity, HEAD, Branch, Index string }

func observeCooperativeReviewSource(ctx context.Context, path string) (cooperativeReviewSource, error) {
	root, identity, err := multiRuntimeRootIdentity(path)
	if err != nil {
		return cooperativeReviewSource{}, err
	}
	git := bridgegit.NewClient(bridgegit.Config{})
	state, err := git.InspectRepositoryState(ctx, root)
	if err != nil {
		return cooperativeReviewSource{}, err
	}
	index, err := git.IndexIdentity(ctx, root)
	if err != nil {
		return cooperativeReviewSource{}, err
	}
	return cooperativeReviewSource{Root: root, RootIdentity: identity, HEAD: state.HeadCommit, Branch: state.BranchRef, Index: index}, nil
}

func (before cooperativeReviewSource) changed(after cooperativeReviewSource) error {
	if before == after {
		return nil
	}
	// Retain both observations with the failure, even for an empty commit or an
	// index-only change whose content-tree diff is empty. Never roll back source.
	return fmt.Errorf("cooperative Review source state changed: before=%+v after=%+v; changes retained for inspection", before, after)
}

// Observe every frozen source, not only the primary CWD. These observations are
// local outcome evidence; they neither grant access nor claim native isolation.
func captureCooperativeReviewSources(ctx context.Context, prepared PreparedRun, cwd string) (map[string]cooperativeReviewSource, error) {
	roots := []string{cwd}
	if prepared.multiRuntime != nil {
		roots = nil
		for _, member := range prepared.multiRuntime.Workset.Members {
			roots = append(roots, member.CanonicalRoot)
		}
	}
	result := make(map[string]cooperativeReviewSource, len(roots))
	for _, root := range roots {
		observed, err := observeCooperativeReviewSource(ctx, root)
		if err != nil {
			return nil, fmt.Errorf("observe Review source %s: %w", root, err)
		}
		result[root] = observed
	}
	return result, nil
}

func compareCooperativeReviewSources(ctx context.Context, before map[string]cooperativeReviewSource) (mutation, unknown error) {
	for root, prior := range before {
		after, err := observeCooperativeReviewSource(ctx, root)
		if err != nil {
			unknown = errors.Join(unknown, fmt.Errorf("observe Review source %s: %w", root, err))
			continue
		}
		mutation = errors.Join(mutation, prior.changed(after))
	}
	return mutation, unknown
}

// UNKNOWN is absence of a complete terminal fact, not a partially known fact.
// Preserve its empty durable shape even when another observer proves mutation.
func mergeReviewMetadataObservation(observation run.RepositoryObservation, state string, cause, metadataErr, mutation error) (run.RepositoryObservation, string, error) {
	if state == "UNKNOWN" || metadataErr != nil {
		observation = run.RepositoryObservation{RunID: observation.RunID, RepositoryID: observation.RepositoryID, ObservationState: "UNKNOWN", ObservedAt: observation.ObservedAt}
		return observation, "UNKNOWN", errors.Join(cause, metadataErr, mutation)
	}
	if mutation != nil {
		observation.ObservationState = "SCOPE_CONFLICT"
		return observation, "SCOPE_CONFLICT", errors.Join(cause, mutation)
	}
	return observation, state, cause
}
