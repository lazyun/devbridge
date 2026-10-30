package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepermission"
	"github.com/lazyun/devbridge/internal/task"
)

func TestProjectTrustParentBothNetworkSourcesRemainFrozen(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		c, w := taskPermissionFixture(t)
		c.Tasks = &fakeTasks{value: task.Task{ID: w.TaskID}, revision: &task.DefinitionRevision{ID: w.TaskDefinitionRevisionID, TaskID: w.TaskID, Definition: task.Definition{WorkSpec: task.WorkSpec{CommandPermissions: &task.CommandPermissions{NetworkAccess: true}}}}}
		c.ProjectAuthorizations = trustedAuthorization(trusted, true)
		prepared, err := c.prepareTaskPermissions(context.Background(), w, true)
		if err != nil {
			t.Fatal(err)
		}
		p := prepared.TaskPermissions
		if p.TrustedExecution == nil || !p.NetworkAccess || !p.TrustedExecution.NetworkFromAuthorization || p.HasTrustedExecution() != trusted {
			t.Fatalf("project capability was lost when the task also granted network: %+v", p)
		}
		if strings.Contains(p.Instructions(), "not from the task definition") {
			t.Fatal("network instructions claim an exclusive source that is not observable")
		}
	}
}

func TestTrustedQualifiedVerificationPreservesFrozenPermissionProvenance(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "root"
		if nested {
			name = "nested"
		}
		t.Run(name, func(t *testing.T) {
			spec, message, scope, commands := qualifiedVerificationFixture(t)
			w := &scope.workset
			p := &runtimepermission.TaskPermissions{Version: runtimepermission.TaskPermissionVersion, TaskID: w.TaskID, RevisionID: w.TaskDefinitionRevisionID, ExecutionID: w.ExecutionID, RunID: w.RunID, WorksetHash: w.ContentHash, Purpose: "VERIFICATION", ScratchRoot: filepath.Join(filepath.Dir(w.Members[0].CanonicalRoot), ".task-runtime", "run-"+runtimepermission.EnvironmentID(w.RunID), "scratch"), TrustedExecution: &runtimepermission.TrustedExecutionSnapshot{ProjectID: w.ProjectID, AuthorizationRevision: 1, Trusted: true}}
			p.ContentHash = p.Hash()
			w.TaskPermissions = p
			if err := w.Validate(); err != nil {
				t.Fatal(err)
			}
			if nested {
				for i := range commands {
					spec.Checks[i].WorkingDirectory = "frontend"
					commands[i].CWD = filepath.Join(commands[i].CWD, "frontend")
					if err := os.Mkdir(commands[i].CWD, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			report, err := buildVerificationReportWithScope(spec, message, commands, evidence.GitEvidence{}, scope)
			if err != nil || report.Availability != run.VerificationReportAvailable || !run.VerificationEvidenceComplete(report, spec, w) {
				t.Fatalf("trusted original-CWD evidence unavailable: %+v %v", report, err)
			}
			for i, check := range report.Checks {
				source := check.Observed.Source
				if source == nil || source.TaskPermissionHash != p.ContentHash || source.CWD != commands[i].CWD || !source.Matches(*w, spec.Checks[i].RepositoryID, spec.Checks[i].WorkingDirectory) {
					t.Fatalf("missing exact frozen permission provenance: %+v", source)
				}
				for _, invalid := range []string{"stripped-hash", "wrong-hash", "foreign-cwd", "foreign-run", "foreign-workset"} {
					mutated := *source
					switch invalid {
					case "stripped-hash":
						mutated.TaskPermissionHash = ""
					case "wrong-hash":
						mutated.TaskPermissionHash = strings.Repeat("f", 64)
					case "foreign-cwd":
						mutated.CWD = commands[(i+1)%len(commands)].CWD
					case "foreign-run":
						mutated.RunID = "another-run"
					case "foreign-workset":
						mutated.RuntimeWorksetHash = strings.Repeat("f", 64)
					}
					if mutated.Matches(*w, spec.Checks[i].RepositoryID, spec.Checks[i].WorkingDirectory) {
						t.Fatalf("accepted %s", invalid)
					}
				}
				changed := *w
				changed.TaskPermissions = p.Clone()
				changed.TaskPermissions.NetworkAccess = true
				changed.TaskPermissions.ContentHash = changed.TaskPermissions.Hash()
				if source.Matches(changed, spec.Checks[i].RepositoryID, spec.Checks[i].WorkingDirectory) {
					t.Fatal("old command became evidence for a different frozen permission policy")
				}
			}
		})
	}
}
