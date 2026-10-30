package wire

import (
	"errors"
	"path/filepath"
)

// ExperimentalActivePermissionProfile is the complete permission provenance
// returned by the generated experimental thread/start and thread/resume
// responses.  It intentionally has no profile-content digest: the backend
// reports only the selected id and optional parent id.
type ExperimentalActivePermissionProfile struct {
	ID      string  `json:"id"`
	Extends *string `json:"extends,omitempty"`
}

func (p ExperimentalActivePermissionProfile) Validate() error {
	if p.ID == "" {
		return errors.New("experimental active permission profile id is required")
	}
	return nil
}

// ExperimentalThreadStartPermissions is the permission-bearing subset of the
// 0.149.0 experimental thread/start schema generated in G0.  It intentionally
// remains separate from ThreadStartParams: the production adapter is still
// bound to its older protocol and must not send experimental fields.
//
// The generated schema states that permissions and sandbox are mutually
// exclusive.  Permissions names a preconfigured profile; it does not carry a
// profile definition on the wire.
type ExperimentalThreadStartPermissions struct {
	Permissions           string   `json:"permissions,omitempty"`
	Sandbox               string   `json:"sandbox,omitempty"`
	RuntimeWorkspaceRoots []string `json:"runtimeWorkspaceRoots,omitempty"`
}

func (p ExperimentalThreadStartPermissions) Validate() error {
	if (p.Permissions == "") == (p.Sandbox == "") {
		return errors.New("experimental thread/start requires exactly one of permissions or sandbox")
	}
	if p.Sandbox != "" && p.Sandbox != "read-only" && p.Sandbox != "workspace-write" {
		return errors.New("unsupported experimental thread/start sandbox")
	}
	return validateExperimentalRoots(p.RuntimeWorkspaceRoots)
}

// ExperimentalThreadResumePermissions mirrors the same generated
// thread/resume fields and their mutually exclusive permission paths.
type ExperimentalThreadResumePermissions struct {
	Permissions           string   `json:"permissions,omitempty"`
	Sandbox               string   `json:"sandbox,omitempty"`
	RuntimeWorkspaceRoots []string `json:"runtimeWorkspaceRoots,omitempty"`
}

func (p ExperimentalThreadResumePermissions) Validate() error {
	if (p.Permissions == "") == (p.Sandbox == "") {
		return errors.New("experimental thread/resume requires exactly one of permissions or sandbox")
	}
	if p.Sandbox != "" && p.Sandbox != "read-only" && p.Sandbox != "workspace-write" {
		return errors.New("unsupported experimental thread/resume sandbox")
	}
	return validateExperimentalRoots(p.RuntimeWorkspaceRoots)
}

// ExperimentalTurnStartPermissions mirrors the generated turn/start schema.
// Unlike thread start/resume, its legacy alternative is sandboxPolicy.
type ExperimentalTurnStartPermissions struct {
	Permissions           string                     `json:"permissions,omitempty"`
	SandboxPolicy         *ExperimentalSandboxPolicy `json:"sandboxPolicy,omitempty"`
	RuntimeWorkspaceRoots []string                   `json:"runtimeWorkspaceRoots,omitempty"`
}

func (p ExperimentalTurnStartPermissions) Validate() error {
	if (p.Permissions == "") == (p.SandboxPolicy == nil) {
		return errors.New("experimental turn/start requires exactly one of permissions or sandboxPolicy")
	}
	if p.SandboxPolicy != nil {
		if err := p.SandboxPolicy.Validate(); err != nil {
			return err
		}
	}
	return validateExperimentalRoots(p.RuntimeWorkspaceRoots)
}

// ExperimentalSandboxPolicy models only the generated local readOnly and
// workspaceWrite alternatives. Their networkAccess field is a boolean.
// externalSandbox uses a different enum and is deliberately excluded because
// G0 did not establish it as an admissible enforcement path.
type ExperimentalSandboxPolicy struct {
	Type                string   `json:"type"`
	NetworkAccess       *bool    `json:"networkAccess,omitempty"`
	WritableRoots       []string `json:"writableRoots,omitempty"`
	ExcludeSlashTmp     *bool    `json:"excludeSlashTmp,omitempty"`
	ExcludeTmpdirEnvVar *bool    `json:"excludeTmpdirEnvVar,omitempty"`
}

func (p ExperimentalSandboxPolicy) Validate() error {
	switch p.Type {
	case "readOnly":
		if len(p.WritableRoots) != 0 || p.ExcludeSlashTmp != nil || p.ExcludeTmpdirEnvVar != nil {
			return errors.New("experimental readOnly sandbox cannot carry workspace-write fields")
		}
	case "workspaceWrite":
		if err := validateExperimentalRoots(p.WritableRoots); err != nil {
			return err
		}
	default:
		return errors.New("unsupported experimental sandbox policy type")
	}
	return nil
}

func validateExperimentalRoots(roots []string) error {
	if len(roots) == 0 || len(roots) > 128 {
		return errors.New("experimental runtime workspace roots must be nonempty and bounded")
	}
	seen := make(map[string]bool, len(roots))
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || seen[root] {
			return errors.New("experimental runtime workspace roots must be distinct absolute paths")
		}
		seen[root] = true
	}
	return nil
}
