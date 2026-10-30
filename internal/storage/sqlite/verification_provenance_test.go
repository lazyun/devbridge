package sqlite

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestVerificationAcceptanceRequiresExactObservedMember(t *testing.T) {
	w := workset.RuntimeWorkset{ProtocolVersion: 1, ProjectID: "p", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", ExecutionID: "execution", RunID: "verify", WorkspaceID: "workspace", PrimaryMemberID: "member-app", BackendConfigID: "backend", BackendConfigFingerprint: "fingerprint", Model: "model", ReasoningEffort: "low", RuntimeAdapterIdentity: "adapter", RuntimeProtocolIdentity: "protocol", CreatedAt: time.Now()}
	spec := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "both", CodexInstruction: "verify", AcceptanceCriteria: []string{"both pass"}}
	report := run.VerificationReport{Version: 2, Availability: run.VerificationReportAvailable, Summary: "observed checks", AcceptanceCriteria: []run.VerificationCriterionOutcome{{Index: 0, Criterion: "both pass", Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "both pass"}}}}
	for i, id := range []string{"app", "sdk"} {
		w.Members = append(w.Members, workset.RuntimeMemberBinding{RepositoryID: id, RepositoryAlias: id, MemberID: "member-" + id, MemberVersion: 1, WorktreeID: "wt-" + id, CanonicalRoot: "/managed/" + id, RootFileIdentity: "root-" + id, OriginCommit: strings.Repeat("a", 40), StartHEAD: strings.Repeat("a", 40), StartSnapshot: strings.Repeat("b", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: "runtime-" + id})
		spec.Checks = append(spec.Checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true, RepositoryID: id, RepositoryIDs: []string{id}, WorkingDirectory: "."})
		zero := 0
		report.Checks = append(report.Checks, run.VerificationCheckOutcome{ID: []string{"check-1", "check-2"}[i], Index: i, Kind: string(task.VerificationCheckTestCommand), Required: true, DeclaredCommand: "go test ./...", Observed: &run.VerificationObservedFact{Status: run.VerificationCheckPassed, Command: "go test ./...", ExitCode: &zero, Summary: "completed", Source: &run.VerificationObservationSource{Kind: "COMMAND", RunID: w.RunID, RepositoryID: id, MemberID: "member-" + id, MemberVersion: 1, CWD: "/managed/" + id, ItemID: "cmd-" + id, CommandStatus: "completed"}}, Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "reported"}})
	}
	var err error
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	for i := range report.Checks {
		report.Checks[i].Observed.Source.RuntimeWorksetHash = w.ContentHash
	}
	if !authoritativeVerificationPasses(report, spec) || !verificationObservationBindingsMatch(report, spec, w) || !run.VerificationEvidenceComplete(report, spec, &w) {
		t.Fatal("exact member evidence rejected")
	}
	for _, kind := range []string{"legacy-no-provenance", "other-member", "other-cwd", "other-run", "other-hash", "member-version", "missing-item", "pending-command", "missing-exit", "nonzero-exit", "action-only"} {
		t.Run(kind, func(t *testing.T) {
			raw, _ := json.Marshal(report)
			var mutated run.VerificationReport
			if err := json.Unmarshal(raw, &mutated); err != nil {
				t.Fatal(err)
			}
			fact := mutated.Checks[1].Observed
			switch kind {
			case "legacy-no-provenance":
				fact.Source = nil
			case "other-member":
				source := *mutated.Checks[0].Observed.Source
				fact.Source = &source
			case "other-cwd":
				fact.Source.CWD = "/managed/app"
			case "other-run":
				fact.Source.RunID = "implementation"
			case "other-hash":
				fact.Source.RuntimeWorksetHash = strings.Repeat("f", 64)
			case "member-version":
				fact.Source.MemberVersion = 2
			case "missing-item":
				fact.Source.ItemID = ""
			case "pending-command":
				fact.Source.CommandStatus = "inProgress"
			case "missing-exit":
				fact.ExitCode = nil
			case "nonzero-exit":
				one := 1
				fact.ExitCode = &one
				fact.Status = run.VerificationCheckFailed
			case "action-only":
				fact.Command = "cd ../app && go test ./..."
			}
			if authoritativeVerificationPasses(mutated, spec) && verificationObservationBindingsMatch(mutated, spec, w) {
				t.Fatalf("accepted unproven evidence: %+v", fact)
			}
			if complete := run.VerificationEvidenceComplete(mutated, spec, &w); complete != (kind == "nonzero-exit") {
				t.Fatalf("acceptance evidence completeness=%t for %s", complete, kind)
			}
		})
	}
	report.AcceptanceCriteria[0].Reported.Status = run.VerificationCheckFailed
	if !run.VerificationEvidenceComplete(report, spec, &w) || authoritativeVerificationPasses(report, spec) {
		t.Fatal("user acceptance and successful Recovery assessment must remain distinct")
	}
}
