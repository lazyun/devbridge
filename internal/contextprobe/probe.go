// Package contextprobe owns bounded, AI-authored context acquisition. It does
// not interpret, summarize, or otherwise rewrite the instruction.
package contextprobe

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
)

type ScopeKind string

const (
	ScopePaths      ScopeKind = "PATHS"
	ScopeRepository ScopeKind = "REPOSITORY"
)

type Status string

const (
	StatusCreated    Status = "CREATED"
	StatusRunning    Status = "RUNNING"
	StatusSucceeded  Status = "SUCCEEDED"
	StatusNoGuidance Status = "NO_GUIDANCE"
	StatusFailed     Status = "FAILED"
	StatusCanceled   Status = "CANCELED"
)

var (
	ErrNotFound        = errors.New("context probe not found")
	ErrConflict        = errors.New("context probe conflict")
	ErrInvalidArgument = errors.New("invalid context probe argument")
)

type Scope struct {
	Kind  ScopeKind `json:"kind"`
	Paths []string  `json:"paths"`
}

type Probe struct {
	AgentExecutionID       string // Read from the exact associated Run; never independently persisted.
	TaskID                 string
	RunID                  string
	RuntimeEpoch           string
	ID                     string
	AISessionID            string
	ProjectID              string
	RepositoryID           string
	CodexConfigID          string
	Scope                  Scope
	CodexInstruction       string
	FinalCodexPrompt       string
	PromptSHA256           string
	PromptBytes            int64
	PromptSentAt           *time.Time
	Status                 Status
	ThreadID               string
	TurnID                 string
	StartedAt              *time.Time
	FinishedAt             *time.Time
	TokenUsage             run.TokenUsageAccounting
	Result                 string
	ResultArtifactRef      string
	FailureMessage         string
	Canonical              *InspectionConfirmationPlan
	CanonicalRequestID     string
	CanonicalPayloadSHA256 string
	PrimaryMemberID        string
	WorkspaceID            string
	RuntimeWorksetJSON     string
	RuntimeWorksetSHA256   string
	ReportSHA256           string
	ResultSHA256           string
	RuntimeLimitation      string
	RecoverySuccessorRunID string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func clone(value Probe) Probe {
	value.Scope.Paths = append([]string(nil), value.Scope.Paths...)
	if value.Canonical != nil {
		v := *value.Canonical
		v.Targets = append([]CanonicalTarget(nil), value.Canonical.Targets...)
		for i := range v.Targets {
			v.Targets[i].Scope.Paths = append([]string(nil), v.Targets[i].Scope.Paths...)
		}
		value.Canonical = &v
	}
	if value.StartedAt != nil {
		v := *value.StartedAt
		value.StartedAt = &v
	}
	if value.FinishedAt != nil {
		v := *value.FinishedAt
		value.FinishedAt = &v
	}
	if value.PromptSentAt != nil {
		v := *value.PromptSentAt
		value.PromptSentAt = &v
	}
	if value.TokenUsage.Usage != nil {
		v := *value.TokenUsage.Usage
		value.TokenUsage.Usage = &v
	}
	if value.TokenUsage.ModelContextWindow != nil {
		v := *value.TokenUsage.ModelContextWindow
		value.TokenUsage.ModelContextWindow = &v
	}
	return value
}

type Store interface {
	Create(context.Context, Probe) error
	Get(context.Context, string) (Probe, error)
	List(context.Context) ([]Probe, error)
	RecordPrompt(context.Context, string, string, time.Time) (Probe, error)
	Update(context.Context, string, func(*Probe) error) (Probe, error)
	RecoverRunning(context.Context, time.Time) error
}

// ValidateExecution proves the durable Inspection Task → Execution → Run
// ownership immediately before a backend handoff. Compatibility stores without
// this capability remain readable but cannot authorize agent work.
func (s *Service) ValidateExecution(ctx context.Context, value Probe) error {
	validator, ok := s.store.(interface {
		ValidateInspectionExecution(context.Context, Probe) error
	})
	if !ok {
		return fmt.Errorf("%w: durable Inspection execution validator required", ErrConflict)
	}
	if value.TaskID == "" || value.RunID == "" || value.RuntimeEpoch == "" || value.ProjectID == "" || value.Status != StatusRunning {
		return fmt.Errorf("%w: incomplete Inspection execution identity", ErrConflict)
	}
	return validator.ValidateInspectionExecution(ctx, value)
}

type SessionLookup interface {
	Get(context.Context, string) (aisession.Session, error)
}
type RepositoryLookup interface {
	Get(context.Context, string) (repository.Repository, error)
}
type ConfigLookup interface {
	Get(context.Context, string) (codexconfig.Config, error)
}
type ProjectContexts interface {
	EnsureRequired(context.Context, string) (projectcontext.ProjectContext, error)
	MarkRunning(context.Context, string, string) (projectcontext.ProjectContext, error)
	MarkFinished(context.Context, string, string, projectcontext.Status, string, string, string) (projectcontext.ProjectContext, error)
}

type CreateInput struct {
	AISessionID      string
	RepositoryID     string
	CodexConfigID    string
	Scope            Scope
	CodexInstruction string
}

type Service struct {
	store        Store
	sessions     SessionLookup
	repositories RepositoryLookup
	configs      ConfigLookup
	contexts     ProjectContexts
	newID        func() (string, error)
	now          func() time.Time
}

func NewService(store Store, sessions SessionLookup, repositories RepositoryLookup, configs ConfigLookup, contexts ProjectContexts, newID func() (string, error), now func() time.Time) (*Service, error) {
	if store == nil || sessions == nil || repositories == nil || configs == nil || contexts == nil {
		return nil, errors.New("context probe dependencies are incomplete")
	}
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, sessions: sessions, repositories: repositories, configs: configs, contexts: contexts, newID: newID, now: now}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Probe, error) {
	if input.AISessionID == "" || input.RepositoryID == "" || input.CodexConfigID == "" {
		return Probe{}, ErrInvalidArgument
	}
	if strings.TrimSpace(input.CodexInstruction) == "" || len(input.CodexInstruction) > 64<<10 || strings.ContainsRune(input.CodexInstruction, '\x00') {
		return Probe{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, input.AISessionID)
	if err != nil {
		return Probe{}, err
	}
	repositoryValue, err := s.repositories.Get(ctx, input.RepositoryID)
	if err != nil {
		return Probe{}, err
	}
	if repositoryValue.ProjectID != session.ProjectID {
		return Probe{}, ErrNotFound
	}
	config, err := s.configs.Get(ctx, input.CodexConfigID)
	if err != nil {
		return Probe{}, err
	}
	if !config.Enabled {
		return Probe{}, ErrConflict
	}
	scope, err := validateScope(repositoryValue.Path, input.Scope)
	if err != nil {
		return Probe{}, err
	}
	if _, err := s.contexts.EnsureRequired(ctx, session.ProjectID); err != nil {
		return Probe{}, err
	}
	id, err := s.newID()
	if err != nil {
		return Probe{}, fmt.Errorf("allocate context probe id: %w", err)
	}
	now := s.now().UTC()
	value := Probe{TaskID: "work_" + id, ID: id, AISessionID: session.ID, ProjectID: session.ProjectID, RepositoryID: repositoryValue.ID, CodexConfigID: config.ID, Scope: scope, CodexInstruction: input.CodexInstruction, Status: StatusCreated, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Create(ctx, value); err != nil {
		return Probe{}, fmt.Errorf("store context probe: %w", err)
	}
	return clone(value), nil
}

func validateScope(repositoryRoot string, scope Scope) (Scope, error) {
	if scope.Kind == ScopeRepository {
		if len(scope.Paths) != 0 {
			return Scope{}, ErrInvalidArgument
		}
		return Scope{Kind: ScopeRepository, Paths: []string{}}, nil
	}
	if scope.Kind != ScopePaths || len(scope.Paths) == 0 || len(scope.Paths) > 128 {
		return Scope{}, ErrInvalidArgument
	}
	root, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return Scope{}, fmt.Errorf("canonicalize probe repository: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return Scope{}, err
	}
	seen := map[string]struct{}{}
	result := Scope{Kind: ScopePaths, Paths: make([]string, 0, len(scope.Paths))}
	for _, supplied := range scope.Paths {
		if supplied == "" || len(supplied) > 4096 || filepath.IsAbs(supplied) || strings.ContainsRune(supplied, '\x00') || strings.Contains(supplied, "\\") {
			return Scope{}, ErrInvalidArgument
		}
		for _, component := range strings.Split(supplied, "/") {
			if component == ".." {
				return Scope{}, ErrInvalidArgument
			}
		}
		clean := filepath.Clean(filepath.FromSlash(supplied))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return Scope{}, ErrInvalidArgument
		}
		target, err := filepath.EvalSymlinks(filepath.Join(root, clean))
		if err != nil {
			return Scope{}, ErrInvalidArgument
		}
		target, err = filepath.Abs(target)
		if err != nil {
			return Scope{}, err
		}
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return Scope{}, ErrInvalidArgument
		}
		info, err := os.Stat(target)
		if err != nil || (!info.Mode().IsRegular() && !info.IsDir()) {
			return Scope{}, ErrInvalidArgument
		}
		normalized := filepath.ToSlash(relative)
		if _, ok := seen[normalized]; ok {
			return Scope{}, ErrInvalidArgument
		}
		seen[normalized] = struct{}{}
		result.Paths = append(result.Paths, normalized)
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, id string) (Probe, error) { return s.store.Get(ctx, id) }

func (s *Service) GetForSession(ctx context.Context, sessionID, id string) (Probe, error) {
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return Probe{}, err
	}
	value, err := s.store.Get(ctx, id)
	if err != nil {
		return Probe{}, err
	}
	if value.ProjectID != session.ProjectID {
		return Probe{}, ErrNotFound
	}
	return value, nil
}

func (s *Service) ListForSession(ctx context.Context, sessionID string) ([]Probe, error) {
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	values, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Probe, 0)
	for _, value := range values {
		if value.ProjectID == session.ProjectID {
			result = append(result, value)
		}
	}
	return result, nil
}

// ListByProject is a read-only control-plane projection for local operator
// surfaces. It does not require or create an AI session.
func (s *Service) ListByProject(ctx context.Context, projectID string) ([]Probe, error) {
	if projectID == "" {
		return nil, ErrInvalidArgument
	}
	values, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Probe, 0)
	for _, value := range values {
		if value.ProjectID == projectID {
			result = append(result, value)
		}
	}
	return result, nil
}

func (s *Service) Start(ctx context.Context, id string) (Probe, error) {
	value, err := s.store.Get(ctx, id)
	if err != nil {
		return Probe{}, err
	}
	if value.Status != StatusCreated {
		return Probe{}, ErrConflict
	}
	repositoryValue, err := s.repositories.Get(ctx, value.RepositoryID)
	if err != nil {
		return Probe{}, err
	}
	validated, err := validateScope(repositoryValue.Path, value.Scope)
	if err != nil || !sameScope(validated, value.Scope) {
		return Probe{}, ErrInvalidArgument
	}
	if _, err := s.contexts.MarkRunning(ctx, value.ProjectID, value.ID); err != nil {
		return Probe{}, err
	}
	now := s.now().UTC()
	return s.store.Update(ctx, id, func(current *Probe) error {
		if current.Status != StatusCreated {
			return ErrConflict
		}
		current.Status, current.StartedAt, current.UpdatedAt = StatusRunning, &now, now
		return nil
	})
}

func sameScope(left, right Scope) bool {
	if left.Kind != right.Kind || len(left.Paths) != len(right.Paths) {
		return false
	}
	for index := range left.Paths {
		if left.Paths[index] != right.Paths[index] {
			return false
		}
	}
	return true
}

func (s *Service) AttachThread(ctx context.Context, id, threadID string) (Probe, error) {
	return s.store.Update(ctx, id, func(value *Probe) error {
		if value.Status != StatusRunning || threadID == "" {
			return ErrConflict
		}
		if value.ThreadID != "" && value.ThreadID != threadID {
			return ErrConflict
		}
		value.ThreadID, value.UpdatedAt = threadID, s.now().UTC()
		return nil
	})
}

func (s *Service) AttachTurn(ctx context.Context, id, turnID string) (Probe, error) {
	return s.store.Update(ctx, id, func(value *Probe) error {
		if value.Status != StatusRunning || turnID == "" {
			return ErrConflict
		}
		if value.TurnID != "" && value.TurnID != turnID {
			return ErrConflict
		}
		value.TurnID, value.UpdatedAt = turnID, s.now().UTC()
		return nil
	})
}

// RecordPrompt persists the exact Context Probe bytes immediately before the
// backend turn. Repository scope remains transport/runtime metadata and must
// never be rendered into this natural-language instruction.
func (s *Service) RecordPrompt(ctx context.Context, id, prompt string, sentAt time.Time) (Probe, error) {
	if id == "" || prompt == "" || sentAt.IsZero() || len(prompt) > 64<<10 {
		return Probe{}, ErrInvalidArgument
	}
	current, err := s.store.Get(ctx, id)
	if err != nil {
		return Probe{}, err
	}
	if current.Status != StatusRunning || current.CodexInstruction != prompt {
		return Probe{}, ErrConflict
	}
	value, err := s.store.RecordPrompt(ctx, id, prompt, sentAt.UTC())
	if err == nil {
		logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: value.AISessionID, TaskID: value.TaskID, ExecutionID: value.AgentExecutionID, RunID: value.RunID})
		observability.Info(logCtx, "contextprobe", "context_probe.prompt.persisted", "exact local Context Probe prompt persisted", map[string]any{"contextProbeId": value.ID, "prompt": observability.Fingerprint(prompt)})
	}
	return value, err
}

func setPrompt(value *Probe, prompt string, sentAt time.Time) error {
	if value.Status != StatusRunning {
		return ErrConflict
	}
	if value.FinalCodexPrompt != "" {
		if value.FinalCodexPrompt == prompt {
			return nil
		}
		return ErrConflict
	}
	sum := sha256.Sum256([]byte(prompt))
	at := sentAt.UTC()
	value.FinalCodexPrompt = prompt
	value.PromptSHA256 = hex.EncodeToString(sum[:])
	value.PromptBytes = int64(len([]byte(prompt)))
	value.PromptSentAt = &at
	value.UpdatedAt = at
	return nil
}

func (s *Service) Succeed(ctx context.Context, id, result, artifactRef, identity string, usage *run.TokenUsageObservation) (Probe, error) {
	return s.finish(ctx, id, StatusSucceeded, result, artifactRef, "", identity, usage)
}

func (s *Service) NoGuidance(ctx context.Context, id, result, identity string, usage *run.TokenUsageObservation) (Probe, error) {
	return s.finish(ctx, id, StatusNoGuidance, result, "", "", identity, usage)
}

func (s *Service) Fail(ctx context.Context, id, message string, canceled bool, usage *run.TokenUsageObservation) (Probe, error) {
	status := StatusFailed
	if canceled {
		status = StatusCanceled
	}
	return s.finish(ctx, id, status, "", "", message, "", usage)
}

func (s *Service) finish(ctx context.Context, id string, status Status, result, artifactRef, failure, identity string, usage *run.TokenUsageObservation) (Probe, error) {
	now := s.now().UTC()
	value, err := s.store.Update(context.WithoutCancel(ctx), id, func(value *Probe) error {
		if value.Status != StatusRunning {
			return ErrConflict
		}
		value.Status, value.FinishedAt, value.UpdatedAt = status, &now, now
		value.Result, value.ResultArtifactRef, value.FailureMessage = result, artifactRef, failure
		value.TokenUsage = tokenAccounting(usage)
		return nil
	})
	if err != nil {
		return value, err
	}
	contextStatus := projectcontext.StatusReady
	if status == StatusNoGuidance {
		contextStatus = projectcontext.StatusNoGuidance
	}
	if status == StatusFailed || status == StatusCanceled {
		contextStatus = projectcontext.StatusFailed
	}
	_, err = s.contexts.MarkFinished(context.WithoutCancel(ctx), value.ProjectID, value.ID, contextStatus, result, artifactRef, identity)
	return value, err
}

func tokenAccounting(observation *run.TokenUsageObservation) run.TokenUsageAccounting {
	value := run.TokenUsageAccounting{Status: run.TokenUsageUnavailable}
	if observation == nil || !observation.Reliable {
		return value
	}
	delta, ok := subtract(observation.EndCumulative, observation.StartCumulative)
	if !ok {
		return value
	}
	value.Status, value.Usage = run.TokenUsageAvailable, &delta
	if observation.ModelContextWindow != nil {
		v := *observation.ModelContextWindow
		value.ModelContextWindow = &v
	}
	return value
}

func subtract(end, start run.TokenCounts) (run.TokenCounts, bool) {
	if end.InputTokens < start.InputTokens || end.CachedInputTokens < start.CachedInputTokens || end.CacheWriteInputTokens < start.CacheWriteInputTokens || end.OutputTokens < start.OutputTokens || end.ReasoningOutputTokens < start.ReasoningOutputTokens || end.TotalTokens < start.TotalTokens {
		return run.TokenCounts{}, false
	}
	return run.TokenCounts{InputTokens: end.InputTokens - start.InputTokens, CachedInputTokens: end.CachedInputTokens - start.CachedInputTokens, CacheWriteInputTokens: end.CacheWriteInputTokens - start.CacheWriteInputTokens, OutputTokens: end.OutputTokens - start.OutputTokens, ReasoningOutputTokens: end.ReasoningOutputTokens - start.ReasoningOutputTokens, TotalTokens: end.TotalTokens - start.TotalTokens}, true
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "context_probe_" + hex.EncodeToString(raw[:]), nil
}
