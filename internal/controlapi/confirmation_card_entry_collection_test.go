package controlapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
)

// The durable reader reconstructs a freshly prepared (uncommitted) intent with a
// non-nil empty step set, so the only place a zero-step intent could collapse to
// JSON null was the public copy (now fixed and covered by the mcpserver
// projection tests). This proves confirmation_get never reintroduces steps:null.
func TestConfirmationReaderKeepsStepsAsEmptyArrayNotNil(t *testing.T) {
	ctx := aisession.WithCallerScope(context.Background(), "confirmation-collection-canonicalization")
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	if _, err = plane.Projects.Create(ctx, "Canonicalization project"); err != nil {
		t.Fatal(err)
	}
	session, err := plane.AIContext.InitializeSession(ctx, aiprojectselection.ClientBindingReference{CallerScope: "confirmation-collection-canonicalization"}, "initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := facade.PrepareConfirmation(ctx, c.PrepareInput{
		AISessionID: session.AISessionID, RequestID: "prepare-empty-steps", Purpose: c.ProjectSetup, Action: "SELECT_PROJECT",
		ProjectSetup: &c.ProjectBody{Summary: "Enter project"},
		Variants:     []c.Variant{{ID: "enter", Label: "Enter", Summary: "Bind this Session to the logical project"}},
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Steps == nil {
		t.Fatal("prepared intent steps must be a non-nil empty slice, not nil")
	}
	if len(prepared.Steps) != 0 {
		t.Fatalf("fresh intent must have zero steps, got %d", len(prepared.Steps))
	}
	read, err := facade.ReadConfirmation(ctx, c.ReadInput{AISessionID: session.AISessionID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: c.ProjectSetup, ContentHash: prepared.ContentHash})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if read.Steps == nil {
		t.Fatal("reader must reconstruct a non-nil empty step set")
	}
	raw, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	if !strings.Contains(encoded, `"steps":[]`) {
		t.Fatalf("reader serialization must emit steps:[], got %s", encoded)
	}
	if strings.Contains(encoded, `"steps":null`) {
		t.Fatalf("reader serialization emitted steps:null: %s", encoded)
	}
}
