package controlapi_test

import (
	"testing"

	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/task"
)

func TestUserConfirmationStartsCodingExecutionExactlyOnce(t *testing.T) {
	for _, action := range []string{"APPROVE_AND_START", "START_APPROVED"} {
		t.Run(action, func(t *testing.T) {
			var f *upperLifecycleFixture
			var request c.CommitInput
			if action == "START_APPROVED" {
				f, _, request, _ = definitionAdoptionStartFixture(t)
			} else {
				f = newUpperLifecycleFixture(t)
				f.plane.Retirement.Close()
				installProtocolFixturePlatform(t, f)
				f.plane.Coordinator.Codex = preparationOnlyCodexFactory{}
				targets, work, verification, _ := submitLifecycleApprovalReady(t, f, "auto-start-report")
				prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "auto-start-prepare", Purpose: c.Execution, Action: action, Execution: exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, action), Variants: []c.Variant{{ID: "start", Summary: "Approve and immediately execute", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}}})
				if err != nil {
					t.Fatal(err)
				}
				opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
				if err != nil || len(opts.BackendCandidates) == 0 {
					t.Fatalf("options: %+v %v", opts, err)
				}
				backend := opts.BackendCandidates[0].Input()
				runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
				request = c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: action, RequestID: "auto-start-confirm", ContentHash: prepared.ContentHash, VariantID: "start", Selection: c.Selection{Backend: &backend, RuntimeConfig: &runtime}}
			}
			service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "confirmation-start", ApplicationVersion: "confirmation-start-test", Ready: true}, f.plane.AIContext, f.facade)
			if err != nil {
				t.Fatal(err)
			}
			committed, err := service.CommitConfirmation(f.ctx, request)
			if err != nil || committed.State != "COMPLETED" {
				t.Fatalf("user confirmation did not continue into execution: %+v %v", committed, err)
			}
			current, err := f.facade.GetTask(f.ctx, f.task)
			if err != nil {
				t.Fatal(err)
			}
			proposal, err := f.facade.GetExecutionProposal(f.ctx, f.session, current.CurrentExecutionID)
			if err != nil || proposal.ImplementationRunID == "" {
				t.Fatalf("confirmation stopped at READY: %+v %v", proposal, err)
			}
			var runs int
			count := func() int {
				t.Helper()
				if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id=?`, proposal.ID).Scan(&runs); err != nil {
					t.Fatal(err)
				}
				return runs
			}
			if count() != 1 {
				t.Fatalf("implementation Runs: %d", runs)
			}
			frozen := c.ValueHash(committed.Steps)
			if replay, err := service.CommitConfirmation(f.ctx, request); err != nil || c.ValueHash(replay.Steps) != frozen || count() != 1 {
				t.Fatalf("replay duplicated launch or mutated sealed receipt: %+v %v", replay, err)
			}
			if _, err := service.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: request.IntentID, Version: request.Version, Purpose: request.Purpose, ContentHash: request.ContentHash}); err != nil || count() != 1 {
				t.Fatalf("read replay: %v", err)
			}
		})
	}
}
