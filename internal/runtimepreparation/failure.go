package runtimepreparation

import "time"

// FailureSettlement is emitted only by the synchronous preparation owner after
// all external calls have returned. A timeout or an interrupted Create does not
// supply this proof. The retained artifact list never authorizes another call.
type FailureSettlement struct {
	PlanID, RunID, RequestID, PayloadHash, RuntimeEpoch string
	Stage, Code, Message                                string
	Artifacts                                           []FailureArtifact
	At                                                  time.Time
}
type FailureArtifact struct {
	MemberID   string `json:"memberId"`
	WorktreeID string `json:"worktreeId"`
	Root       string `json:"root"`
}
