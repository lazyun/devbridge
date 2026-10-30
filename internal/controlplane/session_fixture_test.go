package controlplane_test

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/review"
)

// bindFixtureSession creates the Session through the public Session-first
// protocol. Tests must not reintroduce the retired project-selection/OpenSession
// bootstrap path merely to obtain an AISessionID.
func bindFixtureSession(t *testing.T, ctx context.Context, plane *controlplane.ControlPlane, callerScope, projectID string) aicontext.SessionContext {
	t.Helper()
	ref := aiprojectselection.ClientBindingReference{CallerScope: callerScope}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, callerScope+"-initialize", "")
	if err != nil {
		t.Fatalf("initialize fixture session: %v", err)
	}
	bound, err := plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, projectID, callerScope+"-bind")
	if err != nil {
		t.Fatalf("bind fixture session: %v", err)
	}
	if bound.Context.Session.ID != initialized.AISessionID || bound.Context.Session.ProjectID != projectID {
		t.Fatalf("fixture binding=%+v", bound)
	}
	return bound.Context
}

// frozenCodingReview builds the current closed Review DTO used by fixtures that
// deliberately exercise an already-running review.  It records the exact
// selected backend and runtime choice instead of relying on the retired
// implicit/default Review admission.
func frozenCodingReview(t *testing.T, plane *controlplane.ControlPlane, value review.Review) review.Review {
	t.Helper()
	config, err := plane.CodexConfigs.Get(context.Background(), value.CodexConfigID)
	if err != nil {
		t.Fatalf("read fixture backend: %v", err)
	}
	selection := config.Selection("controlplane-test-fixture")
	value.ExecutorKind = review.ExecutorCodingAgent
	value.BackendSelection = &selection
	value.Model = "gpt-5.6-terra"
	value.ReasoningEffort = "high"
	return value
}
