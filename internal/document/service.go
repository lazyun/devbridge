package document

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/featurearea"
)

const (
	MaxIDBytes      = 128
	MaxTitleBytes   = 256
	MaxContentBytes = 256 << 10
	MaxDocuments    = 100
)

var (
	ErrNotFound        = errors.New("document not found")
	ErrConflict        = errors.New("document conflict")
	ErrInvalidArgument = errors.New("invalid document argument")
	validIdentifier    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

type Store interface {
	Create(context.Context, Document) error
	Get(context.Context, string) (Document, error)
	ListByProject(context.Context, string, int) ([]Document, error)
}

type SessionLookup interface {
	Get(context.Context, string) (aisession.Session, error)
}

type FeatureAreaLookup interface {
	GetForSession(context.Context, string, string) (featurearea.FeatureArea, error)
}

type CreateInput struct {
	AISessionID string
	FeatureAreaID     string
	Title       string
	Content     string
}

type Service struct {
	store    Store
	sessions SessionLookup
	featureAreas   FeatureAreaLookup
	newID    func() (string, error)
	now      func() time.Time
}

func NewService(store Store, sessions SessionLookup, featureAreas FeatureAreaLookup, newID func() (string, error), now func() time.Time) (*Service, error) {
	if store == nil || sessions == nil || featureAreas == nil {
		return nil, errors.New("document store, AI sessions, and FeatureAreas are required")
	}
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, sessions: sessions, featureAreas: featureAreas, newID: newID, now: now}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Document, error) {
	if !validID(input.AISessionID) || input.FeatureAreaID != "" && !validID(input.FeatureAreaID) || !validText(input.Title, MaxTitleBytes) || !validText(input.Content, MaxContentBytes) {
		return Document{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, input.AISessionID)
	if err != nil {
		return Document{}, ErrNotFound
	}
	if input.FeatureAreaID != "" {
		value, featureAreaErr := s.featureAreas.GetForSession(ctx, input.AISessionID, input.FeatureAreaID)
		if featureAreaErr != nil || value.ProjectID != session.ProjectID {
			return Document{}, ErrNotFound
		}
	}
	id, err := s.newID()
	if err != nil {
		return Document{}, fmt.Errorf("allocate document id: %w", err)
	}
	now := s.now().UTC()
	value := Document{ID: "document_" + strings.TrimPrefix(id, "document_"), ProjectID: session.ProjectID, FeatureAreaID: input.FeatureAreaID, CreatedByAISessionID: session.ID, Title: input.Title, Content: input.Content, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Create(ctx, value); err != nil {
		return Document{}, fmt.Errorf("store document: %w", err)
	}
	logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: input.AISessionID, FeatureAreaID: value.FeatureAreaID})
	observability.Info(logCtx, "document", "document.created", "durable Document created", map[string]any{"documentId": value.ID, "titleBytes": len([]byte(value.Title)), "content": observability.Fingerprint(value.Content)})
	return value, nil
}

func (s *Service) GetForProject(ctx context.Context, projectID, documentID string) (Document, error) {
	if !validID(projectID) || !validID(documentID) {
		return Document{}, ErrInvalidArgument
	}
	value, err := s.store.Get(ctx, documentID)
	if err != nil || value.ProjectID != projectID {
		return Document{}, ErrNotFound
	}
	return value, nil
}

func (s *Service) ListByProject(ctx context.Context, projectID string, limit int) ([]Document, error) {
	if !validID(projectID) || limit < 1 || limit > MaxDocuments {
		return nil, ErrInvalidArgument
	}
	return s.store.ListByProject(ctx, projectID, limit)
}

func validID(value string) bool {
	return value != "" && len(value) <= MaxIDBytes && validIdentifier.MatchString(value)
}

func validText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && len([]byte(value)) <= limit && !strings.ContainsRune(value, '\x00')
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "document_" + hex.EncodeToString(raw[:]), nil
}
