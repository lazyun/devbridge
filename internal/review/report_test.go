package review

import (
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/workset"
)

func validBlockedReport() ReviewReport {
	return ReviewReport{SchemaVersion: 1, Summary: "Repository access unavailable", Disposition: DispositionBlocked, Coverage: []string{"requested production scope"}, Findings: []Finding{}, Limitations: []string{"No source content returned"}, RecommendedNextSteps: []string{}, EvidenceRefs: []EvidenceRef{}}
}
func TestReviewReportStructuralBoundary(t *testing.T) {
	if err := validBlockedReport().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ReviewReport){
		"missing summary":         func(r *ReviewReport) { r.Summary = " " },
		"unknown disposition":     func(r *ReviewReport) { r.Disposition = "PASSED" },
		"no coverage":             func(r *ReviewReport) { r.Coverage = nil },
		"no limitations":          func(r *ReviewReport) { r.Limitations = []string{} },
		"no findings array":       func(r *ReviewReport) { r.Findings = nil },
		"unknown version":         func(r *ReviewReport) { r.SchemaVersion = 3 },
		"oversize":                func(r *ReviewReport) { r.Coverage = []string{strings.Repeat("x", MaxReportBytes)} },
		"approval missing specs":  func(r *ReviewReport) { r.Disposition = DispositionApprovalReady },
		"invalid range":           func(r *ReviewReport) { r.EvidenceRefs = []EvidenceRef{{EvidenceID: "e", StartLine: 2, EndLine: 1}} },
		"unbounded nesting count": func(r *ReviewReport) { r.Coverage = make([]string, 129) },
	} {
		t.Run(name, func(t *testing.T) {
			r := validBlockedReport()
			mutate(&r)
			if !errors.Is(r.Validate(), ErrReportInvalid) {
				t.Fatalf("accepted %s", name)
			}
		})
	}
}

func TestV2ReportRequiresFrozenMemberTargetsAndEvidenceIdentity(t *testing.T) {
	target := workset.Target{RepositoryID: "app", MemberID: "member-app", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}
	report := validBlockedReport()
	report.SchemaVersion = 2
	report.RepositoryTargets = []workset.Target{target}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	report.EvidenceRefs = []EvidenceRef{{EvidenceID: "evidence-1", RepositoryID: "app", MemberID: "member-app", Path: "main.go", Digest: strings.Repeat("a", 64)}}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	report.EvidenceRefs[0].MemberID = ""
	if !errors.Is(report.Validate(), ErrReportInvalid) {
		t.Fatal("v2 evidence without member identity was accepted")
	}
}
func TestExecutorDiscriminatorCompatibility(t *testing.T) {
	if ExecutorKind("").Effective() != ExecutorCodingAgent || ExecutorKind("GPT").Valid() || ExecutorKind("FAST").Valid() {
		t.Fatal("executor inferred from brand/depth")
	}
}
