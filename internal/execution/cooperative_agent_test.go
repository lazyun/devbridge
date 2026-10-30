package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
)

type cooperativePolicyRecorder struct {
	policies              []agentpolicy.Envelope
	receipts              []agentpolicy.LaunchReceipt
	policyErr, receiptErr error
}

func (r *cooperativePolicyRecorder) RecordAgentPolicy(_ context.Context, _ string, value agentpolicy.Envelope) error {
	if r.policyErr != nil {
		return r.policyErr
	}
	r.policies = append(r.policies, value)
	return nil
}
func (r *cooperativePolicyRecorder) RecordAgentPolicyReceipt(_ context.Context, _ string, value agentpolicy.LaunchReceipt) error {
	if r.receiptErr != nil {
		return r.receiptErr
	}
	r.receipts = append(r.receipts, value)
	return nil
}
func (r *cooperativePolicyRecorder) Record(_ context.Context, _ string, claim evidence.AgentClaim, git evidence.GitEvidence) (run.Result, error) {
	return run.Result{AgentClaim: claim, GitEvidence: git, Artifacts: evidence.Artifacts(claim, git)}, nil
}

type cooperativeTestFactory struct {
	session     *fakeSession
	recorder    *cooperativePolicyRecorder
	request     agent.LaunchRequest
	starts      int
	receiptMode string
	fingerprint string
}

func (f *cooperativeTestFactory) AgentDescriptor() agent.Descriptor {
	fingerprint := f.fingerprint
	if fingerprint == "" {
		fingerprint = strings.Repeat("a", 64)
	}
	return agent.Descriptor{ConfigFingerprint: fingerprint, ID: "pi", Capabilities: []agent.Capability{agent.CapabilityCooperativePolicy}}
}
func (f *cooperativeTestFactory) StartAgent(ctx context.Context, request agent.LaunchRequest) (agent.Session, error) {
	f.starts++
	f.request = request
	if len(f.recorder.policies) != 1 {
		return nil, errors.New("policy not recorded before transport")
	}
	if request.Policy == nil || request.Policy.Validate() != nil {
		return nil, errors.New("invalid policy")
	}
	if f.receiptMode == "absent" {
		return f.session, nil
	}
	receipt := agentpolicy.LaunchReceipt{AgentConfigFingerprint: request.AgentConfigFingerprint, RunID: request.RunID, AgentID: "pi", PolicySHA256: request.Policy.SHA256, Mechanism: "TEST_CONFIGURED", Status: agentpolicy.LaunchConfigured}
	if f.receiptMode == "foreign-fingerprint" {
		receipt.AgentConfigFingerprint = strings.Repeat("b", 64)
	}
	if f.receiptMode == "foreign" {
		receipt.RunID = "foreign-run"
	}
	err := request.OnPolicyConfigured(receipt)
	if f.receiptMode == "duplicate" {
		_ = request.OnPolicyConfigured(receipt)
		return f.session, nil
	}
	return f.session, err
}

func cooperativeCoreFixture(t *testing.T) (*Coordinator, PreparedRun, *cooperativeTestFactory, *cooperativePolicyRecorder, *fakeTasks) {
	t.Helper()
	c, runs, tasks, trees, backend, _ := successfulCoordinator()
	trees.value.Path = t.TempDir()
	instruction := " \timplement e\u0301 中文\r\n "
	tasks.value.WorkSpec.CodexInstruction = instruction
	tasks.value.FeatureAreaID = "feature-cooperative"
	revisionID := "definition-cooperative"
	tasks.revision = &task.DefinitionRevision{ID: revisionID, TaskID: tasks.value.ID, Ordinal: 1, Definition: task.DefinitionFromTask(tasks.value, nil)}
	value := run.Run{ID: "run-cooperative", ProjectID: tasks.value.ProjectID, FeatureAreaID: tasks.value.FeatureAreaID, TaskID: tasks.value.ID, AgentExecutionID: "execution-cooperative", OperationKind: run.OperationExecution, OperationID: "execution-cooperative", ExecutionProposalID: "execution-cooperative", Purpose: run.PurposeImplementation, Status: run.StatusCreated, Attempt: 1, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}}
	config := codexconfig.Config{ID: "config-1", Enabled: true}
	selected := codexruntime.NextRunRuntimeConfig{AgentConfigFingerprint: strings.Repeat("a", 64), AgentID: "pi", ExecutionPolicy: agentpolicy.PolicyCooperativeTrustedV1, Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	proposal := taskgoal.Execution{ID: value.ExecutionProposalID, TaskID: tasks.value.ID, ProjectID: tasks.value.ProjectID, Status: "CONSUMED", Outcome: "RUNNING", RunID: value.ID, TaskDefinitionRevisionID: revisionID, RuntimeConfig: selected, RuntimeAdapterIdentity: agentpolicy.CooperativeAdapterIdentity, RuntimeProtocolIdentity: agentpolicy.CooperativeProtocolIdentity, BackendSelection: backendSelectionPtr(config.Selection("execution-unit-test"))}
	runs.store = run.NewMemoryStore(value)
	runs.service = run.NewService(runs.store, nil, nil)
	c.Executions = executionSnapshotLookup{value: proposal}
	c.RuntimeConfigProvider = func() codexruntime.Allowlist {
		v := codexruntime.Default()
		v.AgentConfigFingerprints = map[string]string{"pi": strings.Repeat("a", 64)}
		return v
	}
	c.Conversations = &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "cooperative-conversation", FeatureAreaID: tasks.value.FeatureAreaID, ContinuationSafe: true}, bindRun: func(runID, threadID string) error {
		_, err := runs.AttachThread(context.Background(), runID, threadID)
		return err
	}}
	recorder := &cooperativePolicyRecorder{}
	factory := &cooperativeTestFactory{session: backend, recorder: recorder}
	registry, err := agent.NewRegistry(agent.Registration[agent.Factory]{Descriptor: factory.AgentDescriptor(), Value: factory})
	if err != nil {
		t.Fatal(err)
	}
	c.Agents = registry
	c.Results = recorder
	prepared, err := c.PrepareReservedRun(context.Background(), value.ID, Instruction{CodexInstruction: instruction, ExecutionScope: tasks.value.WorkSpec.ExecutionScope})
	if err != nil {
		t.Fatal(err)
	}
	return c, prepared, factory, recorder, tasks
}

func TestRegisteredCooperativeAgentCompletesCoreRunWithoutPromptRewrite(t *testing.T) {
	c, prepared, factory, recorder, tasks := cooperativeCoreFixture(t)
	completed, err := c.ExecuteRun(context.Background(), prepared, nil)
	if err != nil || completed.Status != run.StatusSucceeded {
		t.Fatalf("run=%+v err=%v", completed, err)
	}
	if factory.starts != 1 || len(recorder.policies) != 1 || len(recorder.receipts) != 1 {
		t.Fatalf("launch ordering incomplete: starts=%d policies=%d receipts=%d", factory.starts, len(recorder.policies), len(recorder.receipts))
	}
	if factory.session.prompt != tasks.value.WorkSpec.CodexInstruction || completed.FinalCodexPrompt != factory.session.prompt {
		t.Fatal("frozen instruction bytes changed")
	}
	if strings.Contains(factory.session.prompt, "cooperative trusted") || factory.request.Policy.SHA256 != recorder.policies[0].SHA256 || recorder.receipts[0].Status != agentpolicy.LaunchConfigured {
		t.Fatal("system policy leaked into task prompt or changed receipt")
	}
	if tasks.value.Status != task.StatusActive || tasks.value.Phase != task.PhaseImplementing {
		t.Fatal("agent success decided task acceptance")
	}
	if factory.request.Backend.Executable != "" {
		t.Fatal("Codex config executable was reinterpreted as the registered agent")
	}
	if completed.TokenUsage.Status != run.TokenUsageUnavailable {
		t.Fatalf("missing usage fabricated: %+v", completed.TokenUsage)
	}
}

func TestCooperativeCoreRefusesUnrecordedOrForeignPolicyBeforePrompt(t *testing.T) {
	for _, mode := range []string{"policy-error", "receipt-error", "absent", "foreign", "foreign-fingerprint", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			c, prepared, factory, recorder, _ := cooperativeCoreFixture(t)
			if mode == "policy-error" {
				recorder.policyErr = errors.New("policy store unavailable")
			} else if mode == "receipt-error" {
				recorder.receiptErr = errors.New("receipt store unavailable")
			} else {
				factory.receiptMode = mode
			}
			completed, err := c.ExecuteRun(context.Background(), prepared, nil)
			if err == nil || completed.Status != run.StatusInfrastructureFailed || factory.session.turn != 0 {
				t.Fatalf("mode=%s run=%+v error=%v turns=%d", mode, completed, err, factory.session.turn)
			}
			if mode == "policy-error" && factory.starts != 0 {
				t.Fatal("backend launched before policy persisted")
			}
		})
	}
}

func TestCooperativeLaunchRejectsNativePolicyAndExistingUnsupportedThread(t *testing.T) {
	for _, mode := range []string{"native-binding", "resume", "rejoin"} {
		t.Run(mode, func(t *testing.T) {
			c, p, f, r, _ := cooperativeCoreFixture(t)
			selected := codexruntime.NextRunRuntimeConfig{AgentConfigFingerprint: strings.Repeat("a", 64), AgentID: "pi", ExecutionPolicy: agentpolicy.PolicyCooperativeTrustedV1, Model: "gpt-5.6-terra", ReasoningEffort: "high"}
			thread := ""
			if mode == "native-binding" {
				p.scalarRuntime = &PreparedMultiRuntime{Binding: codex.RuntimeBinding{TaskPermissions: &codex.TaskPermissions{}}}
			}
			if mode == "resume" {
				thread = "old-canonical-thread"
			}
			if mode == "rejoin" {
				p.rejoin = &rejoinExecution{}
			}
			session, err := c.startCooperativeAgent(context.Background(), p, p.config, selected, t.TempDir(), thread)
			if err == nil || session != nil || f.starts != 0 || len(r.policies) != 0 {
				t.Fatalf("mode=%s session=%v error=%v starts=%d", mode, session, err, f.starts)
			}
		})
	}
}

// This fixture crosses the actual reserved v2 Task preparation boundary, not a
// manually constructed PreparedRun. Persistence is deterministic in-memory;
// the agent transport remains fake and never launches a model.
type cooperativeV2Runs struct {
	*multiReviewRuns
	frozen       *workset.RuntimeWorkset
	observations []run.RepositoryObservation
}

func (r *cooperativeV2Runs) Get(ctx context.Context, id string) (run.Run, error) {
	value, err := r.recordingRuns.Get(ctx, id)
	if r.frozen != nil && r.frozen.RunID == id {
		copy := *r.frozen
		value.RuntimeWorkset = &copy
	}
	return value, err
}
func (r *cooperativeV2Runs) BindRuntimeWorkset(ctx context.Context, w workset.RuntimeWorkset) (run.Run, error) {
	if err := w.Validate(); err != nil {
		return run.Run{}, err
	}
	r.frozen = &w
	return r.Get(ctx, w.RunID)
}
func (r *cooperativeV2Runs) RuntimeWorkset(_ context.Context, id string) (workset.RuntimeWorkset, error) {
	if r.frozen == nil || r.frozen.RunID != id {
		return workset.RuntimeWorkset{}, run.ErrNotFound
	}
	return *r.frozen, nil
}
func (r *cooperativeV2Runs) ObserveRepositoryBinding(_ context.Context, o run.RepositoryObservation) error {
	r.observations = append(r.observations, o)
	return nil
}

func TestCooperativeReservedV2SingleMemberPreparesAndExecutesNeutralAgent(t *testing.T) {
	c, oldPrepared, factory, recorder, tasks := cooperativeCoreFixture(t)
	ctx := context.Background()
	runs := c.Runs.(*recordingRuns)
	original := oldPrepared.Run
	original.Status = run.StatusCreated
	original.CreatedAt = time.Now().UTC()
	runs.store = run.NewMemoryStore(original)
	runs.service = run.NewService(runs.store, nil, nil)
	v2Runs := &cooperativeV2Runs{multiReviewRuns: &multiReviewRuns{recordingRuns: runs, bindings: map[string]run.RepositoryBinding{}}}
	c.Runs = v2Runs
	ws := c.Workspaces.(*workspace.Service)
	planned, err := ws.PlanMembers(ctx, tasks.value.ProjectID, tasks.value.FeatureAreaID, []workset.Target{{RepositoryID: "repo-1", OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}}})
	if err != nil {
		t.Fatal(err)
	}
	tasks.value.RepositoryTargets = planned.Targets
	tasks.value.PrimaryMemberID = planned.Targets[0].MemberID
	tasks.value.CurrentReviewID = "approved-review"
	tasks.value.WorkSpec.ExecutionScope = task.ExecutionScope{Kind: task.ExecutionScopeTargets}
	tasks.revision.Definition = task.DefinitionFromTask(tasks.value, nil)
	proposal := c.Executions.(executionSnapshotLookup).value
	proposal.PrimaryMemberID = tasks.value.PrimaryMemberID
	c.Executions = executionSnapshotLookup{value: proposal}
	c.Reviews = &coordinatorReviewRuntime{value: review.Review{ID: tasks.value.CurrentReviewID, ProjectID: tasks.value.ProjectID, FeatureAreaID: tasks.value.FeatureAreaID, WorkspaceID: planned.WorkspaceID, RepositoryTargets: planned.Targets}}
	trees := c.Worktrees.(*fakeWorktrees)
	trees.value.RepositoryID = "repo-1"
	prepared, err := c.PrepareReservedExecution(ctx, original.ID, Instruction{CodexInstruction: tasks.value.WorkSpec.CodexInstruction, ExecutionScope: tasks.value.WorkSpec.ExecutionScope}, nil)
	if err != nil {
		t.Fatalf("reserved v2 cooperative preparation failed: %v", err)
	}
	if prepared.multiRuntime == nil || prepared.multiRuntime.Workset.ExecutionPolicy != agentpolicy.PolicyCooperativeTrustedV1 || prepared.multiRuntime.Workset.TaskPermissions != nil {
		t.Fatalf("not an explicit cooperative v2 workset: %+v", prepared.multiRuntime)
	}
	completed, err := c.ExecuteRun(ctx, prepared, nil)
	if err != nil || completed.Status != run.StatusSucceeded {
		t.Fatalf("v2 run=%+v err=%v", completed, err)
	}
	if factory.starts != 1 || factory.session.prompt != tasks.value.WorkSpec.CodexInstruction || len(recorder.policies) != 1 || recorder.policies[0].SourceWorksetHash != prepared.multiRuntime.Workset.ContentHash || len(v2Runs.observations) == 0 {
		t.Fatal("v2 execution skipped neutral launch, exact instruction, or frozen workset observations")
	}
}

func TestCooperativeCoreRejectsAgentConfigurationDriftBeforePolicyOrLaunch(t *testing.T) {
	c, prepared, factory, recorder, _ := cooperativeCoreFixture(t)
	factory.fingerprint = strings.Repeat("b", 64)
	completed, err := c.ExecuteRun(context.Background(), prepared, nil)
	if err == nil || completed.Status != run.StatusInfrastructureFailed || factory.starts != 0 || len(recorder.policies) != 0 || factory.session.turn != 0 {
		t.Fatalf("drift launched or recorded policy: run=%+v err=%v starts=%d policies=%d", completed, err, factory.starts, len(recorder.policies))
	}
}
