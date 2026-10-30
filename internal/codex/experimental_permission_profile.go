package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lazyun/devbridge/internal/codex/wire"
	"github.com/lazyun/devbridge/internal/workset"
)

// ExperimentalPermissionBinding binds one frozen workset target to the exact
// prepared root that a future experimental adapter would use.  It is a pure
// planning input: it neither writes a Codex profile nor starts a client.
type ExperimentalPermissionBinding struct {
	RepositoryID  string
	MemberID      string
	MemberVersion int
	Root          string
}

// ExperimentalPermissionPlan records the complete authority a named Codex
// permissions profile would need.  ProfileID is sent on the experimental
// wire; the profile's filesystem rules must be independently installed and
// evidenced before this plan can ever be used for production admission.
//
// ScratchRoot and AuditMetadata deliberately stay in the plan rather than being
// smuggled into prompt text or an untyped config map.  The generated wire has
// no per-request profile-definition field for either.
type ExperimentalPermissionPlan struct {
	profileID             string
	workspaceID           string
	worksetFingerprint    string
	cwd                   string
	runtimeWorkspaceRoots []string
	scratchRoot           string
	// This planner performs no filesystem inspection. Consumers must treat this
	// as a declaration, never as evidence of a resolved root identity.
	rootIdentityStatus string
	members            []ExperimentalPermissionMember
	auditMetadata      map[string]string
	seal               string
}

type ExperimentalPermissionMember struct {
	RepositoryID  string
	MemberID      string
	MemberVersion int
	OriginCommit  string
	Root          string
	Read          bool
	Write         workset.WriteScope
}

// BuildExperimentalPermissionPlan derives all repository authority from the
// frozen workset.  Bindings cannot introduce members, paths, or wider write
// scopes.  This is intentionally not connected to Client.StartThread,
// ResumeThread, or StartTurn.
func BuildExperimentalPermissionPlan(set workset.Set, profileID, cwd, scratchRoot string, bindings []ExperimentalPermissionBinding, metadata map[string]string) (ExperimentalPermissionPlan, error) {
	if err := set.Validate(); err != nil {
		return ExperimentalPermissionPlan{}, err
	}
	if strings.TrimSpace(profileID) == "" {
		return ExperimentalPermissionPlan{}, errors.New("experimental permission profile id is required")
	}
	if err := validatePlanPath("cwd", cwd); err != nil {
		return ExperimentalPermissionPlan{}, err
	}
	if err := validatePlanPath("scratch root", scratchRoot); err != nil {
		return ExperimentalPermissionPlan{}, err
	}
	if len(bindings) != len(set.Targets) {
		return ExperimentalPermissionPlan{}, errors.New("experimental permission bindings must cover the exact workset")
	}

	targets := make(map[string]workset.Target, len(set.Targets))
	for _, target := range set.Targets {
		targets[target.MemberID] = target
	}
	seenRoots := map[string]bool{}
	members := make([]ExperimentalPermissionMember, 0, len(bindings))
	cwdIncluded := false
	for _, binding := range bindings {
		target, ok := targets[binding.MemberID]
		if !ok || target.RepositoryID != binding.RepositoryID || target.MemberVersion != binding.MemberVersion {
			return ExperimentalPermissionPlan{}, fmt.Errorf("experimental permission binding does not match frozen member %q", binding.MemberID)
		}
		if err := validatePlanPath("member root", binding.Root); err != nil {
			return ExperimentalPermissionPlan{}, err
		}
		root := filepath.Clean(binding.Root)
		if seenRoots[root] {
			return ExperimentalPermissionPlan{}, errors.New("experimental permission member roots must be distinct")
		}
		if overlapsPath(root, scratchRoot) {
			return ExperimentalPermissionPlan{}, errors.New("experimental scratch root must not overlap a repository root")
		}
		for existing := range seenRoots {
			if overlapsPath(root, existing) {
				return ExperimentalPermissionPlan{}, errors.New("experimental permission member roots must not overlap")
			}
		}
		seenRoots[root] = true
		cwdIncluded = cwdIncluded || filepath.Clean(cwd) == root
		members = append(members, ExperimentalPermissionMember{RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, OriginCommit: target.OriginCommit, Root: root, Read: target.Read, Write: workset.WriteScope{Kind: target.Write.Kind, Paths: append([]string(nil), target.Write.Paths...)}})
		delete(targets, binding.MemberID)
	}
	if len(targets) != 0 || !cwdIncluded {
		return ExperimentalPermissionPlan{}, errors.New("experimental permission cwd must be an exact member root")
	}
	for key, value := range metadata {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return ExperimentalPermissionPlan{}, errors.New("experimental permission metadata requires nonempty keys and values")
		}
	}
	fingerprint, err := set.Fingerprint()
	if err != nil {
		return ExperimentalPermissionPlan{}, err
	}
	sort.Slice(members, func(i, j int) bool { return members[i].RepositoryID < members[j].RepositoryID })
	roots := make([]string, len(members))
	for i, member := range members {
		roots[i] = member.Root
	}
	plan := ExperimentalPermissionPlan{profileID: profileID, workspaceID: set.WorkspaceID, worksetFingerprint: fingerprint, cwd: filepath.Clean(cwd), runtimeWorkspaceRoots: roots, scratchRoot: filepath.Clean(scratchRoot), rootIdentityStatus: "UNVERIFIED", members: members, auditMetadata: cloneMetadata(metadata)}
	plan.seal, err = plan.integritySeal()
	if err != nil {
		return ExperimentalPermissionPlan{}, err
	}
	return plan, nil
}

func (p ExperimentalPermissionPlan) ThreadStartWire() (wire.ExperimentalThreadStartPermissions, error) {
	if err := p.verify(); err != nil {
		return wire.ExperimentalThreadStartPermissions{}, err
	}
	params := wire.ExperimentalThreadStartPermissions{Permissions: p.profileID, RuntimeWorkspaceRoots: append([]string(nil), p.runtimeWorkspaceRoots...)}
	return params, params.Validate()
}

func (p ExperimentalPermissionPlan) ThreadResumeWire() (wire.ExperimentalThreadResumePermissions, error) {
	if err := p.verify(); err != nil {
		return wire.ExperimentalThreadResumePermissions{}, err
	}
	params := wire.ExperimentalThreadResumePermissions{Permissions: p.profileID, RuntimeWorkspaceRoots: append([]string(nil), p.runtimeWorkspaceRoots...)}
	return params, params.Validate()
}

func (p ExperimentalPermissionPlan) TurnStartWire() (wire.ExperimentalTurnStartPermissions, error) {
	if err := p.verify(); err != nil {
		return wire.ExperimentalTurnStartPermissions{}, err
	}
	params := wire.ExperimentalTurnStartPermissions{Permissions: p.profileID, RuntimeWorkspaceRoots: append([]string(nil), p.runtimeWorkspaceRoots...)}
	return params, params.Validate()
}

func (p ExperimentalPermissionPlan) verify() error {
	if p.seal == "" || p.rootIdentityStatus != "UNVERIFIED" {
		return errors.New("experimental permission plan is not sealed evidence-free authority")
	}
	set := workset.Set{Version: workset.Version, WorkspaceID: p.workspaceID, Targets: p.targets()}
	fingerprint, err := set.Fingerprint()
	if err != nil || fingerprint != p.worksetFingerprint {
		return errors.New("experimental permission plan workset integrity mismatch")
	}
	if _, err := BuildExperimentalPermissionPlan(set, p.profileID, p.cwd, p.scratchRoot, p.bindings(), p.auditMetadata); err != nil {
		return err
	}
	got, err := p.integritySeal()
	if err != nil || got != p.seal {
		return errors.New("experimental permission plan integrity mismatch")
	}
	return nil
}
func (p ExperimentalPermissionPlan) targets() []workset.Target {
	out := make([]workset.Target, len(p.members))
	for i, m := range p.members {
		out[i] = workset.Target{RepositoryID: m.RepositoryID, MemberID: m.MemberID, MemberVersion: m.MemberVersion, OriginCommit: m.OriginCommit, Read: m.Read, Write: workset.WriteScope{Kind: m.Write.Kind, Paths: append([]string(nil), m.Write.Paths...)}}
	}
	return out
}
func (p ExperimentalPermissionPlan) bindings() []ExperimentalPermissionBinding {
	out := make([]ExperimentalPermissionBinding, len(p.members))
	for i, m := range p.members {
		out[i] = ExperimentalPermissionBinding{RepositoryID: m.RepositoryID, MemberID: m.MemberID, MemberVersion: m.MemberVersion, Root: m.Root}
	}
	return out
}
func (p ExperimentalPermissionPlan) integritySeal() (string, error) {
	material := struct {
		ProfileID          string
		WorkspaceID        string
		Fingerprint        string
		CWD                string
		Roots              []string
		Scratch            string
		RootIdentityStatus string
		Members            []ExperimentalPermissionMember
		AuditMetadata      map[string]string
	}{p.profileID, p.workspaceID, p.worksetFingerprint, p.cwd, p.runtimeWorkspaceRoots, p.scratchRoot, p.rootIdentityStatus, p.members, p.auditMetadata}
	raw, err := json.Marshal(material)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func validatePlanPath(name, value string) error {
	if value == "" || value == string(filepath.Separator) || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return fmt.Errorf("experimental permission %s must be a clean absolute path", name)
	}
	return nil
}

func overlapsPath(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func cloneMetadata(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	copy := make(map[string]string, len(value))
	for key, item := range value {
		copy[key] = item
	}
	return copy
}
