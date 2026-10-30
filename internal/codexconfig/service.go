package codexconfig

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("codex config not found")
	ErrConflict = errors.New("codex config conflict")
)

type Store interface {
	Create(context.Context, Config) error
	Get(context.Context, string) (Config, error)
	List(context.Context) ([]Config, error)
}

type CreateInput struct {
	Name      string
	CodexPath string
	Enabled   bool
}

type Service struct {
	store Store
	newID func() (string, error)
	now   func() time.Time
}

func NewService(store Store, newID func() (string, error), now func() time.Time) *Service {
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, newID: newID, now: now}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Config, error) {
	if s == nil || s.store == nil {
		return Config{}, errors.New("codex config store is required")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Config{}, errors.New("codex config name is required")
	}
	path := strings.TrimSpace(input.CodexPath)
	if path != "" && !filepath.IsAbs(path) {
		return Config{}, errors.New("codex path must be an absolute path")
	}
	id, err := s.newID()
	if err != nil {
		return Config{}, fmt.Errorf("allocate codex config id: %w", err)
	}
	now := s.now().UTC()
	value := Config{ID: id, Name: name, CodexPath: filepath.Clean(path), Enabled: input.Enabled, CreatedAt: now, UpdatedAt: now}
	if path == "" {
		value.CodexPath = ""
	}
	if err := s.store.Create(ctx, value); err != nil {
		return Config{}, fmt.Errorf("store codex config: %w", err)
	}
	return value, nil
}

func (s *Service) Get(ctx context.Context, id string) (Config, error) { return s.store.Get(ctx, id) }
func (s *Service) List(ctx context.Context) ([]Config, error)         { return s.store.List(ctx) }

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "codex_config_" + hex.EncodeToString(raw[:]), nil
}
