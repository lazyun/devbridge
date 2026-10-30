package controlapi_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func taskReviewBool(value bool) *bool { return &value }

func taskReviewCommit(prepared c.Intent, request string, selected *bool) c.CommitInput {
	return c.CommitInput{AISessionID: prepared.AISessionID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: request, ContentHash: prepared.ContentHash, VariantID: prepared.Content.Variants[0].ID, Selection: c.Selection{StartReview: selected}}
}

func taskReviewOptions(prepared c.Intent, selected *bool) c.OptionsInput {
	return c.OptionsInput{ReadInput: c.ReadInput{AISessionID: prepared.AISessionID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}, StartReview: selected}
}

func TestConfirmationTaskReviewChoice(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prepared bool
		coding   bool
		selected *bool
		wantRun  int
	}{
		{"legacy-create-only", false, false, nil, 0},
		{"explicit-create-only", false, false, taskReviewBool(false), 0},
		{"defer-coding-review-without-runtime-or-backend-inventory", true, true, taskReviewBool(false), 0},
		{"defer-upper-review", true, false, taskReviewBool(false), 0},
		{"explicit-review-on", true, false, taskReviewBool(true), 1},
		{"legacy-prepared-review", true, false, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCompositionFixture(t)
			variant := compositionUpperVariant("prepared", "Inspect repository", "Inspect read-only and report findings.")
			if tc.coding {
				variant.ExecutorKind = string(review.ExecutorCodingAgent)
			}
			input := f.taskPrepare("prepare", variant)
			if !tc.prepared {
				input.Action, input.Task.StartReview = "CREATE_TASK", false
				input.Task.ReviewDepth, input.Task.ReviewPlan = "", run.ReviewPlan{}
				input.Variants = []c.Variant{{ID: "prepared", Label: "Task", Summary: "Create the Task without review."}}
			}
			prepared, err := f.facade.PrepareConfirmation(f.ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			frozenContent := c.ValueHash(prepared.Content)
			// Disable every backend and use the fixture's absent runtime inventory.
			// A task-only selection must qualify independently of either inventory.
			if _, err := f.plane.DB.SQL().ExecContext(f.ctx, `UPDATE codex_configs SET enabled=0`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.facade.CodexRuntimeInventory(); err == nil {
				t.Fatal("fixture unexpectedly has a runtime inventory")
			}
			options, err := f.facade.ConfirmationOptions(f.ctx, taskReviewOptions(prepared, tc.selected))
			if err != nil || !options.Eligibility.Available || len(options.Variants) != 1 || !options.Variants[0].Available {
				t.Fatalf("options=%+v err=%v", options, err)
			}
			if options.StartReview == nil || *options.StartReview != (tc.wantRun == 1) || len(options.BackendCandidates) != 0 || options.LockedBackend != nil {
				t.Fatalf("options qualified another Review choice: %+v", options)
			}
			if tasks, reviews, runs := f.counts(t); tasks != 0 || reviews != 0 || runs != 0 {
				t.Fatalf("read-only preparation/options wrote objects: %d/%d/%d", tasks, reviews, runs)
			}
			request := taskReviewCommit(prepared, "commit", tc.selected)
			committed, err := f.facade.CommitConfirmation(f.ctx, request)
			if err != nil || committed.State != "COMPLETED" {
				t.Fatalf("commit=%+v err=%v", committed, err)
			}
			if committed.Action != prepared.Action || committed.ContentHash != prepared.ContentHash || c.ValueHash(committed.Content) != frozenContent {
				t.Fatalf("the review choice rewrote frozen preparation: %+v", committed)
			}
			if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != tc.wantRun || runs != tc.wantRun || len(committed.Steps) != 1+tc.wantRun {
				t.Fatalf("objects=%d/%d/%d steps=%+v", tasks, reviews, runs, committed.Steps)
			}
			sub := compositionSubmission(t, committed)
			if !sub.Recorded || sub.Outcome != c.Consumed || sub.PayloadSHA256 != c.ValueHash(request) || !reflect.DeepEqual(sub.Request.Selection, request.Selection) {
				t.Fatalf("review choice lost from exact receipt: %+v", sub)
			}
			if committed.Commit.BackendSelection != nil || committed.Commit.Selection.Backend != nil || committed.Commit.Selection.RuntimeConfig != nil {
				t.Fatalf("unexpected review configuration: %+v", committed.Commit)
			}
			if tc.wantRun == 0 {
				owner, err := f.plane.Tasks.Get(f.ctx, compositionStep(t, committed, "CREATE_TASK").TaskID)
				if err != nil || owner.CurrentReviewID != "" || owner.Phase != task.PhaseReviewRequired || owner.CodexConfigID != "" {
					t.Fatalf("task creation bypassed Review/approval or guessed config: %+v err=%v", owner, err)
				}
			}
			replayed, err := f.facade.CommitConfirmation(f.ctx, request)
			if err != nil || !reflect.DeepEqual(replayed.Steps, committed.Steps) {
				t.Fatalf("exact replay changed steps: %+v err=%v", replayed, err)
			}
			read, err := f.read(prepared, request.RequestID)
			if err != nil || !reflect.DeepEqual(compositionSubmission(t, read).Request.Selection, request.Selection) {
				t.Fatalf("readback lost choice: %+v err=%v", read, err)
			}
			// An omitted options selection on a consumed intent observes its receipt,
			// not the broader originally prepared action.
			after, err := f.facade.ConfirmationOptions(f.ctx, taskReviewOptions(prepared, nil))
			if err != nil || after.Eligibility.Available || after.StartReview == nil || *after.StartReview != (tc.wantRun == 1) {
				t.Fatalf("consumed options=%+v err=%v", after, err)
			}
		})
	}
}

func TestConfirmationTaskReviewChoiceCannotChangeAfterConsumption(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("prepare", compositionUpperVariant("upper", "Inspect", "Inspect read-only.")))
	if err != nil {
		t.Fatal(err)
	}
	request := taskReviewCommit(prepared, "commit", taskReviewBool(false))
	if _, err := f.facade.CommitConfirmation(f.ctx, request); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []*bool{nil, taskReviewBool(true)} {
		for _, requestID := range []string{"commit", "other-request"} {
			changed := request
			changed.RequestID, changed.Selection.StartReview = requestID, selected
			if _, err := f.facade.CommitConfirmation(f.ctx, changed); !errors.Is(err, c.ErrRequestConflict) {
				t.Fatalf("changed review selection was not a conflict: %v", err)
			}
		}
	}
	if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != 0 || runs != 0 {
		t.Fatalf("replay launched deferred work: %d/%d/%d", tasks, reviews, runs)
	}
	// Deferral does not remove the canonical later Review entry point.
	created, err := f.read(prepared, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	later, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{
		AISessionID: f.sessionID, RequestID: "later-review", Purpose: c.Review, Action: "START_REVIEW",
		Review:   &c.ReviewBody{TaskID: compositionStep(t, created, "CREATE_TASK").TaskID, FeatureAreaID: f.areaID, RepositoryID: f.repositoryID, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}},
		Variants: []c.Variant{compositionUpperVariant("upper", "Review later", "Inspect read-only and report findings.")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.commit(later, "later-review-commit", "upper"); err != nil {
		t.Fatal(err)
	}
	if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != 1 || runs != 1 {
		t.Fatalf("separate Review did not start exactly once: %d/%d/%d", tasks, reviews, runs)
	}
}

func TestConfirmationTaskReviewChoiceRefusesUnpreparedReview(t *testing.T) {
	f := newCompositionFixture(t)
	input := f.taskPrepare("prepare", c.Variant{ID: "task", Label: "Task", Summary: "Create only"})
	input.Action, input.Task.StartReview = "CREATE_TASK", false
	input.Task.ReviewDepth, input.Task.ReviewPlan = "", run.ReviewPlan{}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	options, err := f.facade.ConfirmationOptions(f.ctx, taskReviewOptions(prepared, taskReviewBool(true)))
	var failure *c.Failure
	if !errors.As(err, &failure) || failure.Code != "TASK_REVIEW_NOT_PREPARED" || options.IntentID != "" {
		t.Fatalf("unprepared Review options=%+v err=%v", options, err)
	}
	refused, err := f.facade.CommitConfirmation(f.ctx, taskReviewCommit(prepared, "unprepared-review", taskReviewBool(true)))
	if !errors.As(err, &failure) || failure.Code != "TASK_REVIEW_NOT_PREPARED" || refused.Commit != nil || refused.State != "PENDING" {
		t.Fatalf("unprepared Review request=%+v err=%v", refused, err)
	}
	if sub := compositionSubmission(t, refused); !sub.Recorded || sub.Outcome != c.RejectedNotConsumed {
		t.Fatalf("rejection=%+v", sub)
	}
	if tasks, reviews, runs := f.counts(t); tasks != 0 || reviews != 0 || runs != 0 {
		t.Fatalf("unprepared review had effects: %d/%d/%d", tasks, reviews, runs)
	}
}

func TestConfirmationTaskReviewOnStillRequiresCodingConfiguration(t *testing.T) {
	for _, selected := range []*bool{nil, taskReviewBool(true)} {
		f := newCompositionFixture(t)
		variant := compositionUpperVariant("coding", "Inspect", "Inspect read-only.")
		variant.ExecutorKind = string(review.ExecutorCodingAgent)
		prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("prepare", variant))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.plane.DB.SQL().ExecContext(f.ctx, `UPDATE codex_configs SET enabled=0`); err != nil {
			t.Fatal(err)
		}
		options, err := f.facade.ConfirmationOptions(f.ctx, taskReviewOptions(prepared, selected))
		if err != nil || options.Eligibility.Available || options.RuntimeInventoryReadError == "" || len(options.BackendCandidates) != 0 {
			t.Fatalf("Coding Review unexpectedly qualified: %+v err=%v", options, err)
		}
		refused, err := f.facade.CommitConfirmation(f.ctx, taskReviewCommit(prepared, "commit", selected))
		if err == nil || refused.Commit != nil || compositionSubmission(t, refused).Outcome != c.RejectedNotConsumed {
			t.Fatalf("Coding Review did not fail before effects: %+v err=%v", refused, err)
		}
		if tasks, reviews, runs := f.counts(t); tasks != 0 || reviews != 0 || runs != 0 {
			t.Fatalf("missing Review configuration had effects: %d/%d/%d", tasks, reviews, runs)
		}
	}
}

func TestConfirmationTaskReviewOffRefusesHiddenRuntimeSelection(t *testing.T) {
	f := newCompositionFixture(t)
	variant := compositionUpperVariant("coding", "Inspect", "Inspect read-only.")
	variant.ExecutorKind = string(review.ExecutorCodingAgent)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("prepare", variant))
	if err != nil {
		t.Fatal(err)
	}
	request := taskReviewCommit(prepared, "commit", taskReviewBool(false))
	request.Selection.RuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "stale", ReasoningEffort: "high"}
	request.Selection.Backend = &codexconfig.BackendSelectionInput{ConfigID: "stale"}
	refused, err := f.facade.CommitConfirmation(f.ctx, request)
	var failure *c.Failure
	if !errors.As(err, &failure) || failure.Code != "BACKEND_SELECTION_NOT_APPLICABLE" || refused.Commit != nil {
		t.Fatalf("create-only accepted unused runtime: %+v err=%v", refused, err)
	}
	if tasks, reviews, runs := f.counts(t); tasks != 0 || reviews != 0 || runs != 0 {
		t.Fatalf("invalid selection had effects: %d/%d/%d", tasks, reviews, runs)
	}
}

func TestConfirmationTaskReviewOffKeepsCanonicalRepositoryPrerequisites(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("prepare", compositionUpperVariant("upper", "Inspect", "Inspect read-only.")))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := f.plane.Repositories.Get(f.ctx, f.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	compositionGit(t, repository.Path, "update-ref", "-d", "HEAD")
	options, err := f.facade.ConfirmationOptions(f.ctx, taskReviewOptions(prepared, taskReviewBool(false)))
	if err == nil && options.Eligibility.Available {
		t.Fatalf("missing development branch qualified for create-only: %+v", options)
	}
	refused, err := f.facade.CommitConfirmation(f.ctx, taskReviewCommit(prepared, "commit", taskReviewBool(false)))
	if err == nil || refused.Commit != nil {
		t.Fatalf("missing development branch accepted create-only: %+v err=%v", refused, err)
	}
	if tasks, reviews, runs := f.counts(t); tasks != 0 || reviews != 0 || runs != 0 {
		t.Fatalf("stale repository had effects: %d/%d/%d", tasks, reviews, runs)
	}
}
