package recoverystatus

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitrepo "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/workset"
)

func factFixture(t *testing.T) (Report, FactualObservation) {
	t.Helper()
	ctx := context.Background()
	w := workset.RuntimeWorkset{Members: []workset.RuntimeMemberBinding{}}
	for _, alias := range []string{"app", "sdk"} {
		root := filepath.Join(t.TempDir(), alias)
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		git := func(args ...string) {
			t.Helper()
			c := exec.Command("git", append([]string{"-C", root}, args...)...)
			c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
			if b, e := c.CombinedOutput(); e != nil {
				t.Fatalf("git %v: %s %v", args, b, e)
			}
		}
		git("init", "-q")
		git("config", "user.email", "fixture@example.invalid")
		git("config", "user.name", "Fixture")
		if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("before\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-qm", "base")
		root, id, e := RootIdentity(root)
		if e != nil {
			t.Fatal(e)
		}
		g := gitrepo.NewClient(gitrepo.Config{})
		head, e := g.ResolveHEAD(ctx, root)
		if e != nil {
			t.Fatal(e)
		}
		snapshot, e := g.Snapshot(ctx, root)
		if e != nil {
			t.Fatal(e)
		}
		w.Members = append(w.Members, workset.RuntimeMemberBinding{RepositoryAlias: alias, RepositoryID: alias, MemberID: alias, WorktreeID: alias, CanonicalRoot: root, RootFileIdentity: id, RuntimeRootIdentity: id, StartHEAD: head, StartSnapshot: snapshot, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"file.txt"}}})
		if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("implemented\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	obs, e := ObserveWorkset(ctx, w)
	if e != nil {
		t.Fatal(e)
	}
	s := Source{OperationKind: "IMPLEMENTATION", OperationID: "operation", RunID: "source", TurnID: "source-turn"}
	r := Report{Version: ReportVersion, SourceOperationKind: s.OperationKind, SourceOperationID: s.OperationID, SourceRunID: s.RunID, SourceTurnID: s.TurnID, OverallStatus: "COMPLETED", Summary: "Implementation observed", CompletedGoals: []GoalObservation{{GoalID: "approved-goal", Status: "COMPLETED", EvidenceSummary: "app and sdk files"}}, RemainingGoals: []GoalObservation{}, RepositoryObservations: []RepositoryObservation{{RuntimeAlias: "app", ChangedPaths: []string{"file.txt"}, Completion: "COMPLETED", VerificationState: "NOT_RUN"}, {RuntimeAlias: "sdk", ChangedPaths: []string{"file.txt"}, Completion: "COMPLETED", VerificationState: "NOT_RUN"}}, VerificationObservations: []VerificationObservation{}, UnresolvedIssues: []string{}, RecommendedNextAction: "FINALIZE"}
	b, _ := json.Marshal(r)
	f := FactualObservation{Source: s, RecoveryRunID: "recovery", ReportID: "report", ReportHash: Hash(b), Workset: w, Before: obs, After: append([]WorktreeObservation{}, obs...), SourceTurnTerminal: true, BackendQuiescent: true, ScopeValid: true, OwnerContractValidated: true, OwnerContractEvidenceHash: Hash([]byte("canonical-result")), ApprovedGoalIDs: []string{"approved-goal"}, SourceResponseDisposition: "UNKNOWN", ObservedAt: time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)}
	return r, f
}
func TestRecoveryStructuredReportStrict(t *testing.T) {
	r, f := factFixture(t)
	b, _ := json.Marshal(r)
	if _, e := ParseReport(b, f.Source); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"prose", "required", "nested-required", "source", "extra", "duplicate", "path"} {
		t.Run(kind, func(t *testing.T) {
			var v map[string]any
			_ = json.Unmarshal(b, &v)
			var raw []byte
			switch kind {
			case "prose":
				raw = []byte("completed")
			case "required":
				delete(v, "scopeExpansionRequired")
			case "nested-required":
				delete(v["repositoryObservations"].([]any)[0].(map[string]any), "changedPaths")
			case "source":
				v["sourceTurnId"] = "other"
			case "extra":
				v["completed"] = true
			case "duplicate":
				raw = []byte(strings.Replace(string(b), `"sourceRunId":"source"`, `"sourceRunId":"evil","sourceRunId":"source"`, 1))
			case "path":
				v["repositoryObservations"].([]any)[0].(map[string]any)["changedPaths"] = []string{"../outside"}
			}
			if raw == nil {
				raw, _ = json.Marshal(v)
			}
			if _, e := ParseReport(raw, f.Source); e == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
}
func TestRecoveryAssessmentObjectiveEvidence(t *testing.T) {
	r, f := factFixture(t)
	a, e := Assess(r, f)
	if e != nil || a.EngineeringDisposition != "RECOVERED_COMPLETE" || a.SourceResponseDisposition != "UNKNOWN" {
		t.Fatalf("%+v %v", a, e)
	}
	again, e := Assess(r, f)
	if e != nil || again.ID != a.ID {
		t.Fatal("assessment not replayable")
	}
	for _, kind := range []string{"report-alone", "missing-sdk", "false-change", "partial", "question-claim", "expansion", "mutation", "head", "metadata", "scope"} {
		t.Run(kind, func(t *testing.T) {
			rr := r
			ff := f
			ff.After = append([]WorktreeObservation{}, f.After...)
			switch kind {
			case "report-alone":
				ff.OwnerContractValidated = false
			case "missing-sdk":
				ff.After = ff.After[:1]
			case "false-change":
				rr.RepositoryObservations = append([]RepositoryObservation{}, r.RepositoryObservations...)
				rr.RepositoryObservations[1].ChangedPaths = []string{"missing.txt"}
			case "partial":
				rr.OverallStatus = "PARTIAL"
				rr.CompletedGoals = []GoalObservation{}
				rr.RemainingGoals = []GoalObservation{{GoalID: "approved-goal", Status: "PARTIAL", EvidenceSummary: "sdk remains"}}
			case "question-claim":
				rr.RecommendedNextAction = "REQUEST_USER"
			case "expansion":
				rr.ScopeExpansionRequired = true
			case "mutation":
				ff.After[0].Snapshot = strings.Repeat("a", 40)
			case "head":
				ff.After[0].HEAD = strings.Repeat("a", 40)
			case "metadata":
				ff.After[0].MetadataDigest = strings.Repeat("a", 64)
			case "scope":
				ff.ScopeValid = false
			}
			got, e := Assess(rr, ff)
			if e != nil {
				t.Fatal(e)
			}
			want := "RECOVERY_INSUFFICIENT"
			if kind == "partial" {
				want = "RECOVERED_PARTIAL"
			}
			if kind == "expansion" {
				want = "REVISION_REQUIRED"
			}
			if kind == "mutation" || kind == "head" || kind == "metadata" || kind == "scope" {
				want = "RECOVERY_FAILED"
			}
			if got.EngineeringDisposition != want || got.SourceResponseDisposition != "UNKNOWN" {
				t.Fatalf("%+v want %s", got, want)
			}
		})
	}
}
func TestRecoveryProbeMutationAndRootReplacementPreserved(t *testing.T) {
	r, f := factFixture(t)
	root := f.Workset.Members[0].CanonicalRoot
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("probe mutation"), 0600); err != nil {
		t.Fatal(err)
	}
	after, e := ObserveWorkset(context.Background(), f.Workset)
	if e != nil {
		t.Fatal(e)
	}
	f.After = after
	a, e := Assess(r, f)
	if e != nil || a.EngineeringDisposition != "RECOVERY_FAILED" {
		t.Fatalf("%+v %v", a, e)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "probe mutation" {
		t.Fatal("mutation cleaned")
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	after, e = ObserveWorkset(context.Background(), f.Workset)
	if e == nil || len(after) != 2 || after[0].State != "SCOPE_CONFLICT" {
		t.Fatalf("root replacement unobserved: %+v %v", after, e)
	}
	if _, e = os.Stat(root + "-old"); e != nil {
		t.Fatal("original removed")
	}
}
func TestRecoveryVerificationRequiresObservedChecks(t *testing.T) {
	r, f := factFixture(t)
	r.SourceOperationKind = "VERIFICATION"
	f.Source.OperationKind = "VERIFICATION"
	f.RequiredChecks = []string{"required"}
	a, e := Assess(r, f)
	if e != nil || a.EngineeringDisposition != "RECOVERY_INSUFFICIENT" {
		t.Fatalf("%+v %v", a, e)
	}
	zero := 0
	f.CheckEvidence = []CheckEvidence{{CheckID: "required", Command: "go test ./...", ExpectedCommand: "go test ./...", Passed: true, ExitCode: &zero, EvidenceHash: Hash([]byte("actual-output"))}}
	a, e = Assess(r, f)
	if e != nil || a.EngineeringDisposition != "RECOVERED_COMPLETE" {
		t.Fatalf("%+v %v", a, e)
	}
	f.CheckEvidence[0].Command = "true"
	a, e = Assess(r, f)
	if e != nil || a.EngineeringDisposition != "RECOVERY_INSUFFICIENT" {
		t.Fatalf("%+v %v", a, e)
	}
}

func TestRecoveryReviewRequiresFullV2Contract(t *testing.T) {
	r, f := factFixture(t)
	r.SourceOperationKind = "REVIEW"
	f.Source.OperationKind = "REVIEW"
	a, e := Assess(r, f)
	if e != nil || a.EngineeringDisposition != "RECOVERY_INSUFFICIENT" {
		t.Fatalf("missing full v2 contract: %+v %v", a, e)
	}
	r.ReviewReport = &review.ReviewReport{SchemaVersion: 2, Summary: "Review completed with blocked recommendation", Disposition: review.DispositionBlocked, Coverage: []string{"approved review target"}, Findings: []review.Finding{}, Limitations: []string{"Execution requires a revised proposal"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}}
	raw, _ := json.Marshal(r)
	if _, e := ParseReport(raw, f.Source); e != nil {
		t.Fatal(e)
	}
	a, e = Assess(r, f)
	if e != nil || a.EngineeringDisposition != "RECOVERED_COMPLETE" {
		t.Fatalf("full v2: %+v %v", a, e)
	}
	r.ReviewReport.SchemaVersion = 1
	if _, e = Assess(r, f); e == nil {
		t.Fatal("v1 accepted")
	}
}
func TestRecoveryReportedVerificationSchema(t *testing.T) {
	r, f := factFixture(t)
	r.VerificationReport = json.RawMessage(`{"version":2,"summary":"Observed checks","checks":[{"index":0,"status":"PASSED","summary":"passed","evidence":"stdout","reason":""}],"acceptanceCriteria":[]}`)
	b, _ := json.Marshal(r)
	if _, e := ParseReport(b, f.Source); e != nil {
		t.Fatal(e)
	}
	r.VerificationReport = json.RawMessage(`{"version":2,"summary":"claims pass","checks":[{"index":0,"status":"PASSED"}],"acceptanceCriteria":[]}`)
	b, _ = json.Marshal(r)
	if _, e := ParseReport(b, f.Source); e == nil {
		t.Fatal("incomplete reported verification accepted")
	}
}

func TestRecoverySchemaBindsSourceAsMachineConstants(t *testing.T) {
	source := Source{OperationKind: "EXECUTION", OperationID: "exact-operation", RunID: "exact-run", TurnID: "exact-turn"}
	var schema map[string]any
	raw := ReportSchemaForSource(source)
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	p := schema["properties"].(map[string]any)
	for key, want := range map[string]string{"sourceOperationKind": source.OperationKind, "sourceOperationId": source.OperationID, "sourceRunId": source.RunID, "sourceTurnId": source.TurnID} {
		if p[key].(map[string]any)["const"] != want {
			t.Fatalf("unbound machine field %s", key)
		}
	}
	if Hash(raw) != Hash(ReportSchemaForSource(source)) {
		t.Fatal("unstable source schema hash")
	}
	other := source
	other.TurnID = "other-turn"
	if Hash(raw) == Hash(ReportSchemaForSource(other)) {
		t.Fatal("source turn not bound into schema hash")
	}
}

func TestRecoveryObservationScopesGitMetadataToOwnedWorktree(t *testing.T) {
	_, f := factFixture(t)
	m := f.Workset.Members[0]
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", m.CanonicalRoot}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %s %v", args, b, e)
		}
	}
	observe := func() WorktreeObservation {
		t.Helper()
		o, e := observeMember(context.Background(), m)
		if e != nil {
			t.Fatal(e)
		}
		return o
	}
	before := observe()
	git("worktree", "add", "-b", "unrelated-area", filepath.Join(t.TempDir(), "other"), "HEAD")
	git("pack-refs", "--all")
	after := observe()
	if ObservationsHash([]WorktreeObservation{before}) != ObservationsHash([]WorktreeObservation{after}) {
		t.Fatal("unrelated worktree/ref packing changed owned observations")
	}
	git("add", "file.txt")
	staged := observe()
	if staged.MetadataDigest == after.MetadataDigest {
		t.Fatal("owned index mutation not observed")
	}
	git("config", "recovery.test", "changed")
	configured := observe()
	if configured.MetadataDigest == staged.MetadataDigest {
		t.Fatal("shared configuration mutation not observed")
	}
	git("checkout", "-b", "changed-owned-branch")
	switched := observe()
	if switched.MetadataDigest == configured.MetadataDigest || switched.Branch == configured.Branch {
		t.Fatal("owned HEAD mutation not observed")
	}
}
