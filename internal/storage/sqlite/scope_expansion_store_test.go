package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
)

func TestReviewScopeExpansionRechecksExactAuthorityAtPersistence(t *testing.T) {
	f := newAgentStorageFixture(t, "scope-authority")
	ctx := context.Background()
	owner := prepareAgentOperation(t, f, agentconversation.Review, "scope-authority")
	other := prepareAgentOperation(t, f, agentconversation.Review, "other-scope-authority")
	initial := run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"internal/a"}}
	for _, id := range []string{owner.id, other.id} {
		grant := reviewgrant.Grant{ReviewID: id, ProjectID: f.project.ID, FeatureAreaID: f.area.ID, RepositoryID: f.task.RepositoryID, Mode: reviewgrant.ModeFocused, InitialScope: initial, SelectedModules: []reviewgrant.ModuleSelection{{Key: "a", Role: reviewgrant.RolePrimary}}, CreatedAt: testNow}
		if err := NewReviewGrantStore(f.db).Create(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	proposal := scopeexpansion.Expansion{ID: "expand-scope", AISessionID: f.session.ID, ProjectID: f.project.ID, ReviewID: owner.id, FeatureAreaID: f.area.ID, RepositoryID: f.task.RepositoryID, CurrentScope: initial, AdditionalScope: run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"internal/b"}}, Reason: "Inspect exact dependency", Status: scopeexpansion.StatusPendingConfirmation, CreatedAt: testNow, UpdatedAt: testNow}
	store := NewScopeExpansionStore(f.db)
	for name, mutate := range map[string]func(*scopeexpansion.Expansion){
		"project":                func(v *scopeexpansion.Expansion) { v.ProjectID = "wrong" },
		"feature":                func(v *scopeexpansion.Expansion) { v.FeatureAreaID = "wrong" },
		"repository":             func(v *scopeexpansion.Expansion) { v.RepositoryID = "wrong" },
		"Task carrier":           func(v *scopeexpansion.Expansion) { v.LegacyTaskID = f.task.ID },
		"missing Review":         func(v *scopeexpansion.Expansion) { v.ReviewID = "" },
		"invented current grant": func(v *scopeexpansion.Expansion) { v.CurrentScope.Paths = []string{"ungranted"} },
	} {
		forged := scopeexpansion.Clone(proposal)
		mutate(&forged)
		if err := store.Create(ctx, forged); !errors.Is(err, scopeexpansion.ErrConflict) {
			t.Fatalf("accepted %s: %v", name, err)
		}
	}
	if err := store.Create(ctx, proposal); err != nil {
		t.Fatal(err)
	}
	// Owner identity stays immutable independently of the service callback.
	if _, err := f.db.sql.Exec(`UPDATE scope_expansions SET review_id=? WHERE id=?`, other.id, proposal.ID); err == nil {
		t.Fatal("changed persisted Review owner")
	}
	allowed, err := store.Decide(ctx, proposal.ID, scopeexpansion.StatusAllowed, testNow.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if allowed.ReviewID != owner.id || allowed.FeatureAreaID != f.area.ID || allowed.RepositoryID != f.task.RepositoryID || allowed.LegacyTaskID != "" {
		t.Fatalf("allowed=%+v", allowed)
	}
	stale := scopeexpansion.Clone(proposal)
	stale.ID = "stale-expansion"
	stale.AdditionalScope.Paths = []string{"internal/c"}
	if err = store.Create(ctx, stale); !errors.Is(err, scopeexpansion.ErrConflict) {
		t.Fatalf("accepted stale snapshot: %v", err)
	}
	next := scopeexpansion.Clone(stale)
	next.ID = "next-expansion"
	next.CurrentScope.Paths = []string{"internal/a", "internal/b"}
	next.AdditionalScope = run.ReviewScope{Kind: run.ReviewScopeRepository}
	if err = store.Create(ctx, next); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Decide(ctx, next.ID, scopeexpansion.StatusAllowed, testNow.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// The sibling Review's grant has not changed through the first Review's chain.
	sibling := scopeexpansion.Clone(proposal)
	sibling.ID = "sibling-expansion"
	sibling.ReviewID = other.id
	if err = store.Create(ctx, sibling); err != nil {
		t.Fatalf("sibling borrowed expansion history: %v", err)
	}
	reopenAgentFixtureDB(t, &f)
	reopened, err := NewScopeExpansionStore(f.db).Get(ctx, allowed.ID)
	if err != nil || !reflect.DeepEqual(reopened, allowed) {
		t.Fatalf("reopened=%+v err=%v", reopened, err)
	}
	var runs int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("scope authorization started runtime: runs=%d err=%v", runs, err)
	}
}
