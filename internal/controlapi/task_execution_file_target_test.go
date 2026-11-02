package controlapi

import (
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Pure projection tests: no storage or fabricated business rows. Writer-side
// admission and frozen runtime checks remain independently authoritative.
func TestTaskExecutionStartTargetDirectFileVerificationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*task.Task, *[]executionproposal.Proposal)
		allowed bool
	}{
		{"file_run_ready", nil, true},
		{"file_implementation_ready", func(v *task.Task, _ *[]executionproposal.Proposal) { v.Phase = task.PhaseImplementationReady }, false},
		{"file_none", func(v *task.Task, _ *[]executionproposal.Proposal) {
			v.VerificationSpec.Mode = task.VerificationModeNone
		}, false},
		{"file_verifying", func(v *task.Task, _ *[]executionproposal.Proposal) { v.Phase = task.PhaseVerifying }, false},
		{"inactive", func(v *task.Task, _ *[]executionproposal.Proposal) { v.Status = task.StatusCanceled }, false},
		{"legacy", func(v *task.Task, _ *[]executionproposal.Proposal) { v.LegacyReadOnly = true }, false},
		{"missing_revision", func(v *task.Task, _ *[]executionproposal.Proposal) { v.CurrentDefinitionRevisionID = "" }, false},
		{"no_execution", func(v *task.Task, p *[]executionproposal.Proposal) { v.CurrentExecutionID = ""; *p = nil }, false},
		{"no_matching_proposal", func(_ *task.Task, p *[]executionproposal.Proposal) { *p = nil }, false},
		{"wrong_execution", func(_ *task.Task, p *[]executionproposal.Proposal) { (*p)[0].ID = "different" }, false},
		{"wrong_task", func(_ *task.Task, p *[]executionproposal.Proposal) { (*p)[0].TaskID = "different" }, false},
		{"wrong_project", func(_ *task.Task, p *[]executionproposal.Proposal) { (*p)[0].ProjectID = "different" }, false},
		{"wrong_revision", func(_ *task.Task, p *[]executionproposal.Proposal) { (*p)[0].TaskDefinitionRevisionID = "different" }, false},
		{"coding_verification_preserved", func(v *task.Task, _ *[]executionproposal.Proposal) {
			v.WorkSpec.ImplementationMode = task.ImplementationCodingAgent
		}, true},
		{"coding_implementation_preserved", func(v *task.Task, _ *[]executionproposal.Proposal) {
			v.WorkSpec.ImplementationMode = task.ImplementationCodingAgent
			v.Phase = task.PhaseImplementationReady
		}, true},
		{"coding_approve_only_preserved", func(v *task.Task, p *[]executionproposal.Proposal) {
			v.WorkSpec.ImplementationMode = task.ImplementationCodingAgent
			v.Phase = task.PhaseImplementationReady
			v.CurrentExecutionID = ""
			*p = nil
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Status: task.StatusActive, Phase: task.PhaseVerificationReady, CurrentDefinitionRevisionID: "revision", CurrentExecutionID: "execution", WorkSpec: task.WorkSpec{ImplementationMode: task.ImplementationDirectFile}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeRun}}
			proposals := []executionproposal.Proposal{{ID: "execution", ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", PrimaryMemberID: "member", ApprovedWorksetHash: "frozen-workset"}}
			if tc.mutate != nil {
				tc.mutate(&owner, &proposals)
			}
			got := taskExecutionStartTarget(owner, proposals)
			if !tc.allowed {
				if got != nil {
					t.Fatalf("unexpected target %+v", got)
				}
				return
			}
			want := wp.TaskExecutionExpectation{FeatureAreaID: owner.FeatureAreaID, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, ExecutionID: owner.CurrentExecutionID, Phase: owner.Phase}
			if len(proposals) > 0 {
				want.PrimaryMemberID = proposals[0].PrimaryMemberID
				want.ApprovedWorksetHash = proposals[0].ApprovedWorksetHash
			}
			if got == nil || !reflect.DeepEqual(*got, want) {
				t.Fatalf("target=%+v want=%+v", got, want)
			}
		})
	}
}
