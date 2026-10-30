package runtimepreparation

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/lazyun/devbridge/internal/workset"
)

// WorkspaceObservation is fresh Git and filesystem evidence captured only
// after the exact Run and durable external-preparation claim exist.
type WorkspaceObservation struct {
	ID             string
	PlanID         string
	RunID          string
	WorkspaceID    string
	RepositoryID   string
	MemberID       string
	WorktreeID     string
	CanonicalRoot  string
	RootIdentity   string
	StartHEAD      string
	StartSnapshot  string
	Branch         string
	MetadataDigest string
	Generation     int
	ObservedAt     time.Time
}

func (o WorkspaceObservation) Validate() error {
	if o.ID == "" || o.PlanID == "" || o.RunID == "" || o.WorkspaceID == "" || o.RepositoryID == "" || o.MemberID == "" || o.WorktreeID == "" || o.RootIdentity == "" || o.Branch == "" || len(o.MetadataDigest) != 64 || o.Generation < 1 || o.ObservedAt.IsZero() || !workset.ValidCommit(o.StartHEAD) || !workset.ValidCommit(o.StartSnapshot) || !filepath.IsAbs(o.CanonicalRoot) || filepath.Clean(o.CanonicalRoot) != o.CanonicalRoot {
		return errors.New("fresh workspace observation identity incomplete")
	}
	return nil
}
