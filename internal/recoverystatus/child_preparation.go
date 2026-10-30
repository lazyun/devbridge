package recoverystatus

import "github.com/lazyun/devbridge/internal/workset"

// ChildPreparation is an exact readback of the admitted child plan. It carries
// intended repository scope, never source worktrees, roots, snapshots or CWD.
type ChildPreparation struct {
	PlanID            string
	RequestID         string
	PayloadSHA256     string
	ProjectID         string
	FeatureAreaID     string
	PrimaryMemberID   string
	Targets           []workset.Target
	ExpectedHEADs     map[string]string
	BackendConfigID   string
	Model             string
	ReasoningEffort   string
	AdapterIdentity   string
	Generation        int
	SourceRunID       string
	SourceThreadID    string
	SourceTurnID      string
	SourceWorksetHash string
	SourceWorkspaceID string
}

type ChildThreadClaim struct {
	RecoveryID, ChildRunID, WorksetHash, WorkspaceID, RuntimeEpoch string
	RequestID, PayloadSHA256                                       string
}

type ChildThreadReceipt struct {
	RequestID, PayloadSHA256, Outcome, ChildThreadID, AppServerInstanceID, BindingHash, FailureCode string
}

type ChildTurnClaim struct {
	RecoveryID, ThreadRequestID, ChildThreadID, RequestID, PayloadSHA256 string
}

type ChildTurnReceipt struct {
	RequestID, PayloadSHA256, Outcome, ChildThreadID, ChildTurnID, DispatchAttemptID, FailureCode string
}
