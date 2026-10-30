package recoverystatus

import (
	"strings"
	"testing"
)

func TestProjectionSeparatesEngineeringFromConsumption(t *testing.T) {
	for _, state := range []string{"REQUIRED", "RUNNING", "ASSESSED", "RESOLVED", "FAILED", "UNKNOWN"} {
		t.Run(state, func(t *testing.T) {
			p := ProjectProjection(Projection{State: state, SourceResponseDisposition: "UNKNOWN", Consistency: "CONSISTENT", EngineeringDisposition: "RECOVERED_COMPLETE"})
			if !strings.Contains(p.DisplayMessage, "无法确认") || strings.Contains(p.DisplayMessage, "回答已确认收到") {
				t.Fatalf("consumption changed: %+v", p)
			}
			if state != "RESOLVED" && strings.Contains(p.DisplayMessage, "独立核验") {
				t.Fatalf("premature resolution: %+v", p)
			}
			for _, action := range p.AvailableActions {
				if strings.Contains(action, "RESPOND") || strings.Contains(action, "RESEND") {
					t.Fatalf("answer action: %s", action)
				}
			}
		})
	}
}

func TestProjectionNeverOffersResolutionForConflictingEvidence(t *testing.T) {
	for _, consistency := range []string{"CONFLICT", "INSUFFICIENT", ""} {
		p := ProjectProjection(Projection{State: "ASSESSED", Consistency: consistency, EngineeringDisposition: "RECOVERED_COMPLETE"})
		for _, action := range p.AvailableActions {
			if action == "RESOLVE_RECOVERY_STATUS" {
				t.Fatal("conflicting evidence offered resolution")
			}
		}
	}
}

func TestProjectionOffersCanonicalExitForKnownInsufficientTurn(t *testing.T) {
	p := ProjectProjection(Projection{Required: true, State: "ASSESSED", Consistency: "INSUFFICIENT", EngineeringDisposition: "RECOVERY_INSUFFICIENT", RecoveryRunID: "child", RecoveryTurnID: "turn"})
	joined := strings.Join(p.AvailableActions, ",")
	if !strings.Contains(joined, "CANCEL_OPERATION") || !strings.Contains(joined, "STOP_AND_REVISE") || strings.Contains(joined, "START_RECOVERY_STATUS") || strings.Contains(joined, "RESOLVE_RECOVERY_STATUS") {
		t.Fatalf("bad exit guidance: %+v", p)
	}
	p = ProjectProjection(Projection{Required: true, State: "UNKNOWN", RecoveryRunID: "child"})
	if len(p.AvailableActions) != 1 {
		t.Fatal("unknown Turn offered unsafe retry or interrupt")
	}
}

func TestProjectionShowsProjectInspectionOwnerSeparately(t *testing.T) {
	p := ProjectProjection(Projection{State: "RESOLVED", Consistency: "CONSISTENT", EngineeringDisposition: "RECOVERED_COMPLETE", SourceResponseDisposition: "UNKNOWN", OwnerKind: "PROJECT_INSPECTION", OwnerID: "probe", ProjectID: "project", OwnerTaskID: "task", WorkspaceID: "workspace", RuntimeWorksetHash: "hash", MemberCount: 2, SourceIsRecoveryChild: true})
	if p.FeatureAreaID != "" || p.OwnerKind != "PROJECT_INSPECTION" || p.WorkspaceID != "workspace" || p.MemberCount != 2 || p.DisplayMessage == "" {
		t.Fatalf("inspection projection=%+v", p)
	}
}

func TestProjectionOffersOnlyCanonicalInspectionContinuationOrSuccessor(t *testing.T) {
	base := Projection{State: "RESOLVED", Consistency: "CONSISTENT", OwnerKind: "PROJECT_INSPECTION", OwnerID: "probe", ProjectID: "project", OwnerTaskID: "task", WorkspaceID: "workspace", RuntimeWorksetHash: "hash", MemberCount: 2}
	partial := base
	partial.EngineeringDisposition = "RECOVERED_PARTIAL"
	if actions := strings.Join(ProjectProjection(partial).AvailableActions, ","); !strings.Contains(actions, "CONTINUE_PROJECT_INSPECTION") || strings.Contains(actions, "PREPARE_SUCCESSOR_INSPECTION") {
		t.Fatalf("partial actions=%s", actions)
	}
	revision := base
	revision.EngineeringDisposition = "REVISION_REQUIRED"
	if actions := strings.Join(ProjectProjection(revision).AvailableActions, ","); strings.Contains(actions, "CONTINUE_PROJECT_INSPECTION") || !strings.Contains(actions, "PREPARE_SUCCESSOR_INSPECTION") {
		t.Fatalf("revision actions=%s", actions)
	}
}
