package consoleapi

import (
	"testing"

	p "github.com/lazyun/devbridge/internal/workflowpresentation"
)

func TestVerificationChoiceTargetsUseExactExecutionDecision(t *testing.T) {
	for _, action := range []string{"CONTINUE_TASK", "DEFER_TASK", "COMPLETE_TASK"} {
		t.Run(action, func(t *testing.T) {
			target := p.ActionTarget{TaskID: "task", FeatureAreaID: "area", SubjectID: "execution", ExecutionID: "execution", TaskDefinitionRevisionID: "revision", DecisionID: "TASK_ACCEPTANCE:execution"}
			if !consoleTaskTargetShape(action, &target) {
				t.Fatal("published verification choice rejected")
			}
			for _, mutate := range []func(*p.ActionTarget){
				func(v *p.ActionTarget) { v.ExecutionID = "other" },
				func(v *p.ActionTarget) { v.SubjectID = "task" },
				func(v *p.ActionTarget) { v.TaskDefinitionRevisionID = "" },
				func(v *p.ActionTarget) { v.DecisionID = "REVIEW_APPROVAL:execution" },
				func(v *p.ActionTarget) { v.RunID = "unexpected" },
			} {
				wrong := target
				mutate(&wrong)
				if consoleTaskTargetShape(action, &wrong) {
					t.Fatalf("incomplete or unrelated choice accepted: %+v", wrong)
				}
			}
		})
	}
}
