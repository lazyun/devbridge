package execution

// ExecutionCancellationAvailable observes the same live-session predicate used
// by CancelExecution before durable admission. It never creates an intent,
// dispatches an interrupt, or adopts a successor Run.
func (s *RuntimeSupervisor) ExecutionCancellationAvailable(runID string) bool {
	if s == nil || s.coordinator == nil || s.coordinator.Cancellations == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[runID]
	return s.accepting && session != nil && session.currentRunID() == runID
}

// NoBackendCancellationAvailable observes whether the canonical cancellation
// service can admit a no-backend owner. It does not assert that an arbitrary Run
// is cancellable: the durable owner-specific eligibility reader proves that.
func (s *RuntimeSupervisor) NoBackendCancellationAvailable() bool {
	return s != nil && s.coordinator != nil && s.coordinator.Cancellations != nil
}
