package execution

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
)

type cooperativeSourceWriter struct {
	agent.Session
	path     string
	mutation string
}

func (s cooperativeSourceWriter) StartTurn(ctx context.Context, thread, prompt string) (agent.TurnResult, error) {
	if s.mutation == "source-written" {
		if err := os.WriteFile(s.path, []byte("review wrote source\n"), 0600); err != nil {
			return agent.TurnResult{}, err
		}
	}
	git := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", filepath.Dir(s.path)}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %v: %w: %s", args, err, out)
		}
		return nil
	}
	if s.mutation == "commit-dirty" || s.mutation == "stage-only" {
		if err := git("add", "source.txt"); err != nil {
			return agent.TurnResult{}, err
		}
	}
	if s.mutation == "branch-only" {
		if err := git("switch", "-c", "review-branch"); err != nil {
			return agent.TurnResult{}, err
		}
	}
	if s.mutation == "empty-commit" || s.mutation == "commit-dirty" {
		if err := git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "review commit"); err != nil {
			return agent.TurnResult{}, err
		}
	}
	return s.Session.StartTurn(ctx, thread, prompt)
}

type cooperativeReviewFactory struct {
	*cooperativeTestFactory
	path     string
	mutation string
}

func (f cooperativeReviewFactory) StartAgent(ctx context.Context, request agent.LaunchRequest) (agent.Session, error) {
	s, err := f.cooperativeTestFactory.StartAgent(ctx, request)
	if err != nil {
		return s, err
	}
	return cooperativeSourceWriter{Session: s, path: f.path, mutation: f.mutation}, nil
}

func TestCooperativeScalarReviewObservesSourceMutationWithoutRollback(t *testing.T) {
	for _, name := range []string{"unchanged", "source-written", "empty-commit", "commit-dirty", "stage-only", "secondary-stage-only", "secondary-branch-only", "secondary-unchanged"} {
		t.Run(name, func(t *testing.T) {
			_, source := taskPermissionFixture(t)
			root := source.Members[0].CanonicalRoot
			path := filepath.Join(root, "source.txt")
			mutation := strings.TrimPrefix(name, "secondary-")
			var secondary *workset.RuntimeWorkset
			if strings.HasPrefix(name, "secondary-") {
				_, value := taskPermissionFixture(t)
				secondary = &value
				path = filepath.Join(value.Members[0].CanonicalRoot, "source.txt")
			}
			initialState, initialErr := observeCooperativeReviewSource(context.Background(), filepath.Dir(path))
			if initialErr != nil {
				t.Fatal(initialErr)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			c, runs, _, trees, session, _ := successfulCoordinator()
			trees.value.Path, trees.value.RepositoryID, trees.value.BaseCommit = root, "repo-1", source.Members[0].StartHEAD
			reserved := reserveCoordinatorReview(t, c, runs, " inspect only\r\n")
			owner := c.Reviews.(*coordinatorReviewRuntime)
			owner.value.BaseCommit = source.Members[0].StartHEAD
			owner.value.RuntimeConfig.AgentID = "pi"
			owner.value.RuntimeConfig.ExecutionPolicy = agentpolicy.PolicyCooperativeTrustedV1
			owner.value.RuntimeConfig.AgentConfigFingerprint = strings.Repeat("a", 64)
			c.RuntimeConfigProvider = func() codexruntime.Allowlist {
				v := codexruntime.Default()
				v.AgentConfigFingerprints = map[string]string{"pi": strings.Repeat("a", 64)}
				return v
			}
			c.Evidence = evidence.NewCollector(bridgegit.NewClient(bridgegit.Config{}))
			recorder := &cooperativePolicyRecorder{}
			factory := cooperativeReviewFactory{cooperativeTestFactory: &cooperativeTestFactory{session: session, recorder: recorder}, path: path, mutation: mutation}
			c.Agents, err = agent.NewRegistry(agent.Registration[agent.Factory]{Descriptor: factory.AgentDescriptor(), Value: factory})
			if err != nil {
				t.Fatal(err)
			}
			c.Results = recorder
			prepared, err := c.PrepareScopeReview(context.Background(), reserved.ID)
			if err != nil {
				t.Fatal(err)
			}
			var multiRuns *cooperativeV2Runs
			if secondary != nil {
				plan, planErr := c.Workspaces.(*workspace.Service).PlanMembers(context.Background(), reserved.ProjectID, reserved.FeatureAreaID, []workset.Target{{RepositoryID: "repo-1", OriginCommit: source.Members[0].OriginCommit, Read: true, Write: workset.WriteScope{Kind: "NONE"}}, {RepositoryID: "repo-2", OriginCommit: secondary.Members[0].OriginCommit, Read: true, Write: workset.WriteScope{Kind: "NONE"}}})
				if planErr != nil {
					t.Fatal(planErr)
				}
				prepared.review.RepositoryTargets = plan.Targets
				prepared.review.WorkspaceID = plan.WorkspaceID
				prepared.review.PrimaryMemberID = plan.Targets[0].MemberID
				owner.value = *prepared.review
				frozen := source
				frozen.AgentID, frozen.ExecutionPolicy, frozen.AgentConfigFingerprint = "pi", agentpolicy.PolicyCooperativeTrustedV1, strings.Repeat("a", 64)
				frozen.RuntimeAdapterIdentity, frozen.RuntimeProtocolIdentity = agentpolicy.CooperativeAdapterIdentity, agentpolicy.CooperativeProtocolIdentity
				frozen.TaskID, frozen.TaskDefinitionRevisionID, frozen.ExecutionID = "", "", ""
				frozen.ProjectID, frozen.FeatureAreaID, frozen.ReviewID, frozen.RunID, frozen.WorkspaceID = reserved.ProjectID, reserved.FeatureAreaID, reserved.ReviewID, reserved.ID, plan.WorkspaceID
				frozen.BackendConfigID, frozen.BackendConfigFingerprint = owner.value.BackendSelection.ConfigID, owner.value.BackendSelection.ConfigFingerprint
				frozen.Model, frozen.ReasoningEffort = owner.value.RuntimeConfig.Model, owner.value.RuntimeConfig.ReasoningEffort
				frozen.CreatedAt = time.Now().UTC()
				first := frozen.Members[0]
				first.RepositoryID, first.MemberID, first.WorktreeID = "repo-1", plan.Targets[0].MemberID, trees.value.ID
				first.Write = workset.WriteScope{Kind: "NONE"}
				second := secondary.Members[0]
				second.RepositoryID, second.MemberID, second.WorktreeID = "repo-2", plan.Targets[1].MemberID, "tree-2"
				second.RepositoryAlias = "secondary"
				second.Write = workset.WriteScope{Kind: "NONE"}
				frozen.PrimaryMemberID = first.MemberID
				frozen.Members = []workset.RuntimeMemberBinding{first, second}
				frozen, err = frozen.WithHash()
				if err != nil {
					t.Fatal(err)
				}
				cwd, binding, bindingErr := multiRuntimeClientBinding(frozen)
				if bindingErr != nil {
					t.Fatal(bindingErr)
				}
				binding.WritableRoots = nil
				prepared.multiRuntime = &PreparedMultiRuntime{Workset: frozen, CWD: cwd, Binding: binding, ReadOnly: true}
				multiRuns = &cooperativeV2Runs{multiReviewRuns: &multiReviewRuns{recordingRuns: runs, bindings: map[string]run.RepositoryBinding{}}, frozen: &frozen}
				c.Runs = multiRuns
			}
			completed, err := c.ExecuteRun(context.Background(), prepared, nil)
			if mutation != "unchanged" {
				if err == nil || completed.Status != run.StatusInfrastructureFailed || completed.Failure == nil || completed.Failure.Code != "REVIEW_MUTATED_WORKTREE" {
					t.Fatalf("mutating Review run=%+v err=%v", completed, err)
				}
				after, readErr := os.ReadFile(path)
				wantBytes := string(before)
				if mutation == "source-written" {
					wantBytes = "review wrote source\n"
				}
				if readErr != nil || string(after) != wantBytes {
					t.Fatalf("mutation rolled back/lost: %q %v", after, readErr)
				}
				if completed.Result == nil || (mutation == "source-written" && !strings.Contains(completed.Result.GitEvidence.Diff, "review wrote source")) {
					t.Fatalf("source mutation evidence lost: %+v", completed.Result)
				}
				if mutation != "source-written" {
					if completed.StartSnapshot != completed.EndSnapshot {
						t.Fatal("fixture must keep source snapshot identical")
					}
					if !strings.Contains(completed.Failure.Message, "before=") || !strings.Contains(completed.Failure.Message, "after=") {
						t.Fatal("metadata observations not retained")
					}
					state, stateErr := observeCooperativeReviewSource(context.Background(), filepath.Dir(path))
					if stateErr != nil {
						t.Fatal(stateErr)
					}
					if mutation == "stage-only" && state.Index == initialState.Index {
						t.Fatal("staged index rolled back")
					}
					if mutation == "branch-only" && state.Branch != "refs/heads/review-branch" {
						t.Fatal("branch mutation rolled back")
					}
					if mutation != "stage-only" && mutation != "branch-only" && (state.HEAD == source.Members[0].StartHEAD || completed.Result.GitEvidence.HeadCommit != state.HEAD) {
						t.Fatal("committed HEAD rolled back or evidence lost")
					}
				}
			} else {
				if err != nil || completed.Status != run.StatusSucceeded {
					t.Fatalf("unchanged Review run=%+v err=%v", completed, err)
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || string(after) != string(before) {
					t.Fatal("unchanged source was modified")
				}
			}
			if multiRuns != nil {
				if len(multiRuns.observations) != 2 {
					t.Fatalf("missing member observations: %+v", multiRuns.observations)
				}
				for _, observed := range multiRuns.observations {
					want := "NO_CHANGE"
					if observed.RepositoryID == "repo-2" && (mutation == "stage-only" || mutation == "branch-only") {
						want = "SCOPE_CONFLICT"
					}
					if observed.ObservationState != want {
						t.Fatalf("member observation=%+v want=%s", observed, want)
					}
				}
			}
			if session.prompt != reserved.CodexInstruction {
				t.Fatal("frozen review instruction changed")
			}
			if len(recorder.policies) != 1 || !recorder.policies[0].ReadOnly || len(recorder.policies[0].WritableRoots) != 0 {
				t.Fatal("review source policy was not read-only")
			}
		})
	}
}
