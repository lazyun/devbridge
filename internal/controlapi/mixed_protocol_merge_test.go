package controlapi_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/workset"
	"os"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func mixedLegacyTask(t *testing.T) *upperLifecycleFixture {
	return mixedLegacyTaskFromFixture(t, newUpperLifecycleFixture(t))
}

func mixedLegacyTaskFromFixture(t *testing.T, f *upperLifecycleFixture) *upperLifecycleFixture {
	commit := func(in c.PrepareInput) c.Intent {
		t.Helper()
		in.AISessionID = f.session
		p, err := f.facade.PrepareConfirmation(f.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "v1-commit-" + in.RequestID, ContentHash: p.ContentHash, VariantID: in.Variants[0].ID})
		if err != nil || out.State != "COMPLETED" {
			t.Fatalf("commit %s: %+v %v", in.Action, out, err)
		}
		return out
	}
	area := commit(c.PrepareInput{RequestID: "v1-area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Legacy compatibility", ProblemStatement: "Preserve the single-repository Upper Review protocol"}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	areaID := area.Steps[0].FeatureAreaID
	created := commit(c.PrepareInput{RequestID: "v1-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: areaID, RepositoryID: f.repos["app"], Title: "Legacy app review", ProblemStatement: "Review app source", Constraints: []string{"read only"}}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	taskID := created.Steps[0].TaskID
	started := commit(c.PrepareInput{RequestID: "v1-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: taskID, FeatureAreaID: areaID, RepositoryID: f.repos["app"], ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "review", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt"}}})
	reviewID, runID := started.Steps[0].ReviewID, started.Steps[0].RunID
	access, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, reviewID)
	if err != nil || access.State != "ACTIVE" {
		t.Fatalf("access=%+v %v", access, err)
	}
	read, err := f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &access.Target, Path: "same.txt"})
	if err != nil {
		t.Fatal(err)
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationDirectFile, Goal: "legacy app update", Constraints: []string{"bounded"}, CodexInstruction: "update app", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "app repository", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true}}, AcceptanceCriteria: []string{"same.txt remains present"}, CodexInstruction: "Verify the app repository"}
	submission := review.SubmitInput{UpperTarget: access.Target, RequestID: "v1-report", Report: review.ReviewReport{SchemaVersion: 1, Summary: "legacy report", Disposition: review.DispositionApprovalReady, Coverage: []string{"same.txt"}, Findings: []review.Finding{}, Limitations: []string{"read only"}, RecommendedNextSteps: []string{"approve"}, EvidenceRefs: []review.EvidenceRef{{EvidenceID: read.EvidenceID, Path: "same.txt", Digest: read.Digest}}, WorkSpec: &work, VerificationSpec: &verification}}
	receipt, err := f.facade.SubmitReviewResult(f.ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := f.facade.SubmitReviewResult(f.ctx, submission); err != nil || replay.ReportID != receipt.ReportID {
		t.Fatalf("report replay=%+v %v", replay, err)
	}
	var raw, reportPayloadHash, beforeWorktree string
	if err := f.plane.DB.SQL().QueryRow(`SELECT report_json,payload_sha256 FROM review_reports WHERE id=?`, receipt.ReportID).Scan(&raw, &reportPayloadHash); err != nil {
		t.Fatal(err)
	}
	if err := f.plane.DB.SQL().QueryRow(`SELECT worktree_id FROM runs WHERE id=?`, runID).Scan(&beforeWorktree); err != nil || beforeWorktree == "" {
		t.Fatalf("worktree=%q %v", beforeWorktree, err)
	}
	waived := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Explicit isolated v1 fixture user waiver"}}
	approval := commit(c.PrepareInput{RequestID: "v1-file-approval", Purpose: c.Execution, Action: "APPROVE_AND_START", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, taskID, areaID, reviewID, "APPROVE_AND_START"), Variants: []c.Variant{{ID: "files", Summary: "Explicit single repository file implementation with user waived command verification", ImplementationMode: task.ImplementationDirectFile, WorkSpec: &work, VerificationSpec: &waived}}})
	current, err := f.facade.GetTask(f.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, taskID)
	if err != nil || state.State != "ACTIVE" {
		t.Fatalf("v1 grant %+v %v", state, err)
	}
	in := directfile.Input{Scope: directfile.Scope{AISessionID: f.session, FeatureAreaID: areaID, WorkflowID: state.WorkflowID}, Path: "same.txt", Content: "single-repo compatibility update\n"}
	if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	resultInput := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: taskID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "v1-file-result", Summary: "v1 file result", VerificationNotes: "explicit user waiver"}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, resultInput)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := f.facade.SubmitFileImplementationResult(f.ctx, resultInput); err != nil || replay.ID != result.ID {
		t.Fatalf("v1 result replay %v", err)
	}
	accept := directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: taskID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "v1-accept-files"}
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, accept)
	if err != nil || !accepted.Accepted || accepted.CommitError != "" {
		t.Fatalf("v1 acceptance %+v %v", accepted, err)
	}
	delivered, err := f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, taskID)
	if err != nil || delivered.CommitSHA == "" {
		t.Fatalf("v1 commit %+v %v", delivered, err)
	}
	_ = approval
	f.area, f.task = areaID, taskID
	f.plane.Retirement.Close()
	return f
}
func mixedNextTask(t *testing.T, source *upperLifecycleFixture, tag string, names []string) *upperLifecycleFixture {
	t.Helper()
	f := *source
	commit := func(in c.PrepareInput) c.Intent {
		t.Helper()
		in.AISessionID = f.session
		prepared, err := f.facade.PrepareConfirmation(f.ctx, in)
		if err != nil {
			t.Fatalf("prepare %s: %v", in.Action, err)
		}
		got, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: tag + "-commit-" + in.RequestID, ContentHash: prepared.ContentHash, VariantID: in.Variants[0].ID})
		if err != nil || got.State != "COMPLETED" {
			t.Fatalf("commit %s: intent=%+v err=%v", in.Action, got, err)
		}
		return got
	}
	created := commit(c.PrepareInput{RequestID: tag + "-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: f.area, Title: "Competing task " + tag, ProblemStatement: "Change exact repository files", Constraints: []string{"preserve unrelated bytes"}}, Variants: []c.Variant{{ID: "create", Summary: "create task"}}})
	f.task = created.Steps[0].TaskID
	targets := []workset.Target{}
	for _, name := range names {
		var ref string
		if err := f.plane.DB.SQL().QueryRow(`SELECT development_ref FROM development_cycles WHERE repository_id=? AND superseded_at IS NULL`, f.repos[name]).Scan(&ref); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, workset.Target{RepositoryID: f.repos[name], OriginCommit: f.heads[name], OriginRef: ref, Read: true, Write: workset.WriteScope{Kind: "NONE"}})
	}
	started := commit(c.PrepareInput{RequestID: tag + "-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "review exact members", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt from both repositories"}}})
	f.review, f.run = started.Steps[0].ReviewID, started.Steps[0].RunID
	var err error
	f.access, err = f.facade.UpperReviewAccessForSession(f.ctx, f.session, f.review)
	if err != nil || f.access.State != "ACTIVE" {
		t.Fatalf("second review access=%+v err=%v", f.access, err)
	}
	value, err := f.plane.Reviews.Get(f.ctx, f.review)
	if err != nil || len(value.RepositoryTargets) != len(names) {
		t.Fatalf("second frozen targets=%+v err=%v", value.RepositoryTargets, err)
	}
	f.targets = workset.CloneTargets(value.RepositoryTargets)
	return &f
}

func TestMixedProtocolAreaMergePreservesLegacyDelivery(t *testing.T) {
	for _, mode := range []string{"fresh", "legacy-already-merged", "applied-restart", "historical-partial-plan", "missing-legacy-source"} {
		t.Run(mode, func(t *testing.T) {
			f := mixedLegacyTask(t)
			legacyTask := f.task
			legacy, err := f.facade.GetTask(f.ctx, legacyTask)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "legacy-already-merged" {
				if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "legacy-merge"); err != nil {
					t.Fatal(err)
				}
			}
			var receiptBefore string
			receiptSQL := `SELECT json_object('revision',revision_id,'snapshot',verified_snapshot,'head',pre_commit_head,'commit',resulting_commit,'completed',completed_at,'workspace',workspace_id,'path',workspace_path,'branch',branch,'protocol',protocol_version) FROM task_commit_receipts WHERE task_id=?`
			if err = f.plane.DB.SQL().QueryRow(receiptSQL, legacyTask).Scan(&receiptBefore); err != nil {
				t.Fatal(err)
			}
			next := mixedNextTask(t, f, "next", []string{"sdk"})
			current := approveDirectOnFixture(t, next, "next")
			acceptCompetingDirectTask(t, next, current, "next")
			var source string
			if err = f.plane.DB.SQL().QueryRow(`SELECT workspace_path FROM task_commit_receipts WHERE task_id=?`, legacyTask).Scan(&source); err != nil {
				t.Fatal(err)
			}
			if mode == "missing-legacy-source" {
				if err = os.Rename(source, source+"-offline"); err != nil {
					t.Fatal(err)
				}
				defer os.Rename(source+"-offline", source)
				if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "missing-merge"); err == nil {
					t.Fatal("missing legacy checkout silently omitted")
				}
				cap, e := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
				if e != nil || cap.CommitStatus != "UNCOMMITTED" {
					t.Fatalf("false completion: %+v %v", cap, e)
				}
				var parents int
				if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_commit_receipts WHERE feature_area_id=?`, f.area).Scan(&parents); e != nil || parents != 0 {
					t.Fatalf("invalid plan wrote parent: %d %v", parents, e)
				}
				return
			}
			plan, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, m := range plan.Members {
				for _, sha := range m.TaskCommits {
					if sha == legacy.CommitSHA {
						found = true
					}
				}
			}
			if found != (mode != "legacy-already-merged") {
				t.Fatalf("legacy coverage: found=%v mode=%s plan=%+v", found, mode, plan)
			}
			if mode == "historical-partial-plan" {
				historical := plan
				historical.Members = nil
				for _, m := range plan.Members {
					if m.RepositoryID == f.repos["sdk"] {
						historical.Members = append(historical.Members, m)
					}
				}
				if err = historical.Canonicalize(); err != nil {
					t.Fatal(err)
				}
				// A newly submitted incomplete plan must be rejected.
				if _, err = f.plane.WorkflowProgression.CommitFeatureAreaPlan(f.ctx, f.project, f.area, "invalid-fresh", historical); err == nil {
					t.Fatal("fresh plan omitted legacy Task")
				}
				reserveHistoricalMixedPlan(t, f, next.task, "old-partial", historical)
				if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "old-partial"); err != nil {
					t.Fatal(err)
				}
				cap, e := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
				if e != nil || cap.CommitStatus != "UNCOMMITTED" || !cap.CanCommit {
					t.Fatalf("old partial history hid legacy work: %+v %v", cap, e)
				}
				oldTask, e := f.facade.GetTask(f.ctx, legacyTask)
				if e != nil || oldTask.MergedCommit != "" {
					t.Fatalf("unmerged legacy Task falsely projected: %+v %v", oldTask, e)
				}
				plan, err = f.plane.WorkflowProgression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
				if err != nil || len(plan.Members) != 1 || plan.Members[0].RepositoryID != f.repos["app"] {
					t.Fatalf("cannot repair omitted legacy commit: %+v %v", plan, err)
				}
			}
			if mode == "applied-restart" {
				f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(stage, repo string) error {
					if stage == "AREA_MERGE_AFTER_GIT" {
						return errors.New("lose merge projection")
					}
					return nil
				})
			}
			_, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "mixed-merge")
			if mode == "applied-restart" {
				if err == nil {
					t.Fatal("expected interrupted merge")
				}
				reopenDirectDeliveryFixture(t, f)
				f.plane.Retirement.Close()
				_, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "mixed-merge")
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range plan.Members {
				for _, sha := range m.TaskCommits {
					if got := gitOutput(t, m.TargetPath, "merge-base", sha, "HEAD"); got != sha {
						t.Fatalf("commit not integrated: %s", sha)
					}
				}
			}
			completed, err := f.facade.GetTask(f.ctx, legacyTask)
			if err != nil || completed.MergedCommit == "" || completed.CommitSHA != legacy.CommitSHA {
				t.Fatalf("legacy checkpoint: %+v %v", completed, err)
			}
			var receiptAfter string
			if err = f.plane.DB.SQL().QueryRow(receiptSQL, legacyTask).Scan(&receiptAfter); err != nil || receiptBefore != receiptAfter {
				t.Fatalf("historical Task receipt changed: %v", err)
			}
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
			cap, err := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
			if err != nil || cap.CommitStatus != "COMMITTED" || cap.CanCommit {
				t.Fatalf("cold capability: %+v %v", cap, err)
			}
			var countBefore, countAfter int
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM merge_delivery_dispatches`).Scan(&countBefore); err != nil {
				t.Fatal(err)
			}
			if _, err = f.facade.CommitFeatureArea(f.ctx, f.session, f.area, "mixed-merge"); err != nil {
				t.Fatal(err)
			}
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM merge_delivery_dispatches`).Scan(&countAfter); err != nil || countBefore != countAfter {
				t.Fatalf("replay dispatched: %d -> %d %v", countBefore, countAfter, err)
			}
		})
	}
}

// Recreate a pre-fix pending plan which legitimately froze only the v2 Task.
// Keep all current SQL guards enabled and let the normal dispatcher perform Git.
func reserveHistoricalMixedPlan(t *testing.T, f *upperLifecycleFixture, taskID, request string, plan delivery.FeatureAreaPlan) {
	t.Helper()
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.plane.DB.SQL().BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO feature_area_commit_receipts(request_id,feature_area_id,repository_id,development_ref,source_head,expected_dev_head,source_path,source_branch,target_path,protocol_version,plan_sha256,plan_json) VALUES(?,?,NULL,'','','','','','',2,?,?)`, request, f.area, plan.SHA256, string(encoded)); err != nil {
		t.Fatal(err)
	}
	for _, m := range plan.Members {
		commits, _ := json.Marshal(m.TaskCommits)
		if _, err = tx.Exec(`INSERT INTO feature_area_commit_members(request_id,repository_id,member_id,member_version,worktree_id,source_path,source_branch,source_head,expected_dev_head,development_ref,development_cycle_id,target_path,task_commits_json,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'PENDING')`, request, m.RepositoryID, m.MemberID, m.MemberVersion, m.WorktreeID, m.SourcePath, m.SourceBranch, m.SourceHead, m.ExpectedDevHead, m.DevelopmentRef, m.DevelopmentCycleID, m.TargetPath, string(commits)); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(struct {
			ProjectID, FeatureAreaID, WorkspaceID, PlanSHA256 string
			Member                                            delivery.FeatureAreaMember
		}{plan.ProjectID, plan.FeatureAreaID, plan.WorkspaceID, plan.SHA256, m})
		sum := sha256.Sum256(payload)
		operation := "historical-" + m.RepositoryID
		if _, err = tx.Exec(`INSERT INTO merge_delivery_operations(operation_id,project_id,feature_area_id,merge_request_id,repository_id,task_id,execution_id,member_id,workset_id,development_context_id,request_id,payload_json,payload_sha256,created_at) SELECT ?,?,?,?, ?,t.id,COALESCE(NULLIF(t.current_execution_id,''),'direct/'||q.result_id),?,?,?,?,?,?,? FROM tasks t JOIN task_commit_receipts q ON q.task_id=t.id WHERE t.id=?`, operation, f.project, f.area, request, m.RepositoryID, m.MemberID, plan.WorkspaceID, m.DevelopmentCycleID, operation, string(payload), hex.EncodeToString(sum[:]), time.Now().UTC().Format(time.RFC3339Nano), taskID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO merge_delivery_current_operations VALUES(?,?,?)`, request, m.RepositoryID, operation); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
