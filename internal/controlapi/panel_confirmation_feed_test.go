package controlapi_test

import (
	"context"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"testing"
)

func TestSinglePanelFeedKeepsSessionIsolationAndUnboundProjectProposals(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	// A fresh Session belongs to the same caller but has no Project or Task.
	caller, _ := aisession.CallerScopeFromContext(f.ctx)
	initialized, err := f.plane.AIContext.InitializeSession(f.ctx, aiprojectselection.ClientBindingReference{CallerScope: caller}, "panel-unbound", "")
	if err != nil {
		t.Fatal(err)
	}
	input := c.PrepareInput{AISessionID: initialized.AISessionID, RequestID: "panel-setup", Purpose: c.ProjectSetup, Action: "SELECT_PROJECT", ProjectSetup: &c.ProjectBody{Summary: "Choose the existing Project"}, Variants: []c.Variant{{ID: "enter", Label: "进入项目", Summary: "Bind only this Session"}}}
	intent, err := f.facade.PrepareConfirmation(f.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{"ai_sessions", "confirmation_intents", "tasks", "reviews", "runs"}
	before := definitionFormalSnapshot(t, f, tables)
	feed, err := f.facade.PanelConfirmationFeed(f.ctx, initialized.AISessionID)
	if err != nil || feed.ProjectID != "" || feed.Scope != "SESSION" || len(feed.Items) != 1 || feed.Items[0].IntentID != intent.IntentID || feed.Items[0].State != "PENDING" || feed.Items[0].RequestID != input.RequestID {
		t.Fatalf("unbound proposal lost: %+v %v", feed, err)
	}
	bound, err := f.facade.PanelConfirmationFeed(f.ctx, f.session)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range bound.Items {
		if item.IntentID == intent.IntentID {
			t.Fatal("another Session's proposal leaked")
		}
	}
	if _, err = f.facade.PanelConfirmationFeed(aisession.WithCallerScope(context.Background(), "foreign-panel-caller"), initialized.AISessionID); err == nil {
		t.Fatal("foreign caller accepted")
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "panel reads")
}

func TestFocusedPanelFeedChecksTaskOwnershipAndDoesNotChangeBusinessState(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	tables := []string{"ai_sessions", "confirmation_intents", "tasks", "reviews", "runs"}
	before := definitionFormalSnapshot(t, f, tables)
	feed, err := f.facade.PanelConfirmationFeedForTask(f.ctx, f.session, f.task)
	if err != nil || feed.FocusTaskID != f.task {
		t.Fatalf("focused panel unavailable: %+v %v", feed, err)
	}
	if _, err = f.facade.PanelConfirmationFeedForTask(f.ctx, f.session, "missing-task"); err == nil {
		t.Fatal("unknown Task accepted")
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "focused panel reads")
}
