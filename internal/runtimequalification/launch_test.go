package runtimequalification

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	rs "github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

type launchSelectionStore struct {
	rs.Store
	authority rs.CurrentAuthority
	err       error
	reads     []rs.Owner
}

func (s *launchSelectionStore) ReadCurrent(_ context.Context, owner rs.Owner) (rs.CurrentAuthority, error) {
	s.reads = append(s.reads, owner)
	return s.authority, s.err
}

type scopedLaunchSelectionStore struct {
	*launchSelectionStore
	authority rs.CurrentAuthority
	err       error
	reads     []struct {
		owner     rs.Owner
		execution string
	}
}

func (s *scopedLaunchSelectionStore) ReadExecutionAuthority(_ context.Context, owner rs.Owner, execution string) (rs.CurrentAuthority, error) {
	s.reads = append(s.reads, struct {
		owner     rs.Owner
		execution string
	}{owner, execution})
	return s.authority, s.err
}

type launchConfigurations struct {
	config codexconfig.Config
	err    error
	reads  int
}

func (c *launchConfigurations) Get(_ context.Context, id string) (codexconfig.Config, error) {
	c.reads++
	return c.config, c.err
}

type executableProbe struct{ calls int }

func (p *executableProbe) Executable(context.Context) (string, error) {
	p.calls++
	return "/captured/native", nil
}

func launchAuthorityFixture() (taskgoal.Execution, rs.CurrentAuthority, codexconfig.Config) {
	config := codexconfig.Config{ID: "backend", Name: "Scoped launch", CodexPath: "/captured/native", Enabled: true}
	backend := config.Selection("TASK_APPROVAL")
	runtime := codexruntime.NextRunRuntimeConfig{Model: "model", ReasoningEffort: "high", CodexFastMode: true, ServiceTier: "priority"}
	execution := taskgoal.Execution{ID: "exact-execution", ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", BackendSelection: &backend, RuntimeConfig: runtime, RuntimeAdapterIdentity: "adapter", RuntimeProtocolIdentity: "protocol"}
	owner := rs.Owner{ProjectID: execution.ProjectID, TaskID: execution.TaskID, TaskDefinitionRevisionID: execution.TaskDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION"}
	values := rs.AuthorityValues{BackendIdentity: backend.ConfigID, BackendConfigFingerprint: backend.ConfigFingerprint, BackendSource: backend.Source, BackendKind: "CODEX", Model: runtime.Model, ReasoningEffort: runtime.ReasoningEffort, CodexFastMode: runtime.CodexFastMode, ServiceTier: runtime.ServiceTier, RuntimeAdapterIdentity: execution.RuntimeAdapterIdentity, RuntimeProtocolIdentity: execution.RuntimeProtocolIdentity}
	authority := rs.CurrentAuthority{Owner: owner, BindingID: "original-authority", Platform: rs.PlatformReference{DescriptorID: "descriptor", DescriptorHash: rs.HashValue("descriptor"), PlatformLineageID: "lineage", QualificationID: "original-qualification", QualificationGeneration: 1}, Values: values, AuthoritySnapshotHash: rs.HashValue(values)}
	return execution, authority, config
}

// Stop at the configuration boundary after the public entrypoint has selected
// authority and validated every frozen launch value. No collector or native
// executable is faked into a qualified production source by this unit test.
func TestVerifiedExecutionLaunchUsesExactExecutionAuthority(t *testing.T) {
	execution, authority, config := launchAuthorityFixture()
	stop := errors.New("configuration boundary reached")
	for _, mode := range []string{"scoped", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			configs := &launchConfigurations{config: config, err: stop}
			base := &launchSelectionStore{authority: authority}
			var store rs.Store = base
			var scoped *scopedLaunchSelectionStore
			if mode == "scoped" {
				base.err = rs.ErrBindingMissing
				scoped = &scopedLaunchSelectionStore{launchSelectionStore: base, authority: authority}
				store = scoped
			}
			service := Service{Selections: store, Configurations: configs}
			if _, err := service.VerifiedExecutionLaunch(context.Background(), execution); !errors.Is(err, stop) {
				t.Fatalf("authority route: %v", err)
			}
			if configs.reads != 1 {
				t.Fatalf("config reads=%d", configs.reads)
			}
			if mode == "scoped" {
				if len(base.reads) != 0 || len(scoped.reads) != 1 || scoped.reads[0].owner != authority.Owner || scoped.reads[0].execution != execution.ID {
					t.Fatalf("wrong scoped read: %+v legacy=%+v", scoped.reads, base.reads)
				}
			} else if len(base.reads) != 1 || base.reads[0] != authority.Owner {
				t.Fatalf("legacy route changed: %+v", base.reads)
			}
		})
	}
}

func TestVerifiedExecutionLaunchScopedFailureNeverFallsBack(t *testing.T) {
	execution, authority, config := launchAuthorityFixture()
	for _, mode := range []string{"denied", "missing-execution"} {
		t.Run(mode, func(t *testing.T) {
			base := &launchSelectionStore{authority: authority}
			scoped := &scopedLaunchSelectionStore{launchSelectionStore: base, authority: authority, err: runtimeplatform.ErrQualificationInvalidated}
			configs := &launchConfigurations{config: config}
			input := execution
			want := runtimeplatform.ErrQualificationInvalidated
			if mode == "missing-execution" {
				input.ID = ""
				want = runtimeplatform.ErrIdentityMismatch
			}
			if _, err := (&Service{Selections: scoped, Configurations: configs}).VerifiedExecutionLaunch(context.Background(), input); !errors.Is(err, want) {
				t.Fatalf("scoped denial: %v", err)
			}
			if len(base.reads) != 0 || configs.reads != 0 {
				t.Fatal("scoped failure borrowed revision authority or reached configuration")
			}
		})
	}
}

func TestVerifiedExecutionLaunchPreservesFrozenValueChecks(t *testing.T) {
	execution, authority, config := launchAuthorityFixture()
	mutations := map[string]func(*rs.CurrentAuthority){
		"owner":               func(a *rs.CurrentAuthority) { a.Owner.TaskID = "foreign" },
		"backend-id":          func(a *rs.CurrentAuthority) { a.Values.BackendIdentity = "other" },
		"backend-fingerprint": func(a *rs.CurrentAuthority) { a.Values.BackendConfigFingerprint = "other" },
		"backend-source":      func(a *rs.CurrentAuthority) { a.Values.BackendSource = "other" },
		"backend-kind":        func(a *rs.CurrentAuthority) { a.Values.BackendKind = "other" },
		"model":               func(a *rs.CurrentAuthority) { a.Values.Model = "other" },
		"effort":              func(a *rs.CurrentAuthority) { a.Values.ReasoningEffort = "low" },
		"fast-mode":           func(a *rs.CurrentAuthority) { a.Values.CodexFastMode = false },
		"tier":                func(a *rs.CurrentAuthority) { a.Values.ServiceTier = "other" },
		"adapter":             func(a *rs.CurrentAuthority) { a.Values.RuntimeAdapterIdentity = "other" },
		"protocol":            func(a *rs.CurrentAuthority) { a.Values.RuntimeProtocolIdentity = "other" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := authority
			mutate(&changed)
			scoped := &scopedLaunchSelectionStore{launchSelectionStore: &launchSelectionStore{authority: authority}, authority: changed}
			configs := &launchConfigurations{config: config}
			if _, err := (&Service{Selections: scoped, Configurations: configs}).VerifiedExecutionLaunch(context.Background(), execution); !errors.Is(err, runtimeplatform.ErrIdentityMismatch) {
				t.Fatalf("changed %s accepted: %v", name, err)
			}
			if configs.reads != 0 || len(scoped.launchSelectionStore.reads) != 0 {
				t.Fatal("identity mismatch reached fallback/configuration")
			}
		})
	}
}

func TestBoundNativeLaunchRechecksExactExecutionOnEveryConsumption(t *testing.T) {
	execution, authority, config := launchAuthorityFixture()
	for _, change := range []string{"revoked", "authority", "platform", "values", "backend-disabled", "backend-changed"} {
		t.Run(change, func(t *testing.T) {
			base := &launchSelectionStore{authority: authority}
			scoped := &scopedLaunchSelectionStore{launchSelectionStore: base, authority: authority}
			configs := &launchConfigurations{config: config}
			native := &executableProbe{}
			token := boundNativeLaunch{selections: scoped, configurations: configs, authority: authority, executionID: execution.ID, native: native}
			if path, err := token.Executable(context.Background()); err != nil || path != "/captured/native" {
				t.Fatalf("initial consumption: %q %v", path, err)
			}
			want := runtimeplatform.ErrIdentityMismatch
			switch change {
			case "revoked":
				scoped.err = runtimeplatform.ErrQualificationInvalidated
				want = scoped.err
			case "authority":
				scoped.authority.BindingID = "another-successor"
			case "platform":
				scoped.authority.Platform.QualificationID = "newest-qualification"
			case "values":
				scoped.authority.Values.Model = "other-model"
			case "backend-disabled":
				configs.config.Enabled = false
			case "backend-changed":
				configs.config.CodexPath = "/changed/native"
			}
			if _, err := token.Executable(context.Background()); !errors.Is(err, want) {
				t.Fatalf("changed %s accepted: %v", change, err)
			}
			if native.calls != 1 || len(base.reads) != 0 || len(scoped.reads) != 2 {
				t.Fatalf("native=%d scoped=%+v fallback=%+v", native.calls, scoped.reads, base.reads)
			}
			for _, call := range scoped.reads {
				if call.execution != execution.ID || call.owner != authority.Owner {
					t.Fatalf("token changed exact execution scope: %+v", call)
				}
			}
		})
	}
}

func TestBoundNativeLaunchKeepsLegacyReadCurrentFallback(t *testing.T) {
	execution, authority, config := launchAuthorityFixture()
	store := &launchSelectionStore{authority: authority}
	native := &executableProbe{}
	token := boundNativeLaunch{selections: store, configurations: &launchConfigurations{config: config}, authority: authority, executionID: execution.ID, native: native}
	if _, err := token.Executable(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.err = rs.ErrBindingMissing
	if _, err := token.Executable(context.Background()); !errors.Is(err, rs.ErrBindingMissing) {
		t.Fatalf("legacy revocation ignored: %v", err)
	}
	if len(store.reads) != 2 || native.calls != 1 {
		t.Fatalf("legacy checks=%d native=%d", len(store.reads), native.calls)
	}
}
