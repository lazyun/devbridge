package controlapi

import (
	"errors"
	"github.com/lazyun/devbridge/internal/runanalysis"

	"github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimeselection"
)

func runtimeAuthorityError(err error) string {
	for _, sentinel := range []error{runanalysis.ErrStale, runanalysis.ErrConflict, runanalysis.ErrInvalid, runtimeselection.ErrSourceMissing, runtimeselection.ErrBindingMissing, runtimeselection.ErrBindingConflict, runtimeselection.ErrNotCurrentRevision, runtimeplatform.ErrBindingCorrupt, runtimeplatform.ErrDescriptorMissing, runtimeplatform.ErrQualificationMissing, runtimeplatform.ErrQualificationAmbiguous, runtimeplatform.ErrQualificationInvalidated, runtimeplatform.ErrQualificationNotCurrent, runtimeplatform.ErrIdentityMismatch, runtimeplatform.ErrSourceConflict, formalexecution.ErrMissing, formalexecution.ErrConflict, formalexecution.ErrCorrupt, formalexecution.ErrParentMissing, formalexecution.ErrQualificationRequired} {
		if errors.Is(err, sentinel) {
			return sentinel.Error()
		}
	}
	return ""
}
