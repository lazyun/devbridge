package review

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

type ExecutorKind string

const (
	ExecutorCodingAgent ExecutorKind = "CODING_AGENT"
	ExecutorUpperLLM    ExecutorKind = "UPPER_LLM"
	ExecutorPiAgent     ExecutorKind = "PI_AGENT"
	DispositionBlocked  Disposition  = "BLOCKED"
	MaxReportBytes                   = 256 << 10
)

func (k ExecutorKind) Valid() bool {
	return k == ExecutorCodingAgent || k == ExecutorUpperLLM || k == ExecutorPiAgent
}

// Omission is the legacy Coding Agent contract, never model-name inference.
func (k ExecutorKind) Effective() ExecutorKind {
	if k == "" {
		return ExecutorCodingAgent
	}
	return k
}

type ExecutorMetadata struct {
	Model      string `json:"model,omitempty"`
	Provenance string `json:"provenance"`
}

type EvidenceRef struct {
	EvidenceID string `json:"evidenceId"`
	// RepositoryID and MemberID bind v2 evidence to one immutable workspace
	// member. They are deliberately absent from the v1 wire contract.
	RepositoryID string `json:"repositoryId,omitempty"`
	MemberID     string `json:"memberId,omitempty"`
	Path         string `json:"path,omitempty"`
	Digest       string `json:"digest,omitempty"`
	StartLine    int    `json:"startLine,omitempty"`
	EndLine      int    `json:"endLine,omitempty"`
}
type Finding struct {
	ID              string        `json:"id"`
	Severity        string        `json:"severity"`
	Title           string        `json:"title"`
	Description     string        `json:"description"`
	Impact          string        `json:"impact"`
	EvidenceRefs    []EvidenceRef `json:"evidenceRefs"`
	SuggestedChange string        `json:"suggestedChange"`
}
type ReviewReport struct {
	SchemaVersion int `json:"schemaVersion"`
	// RepositoryTargets is the proposed implementation target set. It is
	// distinct from the read set admitted for the Review, but storage requires
	// the immutable repository/member/origin identities to match that read set.
	RepositoryTargets    []workset.Target       `json:"repositoryTargets,omitempty"`
	PrimaryMemberID      string                 `json:"primaryMemberId,omitempty"`
	Summary              string                 `json:"summary"`
	Disposition          Disposition            `json:"disposition"`
	Coverage             []string               `json:"coverage"`
	Findings             []Finding              `json:"findings"`
	Limitations          []string               `json:"limitations"`
	RecommendedNextSteps []string               `json:"recommendedNextSteps"`
	EvidenceRefs         []EvidenceRef          `json:"evidenceRefs"`
	WorkSpec             *task.WorkSpec         `json:"workSpec,omitempty"`
	VerificationSpec     *task.VerificationSpec `json:"verificationSpec,omitempty"`
}

var (
	ErrPreparationFailed = errors.New("REVIEW_PREPARATION_FAILED")
	ErrExecutorMismatch  = errors.New("REVIEW_EXECUTOR_MISMATCH")
	ErrReportInvalid     = errors.New("REVIEW_REPORT_INVALID")
	ErrEvidenceMismatch  = errors.New("REVIEW_EVIDENCE_MISMATCH")
	ErrTargetStale       = errors.New("REVIEW_TARGET_STALE")
	ErrResultFinalized   = errors.New("REVIEW_RESULT_FINALIZED")
	ErrFinalizing        = errors.New("REVIEW_FINALIZING")
)

func (r ReviewReport) Validate() error {
	bad := func(detail string) error { return fmt.Errorf("%w: %s", ErrReportInvalid, detail) }
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxReportBytes {
		return bad("report exceeds size limit")
	}
	textOK := func(s string) bool {
		return strings.TrimSpace(s) != "" && len(s) <= 16384 && !strings.ContainsRune(s, '\x00')
	}
	if (r.SchemaVersion != 1 && r.SchemaVersion != 2) || !textOK(r.Summary) {
		return bad("schemaVersion 1 or 2 and a bounded summary are required")
	}
	definition := task.Definition{Version: r.SchemaVersion, RepositoryTargets: r.RepositoryTargets, PrimaryMemberID: r.PrimaryMemberID}
	if r.WorkSpec != nil {
		definition.WorkSpec = *r.WorkSpec
	}
	if r.VerificationSpec != nil {
		definition.VerificationSpec = *r.VerificationSpec
	}
	if r.SchemaVersion == 1 {
		if err := task.ValidateDefinitionTargets(definition); err != nil {
			return bad(err.Error())
		}
	}
	if r.SchemaVersion == 2 && (len(r.RepositoryTargets) > 0 || r.Disposition == DispositionApprovalReady) {
		if err := workset.ValidateTargets(r.RepositoryTargets); err != nil {
			return bad("invalid repository targets: " + err.Error())
		}
	}
	if r.Coverage == nil || len(r.Coverage) == 0 || r.Limitations == nil || len(r.Limitations) == 0 || r.Findings == nil || r.RecommendedNextSteps == nil || r.EvidenceRefs == nil {
		return bad("coverage, limitations and all report arrays are required")
	}
	for _, list := range [][]string{r.Coverage, r.Limitations, r.RecommendedNextSteps} {
		if len(list) > 128 {
			return bad("too many entries")
		}
		for _, s := range list {
			if !textOK(s) {
				return bad("invalid text entry")
			}
		}
	}
	if len(r.Findings) > 128 || len(r.EvidenceRefs) > 256 {
		return bad("too many findings or references")
	}
	refs := append([]EvidenceRef{}, r.EvidenceRefs...)
	seen := map[string]bool{}
	for _, f := range r.Findings {
		if !textOK(f.ID) || len(f.ID) > 128 || seen[f.ID] || !textOK(f.Title) || !textOK(f.Description) || !textOK(f.Impact) || !textOK(f.SuggestedChange) || f.EvidenceRefs == nil || len(f.EvidenceRefs) > 64 {
			return bad("invalid finding")
		}
		seen[f.ID] = true
		switch f.Severity {
		case "CRITICAL", "HIGH", "MEDIUM", "LOW", "INFO":
		default:
			return bad("invalid severity")
		}
		refs = append(refs, f.EvidenceRefs...)
	}
	for _, ref := range refs {
		if _, err := hex.DecodeString(ref.Digest); err != nil || strings.ToLower(ref.Digest) != ref.Digest {
			return bad("invalid evidence digest")
		}
		if strings.TrimSpace(ref.EvidenceID) == "" || strings.IndexFunc(ref.EvidenceID, unicode.IsSpace) >= 0 || len(ref.EvidenceID) > 128 || strings.TrimSpace(ref.Path) == "" || len(ref.Path) > 4096 || len(ref.Digest) != 64 || ref.StartLine < 0 || ref.EndLine < 0 || (ref.StartLine == 0) != (ref.EndLine == 0) || ref.EndLine < ref.StartLine {
			return bad("invalid evidence reference")
		}
		if r.SchemaVersion == 2 && (strings.TrimSpace(ref.RepositoryID) == "" || strings.TrimSpace(ref.MemberID) == "") {
			return bad("v2 evidence requires repository and member identity")
		}
	}
	switch r.Disposition {
	case DispositionApprovalReady:
		if r.WorkSpec == nil || r.VerificationSpec == nil {
			return bad("approval requires explicit workSpec and verificationSpec")
		}
		if r.SchemaVersion == 2 {
			if err := task.ValidateDefinitionTargets(definition); err != nil {
				return bad(err.Error())
			}
			if err := task.ValidateExecutablePrimary(definition); err != nil {
				return bad(err.Error())
			}
		} else if err := task.ValidateWorkSpec(*r.WorkSpec); err != nil {
			return bad(err.Error())
		}
		if err := task.ValidateVerificationSpec(*r.VerificationSpec); err != nil {
			return bad(err.Error())
		}
		if r.VerificationSpec.Mode != task.VerificationModeRun {
			return bad("approval requires RUN verification")
		}
	case DispositionFurtherReviewRequired, DispositionBlocked:
		if r.WorkSpec != nil || r.VerificationSpec != nil {
			return bad("proposal fields require APPROVAL_READY")
		}
	default:
		return bad("invalid disposition")
	}
	return nil
}

// CompatibilityResult retains the explicit proposal contract consumed by the
// existing approval gate; it never extracts instructions from report prose.
func (r ReviewReport) CompatibilityResult() ImplementationResult {
	out := ImplementationResult{Version: r.SchemaVersion, Summary: r.Summary, ReviewDisposition: r.Disposition, PrimaryMemberID: r.PrimaryMemberID}
	if r.SchemaVersion == 2 {
		out.RepositoryTargets = workset.CloneTargets(r.RepositoryTargets)
	}
	if r.WorkSpec != nil {
		out.WorkSpec = *r.WorkSpec
	}
	if r.VerificationSpec != nil {
		out.VerificationSpec = *r.VerificationSpec
	}
	return out
}

type UpperTarget struct {
	AISessionID     string `json:"aiSessionId"`
	FeatureAreaID   string `json:"featureAreaId"`
	TaskID          string `json:"taskId"`
	ReviewID        string `json:"reviewId"`
	RunID           string `json:"runId"`
	RuntimeEpoch    string `json:"runtimeEpoch"`
	GrantGeneration int64  `json:"grantGeneration"`
}
type SubmitInput struct {
	UpperTarget
	WorkerClaim *WorkerClaimIdentity `json:"workerClaim,omitempty"`
	RequestID   string               `json:"requestId"`
	Report      ReviewReport         `json:"report"`
}
type ReportReceipt struct {
	RequestID     string      `json:"requestId"`
	ReviewID      string      `json:"reviewId"`
	RunID         string      `json:"runId"`
	ReportID      string      `json:"reportId"`
	PayloadSHA256 string      `json:"payloadSha256"`
	Status        string      `json:"status"`
	Disposition   Disposition `json:"disposition"`
	CompletedAt   time.Time   `json:"completedAt"`
}
type StoredReport struct {
	StartSnapshot        string       `json:"startSnapshot"`
	ReportID             string       `json:"reportId"`
	ReviewID             string       `json:"reviewId"`
	RunID                string       `json:"runId"`
	CreatedByAISessionID string       `json:"createdByAiSessionId"`
	Baseline             string       `json:"baseline"`
	PayloadSHA256        string       `json:"payloadSha256"`
	Report               ReviewReport `json:"report"`
	CreatedAt            time.Time    `json:"createdAt"`
}

// UpperAccess is an authoritative, bounded projection; no bearer grant can
// substitute a different owner or filesystem root.
type UpperAccess struct {
	ReviewCreatedAt   *time.Time       `json:"reviewCreatedAt,omitempty"`
	ProtocolVersion   int              `json:"protocolVersion"`
	RepositoryTargets []workset.Target `json:"repositoryTargets,omitempty"`
	StartSnapshot     string           `json:"startSnapshot,omitempty"`
	Blocker           string           `json:"blocker,omitempty"`
	Target            UpperTarget      `json:"target"`
	State             string           `json:"state"`
	WorkspaceID       string           `json:"workspaceId,omitempty"`
	WorktreeID        string           `json:"worktreeId,omitempty"`
	Baseline          string           `json:"baseline"`
	AllowedPaths      []string         `json:"allowedPaths"`
	ForbiddenPaths    []string         `json:"forbiddenPaths"`
	LastObservedAt    *time.Time       `json:"lastObservedAt,omitempty"`
}
