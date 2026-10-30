package mcp

import (
	"encoding/json"
	"time"

	"github.com/lazyun/devbridge/internal/artifact"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

const DTOVersion = 1

type ErrorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type ProjectDTO struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
	RepositoryCount int    `json:"repositoryCount,omitempty"`
}
type RepositoryDTO struct {
	ID            string `json:"id"`
	ProjectID     string `json:"projectId"`
	Name          string `json:"name"`
	CanonicalPath string `json:"canonicalPath"`
}
type TaskDTO struct {
	ID           string   `json:"id"`
	ProjectID    string   `json:"projectId"`
	RepositoryID string   `json:"repositoryId"`
	Goal         string   `json:"goal"`
	Constraints  []string `json:"constraints"`
	BaseCommit   string   `json:"baseCommit"`
	Status       string   `json:"status"`
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
}
type FailureDTO struct {
	Class   string `json:"class"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}
type RunDTO struct {
	ID         string      `json:"id"`
	TaskID     string      `json:"taskId"`
	Attempt    int         `json:"attempt"`
	Status     string      `json:"status"`
	WorktreeID string      `json:"worktreeId,omitempty"`
	ThreadID   string      `json:"threadId,omitempty"`
	StartedAt  *string     `json:"startedAt,omitempty"`
	FinishedAt *string     `json:"finishedAt,omitempty"`
	Failure    *FailureDTO `json:"failure,omitempty"`
}
type ArtifactDTO struct {
	ArtifactID string `json:"artifactId"`
	Kind       string `json:"kind"`
	SizeBytes  int64  `json:"sizeBytes"`
	SHA256     string `json:"sha256"`
}
type EvidenceDTO struct {
	RunID                string       `json:"runId"`
	BaseCommit           string       `json:"baseCommit"`
	HeadCommit           string       `json:"headCommit"`
	Dirty                bool         `json:"dirty"`
	ChangedFiles         []string     `json:"changedFiles"`
	DiffArtifact         *ArtifactDTO `json:"diffArtifact,omitempty"`
	AgentMessageArtifact *ArtifactDTO `json:"agentMessageArtifact,omitempty"`
	CreatedAt            string       `json:"createdAt"`
}
type EventDTO struct {
	Sequence   int64           `json:"sequence"`
	Type       string          `json:"type"`
	EntityType string          `json:"entityType"`
	EntityID   string          `json:"entityId"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  string          `json:"createdAt"`
}

type ProjectListInput struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}
type ProjectGetInput struct {
	ProjectID string `json:"projectId"`
}
type RepositoryListInput struct {
	ProjectID string `json:"projectId"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}
type TaskCreateInput struct {
	ProjectID    string   `json:"projectId"`
	RepositoryID string   `json:"repositoryId"`
	Goal         string   `json:"goal"`
	Constraints  []string `json:"constraints,omitempty"`
	BaseRevision string   `json:"baseRevision"`
}
type TaskGetInput struct {
	TaskID string `json:"taskId"`
}
type TaskListInput struct {
	ProjectID    string `json:"projectId,omitempty"`
	RepositoryID string `json:"repositoryId,omitempty"`
	Status       string `json:"status,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Offset       int    `json:"offset,omitempty"`
}
type RunStartInput struct {
	TaskID string `json:"taskId"`
}
type RunGetInput struct {
	RunID string `json:"runId"`
}
type RunListInput struct {
	TaskID string `json:"taskId,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}
type EvidenceGetInput struct {
	RunID string `json:"runId"`
}
type EventListInput struct {
	ProjectID     string `json:"projectId,omitempty"`
	TaskID        string `json:"taskId,omitempty"`
	RunID         string `json:"runId,omitempty"`
	AfterSequence int64  `json:"afterSequence,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}
type ArtifactReadInput struct {
	ArtifactID string `json:"artifactId"`
	Offset     int64  `json:"offset,omitempty"`
	MaxBytes   int    `json:"maxBytes,omitempty"`
}

type ProjectListOutput struct {
	Version  int          `json:"version"`
	Projects []ProjectDTO `json:"projects,omitempty"`
	Error    *ErrorDTO    `json:"error,omitempty"`
}
type ProjectGetOutput struct {
	Version      int             `json:"version"`
	Project      *ProjectDTO     `json:"project,omitempty"`
	Repositories []RepositoryDTO `json:"repositories,omitempty"`
	Error        *ErrorDTO       `json:"error,omitempty"`
}
type RepositoryListOutput struct {
	Version      int             `json:"version"`
	Repositories []RepositoryDTO `json:"repositories,omitempty"`
	Error        *ErrorDTO       `json:"error,omitempty"`
}
type TaskOutput struct {
	Version int       `json:"version"`
	Task    *TaskDTO  `json:"task,omitempty"`
	Error   *ErrorDTO `json:"error,omitempty"`
}
type TaskListOutput struct {
	Version int       `json:"version"`
	Tasks   []TaskDTO `json:"tasks,omitempty"`
	Error   *ErrorDTO `json:"error,omitempty"`
}
type RunOutput struct {
	Version int       `json:"version"`
	Run     *RunDTO   `json:"run,omitempty"`
	Error   *ErrorDTO `json:"error,omitempty"`
}
type RunListOutput struct {
	Version int       `json:"version"`
	Runs    []RunDTO  `json:"runs,omitempty"`
	Error   *ErrorDTO `json:"error,omitempty"`
}
type EvidenceOutput struct {
	Version  int          `json:"version"`
	Evidence *EvidenceDTO `json:"evidence,omitempty"`
	Error    *ErrorDTO    `json:"error,omitempty"`
}
type EventListOutput struct {
	Version int        `json:"version"`
	Events  []EventDTO `json:"events,omitempty"`
	Error   *ErrorDTO  `json:"error,omitempty"`
}
type ArtifactReadOutput struct {
	Version    int          `json:"version"`
	Artifact   *ArtifactDTO `json:"artifact,omitempty"`
	Offset     int64        `json:"offset,omitempty"`
	NextOffset int64        `json:"nextOffset,omitempty"`
	More       bool         `json:"more"`
	Encoding   string       `json:"encoding,omitempty"`
	Data       string       `json:"data,omitempty"`
	Error      *ErrorDTO    `json:"error,omitempty"`
}

func projectDTO(value project.Project, count int) ProjectDTO {
	return ProjectDTO{ID: value.ID, Name: value.Name, CreatedAt: timestamp(value.CreatedAt), UpdatedAt: timestamp(value.UpdatedAt), RepositoryCount: count}
}
func repositoryDTO(value repository.Repository) RepositoryDTO {
	return RepositoryDTO{ID: value.ID, ProjectID: value.ProjectID, Name: value.Name, CanonicalPath: value.Path}
}
func taskDTO(value task.Task) TaskDTO {
	return TaskDTO{ID: value.ID, ProjectID: value.ProjectID, RepositoryID: value.RepositoryID, Goal: value.Goal, Constraints: append([]string(nil), value.Constraints...), BaseCommit: value.BaseCommit, Status: string(value.Status), CreatedAt: timestamp(value.CreatedAt), UpdatedAt: timestamp(value.UpdatedAt)}
}
func runDTO(value run.Run) RunDTO {
	result := RunDTO{ID: value.ID, TaskID: value.TaskID, Attempt: value.Attempt, Status: string(value.Status), WorktreeID: value.WorktreeID, ThreadID: value.ThreadID, StartedAt: optionalTimestamp(value.StartedAt), FinishedAt: optionalTimestamp(value.FinishedAt)}
	if value.Failure != nil {
		message := "managed execution failed"
		switch value.Failure.Kind {
		case run.FailureTask:
			message = "task execution failed"
		case run.FailureInfrastructure:
			message = "managed execution infrastructure failed"
		case run.FailureCanceled:
			message = "managed execution was canceled"
		}
		result.Failure = &FailureDTO{Class: string(value.Failure.Kind), Code: value.Failure.Code, Message: message}
	}
	return result
}
func artifactDTO(value artifact.Artifact) ArtifactDTO {
	return ArtifactDTO{ArtifactID: value.ID, Kind: string(value.Kind), SizeBytes: value.SizeBytes, SHA256: value.SHA256}
}
func evidenceDTO(value evidence.GitEvidence, artifacts []artifact.Artifact) EvidenceDTO {
	result := EvidenceDTO{RunID: value.RunID, BaseCommit: value.BaseCommit, HeadCommit: value.HeadCommit, Dirty: value.Dirty, ChangedFiles: append([]string(nil), value.ChangedFiles...), CreatedAt: timestamp(value.CreatedAt)}
	for _, item := range artifacts {
		dto := artifactDTO(item)
		switch item.Kind {
		case artifact.KindGitDiff:
			result.DiffArtifact = &dto
		case artifact.KindAgentMessage:
			result.AgentMessageArtifact = &dto
		}
	}
	return result
}
func eventDTO(value event.Event) EventDTO {
	return EventDTO{Sequence: value.Sequence, Type: value.Type, EntityType: value.EntityType, EntityID: value.EntityID, Payload: append(json.RawMessage(nil), value.Payload...), CreatedAt: timestamp(value.CreatedAt)}
}
func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func optionalTimestamp(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := timestamp(*value)
	return &formatted
}
func errorDTO(err error) *ErrorDTO {
	apiErr := controlapi.MapError(err)
	return &ErrorDTO{Code: string(apiErr.Code), Message: apiErr.Message}
}
