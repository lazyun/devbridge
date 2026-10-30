package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workspace"
)

// Both orders use real canonical reservation stores and the same Project/DB.
// No terminal states, claims or conversations are inserted by the test.
func TestProjectInspectionAndFeatureAreaOwnersRemainIndependent(t *testing.T) {
	for _, inspectionFirst := range []bool{false, true} {
		name := "area-first"
		if inspectionFirst {
			name = "inspection-first"
		}
		t.Run(name, func(t *testing.T) {
			f := newAgentStorageFixture(t, "inspection-area-"+name)
			ctx := context.Background()
			op := prepareAgentOperation(t, f, agentconversation.Execution, "coexist-"+name)
			config, e := NewCodexConfigStore(f.db).Get(ctx, f.task.CodexConfigID)
			if e != nil {
				t.Fatal(e)
			}
			id := "inspection-coexist-" + name
			plan := contextprobe.InspectionConfirmationPlan{ContractVersion: contextprobe.CanonicalInspectionContractVersion, AISessionID: f.session.ID, ProjectID: f.project.ID, RequestID: id, InspectionID: id, TaskID: "work_" + id, PayloadSHA256: strings.Repeat("a", 64), CodexInstruction: "Inspect only the confirmed repository", Backend: config.Selection("fixture"), Model: f.task.Model, ReasoningEffort: f.task.ReasoningEffort, PrimaryMemberID: "member-" + id, Targets: []contextprobe.CanonicalTarget{{RepositoryID: f.task.RepositoryID, MemberID: "member-" + id, MemberVersion: 1, OriginCommit: f.task.BaseCommit, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}}}}
			raw, _ := json.Marshal(plan)
			p := contextprobe.Probe{ID: id, TaskID: plan.TaskID, AISessionID: f.session.ID, ProjectID: f.project.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: config.ID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository, Paths: []string{}}, CodexInstruction: plan.CodexInstruction, Status: contextprobe.StatusCreated, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}, CanonicalRequestID: id, CanonicalPayloadSHA256: plan.PayloadSHA256, PrimaryMemberID: plan.PrimaryMemberID, CreatedAt: testNow, UpdatedAt: testNow}
			if _, e = NewContextProbeStore(f.db).ConfirmCanonicalInspection(ctx, contextprobe.ConfirmedInspection{Probe: p, Plan: plan, ReceiptJSON: string(raw)}); e != nil {
				t.Fatal(e)
			}
			inspectionStore := NewInspectionRuntimeStore(f.db)
			spec, e := inspectionStore.ConfirmedInspection(ctx, f.session.ID, id)
			if e != nil {
				t.Fatal(e)
			}
			var inspectionRun, inspectionConversation, areaRun, areaConversation string
			inspection := func() {
				state, e := inspectionStore.ReserveInspection(ctx, spec)
				if e != nil {
					t.Fatal(e)
				}
				inspectionRun, inspectionConversation = state.Run.ID, state.Conversation.ID
			}
			area := func() {
				r, e := op.make(ctx, "area-run-"+name)
				if e != nil {
					t.Fatal(e)
				}
				areaRun = r.ID
				if r.Status != run.StatusPreparing {
					if _, e = NewRunStore(f.db).Update(ctx, r.ID, func(v *run.Run) error { v.Status = run.StatusPreparing; return nil }); e != nil {
						t.Fatal(e)
					}
				}
				c, e := NewAgentConversationStore(f.db).BeginRun(ctx, r.ID)
				if e != nil {
					t.Fatal(e)
				}
				areaConversation = c.ID
			}
			if inspectionFirst {
				inspection()
				area()
			} else {
				area()
				inspection()
			}
			if inspectionRun == areaRun || inspectionConversation == areaConversation {
				t.Fatal("owners share run/conversation")
			}
			svc := workspace.NewService(NewWorkspaceStore(f.db), nil, nil)
			areaWorkspace, e := svc.EnsureForFeatureArea(ctx, f.project.ID, f.task.RepositoryID, f.area.ID, f.task.BaseCommit)
			if e != nil {
				t.Fatal(e)
			}
			planned, e := svc.PlanInspectionMembers(ctx, f.project.ID, p.TaskID, id, spec.Targets)
			if e != nil {
				t.Fatal(e)
			}
			inspectionWorkspace, e := svc.EnsurePlannedInspectionMembers(ctx, f.project.ID, p.TaskID, id, planned.WorkspaceID, planned.Targets)
			if e != nil {
				t.Fatal(e)
			}
			if areaWorkspace.ID == inspectionWorkspace.ID || inspectionWorkspace.FeatureAreaID != "" || inspectionWorkspace.OwnerID != id {
				t.Fatal("owners share workspace")
			}
			claim, e := NewAgentConversationStore(f.db).GetClaim(ctx, f.area.ID)
			if e != nil || claim.RunID != areaRun {
				t.Fatalf("FeatureArea claim changed: %+v %v", claim, e)
			}
			var actual string
			if e = f.db.sql.QueryRow(`SELECT run_id FROM project_inspection_agent_claims WHERE inspection_id=?`, id).Scan(&actual); e != nil || actual != inspectionRun {
				t.Fatalf("Inspection claim changed: %s %v", actual, e)
			}
		})
	}
}
