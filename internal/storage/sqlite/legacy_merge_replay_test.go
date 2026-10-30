package sqlite

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/git"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestLegacyMergeReplayRequiresDurableOutcome(t *testing.T) {
	for _, mode := range []string{"unknown", "historical-unknown", "applied-rewind", "applied-offline"} {
		t.Run(mode, func(t *testing.T) {
			f, owner, execution := acceptedExecutionFixture(t, "-legacy-replay")
			execution = terminalExecutionWithReport(t, f, execution)
			repo, _, base := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
			ctx := context.Background()
			accepted, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "legacy-replay-accept"})
			if err != nil || accepted.CommitError != "" {
				t.Fatalf("acceptance: %v %s", err, accepted.CommitError)
			}
			unknown := strings.Contains(mode, "unknown")
			if unknown {
				f.store.SetDeliveryFaultHookForTest(func(stage, _ string) error {
					if stage == "LEGACY_AREA_MERGE_BEFORE_RESULT" {
						return errors.New("lost outcome")
					}
					return nil
				})
			} else {
				if _, err = f.db.sql.Exec(`CREATE TRIGGER lose_legacy_projection BEFORE UPDATE OF merged_commit ON feature_areas BEGIN SELECT RAISE(ABORT,'lost projection'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.store.CommitFeatureArea(ctx, f.project, f.area, "legacy-replay"); err == nil {
				t.Fatal("missing injected failure")
			}
			if got := commitTestGit(t, repo, "rev-parse", "HEAD"); got != accepted.Task.CommitSHA {
				t.Fatalf("missing first effect %s", got)
			}
			if !unknown {
				if _, err = f.db.sql.Exec(`DROP TRIGGER lose_legacy_projection`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "historical-unknown" {
				f.db = restoreHistoricalMigrationFixture(t, f.db, 119)

			}
			commitTestGit(t, repo, "reset", "--hard", base)
			before := commitTestGit(t, repo, "reflog", "show", "--format=%H %gs", "refs/heads/develop")
			if mode == "applied-offline" {
				if err = os.Rename(repo, repo+"-offline"); err != nil {
					t.Fatal(err)
				}
				defer os.Rename(repo+"-offline", repo)
			}
			root := f.db.stateRoot
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := NewWorkflowProgressionStore(db)
			if err = ApplyMigrations(ctx, db.sql, DefaultMigrations()); err != nil {
				t.Fatal(err)
			}
			capability, err := store.CommitCapability(ctx, f.project, f.area)
			if err != nil {
				t.Fatal(err)
			}
			if unknown && (capability.CanCommit || capability.MergeRecoveryState != "UNKNOWN") {
				t.Fatalf("unsafe capability %+v", capability)
			}
			if !unknown && !capability.CanCommit {
				t.Fatalf("applied projection unavailable %+v", capability)
			}
			for i := 0; i < 2; i++ {
				got, replayErr := store.CommitFeatureArea(ctx, f.project, f.area, "legacy-replay")
				if unknown {
					if !errors.Is(replayErr, git.ErrRecoveryRequired) {
						t.Fatalf("UNKNOWN replay: %+v %v", got, replayErr)
					}
				} else if replayErr != nil || got.MergedCommit != accepted.Task.CommitSHA {
					t.Fatalf("APPLIED replay: %+v %v", got, replayErr)
				}
			}
			if unknown {
				if _, err = store.CommitFeatureArea(ctx, f.project, f.area, "replacement-request"); !errors.Is(err, git.ErrRecoveryRequired) {
					t.Fatalf("new request bypass: %v", err)
				}
			} else {
				for _, statement := range []string{`UPDATE legacy_merge_outcomes SET resulting_dev_head='fake'`, `DELETE FROM legacy_merge_outcomes`, `UPDATE feature_area_commit_receipts SET source_head='fake' WHERE request_id='legacy-replay'`} {
					if _, err = db.sql.Exec(statement); err == nil {
						t.Fatalf("mutable merge evidence: %s", statement)
					}
				}
			}
			if mode != "applied-offline" {
				if after := commitTestGit(t, repo, "reflog", "show", "--format=%H %gs", "refs/heads/develop"); after != before {
					t.Fatalf("replay modified Git:\nbefore=%s\nafter=%s", before, after)
				}
				if got := commitTestGit(t, repo, "rev-parse", "HEAD"); got != base {
					t.Fatalf("replay moved HEAD %s", got)
				}
			}
		})
	}
}
