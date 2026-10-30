package review

import (
	"errors"
	"strings"
	"time"
)

// RepositoryProvenance records the durable repository state one Feature Area Review
// was based on. It is objective provenance: Bridge derives no staleness,
// validity, or rerun semantics from it. One record exists per Review and per
// Repository the Review is bound to; the current v0.1 Repository model
// authorizes exactly one Repository per Review, and the record shape evolves
// cleanly to multiple per-Review records.
//
// Historical Reviews created before provenance capture existed have no
// record: their drift is unknown and must never be fabricated.
type RepositoryProvenance struct {
	ReviewID     string
	RepositoryID string
	// Commit is the full commit hash of the repository state the Agent
	// actually reviewed. It is immutable once recorded.
	Commit string
	// RepositoryHEAD is the source Repository HEAD observed when admission
	// froze Commit. It is kept separate so no branch name is inferred from a
	// SHA. For newly admitted Reviews it equals Commit.
	RepositoryHEAD string
	BranchKind     string
	BranchRef      string
	// WorkingTreeDirtyAtAdmission is nil for historical provenance whose
	// dirty state was not captured.
	WorkingTreeDirtyAtAdmission *bool
	// InspectionMode identifies the canonical FeatureArea working workspace.
	// Commit is its integration baseline for this Review; exact observed working
	// state, including prior Task changes, is recorded in the Run snapshots.
	InspectionMode string
	CapturedAt     time.Time
}

const (
	ProvenanceBranch               = "BRANCH"
	ProvenanceDetached             = "DETACHED"
	ProvenanceUnknown              = "UNKNOWN"
	InspectionFeatureAreaWorkspace = "FEATURE_AREA_WORKSPACE"
)

// ErrProvenanceNotFound reports that a Review carries no repository
// provenance record.
var ErrProvenanceNotFound = errors.New("review repository provenance is not recorded")

// RepositoryDrift is the bounded, factual Git drift between the repository
// state a Review was based on and the current authoritative repository state.
// It contains no semantic judgement: Bridge never reports a Review as stale,
// invalid, outdated, or requiring a rerun.
type RepositoryDrift struct {
	RepositoryID string `json:"repositoryId,omitempty"`
	// ReviewCommit is the immutable provenance commit; empty when the Review
	// carries no provenance (historical rows).
	ReviewCommit string `json:"reviewCommit"`
	// CurrentCommit is the authoritative repository HEAD at read time.
	CurrentCommit           string `json:"currentCommit"`
	CurrentBranchKind       string `json:"currentBranchKind"`
	CurrentBranchRef        string `json:"currentBranchRef,omitempty"`
	CurrentWorkingTreeDirty *bool  `json:"currentWorkingTreeDirty,omitempty"`
	// Available reports whether Git drift facts could be derived at all.
	// It is false when provenance is unknown, a commit is unresolvable, the
	// repository is unavailable, or Git failed. Unavailable drift never
	// implies anything about the Review itself.
	Available bool `json:"available"`
	// UnavailableReason is a bounded factual reason for Available=false.
	UnavailableReason string `json:"unavailableReason,omitempty"`

	RepositoryChanged     bool              `json:"repositoryChanged"`
	ChangedFileCount      int               `json:"changedFileCount"`
	FocusPathsChanged     []FocusPathChange `json:"focusPathsChanged"`
	ChangedFocusPathCount int               `json:"changedFocusPathCount"`
	OtherChangedFileCount int               `json:"otherChangedFileCount"`
	Truncated             bool              `json:"truncated"`
}

// FocusPathChange is one changed path that intersects the Review's explicitly
// recorded relevant paths.
type FocusPathChange struct {
	Change  ChangeTypeName `json:"change"`
	Path    string         `json:"path"`
	OldPath string         `json:"oldPath,omitempty"`
}

// ChangeTypeName mirrors git.ChangeType without the drift projector
// importing the Git client package.
type ChangeTypeName string

const (
	ChangeAdded    ChangeTypeName = "ADDED"
	ChangeModified ChangeTypeName = "MODIFIED"
	ChangeDeleted  ChangeTypeName = "DELETED"
	ChangeRenamed  ChangeTypeName = "RENAMED"
)

// DefaultDriftLimit bounds the returned focus-path list for large team
// repositories. Counts always reflect the full change set.
const DefaultDriftLimit = 50

// MatchRecordedPath reports whether one changed Git path falls under the
// recorded structured relevant-path contract: exact path equality, or a
// recorded directory-style path containing the changed path. There is no
// fuzzy, Module-based, or proximity inference.
func MatchRecordedPath(changedPath string, recordedPaths []string) bool {
	if changedPath == "" {
		return false
	}
	for _, recorded := range recordedPaths {
		if recorded == "" {
			continue
		}
		if changedPath == recorded {
			return true
		}
		if len(recorded) < len(changedPath) && strings.HasPrefix(changedPath, recorded+"/") {
			return true
		}
	}
	return false
}
