package controlapi_test

import (
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
	"strings"
	"testing"
)

func TestUnobservableRequiredCheckRejectedBeforeConfirmation(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	targets, work, verification, _ := submitLifecycleApprovalReady(t, f, "observability-review")
	original := task.CloneVerificationSpec(verification)
	for _, kind := range []task.VerificationCheckKind{task.VerificationCheckTestCommand, task.VerificationCheckGitAssertion} {
		verification = task.CloneVerificationSpec(original)
		verification.Checks[0].Kind = kind
		if kind != task.VerificationCheckCustom {
			verification.Checks[0].Command = ""
			verification.Checks[0].Instruction = "unsupported required instruction"
		}
		tables := definitionFormalEveryTable(t, f)
		before := definitionFormalSnapshot(t, f, tables)
		_, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "unobservable-" + string(kind), Purpose: c.Execution, Action: "APPROVE", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE"), Variants: []c.Variant{{ID: "approve", Summary: "Approve", ImplementationMode: work.ImplementationMode, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}}})
		if err == nil || !strings.Contains(err.Error(), "UNOBSERVABLE_REQUIRED_CHECK") {
			t.Fatalf("unsupported %s admitted or wrong gate: %v", kind, err)
		}
		assertDefinitionFormalSnapshot(t, f, tables, before, "unsupported required checks rejected before confirmation writes")
	}
}
