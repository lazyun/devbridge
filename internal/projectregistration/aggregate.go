package projectregistration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var ErrRequestConflict = errors.New("project registration request payload conflict")
var ErrAtomicStoreUnavailable = errors.New("atomic project registration store unavailable")

type RepositoryInput struct {
	Path  string `json:"path"`
	Alias string `json:"alias,omitempty"`
}

// AtomicStore commits the entire resource registration and its replay receipt.
type AtomicStore interface {
	ReplayRegistration(context.Context, string, string) (Registration, bool, error)
	RegisterAggregate(context.Context, Input, string) (Registration, error)
}

func registrationHash(input Input) (string, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (s *Service) registerAggregate(ctx context.Context, input Input) (Registration, error) {
	if s.atomic == nil {
		return Registration{}, ErrAtomicStoreUnavailable
	}
	if strings.TrimSpace(input.RequestID) == "" || len(input.RequestID) > 256 {
		return Registration{}, fmt.Errorf("%w: stable requestId required", ErrInvalidArgument)
	}
	hash, err := registrationHash(input)
	if err != nil {
		return Registration{}, err
	}
	if value, found, err := s.atomic.ReplayRegistration(ctx, input.RequestID, hash); err != nil || found {
		return value, err
	}
	if input.ProjectID == "" && strings.TrimSpace(input.Name) == "" {
		return Registration{}, fmt.Errorf("%w: project name required", ErrInvalidArgument)
	}
	if input.ProjectID != "" && input.Name != "" {
		return Registration{}, fmt.Errorf("%w: append does not rename project", ErrInvalidArgument)
	}
	if len(input.Repositories) > 128 || input.RepositoryPath != "" {
		return Registration{}, fmt.Errorf("%w: use bounded repositories collection", ErrInvalidArgument)
	}
	validated := input
	validated.Name = strings.TrimSpace(input.Name)
	validated.Repositories = make([]RepositoryInput, 0, len(input.Repositories))
	paths, aliases := map[string]bool{}, map[string]bool{}
	for _, r := range input.Repositories {
		canonical, e := s.repositories.ResolveRegistrationPath(ctx, r.Path)
		if e != nil {
			return Registration{}, e
		}
		alias := r.Alias
		if alias == "" {
			sum := sha256.Sum256([]byte(canonical))
			alias = filepath.Base(canonical) + "-" + hex.EncodeToString(sum[:6])
		}
		if alias == "." || alias == ".." || len(alias) > 200 || strings.ContainsAny(alias, "/\\\x00") || strings.TrimSpace(alias) != alias {
			return Registration{}, fmt.Errorf("%w: invalid stable repository alias", ErrInvalidArgument)
		}
		if paths[canonical] || aliases[alias] {
			return Registration{}, fmt.Errorf("%w: duplicate repository path or alias", ErrInvalidArgument)
		}
		paths[canonical] = true
		aliases[alias] = true
		validated.Repositories = append(validated.Repositories, RepositoryInput{Path: canonical, Alias: alias})
	}
	return s.atomic.RegisterAggregate(ctx, validated, hash)
}
