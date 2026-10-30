package confirmation

import (
	"errors"
	"unicode/utf8"
)

// DisplaySummary is optional author-supplied content, never an authorization
// condition or generated synopsis. Its JSON participates in the existing hashes.
type DisplaySummary struct {
	Summary        string   `json:"summary"`
	KeyConstraints []string `json:"keyConstraints,omitempty"`
}

// Validate checks only structure/Unicode code-point limits, like JSON Schema.
// Missing summaries remain valid for old and new prepared intents.
func (d *DisplaySummary) Validate() error {
	if d == nil {
		return nil
	}
	if !utf8.ValidString(d.Summary) || utf8.RuneCountInString(d.Summary) < 1 || utf8.RuneCountInString(d.Summary) > 160 {
		return errors.New("displaySummary.summary must contain 1 to 160 Unicode characters")
	}
	if len(d.KeyConstraints) > 3 {
		return errors.New("displaySummary.keyConstraints permits at most 3 entries")
	}
	for _, value := range d.KeyConstraints {
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 60 {
			return errors.New("each displaySummary.keyConstraints entry must contain 1 to 60 Unicode characters")
		}
	}
	return nil
}

// DisplayMetadata is a separate, best-effort read projection. It never enters
// Intent.Content, the content hash, submitted input or any write target.
// ProjectID is the intent's immutable identity, including empty setup identity.
// Setup names after binding describe the exact confirmed selection instead.
type DisplayMetadata struct {
	AISessionID       string                `json:"aiSessionId"`
	IntentID          string                `json:"intentId"`
	Version           int                   `json:"version"`
	Purpose           Purpose               `json:"purpose"`
	ProjectID         string                `json:"projectId"`
	TaskID            string                `json:"taskId,omitempty"`
	ReviewID          string                `json:"reviewId,omitempty"`
	DecisionID        string                `json:"decisionId,omitempty"`
	FeatureAreaID     string                `json:"featureAreaId,omitempty"`
	RepositoryID      string                `json:"repositoryId,omitempty"`
	ProjectName       string                `json:"projectName,omitempty"`
	TaskName          string                `json:"taskName,omitempty"`
	FeatureAreaName   string                `json:"featureAreaName,omitempty"`
	RepositoryName    string                `json:"repositoryName,omitempty"`
	DevelopmentBranch string                `json:"developmentBranch,omitempty"`
	ReviewOrdinal     int                   `json:"reviewOrdinal,omitempty"`
	RepositoryCount   int                   `json:"repositoryCount,omitempty"`
	TopologyRevision  int64                 `json:"topologyRevision,omitempty"`
	DecisionPurpose   string                `json:"decisionPurpose,omitempty"`
	PresentationIssue string                `json:"presentationIssue,omitempty"`
	Status            string                `json:"status"`
	ObjectNameSource  string                `json:"objectNameSource"`
	Related           *RelatedBusinessState `json:"related,omitempty"`
}

// RelatedBusinessState is a live, exact-object read projection for a decision
// intent. It is deliberately separate from the immutable intent and its
// receipt: confirmation consumption is not an engineering result.
type RelatedBusinessState struct {
	TaskDisposition        string      `json:"taskDisposition,omitempty"`
	TaskDispositionSummary string      `json:"taskDispositionSummary,omitempty"`
	Availability           string      `json:"availability"` // AVAILABLE, UNAVAILABLE
	Kind                   string      `json:"kind"`         // REVIEW, EXECUTION
	ID                     string      `json:"id"`
	State                  string      `json:"state,omitempty"`
	Outcome                string      `json:"outcome,omitempty"`
	FailureCode            string      `json:"failureCode,omitempty"`
	ReviewRun              *RelatedRun `json:"reviewRun,omitempty"`
	ImplementationRun      *RelatedRun `json:"implementationRun,omitempty"`
	VerificationRun        *RelatedRun `json:"verificationRun,omitempty"`
	ReadFailureCode        string      `json:"readFailureCode,omitempty"`
}

type RelatedRun struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	FailureCode string `json:"failureCode,omitempty"`
}
