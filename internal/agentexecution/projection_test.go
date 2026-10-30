package agentexecution

import (
	"github.com/lazyun/devbridge/internal/run"
	"testing"
	"time"
)

func TestProjectionReadSemanticsPreserveOrphanAndPendingAuthorization(t *testing.T) {
	for _, tc := range []struct {
		status               string
		terminal, actionable bool
	}{
		{"PENDING", false, true}, {"EXECUTING", false, false}, {"FINALIZING", false, false},
		{"UNKNOWN", false, false}, {"UNKNOWN_ORPHANED", true, false}, {"COMPLETED", true, false},
		{"FAILED", true, false}, {"REJECTED", true, false}, {"CANCELED", true, false},
	} {
		p := Projection{Kind: KindDevelopment, Status: tc.status, UserAuthorizationRequired: true}
		if p.Terminal() != tc.terminal || p.AuthorizationActionable() != tc.actionable {
			t.Fatal("read semantics", p)
		}
	}
}

func exactFixture(kind Kind) (Execution, run.Run) {
	owner := string(kind)
	if kind == KindDevelopment {
		owner = "EXECUTION"
	}
	e := Execution{ID: "execution", ProjectID: "project", TaskID: "task", FeatureAreaID: "area", Kind: kind, SemanticOwnerKind: owner, SemanticOwnerID: "owner", ProvenanceKind: ProvenanceNative}
	r := run.Run{ID: "run", AgentExecutionID: e.ID, ProjectID: e.ProjectID, TaskID: e.TaskID, FeatureAreaID: e.FeatureAreaID, OperationKind: run.OperationKind(owner), OperationID: e.SemanticOwnerID, Purpose: run.PurposeScopeReview, Status: run.StatusSucceeded, CreatedAt: time.Unix(1, 0)}
	return e, r
}
func TestReviewApprovalDoesNotBecomeRuntimeStatus(t *testing.T) {
	e, r := exactFixture(KindReview)
	p := Reduce(e, Facts{Runs: []run.Run{r}, CurrentRunID: r.ID, DomainStatus: "WAITING_APPROVAL"})
	if p.Status != "COMPLETED" || p.DomainStatus != "WAITING_APPROVAL" {
		t.Fatalf("approval conflated with runtime: %+v", p)
	}
}
func TestDevelopmentRetainsOneExecutionUntilVerificationAndFinalization(t *testing.T) {
	e, r := exactFixture(KindDevelopment)
	e.ExecutionProposalID = e.ID
	r.Purpose = run.PurposeImplementation
	f := Facts{Runs: []run.Run{r}, CurrentRunID: r.ID, DevelopmentOutcome: "RUNNING", VerificationRequired: true}
	p := Reduce(e, f)
	if p.Status == "COMPLETED" || p.Stage != "VERIFICATION" {
		t.Fatalf("early completion: %+v", p)
	}
	v := r
	v.ID = "verification"
	v.Purpose = run.PurposeVerification
	v.Status = run.StatusRunning
	f.Runs = append(f.Runs, v)
	f.CurrentRunID = v.ID
	p = Reduce(e, f)
	if p.ExecutionID != e.ID || p.RunID != v.ID || len(p.RunIDs) != 2 || p.Status != "VERIFYING" {
		t.Fatalf("verification lost ownership: %+v", p)
	}
	f.DevelopmentOutcome = "SUCCEEDED"
	f.Runtime = map[string]RuntimeFacts{v.ID: {FinalizationPending: true}}
	p = Reduce(e, f)
	if p.Status != "FINALIZING" {
		t.Fatalf("ignored pending finalization: %+v", p)
	}
}
func TestReducerNeverBorrowsAnotherRunOrOrphanDisposition(t *testing.T) {
	e, r := exactFixture(KindReview)
	other := r
	other.ID = "newer"
	other.AgentExecutionID = "another"
	other.Status = run.StatusCanceled
	other.CreatedAt = time.Unix(2, 0)
	f := Facts{Runs: []run.Run{r, other}, Runtime: map[string]RuntimeFacts{other.ID: {BackendDisposition: "UNKNOWN_ORPHANED"}}}
	p := Reduce(e, f)
	if p.RunID != r.ID || len(p.RunIDs) != 1 || p.Status != "COMPLETED" {
		t.Fatalf("borrowed unrelated run: %+v", p)
	}
	f.Runtime[r.ID] = RuntimeFacts{BackendDisposition: "UNKNOWN_ORPHANED", CancellationState: "CANCELED"}
	p = Reduce(e, f)
	if p.Status != "UNKNOWN_ORPHANED" || p.BackendDisposition != "UNKNOWN_ORPHANED" || p.RecoveryRequired {
		t.Fatalf("orphan treated as backend death: %+v", p)
	}
}
func TestLegacyUnknownIsReadOnlyWithoutInventedProvenance(t *testing.T) {
	e := Execution{ID: "legacy", Kind: KindInspection, ProvenanceKind: ProvenanceLegacyUnknown}
	p := Reduce(e, Facts{})
	if !p.ReadOnly || p.SemanticOwnerID != "" || p.ExecutionProposalID != "" {
		t.Fatalf("legacy gained authority: %+v", p)
	}
}

func TestDevelopmentAuthoritativeTerminalPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, authorization, outcome, want string
		withRun                            bool
	}{
		{"pending", "PENDING_CONFIRMATION", "PENDING", "PENDING", false},
		{"confirmed", "CONFIRMED", "PENDING", "PENDING", false},
		{"rejected", "REJECTED", "PENDING", "REJECTED", false},
		{"rejection precedes stale outcome", "REJECTED", "RUNNING", "REJECTED", false},
		{"canceled", "CONFIRMED", "CANCELED", "CANCELED", false},
		{"active", "CONSUMED", "RUNNING", "EXECUTING", true},
		{"completed", "CONSUMED", "SUCCEEDED", "COMPLETED", true},
		{"failed", "CONSUMED", "FAILED", "FAILED", true},
		{"canceled after start", "CONSUMED", "CANCELED", "CANCELED", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, r := exactFixture(KindDevelopment)
			r.Purpose = run.PurposeImplementation
			r.Status = run.StatusRunning
			f := Facts{DomainStatus: tc.authorization, DomainResult: tc.outcome, DevelopmentOutcome: tc.outcome, AuthorizationRequired: tc.authorization == "PENDING_CONFIRMATION" || tc.authorization == "CONFIRMED"}
			if tc.withRun {
				f.Runs = []run.Run{r}
				f.CurrentRunID = r.ID
			}
			p := Reduce(e, f)
			if p.Status != tc.want || p.DomainStatus != tc.authorization || p.DomainResult != tc.outcome {
				t.Fatalf("wrong authoritative precedence: %+v", p)
			}
			if tc.want != "PENDING" && p.UserAuthorizationRequired {
				t.Fatal("terminal/active authorization actionable")
			}
			if tc.authorization == "REJECTED" && (p.CancellationState != "" || p.BackendDisposition != "NOT_STARTED") {
				t.Fatalf("invented cancellation: %+v", p)
			}
		})
	}
}
