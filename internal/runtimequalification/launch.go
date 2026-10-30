package runtimequalification

import (
	"context"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

// VerifiedExecutionLaunch checks the exact execution authority and captures
// the native executable from the very same artifact measurement. A subsequent
// factory may not reinterpret the original backend path as a new launcher.
func (s *Service) VerifiedExecutionLaunch(ctx context.Context, execution taskgoal.Execution) (codexlaunch.VerifiedExecutable, error) {
	if s.Selections == nil || s.Configurations == nil || execution.BackendSelection == nil {
		return nil, runtimeselection.ErrBindingMissing
	}
	owner := runtimeselection.Owner{ProjectID: execution.ProjectID, TaskID: execution.TaskID, TaskDefinitionRevisionID: execution.TaskDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION"}
	authority, err := readExecutionAuthority(ctx, s.Selections, owner, execution.ID)
	if err != nil {
		return nil, err
	}
	b, r, v := execution.BackendSelection, execution.RuntimeConfig, authority.Values
	if authority.Owner != owner || v.BackendIdentity != b.ConfigID || v.BackendConfigFingerprint != b.ConfigFingerprint || v.BackendSource != b.Source || v.BackendKind != "CODEX" || v.Model != r.Model || v.ReasoningEffort != r.ReasoningEffort || v.CodexFastMode != r.CodexFastMode || v.ServiceTier != r.ServiceTier || v.RuntimeAdapterIdentity != execution.RuntimeAdapterIdentity || v.RuntimeProtocolIdentity != execution.RuntimeProtocolIdentity {
		return nil, runtimeplatform.ErrIdentityMismatch
	}
	config, err := currentLaunchBackend(ctx, s.Configurations, authority)
	if err != nil {
		return nil, err
	}
	provider, _, err := s.provider(ctx, config)
	if err != nil {
		return nil, err
	}
	descriptor, err := provider.Descriptor(owner.RuntimeRole)
	if err != nil {
		return nil, err
	}
	if descriptor.DescriptorID != authority.Platform.DescriptorID || descriptor.ComputeHash() != authority.Platform.DescriptorHash || descriptor.PlatformLineageID != authority.Platform.PlatformLineageID {
		return nil, runtimeplatform.ErrIdentityMismatch
	}
	native, err := provider.NativeLaunch(ctx, descriptor.DescriptorID)
	if err != nil {
		return nil, err
	}
	return boundNativeLaunch{selections: s.Selections, configurations: s.Configurations, authority: authority, executionID: execution.ID, native: native}, nil
}

// The file token alone does not carry enduring permission to start. Keep its
// already measured native path, but re-prove the exact original authority every
// time a factory consumes it, including independent-process recovery.
type boundNativeLaunch struct {
	selections     runtimeselection.Store
	configurations Configurations
	authority      runtimeselection.CurrentAuthority
	executionID    string
	native         codexlaunch.VerifiedExecutable
}

func (l boundNativeLaunch) Executable(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	current, err := readExecutionAuthority(ctx, l.selections, l.authority.Owner, l.executionID)
	if err != nil {
		return "", err
	}
	// Recheck the same execution scope and original immutable authority. An
	// existing authority may retain its SUPERSEDED qualification; it never
	// selects a newer qualification or another execution's successor chain.
	if current != l.authority {
		return "", runtimeplatform.ErrIdentityMismatch
	}
	if _, err = currentLaunchBackend(ctx, l.configurations, current); err != nil {
		return "", err
	}
	return l.native.Executable(ctx)
}

func readExecutionAuthority(ctx context.Context, store runtimeselection.Store, owner runtimeselection.Owner, executionID string) (runtimeselection.CurrentAuthority, error) {
	if scoped, ok := store.(runtimeselection.ExecutionAuthorityReader); ok {
		if executionID == "" {
			return runtimeselection.CurrentAuthority{}, runtimeplatform.ErrIdentityMismatch
		}
		return scoped.ReadExecutionAuthority(ctx, owner, executionID)
	}
	return store.ReadCurrent(ctx, owner)
}

func currentLaunchBackend(ctx context.Context, configurations Configurations, authority runtimeselection.CurrentAuthority) (codexconfig.Config, error) {
	config, err := configurations.Get(ctx, authority.Values.BackendIdentity)
	if err != nil {
		return codexconfig.Config{}, err
	}
	if !config.Enabled || config.ID != authority.Values.BackendIdentity || config.Fingerprint() != authority.Values.BackendConfigFingerprint {
		return codexconfig.Config{}, runtimeplatform.ErrIdentityMismatch
	}
	return config, nil
}
