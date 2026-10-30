package controlapi

import "github.com/lazyun/devbridge/internal/confirmation"

// WithApprovedStartConfirmationStore supplies a test persistence decorator;
// the copied Facade retains every production dependency and authorization path.
func WithApprovedStartConfirmationStore(f *Facade, store confirmation.Store) *Facade {
	copy := *f
	copy.deps.Confirmations = store
	return &copy
}
