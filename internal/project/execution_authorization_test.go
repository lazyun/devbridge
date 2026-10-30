package project

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memoryAuthorizationStore struct {
	head      map[string]ExecutionAuthorization
	revisions map[[2]string]ExecutionAuthorizationRevision
}

func (s *memoryAuthorizationStore) Get(context.Context, string) (ExecutionAuthorization, error) {
	return ExecutionAuthorization{}, ErrAuthorizationNotFound
}

func (s *memoryAuthorizationStore) GetRevision(context.Context, string, int) (ExecutionAuthorizationRevision, error) {
	return ExecutionAuthorizationRevision{}, ErrAuthorizationNotFound
}

func (s *memoryAuthorizationStore) Apply(_ context.Context, value ExecutionAuthorization, expectedRevision int) (ExecutionAuthorization, error) {
	if expectedRevision != 0 && expectedRevision+1 != value.HeadRevision {
		return ExecutionAuthorization{}, ErrConflict
	}
	return value, nil
}

func TestAuthorizationServiceUpdateValidatesInput(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	service := NewAuthorizationService(&memoryAuthorizationStore{}, func() time.Time { return now })
	if _, err := service.Update(context.Background(), "", 0, true, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("empty project must conflict, got %v", err)
	}
	if _, err := service.Update(context.Background(), "p1", -1, true, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("negative revision must conflict, got %v", err)
	}
	value, err := service.Update(context.Background(), "p1", 3, true, true)
	if err != nil || !value.TrustedExecution || !value.NetworkAccess || value.HeadRevision != 4 {
		t.Fatalf("unexpected update result %+v %v", value, err)
	}
}
