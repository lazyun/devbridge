package projectcontext

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func transitionPublishedLiteral() ProjectContext {
	return ProjectContext{
		ProjectID: "synthetic-project", Status: StatusReady,
		RootGuidanceIdentity: "published-identity", ContextFingerprint: "published-fingerprint",
		Diagnostics:   []string{"diagnostic-one", "diagnostic-two"},
		LatestProbeID: "", Result: "published-guidance", ResultArtifactRef: "",
		CreatedAt: time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC),
	}
}

func transitionRunningLiteral() ProjectContext {
	return ProjectContext{
		ProjectID: "synthetic-project", Status: StatusRunning,
		RootGuidanceIdentity: "published-identity", ContextFingerprint: "published-fingerprint",
		Diagnostics:   []string{"diagnostic-one", "diagnostic-two"},
		LatestProbeID: "synthetic-probe", Result: "published-guidance", ResultArtifactRef: "",
		CreatedAt: time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC),
	}
}

func transitionFinishedLiteral(status Status) ProjectContext {
	return ProjectContext{
		ProjectID: "synthetic-project", Status: status,
		RootGuidanceIdentity: "finished-identity", ContextFingerprint: "published-fingerprint",
		Diagnostics:   []string{"diagnostic-one", "diagnostic-two"},
		LatestProbeID: "synthetic-probe", Result: "finished-result", ResultArtifactRef: "synthetic-artifact",
		CreatedAt: time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC),
	}
}

func transitionEqual(t *testing.T, label string, got, want ProjectContext) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %#v; want independent literal %#v", label, got, want)
	}
}

func transitionRead(t *testing.T, service *Service) ProjectContext {
	t.Helper()
	value, err := service.Get(context.Background(), "synthetic-project")
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func transitionSeed(t *testing.T) (*Service, *time.Time, ProjectContext, ProjectContext) {
	t.Helper()
	now := time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC)
	service := NewService(NewMemoryStore(), func() time.Time { return now })
	created, err := service.EnsureRequired(context.Background(), "synthetic-project")
	if err != nil {
		t.Fatal(err)
	}
	transitionEqual(t, "required prerequisite", created, ProjectContext{
		ProjectID: "synthetic-project", Status: StatusRequired,
		CreatedAt: time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC),
	})
	now = time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC)
	published, err := service.MarkBootstrapped(context.Background(), "synthetic-project", StatusReady,
		"published-fingerprint", "published-identity", "published-guidance", []string{"diagnostic-one", "diagnostic-two"})
	if err != nil {
		t.Fatal(err)
	}
	priorRead := transitionRead(t, service)
	transitionEqual(t, "published prerequisite", published, transitionPublishedLiteral())
	transitionEqual(t, "Get prerequisite", priorRead, transitionPublishedLiteral())
	return service, &now, published, priorRead
}

// Metadata return values describe observations, not mutation handles into the store.
// Get and publication already establish detached diagnostics ownership. Crossing
// a metadata transition must not let an older return rewrite a newer observation.
// No probe is launched: the concrete store is exclusively an in-memory map.
func TestMetadataTransitionReturnOwnershipBaseline(t *testing.T) {
	ctx := context.Background()
	for _, terminal := range []Status{StatusReady, StatusNoGuidance, StatusFailed} {
		for _, mutate := range []string{"earlier_running", "finished"} {
			t.Run(string(terminal)+"/"+mutate, func(t *testing.T) {
				service, now, published, priorRead := transitionSeed(t)
				*now = time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC)
				running, err := service.MarkRunning(ctx, "synthetic-project", "synthetic-probe")
				if err != nil {
					t.Fatal(err)
				}
				transitionEqual(t, "running metadata", running, transitionRunningLiteral())
				transitionEqual(t, "running stored metadata", transitionRead(t, service), transitionRunningLiteral())
				*now = time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)
				finished, err := service.MarkFinished(ctx, "synthetic-project", "synthetic-probe", terminal,
					"finished-result", "synthetic-artifact", "finished-identity")
				if err != nil {
					t.Fatal(err)
				}
				transitionEqual(t, "finished metadata before caller mutation", finished, transitionFinishedLiteral(terminal))
				transitionEqual(t, "finished stored metadata before caller mutation", transitionRead(t, service), transitionFinishedLiteral(terminal))
				if mutate == "earlier_running" {
					running.Diagnostics[0] = "caller-running-only"
					transitionEqual(t, "ALIAS: earlier running return must not rewrite finished return", finished, transitionFinishedLiteral(terminal))
				} else {
					finished.Diagnostics[1] = "caller-finished-only"
					transitionEqual(t, "ALIAS: finished return must not rewrite earlier running return", running, transitionRunningLiteral())
				}
				transitionEqual(t, "ALIAS: caller return mutation must not rewrite completed store", transitionRead(t, service), transitionFinishedLiteral(terminal))
				transitionEqual(t, "published snapshot survives transition return mutation", published, transitionPublishedLiteral())
				transitionEqual(t, "prior Get snapshot survives transition return mutation", priorRead, transitionPublishedLiteral())
			})
		}
	}
	t.Run("rejection_controls", func(t *testing.T) {
		for _, rejection := range []string{"repeated_running", "wrong_probe", "missing_project", "empty_project", "invalid_terminal"} {
			t.Run(rejection, func(t *testing.T) {
				service, now, published, priorRead := transitionSeed(t)
				*now = time.Date(2026, 11, 5, 3, 0, 0, 0, time.UTC)
				if _, err := service.MarkRunning(ctx, "synthetic-project", "synthetic-probe"); err != nil {
					t.Fatal(err)
				}
				*now = time.Date(2026, 11, 5, 4, 0, 0, 0, time.UTC)
				var returned ProjectContext
				var err, sentinel error
				switch rejection {
				case "repeated_running":
					returned, err = service.MarkRunning(ctx, "synthetic-project", "other-probe")
					sentinel = ErrConflict
				case "wrong_probe":
					returned, err = service.MarkFinished(ctx, "synthetic-project", "other-probe", StatusReady, "ignored", "ignored", "ignored")
					sentinel = ErrConflict
				case "missing_project":
					returned, err = service.MarkFinished(ctx, "missing-project", "synthetic-probe", StatusReady, "ignored", "ignored", "ignored")
					sentinel = ErrNotFound
				case "empty_project":
					returned, err = service.MarkRunning(ctx, "", "synthetic-probe")
				case "invalid_terminal":
					returned, err = service.MarkFinished(ctx, "synthetic-project", "synthetic-probe", StatusRunning, "ignored", "ignored", "ignored")
				}
				if err == nil || sentinel != nil && !errors.Is(err, sentinel) {
					t.Fatalf("rejection error = %v; expected nonnil matching %v", err, sentinel)
				}
				transitionEqual(t, "rejection returns zero metadata", returned, ProjectContext{})
				transitionEqual(t, "rejection preserves stored running metadata", transitionRead(t, service), transitionRunningLiteral())
				transitionEqual(t, "rejection preserves published snapshot", published, transitionPublishedLiteral())
				transitionEqual(t, "rejection preserves prior Get snapshot", priorRead, transitionPublishedLiteral())
				if rejection == "missing_project" {
					if _, err := service.Get(ctx, "missing-project"); !errors.Is(err, ErrNotFound) {
						t.Fatalf("missing project unexpectedly created: %v", err)
					}
				}
			})
		}
	})
}
