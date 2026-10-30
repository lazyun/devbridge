package controlapi

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type approvedStartProposalRead struct {
	ExecutionProposalService
	value        executionproposal.Proposal
	err          error
	handoff      executionproposal.Handoff
	handoffErr   error
	reads        int
	handoffReads int
}

func (r *approvedStartProposalRead) Get(context.Context, string, string) (executionproposal.Proposal, error) {
	r.reads++
	return r.value, r.err
}
func (r *approvedStartProposalRead) ReadHandoff(context.Context, string, string) (executionproposal.Handoff, error) {
	r.handoffReads++
	return r.handoff, r.handoffErr
}

type approvedStartRunRead struct {
	RunService
	value run.Run
	err   error
	reads int
}

func (r *approvedStartRunRead) Get(context.Context, string) (run.Run, error) {
	r.reads++
	return r.value, r.err
}

// This unit boundary receives a successfully authenticated canonical Intent.
// It tests only child-read faults and rejects foreign child/step identities;
// the real Local MCP fixture proves production of the underlying receipt.
func approvedStartReadbackFixture() (confirmation.Intent, *approvedStartProposalRead, *approvedStartRunRead) {
	intent := confirmation.Intent{IntentID: "intent", AISessionID: "session", ProjectID: "project", Version: 1, Purpose: confirmation.Execution, Action: "START_APPROVED", State: "COMPLETED", ContentHash: strings.Repeat("a", 64)}
	intent.Content.Execution = &confirmation.ExecutionBody{Action: "START_APPROVED", TaskID: "task", FeatureAreaID: "area", TaskDefinitionRevisionID: "revision"}
	intent.Commit = &confirmation.CommitInput{AISessionID: intent.AISessionID, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, ContentHash: intent.ContentHash, RequestID: "original-commit"}
	intent.Steps = []confirmation.Step{{Kind: "START_IMPLEMENTATION", RequestID: "confirmation_" + confirmationHash([]string{intent.IntentID, "START_IMPLEMENTATION"})[:32], State: "COMPLETED", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", RevisionID: "revision", ExecutionID: "original-execution", RunID: "original-run"}}
	proposals := &approvedStartProposalRead{value: executionproposal.Proposal{ID: "original-execution", ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", ImplementationRunID: "original-run"}, handoff: executionproposal.Handoff{ExecutionID: "original-execution", TaskID: "task", RunID: "original-run"}}
	runs := &approvedStartRunRead{value: run.Run{ID: "original-run", ProjectID: "project", TaskID: "task", ExecutionProposalID: "original-execution", Purpose: run.PurposeImplementation}}
	return intent, proposals, runs
}

func TestApprovedTaskStartReadbackRetainsOnlyVerifiedReceiptIdentity(t *testing.T) {
	for _, point := range []string{"execution", "run", "handoff"} {
		t.Run(point, func(t *testing.T) {
			intent, proposals, runs := approvedStartReadbackFixture()
			fault := errors.New("temporary child reader unavailable")
			switch point {
			case "execution":
				proposals.err = fault
			case "run":
				runs.err = fault
			case "handoff":
				proposals.handoffErr = fault
			}
			facade := &Facade{deps: Dependencies{ExecutionProposals: proposals, Runs: runs}}
			out, err := facade.readApprovedTaskStartResult(context.Background(), intent)
			want := &executionproposal.StartReceiptIdentity{ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", OriginalExecutionID: "original-execution", OriginalRunID: "original-run"}
			if err == nil || !reflect.DeepEqual(out.Receipt, want) {
				t.Fatalf("%s lost verified receipt: %+v %v", point, out, err)
			}
			if point == "execution" && (out.Proposal.ID != "" || out.Run.ID != "") {
				t.Fatal("failed child read fabricated complete child objects")
			}
			if point == "run" && (out.Proposal.ID != "original-execution" || out.Run.ID != "") {
				t.Fatal("failed Run read fabricated a Run or lost verified Execution")
			}
			if point == "handoff" && (out.Run.ID != "original-run" || out.Handoff.RunID != "") {
				t.Fatal("failed handoff read fabricated handoff or lost verified Run")
			}
		})
	}
}

func TestApprovedTaskStartReadbackHidesForeignOrUnverifiedIdentity(t *testing.T) {
	for _, point := range []string{"step-project", "step-task", "step-revision", "step-request", "commit-identity", "execution-project", "execution-id", "run-task", "run-id", "handoff-owner", "execution-denied", "execution-not-found", "run-denied", "run-not-found", "handoff-denied", "handoff-not-found"} {
		t.Run(point, func(t *testing.T) {
			intent, proposals, runs := approvedStartReadbackFixture()
			switch point {
			case "step-project":
				intent.Steps[0].ProjectID = "foreign"
			case "step-task":
				intent.Steps[0].TaskID = "foreign"
			case "step-revision":
				intent.Steps[0].RevisionID = "foreign"
			case "step-request":
				intent.Steps[0].RequestID = "foreign"
			case "commit-identity":
				intent.Commit.IntentID = "foreign"
			case "execution-project":
				proposals.value.ProjectID = "foreign"
			case "execution-id":
				proposals.value.ID = "foreign"
			case "run-task":
				runs.value.TaskID = "foreign"
			case "run-id":
				runs.value.ID = "foreign"
			case "handoff-owner":
				proposals.handoff.TaskID = "foreign"
			case "execution-denied":
				proposals.err = &Error{Code: CodePermissionDenied, Message: "hidden"}
			case "execution-not-found":
				proposals.err = executionproposal.ErrNotFound
			case "run-denied":
				runs.err = &Error{Code: CodePermissionDenied, Message: "hidden"}
			case "run-not-found":
				runs.err = run.ErrNotFound
			case "handoff-denied":
				proposals.handoffErr = &Error{Code: CodePermissionDenied, Message: "hidden"}
			case "handoff-not-found":
				proposals.handoffErr = executionproposal.ErrNotFound
			}
			facade := &Facade{deps: Dependencies{ExecutionProposals: proposals, Runs: runs}}
			out, err := facade.readApprovedTaskStartResult(context.Background(), intent)
			if err == nil || out.Receipt != nil || out.Proposal.ID != "" || out.Run.ID != "" {
				t.Fatalf("%s exposed foreign/unverified result: %+v %v", point, out, err)
			}
		})
	}
}

// The remaining tests isolate the Facade's error projection. Store doubles can
// return a decoded object together with an error, as a failing canonical reader
// can; only a separate successful read is an admissible identity observation.
type approvedStartReceiptRead struct {
	confirmation.Store
	t      *testing.T
	input  ApprovedTaskStartInput
	reads  int
	replay func(int) (confirmation.Intent, error)
	get    func() (confirmation.Intent, error)
}

func (r *approvedStartReceiptRead) ReplayPrepare(_ context.Context, session, request, hash string) (confirmation.Intent, error) {
	r.t.Helper()
	want := "approved_task_start_prepare_" + confirmationHash([]string{"devbridge.approved-task-start.v1", r.input.AISessionID, r.input.RequestID})
	if session != r.input.AISessionID || request != want || hash != confirmation.ValueHash(r.input) {
		r.t.Fatalf("read changed original request identity: %s %s %s", session, request, hash)
	}
	r.reads++
	return r.replay(r.reads)
}
func (r *approvedStartReceiptRead) Get(context.Context, string, string) (confirmation.Intent, error) {
	return r.get()
}
func (r *approvedStartReceiptRead) ReadSubmission(context.Context, confirmation.ReadInput) (confirmation.Submission, error) {
	return confirmation.Submission{}, errors.New("original submission reader unavailable")
}

type approvedStartScopedSession struct{ aicontext.SessionService }

func (approvedStartScopedSession) InitializeSession(_ context.Context, ref aiprojectselection.ClientBindingReference, request, session string) (aisession.Initialization, error) {
	if ref.CallerScope != "test-local" || request != "" || session != "session" {
		return aisession.Initialization{}, aisession.ErrScopeMismatch
	}
	projectID := "project"
	return aisession.Initialization{AISessionID: session, State: aisession.ProjectBound, BoundProjectID: &projectID}, nil
}
func (approvedStartScopedSession) BindProject(context.Context, aiprojectselection.ClientBindingReference, string, string, string) (aisession.Session, aisession.RequestReceipt, bool, error) {
	panic("receipt read must not bind a Project")
}

type approvedStartProjectRead struct{ aicontext.ProjectService }

func (approvedStartProjectRead) Get(_ context.Context, id string) (project.Project, error) {
	if id != "project" {
		return project.Project{}, project.ErrNotFound
	}
	return project.Project{ID: id}, nil
}

func approvedStartReceiptFacade(t *testing.T, store confirmation.Store, proposals *approvedStartProposalRead, runs *approvedStartRunRead) *Facade {
	t.Helper()
	sessions, err := aicontext.NewService(approvedStartScopedSession{}, struct {
		aicontext.ProjectSelectionService
	}{}, approvedStartProjectRead{}, struct{ aicontext.RepositoryService }{}, struct{ aicontext.ModuleService }{}, struct{ aicontext.TaskService }{}, struct{ aicontext.CodexConfigService }{})
	if err != nil {
		t.Fatal(err)
	}
	// No Task, inventory, model, preparation or dispatch ports are installed.
	// A receipt-read error must not fall through to any fresh-start path.
	return &Facade{deps: Dependencies{ConfirmationSessions: sessions, Confirmations: store, ExecutionProposals: proposals, Runs: runs}}
}

func TestApprovedTaskStartDoesNotTrustPartialReceiptErrors(t *testing.T) {
	for _, point := range []string{"replay-unverified", "replay-independent-proof", "commit-unverified", "commit-no-prior-proof", "replay-denied", "commit-denied"} {
		t.Run(point, func(t *testing.T) {
			original, proposals, runs := approvedStartReadbackFixture()
			poison, _, _ := approvedStartReadbackFixture()
			poison.Steps[0].ExecutionID, poison.Steps[0].RunID = "unverified-execution", "unverified-run"
			input := ApprovedTaskStartInput{AISessionID: "session", TaskID: "task", RequestID: "caller-request", ExpectedTarget: wp.TaskExecutionExpectation{FeatureAreaID: "area", TaskDefinitionRevisionID: "revision", Phase: task.PhaseImplementationReady}}
			store := &approvedStartReceiptRead{t: t, input: input}
			fault := errors.New("canonical receipt read failed after decoding")
			store.replay = func(n int) (confirmation.Intent, error) {
				switch point {
				case "replay-independent-proof":
					if n == 2 {
						return original, nil
					}
				case "commit-unverified", "commit-denied":
					if n == 1 {
						return original, nil
					}
				case "commit-no-prior-proof":
					if n == 1 {
						pending := original
						pending.Commit = nil
						pending.Steps = nil
						pending.State = "PENDING"
						return pending, nil
					}
				case "replay-denied":
					return poison, &Error{Code: CodePermissionDenied, Message: "hidden"}
				}
				return poison, fault
			}
			store.get = func() (confirmation.Intent, error) {
				if point == "commit-denied" {
					return confirmation.Intent{}, &Error{Code: CodePermissionDenied, Message: "hidden"}
				}
				return poison, fault
			}
			facade := approvedStartReceiptFacade(t, store, proposals, runs)
			out, err := facade.StartApprovedTaskExecution(aisession.WithCallerScope(context.Background(), "test-local"), input)
			if err == nil {
				t.Fatal("reader error converted to success")
			}
			proven := point == "replay-independent-proof" || point == "commit-unverified"
			if proven {
				if out.Receipt == nil || out.Receipt.OriginalExecutionID != "original-execution" || out.Receipt.OriginalRunID != "original-run" || out.Proposal.ID != "original-execution" || out.Run.ID != "original-run" {
					t.Fatalf("verified original lost or poisoned: %+v %v", out, err)
				}
			} else if out.Receipt != nil || out.Proposal.ID != "" || out.Run.ID != "" {
				t.Fatalf("unverified partial leaked: %+v %v", out, err)
			}
			if !proven && (proposals.reads != 0 || proposals.handoffReads != 0 || runs.reads != 0) {
				t.Fatal("unverified partial was used to address child readers")
			}
			wantReads := 2
			if strings.HasSuffix(point, "denied") {
				wantReads = 1
			}
			if store.reads != wantReads {
				t.Fatalf("got %d replay reads, want %d", store.reads, wantReads)
			}
		})
	}
}
