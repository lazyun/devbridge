package artifact

import (
	"context"
	"time"
)

type Kind string

const (
	KindGitDiff      Kind = "git_diff"
	KindAgentMessage Kind = "agent_message"
	KindDiagnostic   Kind = "diagnostic"
)

type Artifact struct {
	ID        string
	RunID     string
	Kind      Kind
	Path      string
	SizeBytes int64
	SHA256    string
	CreatedAt time.Time
}

type Store interface {
	Create(context.Context, Artifact) error
	Get(context.Context, string) (Artifact, error)
	ListByRun(context.Context, string) ([]Artifact, error)
}
