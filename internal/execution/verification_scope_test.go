package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func qualifiedVerificationFixture(t *testing.T) (task.VerificationSpec, string, *verificationScope, []ObservedCommand) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := workset.RuntimeWorkset{ProtocolVersion: 1, ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", ExecutionID: "execution", RunID: "verification-run", WorkspaceID: "workspace", PrimaryMemberID: "member-app", BackendConfigID: "backend", BackendConfigFingerprint: "fingerprint", Model: "model", ReasoningEffort: "low", RuntimeAdapterIdentity: "adapter", RuntimeProtocolIdentity: "protocol", CreatedAt: time.Now()}
	spec := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "both", CodexInstruction: "check both", AcceptanceCriteria: []string{"both pass"}}
	reported := reportedVerificationV2{Version: 2, Summary: "checks", AcceptanceCriteria: []reportedVerificationItem{{Index: 0, Status: run.VerificationCheckPassed, Summary: "both pass"}}}
	var commands []ObservedCommand
	for i, id := range []string{"app", "sdk"} {
		dir := filepath.Join(root, id)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		w.Members = append(w.Members, workset.RuntimeMemberBinding{RepositoryID: id, RepositoryAlias: id, MemberID: "member-" + id, MemberVersion: 1, WorktreeID: "wt-" + id, CanonicalRoot: dir, RootFileIdentity: "root-" + id, OriginCommit: strings.Repeat("a", 40), StartHEAD: strings.Repeat("a", 40), StartSnapshot: strings.Repeat("b", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: "runtime-" + id})
		spec.Checks = append(spec.Checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true, RepositoryID: id, RepositoryIDs: []string{id}, WorkingDirectory: "."})
		reported.Checks = append(reported.Checks, reportedVerificationItem{Index: i, Status: run.VerificationCheckPassed, Summary: "reported pass"})
		commands = append(commands, ObservedCommand{ItemID: "cmd-" + id, Command: "go test ./...", CWD: dir, Status: "completed", ExitCode: exitCode(0)})
	}
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reported)
	return spec, string(raw), &verificationScope{workset: w, gitByRepository: map[string]evidence.GitEvidence{}}, commands
}

func TestQualifiedVerificationSeparatesRepositories(t *testing.T) {
	spec, msg, scope, commands := qualifiedVerificationFixture(t)
	report, err := buildVerificationReportWithScope(spec, msg, commands[:1], evidence.GitEvidence{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if report.Checks[0].Observed.Status != run.VerificationCheckPassed || report.Checks[1].Observed.Status != run.VerificationCheckNotRun {
		t.Fatalf("one command used for both repositories: %+v", report.Checks)
	}
	if report.Availability != run.VerificationReportInsufficient || run.ValidateVerificationReportV2(report) != nil || run.VerificationEvidenceComplete(report, spec, &scope.workset) {
		t.Fatalf("partial observations must remain readable but insufficient: %+v", report)
	}
	report, err = buildVerificationReportWithScope(spec, msg, commands, evidence.GitEvidence{}, scope)
	if err != nil || run.ValidateVerificationReportV2(report) != nil {
		t.Fatalf("report: %+v %v", report, err)
	}
	if report.Availability != run.VerificationReportAvailable || !run.VerificationEvidenceComplete(report, spec, &scope.workset) {
		t.Fatal("complete observations unavailable")
	}
	raw, _ := json.Marshal(report)
	var decoded run.VerificationReport
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatal("decode")
	}
	for i, check := range decoded.Checks {
		if check.Observed.Status != run.VerificationCheckPassed || !check.Observed.Source.Matches(scope.workset, spec.Checks[i].RepositoryID, ".") || check.Observed.Source.ItemID != commands[i].ItemID {
			t.Fatalf("lost provenance: %+v", check)
		}
	}
}

func TestQualifiedVerificationRejectsUnprovenCommand(t *testing.T) {
	for _, kind := range []string{"foreign-root", "missing-cwd", "missing-item", "pending", "action-only", "shell-cd", "later-failure", "unbound", "symlink-escape"} {
		t.Run(kind, func(t *testing.T) {
			spec, msg, scope, commands := qualifiedVerificationFixture(t)
			switch kind {
			case "foreign-root":
				commands[0].CWD = commands[1].CWD
			case "missing-cwd":
				commands[0].CWD = ""
			case "missing-item":
				commands[0].ItemID = ""
			case "pending":
				commands[0].Status = "inProgress"
			case "action-only":
				commands[0].Command = "true"
				commands[0].Actions = []string{spec.Checks[0].Command}
			case "shell-cd":
				commands[0].Command = "/bin/zsh -lc 'cd ../sdk && go test ./...'"
				commands[0].Actions = []string{spec.Checks[0].Command}
			case "later-failure":
				later := commands[0]
				later.ItemID = "later"
				later.ExitCode = exitCode(1)
				commands = append(commands, later)
			case "unbound":
				scope = nil
			case "symlink-escape":
				link := filepath.Join(commands[0].CWD, "elsewhere")
				if err := os.Symlink(commands[1].CWD, link); err != nil {
					t.Fatal(err)
				}
				spec.Checks[0].WorkingDirectory = "elsewhere"
				commands[0].CWD = link
			}
			report, err := buildVerificationReportWithScope(spec, msg, commands, evidence.GitEvidence{}, scope)
			if err != nil {
				t.Fatal(err)
			}
			if report.Checks[0].Observed.Status == run.VerificationCheckPassed {
				t.Fatalf("unproven passed: %+v", report.Checks[0])
			}
			if kind == "later-failure" {
				if report.Availability != run.VerificationReportAvailable || !run.VerificationEvidenceComplete(report, spec, &scope.workset) {
					t.Fatal("observed failure must remain eligible for user acceptance")
				}
			} else if report.Availability != run.VerificationReportInsufficient {
				t.Fatal("missing observation must be evidence-insufficient")
			}
		})
	}
}

func TestQualifiedVerificationShellWrapperAndMemberGit(t *testing.T) {
	spec, msg, scope, commands := qualifiedVerificationFixture(t)
	commands[0].Command = "/bin/zsh -lc 'go test ./...'"
	report, err := buildVerificationReportWithScope(spec, msg, commands, evidence.GitEvidence{}, scope)
	if err != nil || report.Checks[0].Observed.Status != run.VerificationCheckPassed {
		t.Fatalf("exact wrapper: %+v %v", report, err)
	}
	spec.Checks[0].Kind = task.VerificationCheckGitAssertion
	spec.Checks[0].Command = ""
	spec.Checks[0].Instruction = "worktree is unchanged"
	scope.gitByRepository["sdk"] = evidence.GitEvidence{StartSnapshot: "same", EndSnapshot: "same"}
	report, err = buildVerificationReportWithScope(spec, msg, commands, evidence.GitEvidence{StartSnapshot: "same", EndSnapshot: "same"}, scope)
	if err != nil || report.Checks[0].Observed.Status == run.VerificationCheckPassed {
		t.Fatalf("foreign member Git used: %+v %v", report, err)
	}
	scope.gitByRepository["app"] = evidence.GitEvidence{StartSnapshot: "same", EndSnapshot: "same"}
	report, err = buildVerificationReportWithScope(spec, msg, commands, evidence.GitEvidence{}, scope)
	if err != nil || report.Checks[0].Observed.Status != run.VerificationCheckPassed || report.Checks[0].Observed.Source.Kind != "GIT" {
		t.Fatalf("exact member Git: %+v %v", report, err)
	}
}

func TestCustomCommandsStillRequireExactObservedExecution(t *testing.T) {
	spec, msg, scope, commands := qualifiedVerificationFixture(t)
	spec.Checks[0].Kind = task.VerificationCheckCustom
	report, err := buildVerificationReportWithScope(spec, msg, commands, evidence.GitEvidence{}, scope)
	if err != nil || report.Availability != run.VerificationReportAvailable || report.Checks[0].Observed == nil || report.Checks[0].Observed.Source.ItemID != commands[0].ItemID {
		t.Fatalf("custom command lost actual evidence: %+v %v", report, err)
	}
	commands[0].CWD = commands[1].CWD
	report, err = buildVerificationReportWithScope(spec, msg, commands, evidence.GitEvidence{}, scope)
	if err != nil || report.Availability != run.VerificationReportInsufficient {
		t.Fatal("custom command borrowed another repository's execution")
	}
}
