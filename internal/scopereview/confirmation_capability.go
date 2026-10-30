package scopereview

// Confirmation capabilities expose installed handlers, not permission to start.
// Original admission transactions remain the only execution authority.
func (s *Service) ConfirmationReviewCapabilities() (coding, upper bool) {
	if s == nil {
		return false, false
	}
	return s.reviews != nil && s.starter != nil, s.reviews != nil && s.upperStarter != nil
}
