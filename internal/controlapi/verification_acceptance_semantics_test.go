package controlapi_test

import (
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"testing"
)

func TestCodingVerificationCompleteFailuresPermitExplicitAcceptance(t *testing.T) {
	for _, mode := range []string{"observed", "criterion"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("DEVBRIDGE_TEST_VERIFICATION_FAILURE", mode)
			runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
				current := awaitAcceptanceRecoveryFixture(t, f, executionID)
				proposal, err := f.facade.GetExecutionProposal(f.ctx, f.session, executionID)
				if err != nil {
					t.Fatal(err)
				}
				vr, err := f.facade.GetRunForSession(f.ctx, f.session, proposal.VerificationRunID)
				if err != nil {
					t.Fatal(err)
				}
				report := vr.VerificationReport
				if report == nil || report.Availability != run.VerificationReportAvailable || len(report.Checks) != 3 {
					t.Fatalf("missing complete report: %+v", report)
				}
				for i, check := range report.Checks {
					if check.Observed == nil || check.Observed.Source == nil || check.Observed.Source.ItemID == "" || check.Observed.Source.CommandStatus != "completed" || check.Observed.ExitCode == nil {
						t.Fatalf("incomplete observed evidence %d: %+v", i, check)
					}
				}
				if mode == "observed" && (report.Checks[0].Observed.Status != run.VerificationCheckFailed || *report.Checks[0].Observed.ExitCode != 1) {
					t.Fatal("real failing command not observed")
				}
				if mode == "criterion" {
					for _, check := range report.Checks {
						if check.Observed.Status != run.VerificationCheckPassed {
							t.Fatal("positive command control failed")
						}
					}
					if report.AcceptanceCriteria[0].Reported.Status != run.VerificationCheckFailed {
						t.Fatal("criterion fixture failed")
					}
				}
				view, viewErr := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
				if viewErr != nil || view.TaskView == nil {
					t.Fatalf("read acceptance: %v", viewErr)
				}
				available := false
				for _, action := range view.TaskView.Actions {
					if action.Action == "ACCEPT" && action.Available {
						available = true
					}
				}
				if !available {
					t.Fatal("complete failed evidence hid user acceptance")
				}
				intent, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "audit-accept", Purpose: c.Execution, Action: "ACCEPT", Execution: &c.ExecutionBody{Action: "ACCEPT", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: executionID, DecisionID: wp.TaskAcceptanceDecisionID(executionID)}, Variants: []c.Variant{{ID: "accept", Summary: "User explicitly accepts after reviewing complete failed evidence"}}})
				if err != nil {
					t.Fatal(err)
				}
				result, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "audit-accept-commit", ContentHash: intent.ContentHash, VariantID: "accept"})
				t.Logf("report=%s Run=%s observed=%s exit=%d criterion=%s acceptanceState=%s err=%v", report.Availability, vr.Status, report.Checks[0].Observed.Status, *report.Checks[0].Observed.ExitCode, report.AcceptanceCriteria[0].Reported.Status, result.State, err)
				if err != nil || result.State != "COMPLETED" {
					t.Errorf("VERIFICATION_ACCEPTANCE_AVAILABLE: complete observed facts must not decide instead of explicit user ACCEPT: state=%s err=%v", result.State, err)
				}
				reopenDirectDeliveryFixture(t, f)
				replayed, replayErr := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "audit-accept-commit", ContentHash: intent.ContentHash, VariantID: "accept"})
				if replayErr != nil || replayed.State != "COMPLETED" {
					t.Fatalf("cold exact acceptance receipt: %+v %v", replayed, replayErr)
				}
				persisted, readErr := f.facade.GetRunForSession(f.ctx, f.session, proposal.VerificationRunID)
				if readErr != nil || persisted.VerificationReport == nil || persisted.VerificationReport.Checks[0].Observed.Status != report.Checks[0].Observed.Status || persisted.VerificationReport.AcceptanceCriteria[0].Reported.Status != report.AcceptanceCriteria[0].Reported.Status {
					t.Fatalf("acceptance rewrote failure evidence: %+v %v", persisted.VerificationReport, readErr)
				}
				return false
			})
		})
	}
}

func TestCodingVerificationMissingEvidenceHidesAcceptance(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_DUPLICATE_VERIFICATION_ROOT", "1")
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		current := awaitAcceptanceRecoveryFixture(t, f, executionID)
		proposal, err := f.facade.GetExecutionProposal(f.ctx, f.session, executionID)
		if err != nil {
			t.Fatal(err)
		}
		vr, err := f.facade.GetRunForSession(f.ctx, f.session, proposal.VerificationRunID)
		if err != nil {
			t.Fatal(err)
		}
		report := vr.VerificationReport
		if report == nil || len(report.Checks) != 3 || report.Checks[1].Observed == nil || report.Checks[1].Observed.Status != run.VerificationCheckNotRun || report.Availability != run.VerificationReportInsufficient {
			t.Fatalf("missing member observation fixture not reached: %+v", report)
		}
		view, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
		if err != nil {
			t.Fatal(err)
		}
		available := false
		if view.TaskView == nil {
			t.Fatal("Task presentation missing")
		}
		for _, cap := range view.TaskView.Actions {
			if cap.Action == "ACCEPT" && cap.Available {
				available = true
			}
		}
		t.Logf("SDK observed=%s source=%+v report=%s Task=%s ACCEPT available=%t", report.Checks[1].Observed.Status, report.Checks[1].Observed.Source, report.Availability, current.Phase, available)
		if available {
			t.Errorf("ACTUAL_COMMAND_EVIDENCE: missing required member evidence must not offer a fresh ACCEPT capability")
		}
		reopenDirectDeliveryFixture(t, f)
		cold, coldErr := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
		if coldErr != nil || cold.TaskView == nil || cold.TaskView.VerificationReport == nil || cold.TaskView.VerificationReport.Availability != run.VerificationReportInsufficient {
			t.Fatalf("cold insufficient report: %+v %v", cold.TaskView, coldErr)
		}
		for _, action := range cold.TaskView.Actions {
			if action.Action == "ACCEPT" && action.Available {
				t.Fatal("cold read offered missing evidence acceptance")
			}
		}
		return false
	})
}
