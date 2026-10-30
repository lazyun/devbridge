package codex

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"

	"github.com/lazyun/devbridge/internal/codex/wire"
)

func bindingForThread(cwd string, binding RuntimeBinding) (RuntimeBinding, error) {
	if !validRuntimePath(cwd) {
		return RuntimeBinding{}, errors.New("thread working directory must be a clean absolute path")
	}
	if bindingIsZero(binding) {
		// Compatibility is deliberately limited to this explicit one-root
		// Thread API. It cannot invent a second repository root.
		binding = RuntimeBinding{RuntimeWorkspaceRoots: []string{cwd}, WritableRoots: []string{cwd}}
	}
	if err := validateRuntimeBinding(binding); err != nil {
		return RuntimeBinding{}, err
	}
	for _, root := range binding.RuntimeWorkspaceRoots {
		if root == cwd {
			return cloneRuntimeBinding(binding), nil
		}
	}
	return RuntimeBinding{}, errors.New("thread working directory must be a runtime workspace root")
}

func (c *Client) requireSameThreadBinding(threadID string, binding RuntimeBinding) error {
	c.runtimeMu.Lock()
	previous, ok := c.threadBindings[threadID]
	c.runtimeMu.Unlock()
	if ok && !sameRuntimeBinding(previous, binding) {
		return fmt.Errorf("thread %q runtime binding differs from its frozen roots", threadID)
	}
	return nil
}

func (c *Client) bindingForTurn(threadID string, requested RuntimeBinding) (RuntimeBinding, bool, error) {
	c.runtimeMu.Lock()
	previous, known := c.threadBindings[threadID]
	c.runtimeMu.Unlock()
	if bindingIsZero(requested) {
		if known {
			return cloneRuntimeBinding(previous), true, nil
		}
		// Old direct turn callers have no CWD from which a binding could be
		// derived. Keep their legacy protocol path, but never use it for a
		// Thread that has a frozen binding.
		return RuntimeBinding{}, false, nil
	}
	if err := validateRuntimeBinding(requested); err != nil {
		return RuntimeBinding{}, false, err
	}
	if known && !sameRuntimeBinding(previous, requested) {
		return RuntimeBinding{}, false, fmt.Errorf("thread %q runtime binding differs from its frozen roots", threadID)
	}
	return cloneRuntimeBinding(requested), true, nil
}

func validateRuntimeBinding(binding RuntimeBinding) error {
	if len(binding.RuntimeWorkspaceRoots) == 0 {
		return errors.New("runtime workspace roots are required")
	}
	if len(binding.RuntimeWorkspaceRoots) > 128 {
		return errors.New("runtime workspace roots exceed the protocol limit")
	}
	roots := make(map[string]struct{}, len(binding.RuntimeWorkspaceRoots))
	for _, root := range binding.RuntimeWorkspaceRoots {
		if !validRuntimePath(root) {
			return errors.New("runtime workspace roots must be distinct clean absolute paths")
		}
		if _, duplicate := roots[root]; duplicate {
			return errors.New("runtime workspace roots must be distinct")
		}
		roots[root] = struct{}{}
	}
	seenWritable := make(map[string]struct{}, len(binding.WritableRoots))
	for _, root := range binding.WritableRoots {
		if _, duplicate := seenWritable[root]; duplicate {
			return errors.New("runtime writable roots must be distinct")
		}
		if _, permitted := roots[root]; !permitted {
			return errors.New("runtime writable roots must be runtime workspace roots")
		}
		seenWritable[root] = struct{}{}
	}
	if binding.TaskPermissions != nil {
		return binding.TaskPermissions.Validate(binding.RuntimeWorkspaceRoots, binding.WritableRoots)
	}
	return nil
}

func validRuntimePath(path string) bool {
	return path != "" && path != string(filepath.Separator) && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func bindingIsZero(binding RuntimeBinding) bool {
	return len(binding.RuntimeWorkspaceRoots) == 0 && len(binding.WritableRoots) == 0 && binding.TaskPermissions == nil
}

func cloneRoots(roots []string) []string {
	return append([]string(nil), roots...)
}

func cloneRuntimeBinding(binding RuntimeBinding) RuntimeBinding {
	copy := RuntimeBinding{RuntimeWorkspaceRoots: cloneRoots(binding.RuntimeWorkspaceRoots), WritableRoots: cloneRoots(binding.WritableRoots)}
	if binding.TaskPermissions != nil {
		copy.TaskPermissions = binding.TaskPermissions.Clone()
	}
	return copy
}

func sameRuntimeBinding(left, right RuntimeBinding) bool {
	if !reflect.DeepEqual(left.TaskPermissions, right.TaskPermissions) {
		return false
	}
	if len(left.RuntimeWorkspaceRoots) != len(right.RuntimeWorkspaceRoots) || len(left.WritableRoots) != len(right.WritableRoots) {
		return false
	}
	for index := range left.RuntimeWorkspaceRoots {
		if left.RuntimeWorkspaceRoots[index] != right.RuntimeWorkspaceRoots[index] {
			return false
		}
	}
	for index := range left.WritableRoots {
		if left.WritableRoots[index] != right.WritableRoots[index] {
			return false
		}
	}
	return true
}

func threadSandbox(binding RuntimeBinding) string {
	if len(binding.WritableRoots) == 0 {
		return "read-only"
	}
	return "workspace-write"
}

func sandboxPolicyForBinding(ceiling AuthorizationCeiling, binding RuntimeBinding, bound bool) *wire.SandboxPolicy {
	if !bound {
		return sandboxPolicy(ceiling)
	}
	if ceiling == AuthorizationReadOnly && binding.TaskPermissions == nil || len(binding.WritableRoots) == 0 {
		return &wire.SandboxPolicy{Type: "readOnly"}
	}
	policy := &wire.SandboxPolicy{Type: "workspaceWrite", WritableRoots: cloneRoots(binding.WritableRoots)}
	if binding.TaskPermissions != nil {
		policy.ExcludeSlashTmp = true
		policy.ExcludeTmpdirEnvVar = true
	}
	return policy
}
