package mcpserver

import (
	"context"

	productreview "github.com/lazyun/devbridge/internal/review"
)

// RepositoryDriftOutput is the adapter-facing projection of the shared Review
// repository drift facts. It contains objective Git facts only: Bridge never
// projects staleness, validity, or rerun requirements. Internal repository
// identifiers are deliberately not part of this contract.
type RepositoryDriftOutput struct {
	ReviewCommit            string                  `json:"reviewCommit"`
	CurrentCommit           string                  `json:"currentCommit"`
	CurrentBranchKind       string                  `json:"currentBranchKind"`
	CurrentBranchRef        string                  `json:"currentBranchRef,omitempty"`
	CurrentWorkingTreeDirty *bool                   `json:"currentWorkingTreeDirty,omitempty"`
	Available               bool                    `json:"available"`
	UnavailableReason       string                  `json:"unavailableReason,omitempty"`
	RepositoryChanged       bool                    `json:"repositoryChanged"`
	ChangedFileCount        int                     `json:"changedFileCount"`
	FocusPathsChanged       []ReviewDriftPathChange `json:"focusPathsChanged"`
	ChangedFocusPathCount   int                     `json:"changedFocusPathCount"`
	OtherChangedFileCount   int                     `json:"otherChangedFileCount"`
	Truncated               bool                    `json:"truncated"`
}

// ReviewDriftPathChange is one changed path that intersects the paths
// explicitly recorded in the Review focus. It is not a claim that the Agent
// inspected the file.
type ReviewDriftPathChange struct {
	Change  string `json:"change"`
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
}

const repositoryDriftOutputSchema = `{"type":"object","properties":{
"reviewCommit":{"type":"string"},
"currentCommit":{"type":"string"},
"currentBranchKind":{"type":"string","enum":["BRANCH","DETACHED","UNKNOWN"]},
"currentBranchRef":{"type":"string"},
"currentWorkingTreeDirty":{"type":"boolean"},
"available":{"type":"boolean"},
"unavailableReason":{"type":"string"},
"repositoryChanged":{"type":"boolean"},
"changedFileCount":{"type":"integer","minimum":0},
"focusPathsChanged":{"type":"array","maxItems":50,"items":{"type":"object","properties":{"change":{"type":"string","enum":["ADDED","MODIFIED","DELETED","RENAMED"]},"path":{"type":"string","minLength":1},"oldPath":{"type":"string"}},"required":["change","path"],"additionalProperties":false}},
"changedFocusPathCount":{"type":"integer","minimum":0},
"otherChangedFileCount":{"type":"integer","minimum":0},
"truncated":{"type":"boolean"}
},"required":["reviewCommit","currentCommit","available","repositoryChanged","changedFileCount","focusPathsChanged","changedFocusPathCount","otherChangedFileCount","truncated"],"additionalProperties":false}`

// RepositoryDriftOutputFor maps the shared product drift projection into the
// adapter-facing shape. It is the single converter used by both adapters.
func RepositoryDriftOutputFor(drift productreview.RepositoryDrift) *RepositoryDriftOutput {
	out := &RepositoryDriftOutput{
		ReviewCommit:            drift.ReviewCommit,
		CurrentCommit:           drift.CurrentCommit,
		CurrentBranchKind:       drift.CurrentBranchKind,
		CurrentBranchRef:        drift.CurrentBranchRef,
		CurrentWorkingTreeDirty: drift.CurrentWorkingTreeDirty,
		Available:               drift.Available,
		UnavailableReason:       drift.UnavailableReason,
		RepositoryChanged:       drift.RepositoryChanged,
		ChangedFileCount:        drift.ChangedFileCount,
		FocusPathsChanged:       []ReviewDriftPathChange{},
		ChangedFocusPathCount:   drift.ChangedFocusPathCount,
		OtherChangedFileCount:   drift.OtherChangedFileCount,
		Truncated:               drift.Truncated,
	}
	for _, change := range drift.FocusPathsChanged {
		out.FocusPathsChanged = append(out.FocusPathsChanged, ReviewDriftPathChange{Change: string(change.Change), Path: change.Path, OldPath: change.OldPath})
	}
	return out
}

// reviewDriftReader is implemented by application services that can resolve
// objective repository drift facts for one Project-bound session.
type reviewDriftReader interface {
	ReviewRepositoryDriftForSession(ctx context.Context, aiSessionID, reviewID string, limit int) (productreview.RepositoryDrift, error)
}

// AttachReviewDrift fills the bounded repository drift facts on one review
// output. Both the GPT adapter and the Local MCP adapter call this shared
// helper; a failed drift read leaves the field absent rather than fabricating
// facts.
func AttachReviewDrift(ctx context.Context, service BridgeService, aiSessionID, reviewID string, out *ExternalReviewOutput) {
	if out == nil {
		return
	}
	reader, ok := service.(reviewDriftReader)
	if !ok {
		return
	}
	drift, err := reader.ReviewRepositoryDriftForSession(ctx, aiSessionID, reviewID, productreview.DefaultDriftLimit)
	if err != nil {
		return
	}
	out.RepositoryDrift = RepositoryDriftOutputFor(drift)
}
