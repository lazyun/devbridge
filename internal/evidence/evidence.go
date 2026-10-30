package evidence

import (
	"context"
	"time"
)

type AgentClaim struct {
	Message string
}

type GitEvidence struct {
	RunID          string
	BaseCommit     string
	HeadCommit     string
	StartSnapshot  string
	EndSnapshot    string
	Dirty          bool
	ChangedFiles   []string
	Diff           string
	DiffArtifactID string
	CreatedAt      time.Time
}

type Store interface {
	Save(context.Context, GitEvidence) error
	Get(context.Context, string) (GitEvidence, error)
}

type ArtifactKind string

const (
	ArtifactAgentMessage ArtifactKind = "AGENT_MESSAGE"
	ArtifactGitDiff      ArtifactKind = "GIT_DIFF"
)

type Artifact struct {
	Kind    ArtifactKind
	Content string
}
