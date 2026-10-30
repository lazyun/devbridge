package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func TestRecoveryReviewEvidenceRangeMatchesObservedFile(t *testing.T) {
	for _, location := range []string{"report", "finding"} {
		for _, tc := range []struct {
			name, content string
			first, last   int
			valid         bool
		}{
			{"whole-file", "one", 0, 0, true},
			{"last-line", "one\ntwo", 2, 2, true},
			{"trailing-newline", "one\n", 2, 2, true},
			{"outside-file", "one\ntwo", 2, 3, false},
			{"empty-outside-file", "", 2, 2, false},
		} {
			t.Run(location+"/"+tc.name, func(t *testing.T) {
				_, _, scope, _ := qualifiedVerificationFixture(t)
				w := scope.workset
				taskID := w.TaskID
				w.TaskID, w.ExecutionID, w.TaskDefinitionRevisionID, w.ReviewID = "", "", "", "review"
				var err error
				w, err = w.WithHash()
				if err != nil {
					t.Fatal(err)
				}
				data := []byte(tc.content)
				member := w.Members[0]
				if err := os.WriteFile(filepath.Join(member.CanonicalRoot, "evidence.txt"), data, 0600); err != nil {
					t.Fatal(err)
				}
				source := run.Run{ID: w.RunID, TaskID: taskID, ProjectID: w.ProjectID, FeatureAreaID: w.FeatureAreaID, ReviewID: "review", OperationKind: run.OperationReview, OperationID: "review", Purpose: run.PurposeScopeReview, TurnID: "turn", RuntimeWorkset: &w}
				c := &Coordinator{Tasks: &fakeTasks{value: task.Task{ID: taskID, ProjectID: w.ProjectID, FeatureAreaID: w.FeatureAreaID, CurrentDefinitionRevisionID: "current-task-revision", CurrentReviewID: "review"}}, Reviews: &coordinatorReviewRuntime{value: review.Review{ID: "review", TaskID: taskID, RunID: source.ID, ExecutorKind: review.ExecutorCodingAgent}}}
				rr := &review.ReviewReport{SchemaVersion: 2, Summary: "Review observations", Disposition: review.DispositionBlocked, Coverage: []string{"source files"}, Findings: []review.Finding{}, Limitations: []string{"requires follow-up"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}}
				ref := review.EvidenceRef{EvidenceID: "observed", RepositoryID: member.RepositoryID, MemberID: member.MemberID, Path: "evidence.txt", Digest: recoverystatus.Hash(data), StartLine: tc.first, EndLine: tc.last}
				if location == "report" {
					rr.EvidenceRefs = append(rr.EvidenceRefs, ref)
				} else {
					rr.Findings = append(rr.Findings, review.Finding{ID: "finding", Severity: "LOW", Title: "Observation", Description: "Review observation", Impact: "Needs review", EvidenceRefs: []review.EvidenceRef{ref}, SuggestedChange: "Review the referenced source"})
				}
				if err := rr.Validate(); err != nil {
					t.Fatalf("structural contract invalid: %v", err)
				}
				facts := recoverystatus.FactualObservation{Source: recoverystatus.Source{RunID: source.ID, OperationID: source.OperationID, TurnID: source.TurnID, RuntimeWorksetHash: w.ContentHash, TaskDefinitionRevisionID: w.TaskDefinitionRevisionID}}
				if err := c.ValidateRecoveryOwnerFacts(context.Background(), source, recoverystatus.Report{ReviewReport: rr}, &facts); err != nil {
					t.Fatal(err)
				}
				if facts.OwnerContractValidated != tc.valid {
					t.Fatalf("observed line range accepted=%v want=%v range=%d:%d", facts.OwnerContractValidated, tc.valid, tc.first, tc.last)
				}
				if !tc.valid && (len(facts.OwnerResult) != 0 || facts.OwnerContractEvidenceHash != "") {
					t.Fatal("invalid coordinates acquired owner result evidence")
				}
			})
		}
	}
}

func TestRecoveryReviewOwnerUsesCurrentReviewAndFrozenRevision(t *testing.T) {
	for _, kind := range []string{"review-owned", "explicit-revision", "stale-current-review", "foreign-review-run", "foreign-workset-review", "unbound-revision", "stale-explicit-revision"} {
		t.Run(kind, func(t *testing.T) {
			_, _, scope, _ := qualifiedVerificationFixture(t)
			w := scope.workset
			taskID := w.TaskID
			w.TaskID, w.ExecutionID, w.TaskDefinitionRevisionID, w.ReviewID = "", "", "", "review"
			if kind == "explicit-revision" {
				w.TaskDefinitionRevisionID = "current-revision"
			}
			if kind == "stale-explicit-revision" {
				w.TaskDefinitionRevisionID = "old-revision"
			}
			if kind == "foreign-workset-review" {
				w.ReviewID = "other-review"
			}
			var err error
			w, err = w.WithHash()
			if err != nil {
				t.Fatal(err)
			}
			source := run.Run{ID: w.RunID, TaskID: taskID, ProjectID: w.ProjectID, FeatureAreaID: w.FeatureAreaID, ReviewID: "review", OperationKind: run.OperationReview, OperationID: "review", Purpose: run.PurposeScopeReview, TurnID: "turn", RuntimeWorkset: &w}
			tasks := &fakeTasks{value: task.Task{ID: taskID, ProjectID: w.ProjectID, FeatureAreaID: w.FeatureAreaID, CurrentDefinitionRevisionID: "current-revision", CurrentReviewID: "review"}}
			reviews := &coordinatorReviewRuntime{value: review.Review{ID: "review", TaskID: taskID, RunID: source.ID, ExecutorKind: review.ExecutorCodingAgent}}
			if kind == "stale-current-review" {
				tasks.value.CurrentReviewID = "new-review"
			}
			if kind == "foreign-review-run" {
				reviews.value.RunID = "other-run"
			}
			c := &Coordinator{Tasks: tasks, Reviews: reviews}
			rr := &review.ReviewReport{SchemaVersion: 2, Summary: "Completed review", Disposition: review.DispositionBlocked, Coverage: []string{"source"}, Findings: []review.Finding{}, Limitations: []string{"follow-up needed"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}}
			facts := recoverystatus.FactualObservation{Source: recoverystatus.Source{RunID: source.ID, OperationID: source.OperationID, TurnID: source.TurnID, RuntimeWorksetHash: w.ContentHash, TaskDefinitionRevisionID: w.TaskDefinitionRevisionID}}
			if kind == "unbound-revision" {
				facts.Source.TaskDefinitionRevisionID = "current-revision"
			}
			err = c.ValidateRecoveryOwnerFacts(context.Background(), source, recoverystatus.Report{ReviewReport: rr}, &facts)
			valid := kind == "review-owned" || kind == "explicit-revision"
			if valid {
				if err != nil || !facts.OwnerContractValidated {
					t.Fatalf("valid Review owner blocked: %v", err)
				}
			} else if !errors.Is(err, recoverystatus.ErrConflict) || facts.OwnerContractValidated {
				t.Fatalf("foreign Review authority accepted: %v validated=%v", err, facts.OwnerContractValidated)
			}
		})
	}
}
