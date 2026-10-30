package execution

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

type qualifiedRecoveryRuns struct {
	RunService
	value run.Run
}

func (r qualifiedRecoveryRuns) Get(context.Context, string) (run.Run, error) { return r.value, nil }

type qualifiedRecoveryCheckpoints struct {
	raw    json.RawMessage
	writes int
}

func (s *qualifiedRecoveryCheckpoints) AppServerCheckpoint(context.Context, string) (json.RawMessage, error) {
	return s.raw, nil
}
func (s *qualifiedRecoveryCheckpoints) BindAppServer(context.Context, string, appserver.Binding) error {
	s.writes++
	return errors.New("unexpected binding write")
}
func (s *qualifiedRecoveryCheckpoints) SaveAppServerCheckpoint(context.Context, string, json.RawMessage) error {
	s.writes++
	return errors.New("unexpected checkpoint write")
}

type recoveryLaunchPath string

func (p recoveryLaunchPath) Executable(ctx context.Context) (string, error) {
	return string(p), ctx.Err()
}

type qualifiedRecoveryVerifier struct {
	err     error
	path    string
	checked []taskgoal.Execution
}

func (v *qualifiedRecoveryVerifier) VerifyExecution(context.Context, taskgoal.Execution) error {
	panic("recovery must use atomic launch verification")
}
func (v *qualifiedRecoveryVerifier) VerifiedExecutionLaunch(_ context.Context, e taskgoal.Execution) (codexlaunch.VerifiedExecutable, error) {
	v.checked = append(v.checked, e)
	if v.err != nil {
		return nil, v.err
	}
	return recoveryLaunchPath(v.path), nil
}

func recoveryQualifiedIdentity() (run.Run, executionCheckpoint, taskgoal.Execution, appserver.Binding) {
	b := &codexconfig.BackendSelection{ConfigID: "backend", ConfigFingerprint: "fingerprint", Source: "EXPLICIT"}
	current := run.Run{ID: "run", ProjectID: "project", TaskID: "task", FeatureAreaID: "area", ExecutionProposalID: "execution", OperationKind: run.OperationExecution, OperationID: "execution", Purpose: run.PurposeImplementation, Status: run.StatusRunning, ThreadID: "thread", TurnID: "turn"}
	cp := executionCheckpoint{Version: 1, RunID: current.ID, Purpose: current.Purpose, BackendSelection: b, Task: task.Task{ID: current.TaskID, ProjectID: current.ProjectID, FeatureAreaID: current.FeatureAreaID, CurrentDefinitionRevisionID: "revision"}}
	digest := strings.Repeat("a", 64)
	value := taskgoal.Execution{ID: "execution", ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", RunID: "run", VerificationRunID: "verification", Status: "CONSUMED", Outcome: "RUNNING", BackendSelection: b, RuntimeAdapterIdentity: `{"adapterId":"devbridge-codex","adapterImplementationDigest":"` + digest + `"}`, RuntimeProtocolIdentity: `{"protocolId":"codex-app-server/codex-cli 0.159.2","protocolSchemaDigest":"` + digest + `"}`}
	return current, cp, value, appserver.Binding{ExecutablePath: "/qualified/native"}
}

// Exercise the public cold recovery entry. Its persisted checkpoint is valid;
// the original platform authority or process identity has become unusable.
// Neither condition may reach transport attachment or mutate durable state.
func TestQualifiedAppServerRecoveryRejectsInvalidatedAndForeignNative(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault error
		path  string
		want  error
	}{
		{"invalidated", runtimeplatform.ErrQualificationInvalidated, "/qualified/native", runtimeplatform.ErrQualificationInvalidated},
		{"foreign native", nil, "/another/native", runtimeplatform.ErrIdentityMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current, cp, value, binding := recoveryQualifiedIdentity()
			raw, err := json.Marshal(cp)
			if err != nil {
				t.Fatal(err)
			}
			checkpoints := &qualifiedRecoveryCheckpoints{raw: raw}
			verifier := &qualifiedRecoveryVerifier{err: tc.fault, path: tc.path}
			c := &Coordinator{Runs: qualifiedRecoveryRuns{value: current}, AppServers: checkpoints, Executions: executionSnapshotLookup{value: value}, QualifiedRuntime: verifier}
			supervisor, err := NewRuntimeSupervisor(c)
			if err != nil {
				t.Fatal(err)
			}
			defer supervisor.cancelRoot(nil)
			got, err := supervisor.RecoverAppServerRun(context.Background(), current.ID, binding)
			if !errors.Is(err, tc.want) {
				t.Fatalf("recovery did not refuse authority/process mismatch: %v", err)
			}
			if !reflect.DeepEqual(got, current) || checkpoints.writes != 0 || len(supervisor.sessions) != 0 {
				t.Fatalf("refused recovery changed durable/live state: %+v writes=%d", got, checkpoints.writes)
			}
			if len(verifier.checked) != 1 || !reflect.DeepEqual(verifier.checked[0], value) {
				t.Fatalf("recovery did not verify exact stored Execution: %+v", verifier.checked)
			}
		})
	}
}

func TestQualifiedRecoveryUsesExactExecutionOwnerBeforeLaunch(t *testing.T) {
	for _, phase := range []run.Purpose{run.PurposeImplementation, run.PurposeVerification} {
		t.Run(string(phase), func(t *testing.T) {
			current, cp, value, binding := recoveryQualifiedIdentity()
			current.Purpose, cp.Purpose = phase, phase
			if phase == run.PurposeVerification {
				current.ID, cp.RunID = value.VerificationRunID, value.VerificationRunID
			}
			verifier := &qualifiedRecoveryVerifier{path: binding.ExecutablePath}
			c := &Coordinator{Executions: executionSnapshotLookup{value: value}, QualifiedRuntime: verifier}
			actual, err := c.qualifiedRecoveryExecution(context.Background(), current, cp, binding)
			if err != nil || actual == nil || !reflect.DeepEqual(*actual, value) {
				t.Fatalf("exact recovery lost original Execution: %+v %v", actual, err)
			}
			for _, field := range []string{"task", "project", "revision", "run", "backend"} {
				t.Run(field, func(t *testing.T) {
					forged := value
					switch field {
					case "task":
						forged.TaskID = "foreign"
					case "project":
						forged.ProjectID = "foreign"
					case "revision":
						forged.TaskDefinitionRevisionID = "foreign"
					case "run":
						forged.RunID = "foreign"
						forged.VerificationRunID = "foreign"
					case "backend":
						other := *value.BackendSelection
						other.ConfigID = "foreign"
						forged.BackendSelection = &other
					}
					bad := &Coordinator{Executions: executionSnapshotLookup{value: forged}, QualifiedRuntime: verifier}
					before := len(verifier.checked)
					if _, err := bad.qualifiedRecoveryExecution(context.Background(), current, cp, binding); !errors.Is(err, runtimeplatform.ErrIdentityMismatch) {
						t.Fatalf("foreign %s accepted: %v", field, err)
					}
					if len(verifier.checked) != before {
						t.Fatal("foreign owner reached qualification reader")
					}
				})
			}
		})
	}
}
