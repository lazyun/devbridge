package localmcp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Uses isolated SQLite and the production supervisor with a test backend.
func finalAcceptanceFixture(t *testing.T) (*integrationFixture, string, executionproposal.Proposal) {
	t.Helper()
	f := newIntegrationFixture(t)
	f.plane.Retirement.Close()
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	_, e := f.admitScalarExecution(t, sid)
	e = completeScalarExecutionForLocalMCP(t, f, sid, e)
	return f, sid, e
}

func TestFinalAcceptanceCrossAdapterParity(t *testing.T) {
	// Bound independent runtime fixtures to avoid saturating the shared host's
	// Git/AppServer resources while retaining all adapter/decision cases.
	slots := make(chan struct{}, 2)
	for _, adapter := range []string{"GPT", "Local", "Web"} {
		for _, option := range []string{"ACCEPT", "REJECT", "REQUEST_CHANGES", "REQUEST_REVERIFICATION"} {
			t.Run(adapter+"/"+option, func(t *testing.T) {
				// Each case owns its SQLite, Git workspace and runtime seam.
				t.Parallel()
				slots <- struct{}{}
				t.Cleanup(func() { <-slots })
				f, sid, e := finalAcceptanceFixture(t)
				owner, err := f.plane.Tasks.Get(f.ctx, e.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				input := wp.TaskAcceptanceInput{AISessionID: sid, TaskID: owner.ID, FeatureAreaID: owner.FeatureAreaID, ExecutionID: e.ID, TaskDefinitionRevisionID: e.TaskDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(e.ID), Option: option, RequestID: "final-acceptance"}
				if option == "REQUEST_CHANGES" {
					work, verify := e.WorkSpec, e.VerificationSpec
					work.CodexInstruction = "  exact implementation\r\n中文\n"
					verify.CodexInstruction = "\t exact verification\r\n"
					input.NextWorkSpec = &work
					input.NextVerificationSpec = &verify
					input.NextRepositoryTargets = append(input.NextRepositoryTargets, owner.RepositoryTargets...)
					input.NextPrimaryMemberID = owner.PrimaryMemberID
				}
				web, err := bridgehttp.New(f.plane, false)
				if err != nil {
					t.Fatal(err)
				}
				// The v45 SQL guard must reject legacy pending candidate writes before
				// they can interfere with canonical Acceptance successor reservation.
				legacyBody, _ := json.Marshal(map[string]string{"aiSessionId": sid, "taskPlanId": e.TaskPlanID})
				legacy := httptest.NewRecorder()
				legacyRequest := httptest.NewRequest(http.MethodPost, "/v1/execution-proposals", bytes.NewReader(legacyBody))
				setLocalOperatorLoopback(legacyRequest)
				web.Handler().ServeHTTP(legacy, legacyRequest)
				if legacy.Code != http.StatusConflict {
					t.Fatalf("legacy candidate write escaped v45 guard: %d %s", legacy.Code, legacy.Body.String())
				}
				invoke := func(which string, in wp.TaskAcceptanceInput, ok bool) {
					t.Helper()
					if which == "Web" {
						webInput := consoleapi.TaskActionInput{Action: in.Option, RequestID: in.RequestID, Target: &workflowpresentation.ActionTarget{TaskID: in.TaskID, FeatureAreaID: in.FeatureAreaID, ExecutionID: in.ExecutionID, SubjectID: in.ExecutionID, DecisionID: in.DecisionID, TaskDefinitionRevisionID: in.TaskDefinitionRevisionID}, NextWorkSpec: in.NextWorkSpec, NextVerificationSpec: in.NextVerificationSpec}
						if in.Option == "REQUEST_CHANGES" {
							webInput.NextRunRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
						}
						view, err := f.control.TaskPresentationForSession(f.ctx, sid, owner.FeatureAreaID, owner.ID)
						if err != nil || view.TaskView == nil || view.TaskView.ActionNode == nil {
							t.Fatalf("read current task action: %v", err)
						}
						if ok {
							// Missing observed generation must be a recoverable domain
							// refusal, with no effect on the following current request.
							staleBody, _ := json.Marshal(webInput)
							staleResponse := httptest.NewRecorder()
							staleRequest := httptest.NewRequest(http.MethodPost, "/ui/api/tasks/"+owner.ID+"/action?project_id="+f.projectID, bytes.NewReader(staleBody))
							setLocalOperatorLoopback(staleRequest)
							web.Handler().ServeHTTP(staleResponse, staleRequest)
							var problem struct {
								Code string `json:"code"`
							}
							if err := json.Unmarshal(staleResponse.Body.Bytes(), &problem); err != nil || staleResponse.Code != http.StatusConflict || problem.Code != "TASK_ACTION_STALE" {
								t.Fatalf("stale generation status=%d body=%s: %v", staleResponse.Code, staleResponse.Body.String(), err)
							}
						}
						webInput.Target.TaskActionGeneration = view.TaskView.ActionNode.Generation
						body, _ := json.Marshal(webInput)
						w := httptest.NewRecorder()
						request := httptest.NewRequest(http.MethodPost, "/ui/api/tasks/"+owner.ID+"/action?project_id="+f.projectID, bytes.NewReader(body))
						setLocalOperatorLoopback(request)
						web.Handler().ServeHTTP(w, request)
						if (w.Code == 200) != ok {
							t.Fatalf("Web status=%d body=%s", w.Code, w.Body.String())
						}
						return
					}
					args := map[string]any{}
					b, _ := json.Marshal(in)
					json.Unmarshal(b, &args)
					args["reviewId"] = owner.CurrentReviewID
					client := connectGPT(t, f.gptURL)
					if which == "Local" {
						client = connectLocalMCP(t, f.libreURL)
					}
					if ok {
						callToolMap(t, f.ctx, client, "task_acceptance_respond", args)
					} else {
						callToolErrorText(t, f.ctx, client, "task_acceptance_respond", args)
					}
				}
				// A terminal child alone cannot authorize Acceptance in a Review phase.
				if _, err := f.plane.DB.SQL().Exec("UPDATE tasks SET phase='REVIEW_REQUIRED' WHERE id=?", owner.ID); err != nil {
					t.Fatal(err)
				}
				invoke(adapter, input, false)
				if _, err := f.plane.DB.SQL().Exec("UPDATE tasks SET phase=? WHERE id=?", string(owner.Phase), owner.ID); err != nil {
					t.Fatal(err)
				}
				for _, field := range []string{"task", "area", "execution", "revision"} {
					missing := input
					switch field {
					case "task":
						missing.TaskID = ""
					case "area":
						missing.FeatureAreaID = ""
					case "execution":
						missing.ExecutionID = ""
					case "revision":
						missing.TaskDefinitionRevisionID = ""
					}
					invoke(adapter, missing, false)
				}
				for _, field := range []string{"task", "execution", "revision", "gate"} {
					bad := input
					switch field {
					case "task":
						bad.TaskID = "wrong-task"
					case "execution":
						bad.ExecutionID = "wrong-execution"
					case "revision":
						bad.TaskDefinitionRevisionID = "wrong-revision"
					case "gate":
						bad.DecisionID = "TASK_ACCEPTANCE:wrong"
					}
					invoke(adapter, bad, false)
				}
				if option == "REQUEST_CHANGES" {
					bad := input
					bad.NextWorkSpec = nil
					invoke(adapter, bad, false)
					bad = input
					work := *bad.NextWorkSpec
					work.CodexInstruction = ""
					bad.NextWorkSpec = &work
					invoke(adapter, bad, false)
				}
				invoke(adapter, input, true)
				// A GPT confirmation and a Local operator decision have distinct
				// immutable request identities. Cross-adapter submission replay is
				// intentionally forbidden; each adapter's receipt contract is
				// exercised by its dedicated transport fixtures.
				bad := input
				bad.Option = "REJECT"
				if option == "REJECT" {
					bad.Option = "ACCEPT"
				}
				bad.NextWorkSpec = nil
				bad.NextVerificationSpec = nil
				invoke(adapter, bad, false)
				stale := input
				stale.RequestID = "new-request-old-gate"
				// GPT's confirmation prepare is a separate immutable intent whose
				// preflight/readback carries its own disposition. The retained Local
				// command and operator surface retain this direct stale-command check.
				if adapter != "GPT" {
					invoke(adapter, stale, false)
				}
				current, err := f.plane.Tasks.Get(f.ctx, owner.ID)
				if err != nil {
					t.Fatal(err)
				}
				var count int
				f.plane.DB.SQL().QueryRow("SELECT count(*) FROM tasks WHERE id=?", owner.ID).Scan(&count)
				if count != 1 {
					t.Fatal("changed semantic Task")
				}
				executions, err := sqlite.NewExecutionProposalStore(f.plane.DB).ListByTask(f.ctx, owner.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantCount := 1
				switch option {
				case "ACCEPT":
					// The real runtime now supplies verified workspace evidence, so
					// synchronous ACCEPT completes delivery instead of stopping at
					// the old synthetic fixture's delivery admission boundary.
					if current.Status != task.StatusSucceeded || current.Phase != task.PhaseDelivering || current.CommitSHA == "" || current.CommittedAt == nil {
						t.Fatal(current)
					}
					var completed bool
					if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT EXISTS(SELECT 1 FROM task_commit_receipts WHERE task_id=? AND completed_at IS NOT NULL)", owner.ID).Scan(&completed); err != nil || !completed {
						t.Fatalf("canonical delivery receipt incomplete: %v", err)
					}
				case "REJECT":
					if current.Status != task.StatusCanceled {
						t.Fatal(current.Status)
					}
				case "REQUEST_CHANGES":
					if adapter == "GPT" {
						// GPT records the closed EXECUTION confirmation submission;
						// the host-owned continuation is intentionally not projected as
						// the Local operator's synchronous successor receipt.
						if current.CurrentDefinitionRevisionID != e.TaskDefinitionRevisionID || current.Phase != task.PhaseAcceptancePending {
							t.Fatal(current)
						}
						break
					}
					wantCount = 2
					if current.CurrentDefinitionRevisionID == e.TaskDefinitionRevisionID || current.Phase != task.PhaseImplementationReady {
						t.Fatal(current)
					}
					next, err := f.plane.ExecutionProposals.Get(f.ctx, sid, current.CurrentExecutionID)
					if err != nil {
						t.Fatal(err)
					}
					if next.WorkSpec.CodexInstruction != input.NextWorkSpec.CodexInstruction || next.VerificationSpec.CodexInstruction != input.NextVerificationSpec.CodexInstruction {
						t.Fatal("authored bytes changed")
					}
					bad := input
					work := *input.NextWorkSpec
					work.CodexInstruction += "changed"
					bad.NextWorkSpec = &work
					invoke(adapter, bad, false)
				case "REQUEST_REVERIFICATION":
					wantCount = 2
					if current.CurrentDefinitionRevisionID != e.TaskDefinitionRevisionID || current.Phase != task.PhaseVerificationReady {
						t.Fatal(current)
					}
				}
				if len(executions) != wantCount {
					t.Fatalf("executions=%d want=%d", len(executions), wantCount)
				}
			})
		}
	}
}
