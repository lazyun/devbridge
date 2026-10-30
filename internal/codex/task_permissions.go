package codex

import (
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/codex/wire"
	"github.com/lazyun/devbridge/internal/runtimepermission"
	"os"
	"path/filepath"
)

type TaskPermissions = runtimepermission.TaskPermissions
type VerificationCopy = runtimepermission.VerificationCopy

const TaskPermissionVersion = runtimepermission.TaskPermissionVersion

func (c *Client) requireTaskProfile(binding RuntimeBinding) error {
	if c.diagnosticTaskPolicy != nil {
		if binding.TaskPermissions != nil || len(binding.RuntimeWorkspaceRoots) == 0 || len(binding.WritableRoots) > 0 {
			return errors.New("TASK_DIAGNOSTIC_BINDING_NOT_READ_ONLY")
		}
		return nil
	}
	if c.executorBoundary && c.taskPermissionHash != "" && binding.TaskPermissions == nil {
		return errors.New("TASK_PERMISSION_BINDING_MISSING")
	}
	if binding.TaskPermissions != nil && c.executorBoundary && c.taskPermissionHash != binding.TaskPermissions.ContentHash {
		return errors.New("TASK_PERMISSION_PROCESS_BINDING_MISMATCH")
	}
	return nil
}

func (c *Client) verifyTaskProfile(binding RuntimeBinding, actual *wire.ExperimentalActivePermissionProfile) error {
	if profile := c.taskProfileID(binding); profile != "" && c.executorBoundary && (actual == nil || actual.ID != profile) {
		return errors.New("TASK_PERMISSION_PROFILE_UNOBSERVED")
	}
	return nil
}

func (c *Client) taskApprovalPolicy(binding RuntimeBinding) string {
	if c.taskProfileID(binding) != "" {
		return "never"
	}
	return ""
}

func (c *Client) taskEnvironmentConfig(binding RuntimeBinding, config map[string]any) map[string]any {
	if c.diagnosticTaskPolicy != nil {
		binding.TaskPermissions = c.diagnosticTaskPolicy
	}
	if binding.TaskPermissions == nil {
		return config
	}
	if config == nil {
		config = map[string]any{}
	}
	root := binding.TaskPermissions.ScratchRoot
	config["features.hooks"] = false
	config["features.multi_agent"] = false
	config["features.multi_agent_v2"] = false
	config["web_search"] = "disabled"
	environment := map[string]any{"TMPDIR": root, "TMP": root, "TEMP": root, "GOCACHE": root + "/go-build", "GOTMPDIR": root, "npm_config_cache": root + "/npm", "PYTHONPYCACHEPREFIX": root + "/pycache", "XDG_CACHE_HOME": root + "/cache", "GIT_OPTIONAL_LOCKS": "0"}
	// Module downloads otherwise write the user's GOPATH outside the task
	// sandbox. Only explicitly online trusted runs use a shared project cache
	// under already-authorized system temp. Offline runs retain their existing
	// readable module cache; diagnostics never inherit this write destination.
	if p := binding.TaskPermissions; c.diagnosticTaskPolicy == nil && p.HasTrustedExecution() && p.NetworkAccess {
		environment["GOMODCACHE"] = filepath.Join(os.TempDir(), "devbridge-tool-cache", runtimepermission.EnvironmentID(p.TrustedExecution.ProjectID), "go-mod")
	}
	config["shell_environment_policy.set"] = environment
	return config
}

func (c *Client) taskProfileID(binding RuntimeBinding) string {
	if c.diagnosticTaskPolicy != nil {
		return c.diagnosticTaskPolicy.DiagnosticProfileID()
	}
	if binding.TaskPermissions != nil {
		return binding.TaskPermissions.ProfileID()
	}
	return ""
}
func (c *Client) taskInstructions(binding RuntimeBinding) string {
	if c.diagnosticTaskPolicy != nil {
		return fmt.Sprintf("\nCore diagnostic sandbox: all source and prepared child repositories are read-only. Only %s permits temporary files and caches. Network is disabled. Do not request broader permissions. Report blocked checks truthfully and preserve the original engineering instruction.\n", c.diagnosticTaskPolicy.ScratchRoot)
	}
	if binding.TaskPermissions != nil {
		return binding.TaskPermissions.Instructions()
	}
	return ""
}
