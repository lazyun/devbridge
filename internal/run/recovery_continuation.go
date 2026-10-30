package run

import "context"

// RecoveryContinuationInstruction reads an independently authorized exact Run
// instruction. It never changes the TaskDefinitionRevision's engineering scope.
func (s *Service) RecoveryContinuationInstruction(ctx context.Context, executionID string) (string, error) {
	reader, ok := s.store.(interface {
		RecoveryContinuationInstruction(context.Context, string) (string, error)
	})
	if !ok {
		return "", nil
	}
	return reader.RecoveryContinuationInstruction(ctx, executionID)
}
