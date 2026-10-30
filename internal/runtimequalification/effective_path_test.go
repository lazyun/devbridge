package runtimequalification_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/platformcollector"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// Qualification must measure the same executable choice that the Core factory
// will launch. Every successful qualification here runs the installed native
// conformance collector; the poisoned default selection never grants authority.
func TestInstalledNativeQualificationUsesCoreEffectivePath(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if native == "" {
		t.Skip("explicit installed native binary required; no online provider")
	}
	native, err := filepath.EvalSymlinks(native)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../build/remaining-constraints-2026-10-02/qualification-effective-path")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err = os.MkdirTemp(root, "measurement-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("durable effective-path evidence:", root)
	selectionFile := filepath.Join(root, "unusable-default-selection.json")
	if err = codexlaunch.Write(selectionFile, codexlaunch.Selection{Error: "default selection must not replace the Core base or explicit backend"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(codexlaunch.SelectionEnv, selectionFile)
	if _, err = codexlaunch.Runtime(""); err == nil {
		t.Fatal("the test's default selection is unexpectedly usable")
	}
	for _, tc := range []struct {
		name, base, selected string
	}{
		{name: "empty_backend_inherits_core_base", base: native},
		{name: "explicit_backend_overrides_core_base", base: filepath.Join(root, "missing-base-native"), selected: native},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cfg := controlplane.Config{DataRoot: filepath.Join(root, tc.name), Codex: codex.Config{CodexPath: tc.base}}
			plane, err := controlplane.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer plane.Close()
			factory, ok := plane.Coordinator.Codex.(execution.DefaultCodexFactory)
			if !ok || factory.Config.CodexPath != tc.base {
				t.Fatalf("Core factory base path: %T %+v", plane.Coordinator.Codex, factory.Config)
			}
			backend, err := plane.CodexConfigs.Create(ctx, codexconfig.CreateInput{Name: tc.name, CodexPath: tc.selected, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			mapping, err := plane.RuntimeQualification.QualifyBackend(ctx, backend.ID, "IMPLEMENTATION", "qualify-effective-path")
			if err != nil {
				t.Fatalf("qualify actual factory selection (base=%q backend=%q): %v", tc.base, tc.selected, err)
			}
			store := sqlite.NewRuntimePlatformStore(plane.DB)
			evidence, err := store.GetEvidence(ctx, mapping.Platform.CompatibilityEvidenceHash)
			if err != nil {
				t.Fatal(err)
			}
			artifacts, err := platformcollector.NewArtifactStore(filepath.Join(cfg.DataRoot, "platform-evidence", "compatibility"))
			if err != nil {
				t.Fatal(err)
			}
			report, err := artifacts.ReadReport(evidence.CollectionArtifactHash)
			if err != nil {
				t.Fatal(err)
			}
			if report.Manifest.NativePath != native || report.DescriptorID != mapping.Platform.DescriptorID {
				t.Fatalf("qualification measured a different artifact: %+v", report.Manifest)
			}
			for _, check := range platformcollector.RequiredChecks() {
				if !report.Checks[check] {
					t.Fatalf("qualification omitted actual native conformance check %q", check)
				}
			}
			if reference, err := plane.RuntimeQualification.ResolvePlatformReference(ctx, backend.Selection("CORE_TEST"), "IMPLEMENTATION"); err != nil || reference != mapping.Platform {
				t.Fatalf("resolve did not retain the effective artifact: %+v %v", reference, err)
			}
			var runs int
			if err = plane.DB.SQL().QueryRowContext(ctx, "SELECT count(*) FROM runs").Scan(&runs); err != nil || runs != 0 {
				t.Fatalf("qualification created business runtime authority: runs=%d error=%v", runs, err)
			}
			t.Logf("native=%s evidence=%s checks=%v", report.Manifest.NativePath, evidence.CollectionArtifactHash, report.Checks)
		})
	}
}
