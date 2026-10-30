package controlapi_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestRecoveryStatusOfflineFeatureAreaSerialization(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer unblock()
	runRecoveryStatusOfflineProvider(t, "complete-old-unknown", func(n int64) {
		if n == 4 {
			close(entered)
			select {
			case <-release:
			case <-time.After(60 * time.Second):
			}
		}
	}, func(f *upperLifecycleFixture, _ controlplane.Config, cmd controlapi.RecoveryStatusCommand, calls *atomic.Int64) bool {
		defer unblock()
		if _, err := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(30 * time.Second):
			t.Fatal("recovery provider not entered")
		}
		before := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool { return r.State == "RUNNING" && r.RecoveryTurnID != "" })
		startReview := func(areaID, taskID, request string, targets []workset.Target) (c.Intent, error) {
			prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: request, Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: taskID, FeatureAreaID: areaID, RepositoryTargets: targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "coding", Summary: "Read exact approved members", ExecutorKind: string(review.ExecutorCodingAgent), PrimaryMemberID: targets[0].MemberID, ReviewInstruction: "Read the approved members and report findings only."}}})
			if err != nil {
				return prepared, err
			}
			options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
			if err != nil {
				return prepared, err
			}
			if len(options.BackendCandidates) == 0 {
				t.Fatal("missing backend options")
			}
			b := options.BackendCandidates[0]
			return f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: request + "-commit", ContentHash: prepared.ContentHash, VariantID: "coding", Selection: c.Selection{RuntimeConfig: &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}, Backend: &codexconfig.BackendSelectionInput{ConfigID: b.ConfigID, ConfigFingerprint: b.ConfigFingerprint}}})
		}
		if contender, err := startReview(cmd.FeatureAreaID, cmd.TaskID, "same-area-contender", workset.CloneTargets(f.targets)); err == nil && contender.State == "COMPLETED" {
			t.Fatal("same-area Coding operation bypassed recovery claim")
		}
		if calls.Load() != 4 {
			t.Fatal("blocked contender invoked provider")
		}
		commit := func(in c.PrepareInput) c.Intent {
			in.AISessionID = f.session
			prepared, err := f.facade.PrepareConfirmation(f.ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			got, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: in.RequestID + "-commit", ContentHash: prepared.ContentHash, VariantID: in.Variants[0].ID})
			if err != nil || got.State != "COMPLETED" {
				t.Fatalf("create peer: %+v %v", got, err)
			}
			return got
		}
		area := commit(c.PrepareInput{RequestID: "peer-area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Independent recovery peer", ProblemStatement: "Read separate managed members"}, Variants: []c.Variant{{ID: "create", Summary: "Create independent area"}}}).Steps[0].FeatureAreaID
		peerTask := commit(c.PrepareInput{RequestID: "peer-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: area, Title: "Peer review", ProblemStatement: "Read members independently"}, Variants: []c.Variant{{ID: "create", Summary: "Create peer Task"}}}).Steps[0].TaskID
		targets := []workset.Target{}
		for _, name := range []string{"app", "sdk"} {
			targets = append(targets, workset.Target{RepositoryID: f.repos[name], OriginCommit: f.heads[name], OriginRef: "refs/heads/" + name + "-development", Read: true, Write: workset.WriteScope{Kind: "NONE"}})
		}
		frozen := commit(c.PrepareInput{RequestID: "peer-freeze-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: peerTask, FeatureAreaID: area, RepositoryTargets: targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "Read exact members", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt from the approved members."}}})
		peerFixture := *f
		peerFixture.area, peerFixture.task, peerFixture.review = area, peerTask, frozen.Steps[0].ReviewID
		var err error
		peerFixture.access, err = f.facade.UpperReviewAccessForSession(f.ctx, f.session, peerFixture.review)
		if err != nil {
			t.Fatal(err)
		}
		frozenReview, err := f.plane.Reviews.Get(f.ctx, peerFixture.review)
		if err != nil {
			t.Fatal(err)
		}
		targets = frozenReview.RepositoryTargets
		peerFixture.targets = targets
		if _, err = f.facade.SubmitReviewResult(f.ctx, peerFixture.findingsOnly("peer-freeze-complete", peerFixture.access.Target)); err != nil {
			t.Fatal(err)
		}
		peer, err := startReview(area, peerTask, "other-area-contender", targets)
		if err != nil || peer.State != "COMPLETED" || len(peer.Steps) != 1 || peer.Steps[0].RunID == "" {
			t.Fatalf("other area blocked: %+v %v", peer, err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for calls.Load() < 5 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if calls.Load() != 5 {
			t.Fatalf("other area never entered provider during active recovery: %d", calls.Load())
		}
		still, err := f.plane.RecoveryStatus.Read(f.ctx, cmd.ID)
		if err != nil || still.State != "RUNNING" || still.RecoveryTurnID != before.RecoveryTurnID {
			t.Fatalf("peer changed recovery: %+v %v", still, err)
		}
		unblock()
		after := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool { return r.State == "ASSESSED" || r.State == "FAILED" })
		resolved, err := resolveRecoveryFixture(t, f, cmd, after)
		if err != nil || resolved.Record.State != "RESOLVED" {
			t.Fatalf("original resolution: %+v %v", resolved, err)
		}
		return false
	})
}
