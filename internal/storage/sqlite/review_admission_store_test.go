package sqlite

import (
	"context"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/task"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
)

func TestReviewStartAdmissionIsAtomicIdempotentAndFeatureAreaScoped(t *testing.T) {
	f := newAgentStorageFixture(t, "review-admission")
	other := f.addAreaTask(t, "agent-area-review-admission-other", "agent-task-review-admission-other")
	next := 0
	reviews, err := review.NewService(NewReviewStore(f.db), func() (string, error) {
		next++
		return fmt.Sprintf("admission-%d", next), nil
	}, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	candidate := func(areaID string) review.Review {
		dirty := true
		return review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: f.task.ID, ProjectID: f.project.ID, FeatureAreaID: areaID, CreatedByAISessionID: f.session.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, BackendSelection: reviewFixtureSelection(t, f.db, f.task.CodexConfigID), RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}, BaseCommit: f.task.BaseCommit, RepositoryProvenance: &review.RepositoryProvenance{RepositoryID: f.task.RepositoryID, Commit: f.task.BaseCommit, RepositoryHEAD: f.task.BaseCommit, BranchKind: review.ProvenanceBranch, BranchRef: "refs/heads/review-source", WorkingTreeDirtyAtAdmission: &dirty, InspectionMode: review.InspectionFeatureAreaWorkspace, CapturedAt: testNow}, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Inspect admission", Instruction: "Inspect read-only"}
	}
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}
	plan := run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope}
	accepted, err := reviews.AdmitStart(context.Background(), candidate(f.area.ID), "start-a", strings.Repeat("a", 64), "Inspect read-only", scope, plan)
	if err != nil || accepted.Replayed || accepted.Review.ID == "" || accepted.Run.ID == "" || accepted.Review.RunID != accepted.Run.ID {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	provenance, err := NewReviewStore(f.db).RepositoryProvenance(context.Background(), accepted.Review.ID)
	if err != nil || provenance.BranchRef != "refs/heads/review-source" || provenance.WorkingTreeDirtyAtAdmission == nil || !*provenance.WorkingTreeDirtyAtAdmission || provenance.Commit != accepted.Review.BaseCommit {
		t.Fatalf("atomic admission provenance=%+v err=%v", provenance, err)
	}
	replayed, err := reviews.AdmitStart(context.Background(), candidate(f.area.ID), "start-a", strings.Repeat("a", 64), "Inspect read-only", scope, plan)
	if err != nil || !replayed.Replayed || replayed.Review.ID != accepted.Review.ID || replayed.Run.ID != accepted.Run.ID {
		t.Fatalf("accepted replay=%+v err=%v", replayed, err)
	}

	_, err = reviews.AdmitStart(context.Background(), candidate(f.area.ID), "start-busy", strings.Repeat("b", 64), "Inspect read-only", scope, plan)
	var busy *review.AdmissionBusyError
	if !errors.As(err, &busy) || busy.Owner.Kind != agentconversation.Review || busy.Owner.OperationID != accepted.Review.ID || busy.Owner.RunID != accepted.Run.ID {
		t.Fatalf("busy owner=%+v err=%v", busy, err)
	}
	_, retryErr := reviews.AdmitStart(context.Background(), candidate(f.area.ID), "start-busy", strings.Repeat("b", 64), "Inspect read-only", scope, plan)
	var retryBusy *review.AdmissionBusyError
	if !errors.As(retryErr, &retryBusy) || retryBusy.Owner.Kind != busy.Owner.Kind || retryBusy.Owner.OperationID != busy.Owner.OperationID || retryBusy.Owner.RunID != busy.Owner.RunID || retryErr.Error() != err.Error() {
		t.Fatalf("idempotent busy owner=%+v err=%v", retryBusy, retryErr)
	}
	if _, conflictErr := reviews.AdmitStart(context.Background(), candidate(f.area.ID), "start-busy", strings.Repeat("c", 64), "Inspect read-only", scope, plan); !errors.Is(conflictErr, review.ErrRequestConflict) {
		t.Fatalf("same requestId different payload err=%v", conflictErr)
	}

	otherCandidate := candidate(other.area.ID)
	otherCandidate.TaskID = other.task.ID
	otherAccepted, err := reviews.AdmitStart(context.Background(), otherCandidate, "start-busy", strings.Repeat("d", 64), "Inspect read-only", scope, plan)
	if err != nil || otherAccepted.Review.ID == "" || otherAccepted.Run.ID == "" {
		t.Fatalf("different Feature Area admission=%+v err=%v", otherAccepted, err)
	}
	var reviewCount, runCount, receiptCount int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM reviews WHERE feature_area_id=?`, f.area.ID).Scan(&reviewCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM runs WHERE feature_area_id=? AND operation_kind='REVIEW'`, f.area.ID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM feature_area_review_start_requests WHERE feature_area_id=?`, f.area.ID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if reviewCount != 1 || runCount != 1 || receiptCount != 2 {
		t.Fatalf("busy start polluted workflow history: reviews=%d runs=%d receipts=%d", reviewCount, runCount, receiptCount)
	}
}

func TestConcurrentReviewAdmissionsSerializeBeforeProductCreation(t *testing.T) {
	f := newAgentStorageFixture(t, "review-admission-race")
	reviews, err := review.NewService(NewReviewStore(f.db), nil, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	candidate := review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: f.task.ID, ProjectID: f.project.ID, FeatureAreaID: f.area.ID, CreatedByAISessionID: f.session.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, BackendSelection: reviewFixtureSelection(t, f.db, f.task.CodexConfigID), RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}, BaseCommit: f.task.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Serialize admission", Instruction: "Inspect read-only"}
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}
	plan := run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope}
	type outcome struct {
		admission review.StartAdmission
		err       error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	var workers sync.WaitGroup
	for index, hash := range []string{strings.Repeat("1", 64), strings.Repeat("2", 64)} {
		workers.Add(1)
		go func(requestID, payloadHash string) {
			defer workers.Done()
			<-start
			value, startErr := reviews.AdmitStart(context.Background(), candidate, requestID, payloadHash, candidate.Instruction, scope, plan)
			results <- outcome{value, startErr}
		}(fmt.Sprintf("concurrent-%d", index), hash)
	}
	close(start)
	workers.Wait()
	close(results)
	var accepted review.StartAdmission
	var busy *review.AdmissionBusyError
	for result := range results {
		if result.err == nil {
			if accepted.Review.ID != "" {
				t.Fatalf("both concurrent starts were accepted: first=%+v second=%+v", accepted, result.admission)
			}
			accepted = result.admission
			continue
		}
		if !errors.As(result.err, &busy) {
			t.Fatalf("concurrent admission error=%v", result.err)
		}
	}
	if accepted.Review.ID == "" || busy == nil || busy.Owner.OperationID != accepted.Review.ID || busy.Owner.RunID != accepted.Run.ID {
		t.Fatalf("serialized outcomes accepted=%+v busy=%+v", accepted, busy)
	}
	var reviewCount, runCount, receiptCount int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM reviews WHERE feature_area_id=?`, f.area.ID).Scan(&reviewCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM runs WHERE feature_area_id=? AND operation_kind='REVIEW'`, f.area.ID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM feature_area_review_start_requests WHERE feature_area_id=?`, f.area.ID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if reviewCount != 1 || runCount != 1 || receiptCount != 2 {
		t.Fatalf("concurrent starts escaped admission: reviews=%d runs=%d receipts=%d", reviewCount, runCount, receiptCount)
	}
}
