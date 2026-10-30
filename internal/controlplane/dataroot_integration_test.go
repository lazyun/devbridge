package controlplane_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

func TestDataRootSwitchKeepsIndependentEnvironmentState(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	rootA := filepath.Join(base, "data-a")
	rootB := filepath.Join(base, "data-b")

	planeA, err := controlplane.Open(ctx, controlplane.Config{DataRoot: rootA})
	if err != nil {
		t.Fatal(err)
	}
	projectA, err := planeA.Projects.Create(ctx, "Environment A")
	if err != nil {
		t.Fatal(err)
	}
	sessionA := bindFixtureSession(t, ctx, planeA, "dataroot-a", projectA.ID)
	featureAreaA := activateContextFixtureArea(t, planeA, projectA.ID, sessionA.Session.ID)
	pathsA := planeA.DataPaths
	for path, value := range map[string]string{
		filepath.Join(pathsA.Logs, "sentinel.log"):      "log-a",
		filepath.Join(pathsA.Evidence, "sentinel.txt"):  "evidence-a",
		filepath.Join(pathsA.Artifacts, "sentinel.txt"): "artifact-a",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := planeA.Close(); err != nil {
		t.Fatal(err)
	}

	planeB, err := controlplane.Open(ctx, controlplane.Config{DataRoot: rootB})
	if err != nil {
		t.Fatal(err)
	}
	projectsB, err := planeB.Projects.List(ctx)
	if err != nil || len(projectsB) != 0 {
		t.Fatalf("fresh root B projects = %+v, %v", projectsB, err)
	}
	featureAreasB, err := sqlite.NewFeatureAreaStore(planeB.DB).ListByProject(ctx, projectA.ID)
	if err != nil {
		t.Fatalf("fresh root B featureAreas: %v", err)
	}
	if len(featureAreasB) != 0 {
		t.Fatalf("root A featureAreas copied into root B: %+v", featureAreasB)
	}
	pathsB := planeB.DataPaths
	if pathsB.Worktrees == pathsA.Worktrees || pathsB.Logs == pathsA.Logs || pathsB.Evidence == pathsA.Evidence {
		t.Fatalf("derived paths are shared: A=%+v B=%+v", pathsA, pathsB)
	}
	for _, path := range []string{
		filepath.Join(pathsB.Logs, "sentinel.log"),
		filepath.Join(pathsB.Evidence, "sentinel.txt"),
		filepath.Join(pathsB.Artifacts, "sentinel.txt"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("root A file copied to B at %s: %v", path, err)
		}
	}
	if err := planeB.Close(); err != nil {
		t.Fatal(err)
	}

	planeA, err = controlplane.Open(ctx, controlplane.Config{DataRoot: rootA})
	if err != nil {
		t.Fatal(err)
	}
	defer planeA.Close()
	gotProject, err := planeA.Projects.Get(ctx, projectA.ID)
	if err != nil || gotProject.Name != projectA.Name {
		t.Fatalf("reopened project = %+v, %v", gotProject, err)
	}
	gotTopic, err := sqlite.NewFeatureAreaStore(planeA.DB).Get(ctx, featureAreaA.ID)
	if err != nil || gotTopic.Title != featureAreaA.Title {
		t.Fatalf("reopened featureArea = %+v, %v", gotTopic, err)
	}
	if planeA.DB.StateRoot() != pathsA.State {
		t.Fatalf("SQLite state root = %q, want %q", planeA.DB.StateRoot(), pathsA.State)
	}
}
