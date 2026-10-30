package agentruntime

import "testing"

func TestRecoveryDecisionIdentityDoesNotRelaxOrdinaryOwner(t *testing.T) {
	base := Identity{ProjectID: "project", TaskID: "task", RunID: "child", ThreadID: "child-thread", TurnID: "child-turn", ConnectionID: "connection", RuntimeEpoch: "epoch", OperationKind: "EXECUTION", OperationID: "execution", ExecutionID: "execution", TaskDefinitionRevisionID: "revision"}
	if CompleteDecisionIdentity(base) {
		t.Fatal("ordinary Execution accepted absent business identity")
	}
	base.RecoveryStatusID = "recovery"
	if !CompleteDecisionIdentity(base) {
		t.Fatal("complete independent diagnostic identity rejected")
	}
	for _, mutation := range []func(*Identity){func(v *Identity) { v.AgentConversationID = "source-conversation" }, func(v *Identity) { v.ExecutionID = "foreign-execution" }, func(v *Identity) { v.ReviewID = "review" }, func(v *Identity) { v.TaskDefinitionRevisionID = "" }, func(v *Identity) { v.OperationKind = "OTHER" }, func(v *Identity) { v.TurnID = "" }} {
		changed := base
		mutation(&changed)
		if CompleteDecisionIdentity(changed) {
			t.Fatalf("mixed source/child identity accepted: %+v", changed)
		}
	}
	base.RecoveryStatusID, base.OperationKind = "", "REVIEW"
	if CompleteDecisionIdentity(base) {
		t.Fatal("ordinary Review accepted absent business identity")
	}
}
