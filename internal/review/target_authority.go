package review

import "github.com/lazyun/devbridge/internal/workset"

// ValidateAdmittedTargets binds an implementation proposal to the exact
// repository/member/origin set admitted for Review. Write scopes are proposals
// for later approval; they need not equal the Review's read-only permissions.
func ValidateAdmittedTargets(admitted, proposed []workset.Target) error {
	if err := workset.ValidateTargets(proposed); err != nil {
		return err
	}
	if err := workset.ValidateTargets(admitted); err != nil || len(admitted) != len(proposed) {
		return ErrTargetStale
	}
	byRepository := make(map[string]workset.Target, len(admitted))
	for _, target := range admitted {
		byRepository[target.RepositoryID] = target
	}
	for _, target := range proposed {
		frozen, ok := byRepository[target.RepositoryID]
		if !ok || frozen.MemberID != target.MemberID || frozen.MemberVersion != target.MemberVersion || frozen.OriginCommit != target.OriginCommit || frozen.OriginRef != target.OriginRef || frozen.DevelopmentCycleID != target.DevelopmentCycleID || frozen.ObservedCommit != target.ObservedCommit || !target.Read {
			return ErrTargetStale
		}
	}
	return nil
}
