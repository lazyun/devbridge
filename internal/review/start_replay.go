package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

func (s *Service) ReplayAdmittedStart(ctx context.Context, projectID, taskID, areaID, requestID, payloadHash string) (StartAdmission, bool, error) {
	digest, err := hex.DecodeString(payloadHash)
	if requestID == "" || len(requestID) > 128 || err != nil || len(digest) != sha256.Size {
		return StartAdmission{}, false, ErrInvalidArgument
	}
	reader, ok := s.store.(interface {
		ReplayAdmittedStart(context.Context, string, string, string, string, string) (StartAdmission, bool, error)
	})
	if !ok {
		return StartAdmission{}, false, errors.New("Review admission replay unavailable")
	}
	return reader.ReplayAdmittedStart(ctx, projectID, taskID, areaID, requestID, payloadHash)
}
