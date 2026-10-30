package projectcontext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
)

type bootstrapProjectLookup map[string]project.Project

func (p bootstrapProjectLookup) Get(_ context.Context, id string) (project.Project, error) {
	value, ok := p[id]
	if !ok {
		return project.Project{}, project.ErrNotFound
	}
	return value, nil
}

type bootstrapRepositoryList []repository.Repository

func (r bootstrapRepositoryList) List(context.Context) ([]repository.Repository, error) {
	return append([]repository.Repository(nil), r...), nil
}

func TestBootstrapGuidanceBuildsValidReusableModuleMapAndInvalidatesStructurally(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeBootstrapFile(t, root, "AGENTS.md", "# Project guidance\n`internal/core` owns request routing.\n`internal/support` supports core.\n`internal/removed` is historical.\n")
	writeBootstrapFile(t, root, "internal/core/core.go", "// Package core routes production requests.\npackage core\nimport _ \"example/internal/support\"\n")
	writeBootstrapFile(t, root, "internal/support/support.go", "// Package support supplies shared production state.\npackage support\n")
	writeBootstrapFile(t, root, "internal/unrelated/unrelated.go", "package unrelated\n")
	writeBootstrapFile(t, root, "tests/should_not_be_read_test.go", "package tests\n")
	external := t.TempDir()
	writeBootstrapFile(t, external, "escape.go", "package escape\n")
	if err := os.Symlink(external, filepath.Join(root, "internal", "escape")); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)
	clock := func() time.Time { now = now.Add(time.Second); return now }
	contexts := NewService(NewMemoryStore(), clock)
	moduleStore := module.NewMemoryStore()
	nextID := 0
	modules := module.NewService(moduleStore, bootstrapProjectLookup{"project": {ID: "project"}}, func() (string, error) {
		nextID++
		return "module-" + string(rune('0'+nextID)), nil
	}, clock)
	bootstrapper, err := NewBootstrapper(contexts, bootstrapRepositoryList{{ID: "repo", ProjectID: "project", Name: "Primary", Path: root}}, modules)
	if err != nil {
		t.Fatal(err)
	}

	first, err := bootstrapper.Ensure(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusReady || first.RootGuidanceIdentity == "" || first.ContextFingerprint == "" || !strings.Contains(first.Result, "Project guidance") {
		t.Fatalf("guided context = %+v", first)
	}
	if len(first.Diagnostics) != 1 || !strings.Contains(first.Diagnostics[0], "internal/removed") {
		t.Fatalf("guided diagnostics = %+v", first.Diagnostics)
	}
	firstMap, err := modules.ListContextMapByProject(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	assertBootstrapRoots(t, firstMap, []string{"internal/core", "internal/support", "internal/unrelated"})
	coreID := moduleIDForRoot(t, firstMap, "internal/core")
	core := moduleForRoot(t, firstMap, "internal/core")
	if core.ProductionFacts[0].ResponsibilitySource != module.ResponsibilityGuidance || len(core.ProductionFacts[0].RelatedModules) != 1 {
		t.Fatalf("guided core facts = %+v", core.ProductionFacts[0])
	}

	// An ordinary source edit leaves the cheap structural fingerprint intact.
	writeBootstrapFile(t, root, "internal/core/core.go", "// Package core now routes changed production requests.\npackage core\n")
	reused, err := bootstrapper.Ensure(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if reused.UpdatedAt != first.UpdatedAt || reused.ContextFingerprint != first.ContextFingerprint || nextID != 3 {
		t.Fatalf("ordinary source edit rebuilt context: first=%+v reused=%+v ids=%d", first, reused, nextID)
	}

	// A new production boundary invalidates and refreshes the map, while the
	// stable key for an existing boundary retains its durable Module identity.
	writeBootstrapFile(t, root, "internal/newboundary/new.go", "package newboundary\n")
	refreshed, err := bootstrapper.Ensure(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	refreshedMap, err := modules.ListContextMapByProject(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ContextFingerprint == first.ContextFingerprint || refreshed.UpdatedAt == first.UpdatedAt {
		t.Fatalf("structural change did not refresh context: first=%+v refreshed=%+v", first, refreshed)
	}
	assertBootstrapRoots(t, refreshedMap, []string{"internal/core", "internal/newboundary", "internal/support", "internal/unrelated"})
	if got := moduleIDForRoot(t, refreshedMap, "internal/core"); got != coreID {
		t.Fatalf("stable Module identity changed: got %q want %q", got, coreID)
	}
}

func TestBootstrapWithoutGuidanceCreatesLightweightProductionMap(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeBootstrapFile(t, root, "src/api/index.ts", "export const api = true;\n")
	writeBootstrapFile(t, root, "src/worker/worker.ts", "export const worker = true;\n")
	writeBootstrapFile(t, root, "test/api.test.ts", "throw new Error('must stay outside module map');\n")

	contexts := NewService(NewMemoryStore(), nil)
	modules := module.NewService(module.NewMemoryStore(), bootstrapProjectLookup{"project": {ID: "project"}}, nil, nil)
	bootstrapper, err := NewBootstrapper(contexts, bootstrapRepositoryList{{ID: "repo", ProjectID: "project", Name: "Primary", Path: root}}, modules)
	if err != nil {
		t.Fatal(err)
	}
	value, err := bootstrapper.Ensure(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != StatusNoGuidance || value.ContextFingerprint == "" || len(value.Diagnostics) != 1 || !strings.Contains(value.Diagnostics[0], "lightweight production map") {
		t.Fatalf("no-guidance context = %+v", value)
	}
	values, err := modules.ListContextMapByProject(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	assertBootstrapRoots(t, values, []string{"src/api", "src/worker"})
	for _, value := range values {
		if len(value.ProductionFacts) != 1 || value.ProductionFacts[0].ResponsibilitySource == module.ResponsibilityGuidance {
			t.Fatalf("no-guidance provenance = %+v", value)
		}
	}
}

func TestBootstrapDoesNotCompeteWithRunningContextAcquisition(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeBootstrapFile(t, root, "src/api/index.ts", "export const api = true;\n")
	contexts := NewService(NewMemoryStore(), nil)
	if _, err := contexts.EnsureRequired(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := contexts.MarkRunning(ctx, "project", "probe"); err != nil {
		t.Fatal(err)
	}
	modules := module.NewService(module.NewMemoryStore(), bootstrapProjectLookup{"project": {ID: "project"}}, nil, nil)
	bootstrapper, err := NewBootstrapper(contexts, bootstrapRepositoryList{{ID: "repo", ProjectID: "project", Name: "Primary", Path: root}}, modules)
	if err != nil {
		t.Fatal(err)
	}
	value, err := bootstrapper.Ensure(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	moduleMap, err := modules.ListContextMapByProject(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != StatusRunning || len(moduleMap) != 0 {
		t.Fatalf("running acquisition was overwritten: context=%+v modules=%+v", value, moduleMap)
	}
}

func TestBootstrapPreservesRepositoryOwnershipAcrossGuidedAndDiscoveredRepositories(t *testing.T) {
	ctx := context.Background()
	guidedRoot, discoveredRoot := filepath.Join(t.TempDir(), "guided"), filepath.Join(t.TempDir(), "discovered")
	writeBootstrapFile(t, guidedRoot, "AGENTS.md", "# Guidance\n`src/api` owns the guided API.\n")
	writeBootstrapFile(t, guidedRoot, "src/api/main.go", "package api\n")
	writeBootstrapFile(t, discoveredRoot, "lib/worker/index.ts", "export const worker = true;\n")
	contexts := NewService(NewMemoryStore(), nil)
	modules := module.NewService(module.NewMemoryStore(), bootstrapProjectLookup{"project": {ID: "project"}}, nil, nil)
	bootstrapper, err := NewBootstrapper(contexts, bootstrapRepositoryList{
		{ID: "repo-guided", ProjectID: "project", Name: "Guided Repo", Path: guidedRoot},
		{ID: "repo-discovered", ProjectID: "project", Name: "Worker Repo", Path: discoveredRoot},
	}, modules)
	if err != nil {
		t.Fatal(err)
	}
	value, err := bootstrapper.Ensure(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	values, err := modules.ListContextMapByProject(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != StatusReady || len(values) != 2 || values[0].Key != "guided-repo.api" || values[1].Key != "worker-repo.worker" {
		t.Fatalf("multi-Repository map = %+v context=%+v", values, value)
	}
	if values[0].ProductionFacts[0].RepositoryID != "repo-guided" || values[1].ProductionFacts[0].RepositoryID != "repo-discovered" || values[0].ProductionFacts[0].ResponsibilitySource != module.ResponsibilityGuidance || values[1].ProductionFacts[0].ResponsibilitySource == module.ResponsibilityGuidance {
		t.Fatalf("multi-Repository ownership/provenance = %+v", values)
	}
}

func writeBootstrapFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertBootstrapRoots(t *testing.T, values []module.Module, want []string) {
	t.Helper()
	got := make([]string, 0, len(values))
	for _, value := range values {
		for _, facts := range value.ProductionFacts {
			got = append(got, facts.ProductionRoots...)
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("module roots = %v, want %v", got, want)
	}
}

func moduleForRoot(t *testing.T, values []module.Module, root string) module.Module {
	t.Helper()
	for _, value := range values {
		for _, facts := range value.ProductionFacts {
			if len(facts.ProductionRoots) == 1 && facts.ProductionRoots[0] == root {
				return value
			}
		}
	}
	t.Fatalf("module root %q not found in %+v", root, values)
	return module.Module{}
}

func moduleIDForRoot(t *testing.T, values []module.Module, root string) string {
	t.Helper()
	return moduleForRoot(t, values, root).ID
}
