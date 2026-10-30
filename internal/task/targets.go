package task

import (
	"errors"
	"path"
	"strings"

	"github.com/lazyun/devbridge/internal/workset"
)

// ErrRepositoryTargetsUnsupported blocks adapters that cannot enforce a complete frozen set.
var ErrRepositoryTargetsUnsupported = errors.New("REPOSITORY_TARGETS_UNSUPPORTED: exact multi-repository authorization is not supported by this adapter")

// ValidateDefinitionTargets keeps old bytes interpretable while preventing
// either encoding from carrying a second, conflicting authorization source.
func ValidateDefinitionTargets(d Definition) error {
	if d.Version == 1 {
		if len(d.RepositoryTargets) != 0 || d.PrimaryMemberID != "" || d.WorkSpec.ExecutionScope.Kind == ExecutionScopeTargets {
			return errors.New("v1 definition cannot carry repository target authority")
		}
		for _, check := range d.VerificationSpec.Checks {
			if check.RepositoryID != "" || check.WorkingDirectory != "" || len(check.RepositoryIDs) != 0 {
				return errors.New("repository-qualified verification requires v2 definition")
			}
		}
		return nil
	}
	if d.Version != 2 {
		return errors.New("unsupported task definition version")
	}
	if d.RepositoryID != "" || d.BaseCommit != "" {
		return errors.New("v2 definition must use repositoryTargets exclusively")
	}
	if err := workset.ValidateTargets(d.RepositoryTargets); err != nil {
		return err
	}
	if d.PrimaryMemberID != "" {
		found := false
		for _, target := range d.RepositoryTargets {
			found = found || target.MemberID == d.PrimaryMemberID
		}
		if !found {
			return errors.New("v2 primary member must belong to repositoryTargets")
		}
	}
	if d.WorkSpec.ExecutionScope.Kind != ExecutionScopeTargets || len(d.WorkSpec.ExecutionScope.Paths) != 0 {
		return errors.New("v2 scope must reference repositoryTargets exclusively")
	}
	if err := ValidateTargetWorkSpec(d.WorkSpec); err != nil {
		return err
	}
	return ValidateTargetVerification(d.VerificationSpec, d.RepositoryTargets)
}

// ValidateExecutablePrimary applies only when a definition is proposed for a
// new Coding approval or execution. Keeping it separate from
// ValidateDefinitionTargets preserves the ability to decode historical v2
// definition bytes that predate the runtime-layout field; those definitions
// remain interpretable but cannot be started as multi-repository Coding work.
func ValidateExecutablePrimary(d Definition) error {
	if d.Version != 2 || d.WorkSpec.ImplementationMode != ImplementationCodingAgent || len(d.RepositoryTargets) < 2 {
		return nil
	}
	if d.PrimaryMemberID == "" {
		return errors.New("RUNTIME_WORKSET_INCOMPLETE: multi-repository Coding requires explicit primaryMemberId")
	}
	for _, target := range d.RepositoryTargets {
		if target.MemberID == d.PrimaryMemberID {
			return nil
		}
	}
	return errors.New("RUNTIME_WORKSET_INCOMPLETE: primaryMemberId is outside the frozen repository target set")
}

// ValidateTargetVerification binds each check's execution root and applicable
// repositories to the exact approved set. A check for app alone cannot stand
// in for required coverage of sdk.
func ValidateTargetVerification(v VerificationSpec, targets []workset.Target) error {
	if err := ValidateVerificationSpec(v); err != nil {
		return err
	}
	if v.Mode == VerificationModeNone {
		return nil
	}
	allowed, covered := map[string]bool{}, map[string]bool{}
	for _, target := range targets {
		allowed[target.RepositoryID] = true
	}
	for _, check := range v.Checks {
		if !allowed[check.RepositoryID] || check.WorkingDirectory == "" || len(check.RepositoryIDs) == 0 {
			return errors.New("verification requires an authorized repository, execution directory and applicable repository set")
		}
		dir := check.WorkingDirectory
		if path.Clean(dir) != dir || strings.HasPrefix(dir, "/") || dir == ".." || strings.HasPrefix(dir, "../") || strings.ContainsAny(dir, "\\\x00") {
			return errors.New("verification directory must be repository-relative")
		}
		seen := map[string]bool{}
		for _, id := range check.RepositoryIDs {
			if !allowed[id] || seen[id] {
				return errors.New("verification references an unauthorized or duplicate repository")
			}
			seen[id] = true
			if check.Required {
				covered[id] = true
			}
		}
		if !seen[check.RepositoryID] {
			return errors.New("verification root must belong to its applicable repository set")
		}
	}
	for id := range allowed {
		if !covered[id] {
			return errors.New("required verification does not cover the complete repository target set")
		}
	}
	return nil
}

// ValidateTargetWorkSpec validates authored content without interpreting the
// compatibility scope as repository authority.
func ValidateTargetWorkSpec(value WorkSpec) error {
	if value.ExecutionScope.Kind != ExecutionScopeTargets || len(value.ExecutionScope.Paths) != 0 {
		return errors.New("repository-target work requires REPOSITORY_TARGETS scope")
	}
	value.ExecutionScope = ExecutionScope{Kind: ExecutionScopeRepository}
	return ValidateWorkSpec(value)
}
