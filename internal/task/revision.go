package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/workset"
	"time"
)

// Definition is the canonical authored engineering intent. Runtime lifecycle,
// organization-only WorkKind, and runtime results are excluded.
type Definition struct {
	RepositoryTargets []workset.Target `json:"repositoryTargets,omitempty"`
	// PrimaryMemberID is the approved runtime cwd layout for v2 Coding. It does
	// not select or narrow the repository target set.
	PrimaryMemberID string `json:"primaryMemberId,omitempty"`
	// Only acceptance successors set this marker. Missing means the legacy
	// trim/path normalization contract; 2 preserves newly approved text bytes.
	ContinuationNormalizationVersion int `json:"continuationNormalizationVersion,omitempty"`

	ReviewApprovalID string           `json:"reviewApprovalId,omitempty"`
	Version          int              `json:"version"`
	ProjectID        string           `json:"projectId"`
	FeatureAreaID    string           `json:"featureAreaId"`
	ModuleID         string           `json:"moduleId"`
	RepositoryID     string           `json:"repositoryId"`
	CodexConfigID    string           `json:"codexConfigId"`
	Model            string           `json:"model"`
	ReasoningEffort  string           `json:"reasoningEffort"`
	ProblemStatement string           `json:"problemStatement"`
	BaseCommit       string           `json:"baseCommit"`
	WorkSpec         WorkSpec         `json:"workSpec"`
	VerificationSpec VerificationSpec `json:"verificationSpec"`
	Goals            []DefinitionGoal `json:"goals"`
}

type DefinitionGoal struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	Title    string `json:"title"`
}

type DefinitionRevision struct {
	InstructionSource *InstructionSource `json:"instructionSource,omitempty"`
	ID                string
	TaskID            string
	Ordinal           int
	Definition        Definition
	PayloadSHA256     string
	CreatedAt         time.Time
}

func DefinitionFromTask(value Task, goals []Goal) Definition {
	result := Definition{ReviewApprovalID: value.ReviewApprovalID, Version: 1, ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, ModuleID: value.ModuleID, RepositoryID: value.RepositoryID, CodexConfigID: value.CodexConfigID, Model: value.Model, ReasoningEffort: value.ReasoningEffort, ProblemStatement: value.ProblemStatement, BaseCommit: value.BaseCommit, WorkSpec: CloneWorkSpec(value.WorkSpec), VerificationSpec: CloneVerificationSpec(value.VerificationSpec), Goals: make([]DefinitionGoal, 0, len(goals))}
	if len(value.RepositoryTargets) > 0 {
		result.Version = 2
		result.RepositoryID, result.BaseCommit = "", ""
		result.RepositoryTargets = workset.CloneTargets(value.RepositoryTargets)
		result.PrimaryMemberID = value.PrimaryMemberID
	}
	for _, goal := range goals {
		result.Goals = append(result.Goals, DefinitionGoal{ID: goal.ID, Position: goal.Position, Title: goal.Title})
	}
	return result
}

// ApplyTo overlays immutable semantic fields while retaining the Task's factual
// lifecycle and identity. An Execution's prepared scope remains its own snapshot.
func (d Definition) ApplyTo(value Task) Task {
	value.RepositoryTargets = workset.CloneTargets(d.RepositoryTargets)
	value.PrimaryMemberID = d.PrimaryMemberID
	value.ProjectID, value.FeatureAreaID, value.ModuleID, value.RepositoryID = d.ProjectID, d.FeatureAreaID, d.ModuleID, d.RepositoryID
	value.CodexConfigID, value.Model, value.ReasoningEffort = d.CodexConfigID, d.Model, d.ReasoningEffort
	value.ProblemStatement, value.BaseCommit = d.ProblemStatement, d.BaseCommit
	value.WorkSpec, value.VerificationSpec = CloneWorkSpec(d.WorkSpec), CloneVerificationSpec(d.VerificationSpec)
	value.Goal, value.Constraints = value.WorkSpec.Goal, append([]string(nil), value.WorkSpec.Constraints...)
	return value
}

func EncodeDefinition(value Definition) (string, string, error) {
	if value.Version != 1 && value.Version != 2 {
		return "", "", errors.New("unsupported task definition version")
	}
	if err := ValidateDefinitionTargets(value); err != nil {
		return "", "", err
	}
	// Normalize nil ordered collections, preserving authored ordering and bytes.
	value.WorkSpec = CloneWorkSpec(value.WorkSpec)
	value.VerificationSpec = CloneVerificationSpec(value.VerificationSpec)
	if value.Goals == nil {
		value.Goals = []DefinitionGoal{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(encoded)
	return string(encoded), hex.EncodeToString(sum[:]), nil
}

func DecodeDefinition(raw, expectedHash string) (Definition, error) {
	sum := sha256.Sum256([]byte(raw))
	if hex.EncodeToString(sum[:]) != expectedHash {
		return Definition{}, errors.New("task definition hash mismatch")
	}
	var value Definition
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return value, err
	}
	if value.Version != 1 && value.Version != 2 {
		return Definition{}, errors.New("unsupported task definition version")
	}
	if err := ValidateDefinitionTargets(value); err != nil {
		return Definition{}, err
	}
	return value, nil
}

func (s *Service) GetDefinitionRevision(ctx context.Context, id string) (DefinitionRevision, error) {
	store, ok := s.store.(interface {
		GetDefinitionRevision(context.Context, string) (DefinitionRevision, error)
	})
	if !ok {
		return DefinitionRevision{}, errors.New("task definition revision store unavailable")
	}
	return store.GetDefinitionRevision(ctx, id)
}
