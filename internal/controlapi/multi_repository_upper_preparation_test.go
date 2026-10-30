package controlapi_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/worktree"
)

type failSecondUpperWorktree struct {
	inner interface {
		Create(context.Context, worktree.CreateInput) (worktree.Worktree, error)
		Get(context.Context, string) (worktree.Worktree, error)
	}
	creates int
	fail    bool
	failAt  int
	message string
}

type failSecondUpperWorktreeMetadata struct {
	inner   worktree.Store
	creates int
	fail    bool
	failed  worktree.Worktree
}

func (s *failSecondUpperWorktreeMetadata) Create(ctx context.Context, value worktree.Worktree) error {
	s.creates++
	if s.fail && s.creates == 2 {
		s.failed = value
		return errors.New("injected second member metadata failure")
	}
	return s.inner.Create(ctx, value)
}
func (s *failSecondUpperWorktreeMetadata) Get(ctx context.Context, id string) (worktree.Worktree, error) {
	return s.inner.Get(ctx, id)
}
func (s *failSecondUpperWorktreeMetadata) List(ctx context.Context) ([]worktree.Worktree, error) {
	return s.inner.List(ctx)
}
func (s *failSecondUpperWorktreeMetadata) Update(ctx context.Context, id string, update func(*worktree.Worktree) error) (worktree.Worktree, error) {
	return s.inner.Update(ctx, id, update)
}

func (m *failSecondUpperWorktree) Create(ctx context.Context, in worktree.CreateInput) (worktree.Worktree, error) {
	m.creates++
	failAt := m.failAt
	if failAt == 0 {
		failAt = 2
	}
	if m.fail && m.creates == failAt {
		message := m.message
		if message == "" {
			message = "injected second member creation failure"
		}
		return worktree.Worktree{}, errors.New(message)
	}
	return m.inner.Create(ctx, in)
}
func (m *failSecondUpperWorktree) Get(ctx context.Context, id string) (worktree.Worktree, error) {
	return m.inner.Get(ctx, id)
}
func (m *failSecondUpperWorktree) Validate(ctx context.Context, value worktree.Worktree) error {
	if validator, ok := m.inner.(interface {
		Validate(context.Context, worktree.Worktree) error
	}); ok {
		return validator.Validate(ctx, value)
	}
	return nil
}

func TestMultiRepositoryUpperPreparationRetryKeepsFirstMemberAndGrantPreparing(t *testing.T) {
	for _, tc := range []struct {
		name                string
		fault               string
		physicalBeforeRetry int
		restart             bool
	}{
		{name: "before-second-physical-create", fault: "CREATE", physicalBeforeRetry: 1},
		{name: "after-second-physical-before-metadata", fault: "METADATA", physicalBeforeRetry: 2},
		{name: "restart-after-second-physical-before-metadata", fault: "METADATA_RESTART", physicalBeforeRetry: 2, restart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := aisession.WithCallerScope(context.Background(), "multi-upper-preparation-"+tc.fault)
			root := t.TempDir()
			dataRoot := filepath.Join(root, "data")
			plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: dataRoot})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = plane.Close() }()
			facade, err := controlapi.NewFromControlPlane(plane)
			if err != nil {
				t.Fatal(err)
			}
			git := func(path string, args ...string) string {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = path
				out, e := cmd.CombinedOutput()
				if e != nil {
					t.Fatalf("git %v: %s %v", args, out, e)
				}
				return strings.TrimSpace(string(out))
			}
			inputs := []projectregistration.RepositoryInput{}
			heads := map[string]string{}
			for _, name := range []string{"app", "sdk"} {
				path := filepath.Join(root, name)
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				git(path, "init", "-b", name+"-development")
				if err := os.WriteFile(filepath.Join(path, "same.txt"), []byte(name+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				git(path, "add", "same.txt")
				git(path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
				heads[name] = git(path, "rev-parse", "HEAD")
				inputs = append(inputs, projectregistration.RepositoryInput{Path: path, Alias: name})
			}
			registered, err := plane.ProjectRegistration.Register(ctx, projectregistration.Input{RequestID: "register", Name: "prep retry", Repositories: inputs})
			if err != nil {
				t.Fatal(err)
			}
			ref := aiprojectselection.ClientBindingReference{CallerScope: "multi-upper-preparation-" + tc.fault}
			session, err := plane.AIContext.InitializeSession(ctx, ref, "init", "")
			if err != nil {
				t.Fatal(err)
			}
			sid := session.AISessionID
			if _, err = plane.AIContext.BindProject(ctx, ref, sid, registered.Project.ID, "bind"); err != nil {
				t.Fatal(err)
			}
			repositories, err := plane.Repositories.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			targets := make([]workset.Target, 0, 2)
			for _, name := range []string{"app", "sdk"} {
				for _, repo := range repositories {
					if repo.Name != name {
						continue
					}
					if _, err = facade.SelectDevelopmentBranchForSession(ctx, developmentcycle.SelectInput{AISessionID: sid, ProjectID: registered.Project.ID, RepositoryID: repo.ID, DevelopmentRef: "refs/heads/" + name + "-development", RequestID: "branch-" + name}); err != nil {
						t.Fatal(err)
					}
					targets = append(targets, workset.Target{RepositoryID: repo.ID, OriginCommit: heads[name], OriginRef: "refs/heads/" + name + "-development", Read: true, Write: workset.WriteScope{Kind: "NONE"}})
				}
			}
			commit := func(in c.PrepareInput) (c.Intent, c.CommitInput, error) {
				in.AISessionID = sid
				prepared, e := facade.PrepareConfirmation(ctx, in)
				if e != nil {
					t.Fatalf("prepare %s: %v", in.Action, e)
				}
				ci := c.CommitInput{AISessionID: sid, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "commit-" + in.RequestID, ContentHash: prepared.ContentHash, VariantID: in.Variants[0].ID}
				got, e := facade.CommitConfirmation(ctx, ci)
				return got, ci, e
			}
			area, _, err := commit(c.PrepareInput{RequestID: "area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Cross repo change", ProblemStatement: "Inspect app and sdk together"}, Variants: []c.Variant{{ID: "create", Summary: "Create area"}}})
			if err != nil {
				t.Fatal(err)
			}
			areaID := area.Steps[0].FeatureAreaID
			if areaID == "" {
				t.Fatalf("area commit had no feature area: %+v", area)
			}
			created, _, err := commit(c.PrepareInput{RequestID: "task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: areaID, Title: "Review cross repo intent", ProblemStatement: "Implement app sdk change", Constraints: []string{"Keep both contracts"}}, Variants: []c.Variant{{ID: "create", Summary: "Create intent"}}})
			if err != nil {
				t.Fatal(err)
			}

			durable, ok := plane.Coordinator.Worktrees.(*worktree.DurableManager)
			if !ok {
				t.Fatalf("production coordinator worktree manager=%T", plane.Coordinator.Worktrees)
			}
			var stopFailure func()
			var metadataFailure *failSecondUpperWorktreeMetadata
			if tc.fault == "CREATE" {
				failing := &failSecondUpperWorktree{inner: durable, fail: true}
				plane.Coordinator.Worktrees = failing
				stopFailure = func() { failing.fail = false }
			} else {
				metadataFailure = &failSecondUpperWorktreeMetadata{inner: durable.Store, fail: true}
				plane.Coordinator.Worktrees = &worktree.DurableManager{External: durable.External, Store: metadataFailure}
				stopFailure = func() { metadataFailure.fail = false }
			}
			proposal := c.PrepareInput{AISessionID: sid, RequestID: "review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: created.Steps[0].TaskID, FeatureAreaID: areaID, RepositoryTargets: targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "upper", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read both repositories"}}}
			started, admission, err := commit(proposal)
			if err == nil {
				t.Fatal("second member creation unexpectedly succeeded")
			}
			if started.Steps[0].ReviewID == "" || started.Steps[0].RunID == "" {
				t.Fatalf("missing admitted identities: %+v err=%v", started, err)
			}
			reviewID, runID := started.Steps[0].ReviewID, started.Steps[0].RunID
			var workspaceID, grantState, firstRepository, firstWorktree, firstPath, runtimeEpoch string
			var generation int64
			if err := plane.DB.SQL().QueryRow(`SELECT workspace_id FROM reviews WHERE id=?`, reviewID).Scan(&workspaceID); err != nil {
				t.Fatal(err)
			}
			if err := plane.DB.SQL().QueryRow(`SELECT state FROM review_grants WHERE review_id=?`, reviewID).Scan(&grantState); err != nil || grantState == "ACTIVE" {
				t.Fatalf("grant=%q err=%v", grantState, err)
			}
			if err := plane.DB.SQL().QueryRow(`SELECT runtime_epoch,generation FROM review_grants WHERE review_id=?`, reviewID).Scan(&runtimeEpoch, &generation); err != nil || runtimeEpoch == "" || generation != 1 {
				t.Fatalf("grant identity epoch=%q generation=%d err=%v", runtimeEpoch, generation, err)
			}
			if err := plane.DB.SQL().QueryRow(`SELECT b.repository_id,b.worktree_id,wt.path FROM run_repository_bindings b JOIN worktrees wt ON wt.id=b.worktree_id WHERE b.run_id=? AND b.preparation='READY'`, runID).Scan(&firstRepository, &firstWorktree, &firstPath); err != nil || firstWorktree == "" {
				t.Fatalf("first preparation err=%v worktree=%q", err, firstWorktree)
			}
			dirtyPath := filepath.Join(firstPath, "dirty-retained.txt")
			if err := os.WriteFile(dirtyPath, []byte("retained across preparation retry\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var ready, failed int
			if err := plane.DB.SQL().QueryRow(`SELECT count(*) FROM run_repository_bindings WHERE run_id=? AND preparation='READY'`, runID).Scan(&ready); err != nil || ready != 1 {
				t.Fatalf("ready=%d err=%v", ready, err)
			}
			if err := plane.DB.SQL().QueryRow(`SELECT count(*) FROM run_repository_bindings WHERE run_id=? AND preparation='FAILED'`, runID).Scan(&failed); err != nil || failed != 1 {
				t.Fatalf("failed=%d err=%v", failed, err)
			}
			physical, err := os.ReadDir(plane.DataPaths.Worktrees)
			if err != nil || len(physical) != tc.physicalBeforeRetry {
				t.Fatalf("physical roots before retry=%d want=%d err=%v", len(physical), tc.physicalBeforeRetry, err)
			}
			if tc.fault != "CREATE" {
				if metadataFailure.failed.ID == "" || metadataFailure.failed.Path == "" {
					t.Fatalf("metadata failure did not observe created physical identity: %+v", metadataFailure.failed)
				}
				if _, err := os.Stat(metadataFailure.failed.Path); err != nil {
					t.Fatalf("second physical root missing after metadata failure: %v", err)
				}
				var persisted int
				if err := plane.DB.SQL().QueryRow(`SELECT count(*) FROM worktrees WHERE id=?`, metadataFailure.failed.ID).Scan(&persisted); err != nil || persisted != 0 {
					t.Fatalf("failed metadata unexpectedly persisted=%d err=%v", persisted, err)
				}
			}

			stopFailure()
			expectedEpoch, expectedGeneration := runtimeEpoch, generation
			var replayed c.Intent
			confirmationReplayed := false
			if tc.restart {
				if err := plane.Close(); err != nil {
					t.Fatal(err)
				}
				plane, err = controlplane.Open(ctx, controlplane.Config{DataRoot: dataRoot})
				if err != nil {
					t.Fatal(err)
				}
				facade, err = controlapi.NewFromControlPlane(plane)
				if err != nil {
					t.Fatal(err)
				}
				if plane.DB.RuntimeEpoch() == runtimeEpoch {
					t.Fatal("restart reused preparation runtime epoch")
				}
				observed, err := facade.UpperReviewAccessForSession(ctx, sid, reviewID)
				if err != nil || observed.State != "RECOVERY_REQUIRED" || observed.Target.RuntimeEpoch != runtimeEpoch || observed.Target.GrantGeneration != generation {
					t.Fatalf("restart access=%+v err=%v", observed, err)
				}
				recovered, err := facade.RecoverFeatureAreaAgentOperation(ctx, sid, agentconversation.RecoverInput{ExpectedUpperVersion: &agentconversation.UpperVersion{AuthorityEpoch: plane.DB.RuntimeEpoch(), GrantEpoch: runtimeEpoch, GrantGeneration: generation}, FeatureAreaID: areaID, RunID: runID, OperationKind: agentconversation.Review, OperationID: reviewID, RequestID: "recover-partial-preparation"})
				if err != nil || recovered.GrantState != "PREPARING" || recovered.AuthorizedUpperVersion == nil || recovered.AuthorizedUpperVersion.GrantGeneration != generation+1 || recovered.AuthorizedUpperVersion.GrantEpoch != plane.DB.RuntimeEpoch() {
					t.Fatalf("partial preparation recovery=%+v err=%v", recovered, err)
				}
				expectedEpoch, expectedGeneration = recovered.AuthorizedUpperVersion.GrantEpoch, recovered.AuthorizedUpperVersion.GrantGeneration
				// The original admission receipt remains generation 1. Replaying it
				// proves stable identities but must not silently adopt or activate the
				// recovered generation.
				replayed, err = facade.CommitConfirmation(ctx, admission)
				confirmationReplayed = true
				if err != nil || replayed.Steps[0].ReviewID != reviewID || replayed.Steps[0].RunID != runID {
					t.Fatalf("recovered admission replay=%+v err=%v", replayed, err)
				}
				pending, err := facade.UpperReviewAccessForSession(ctx, sid, reviewID)
				if err != nil || pending.State != "PREPARING" || pending.Target.RuntimeEpoch != expectedEpoch || pending.Target.GrantGeneration != expectedGeneration {
					t.Fatalf("old admission activated recovered grant: access=%+v err=%v", pending, err)
				}
				if _, err = facade.PrepareUpperReview(ctx, observed.Target); err == nil {
					t.Fatal("old generation prepared recovered Review")
				}
				// Fail the first missing-member Create in the recovered generation.
				// Its blocker must be owned by generation 2; generation 1 cannot
				// overwrite it through a stale preparation callback.
				reopenedDurable, ok := plane.Coordinator.Worktrees.(*worktree.DurableManager)
				if !ok {
					t.Fatalf("reopened coordinator worktree manager=%T", plane.Coordinator.Worktrees)
				}
				const recoveredFailure = "injected recovered generation preparation failure"
				generationFailure := &failSecondUpperWorktree{inner: reopenedDurable, fail: true, failAt: 1, message: recoveredFailure}
				plane.Coordinator.Worktrees = generationFailure
				if _, err = facade.PrepareUpperReview(ctx, pending.Target); err == nil {
					t.Fatal("recovered generation preparation failure unexpectedly succeeded")
				}
				blocked, err := facade.UpperReviewAccessForSession(ctx, sid, reviewID)
				if err != nil || blocked.State != "PREPARING" || !strings.Contains(blocked.Blocker, recoveredFailure) || blocked.Target != pending.Target {
					t.Fatalf("recovered generation blocker=%+v err=%v", blocked, err)
				}
				if _, err = facade.PrepareUpperReview(ctx, observed.Target); err == nil {
					t.Fatal("old generation retried after recovered failure")
				}
				unchanged, err := facade.UpperReviewAccessForSession(ctx, sid, reviewID)
				if err != nil || unchanged.Blocker != blocked.Blocker || unchanged.Target != blocked.Target {
					t.Fatalf("old generation overwrote recovered blocker before=%+v after=%+v err=%v", blocked, unchanged, err)
				}
				generationFailure.fail = false
				prepared, err := facade.PrepareUpperReview(ctx, pending.Target)
				if err != nil || prepared.State != "ACTIVE" || prepared.Target != pending.Target {
					t.Fatalf("explicit recovered preparation=%+v err=%v", prepared, err)
				}
			}
			if !confirmationReplayed {
				replayed, err = facade.CommitConfirmation(ctx, admission)
				if err != nil || replayed.Steps[0].ReviewID != reviewID || replayed.Steps[0].RunID != runID {
					t.Fatalf("retry=%+v err=%v", replayed, err)
				}
			}
			access, err := facade.UpperReviewAccessForSession(ctx, sid, reviewID)
			if err != nil || access.State != "ACTIVE" || access.WorkspaceID != workspaceID {
				t.Fatalf("access=%+v err=%v", access, err)
			}
			var retained string
			if err := plane.DB.SQL().QueryRow(`SELECT worktree_id FROM run_repository_bindings WHERE run_id=? AND repository_id=?`, runID, firstRepository).Scan(&retained); err != nil {
				t.Fatal(err)
			}
			var allReady int
			if err := plane.DB.SQL().QueryRow(`SELECT count(*) FROM run_repository_bindings WHERE run_id=? AND preparation='READY'`, runID).Scan(&allReady); err != nil || allReady != 2 {
				t.Fatalf("all ready=%d err=%v", allReady, err)
			}
			if retained != firstWorktree {
				t.Fatalf("first worktree changed from %s to %s", firstWorktree, retained)
			}
			if dirty, err := os.ReadFile(dirtyPath); err != nil || string(dirty) != "retained across preparation retry\n" {
				t.Fatalf("first member dirty state lost: %q err=%v", dirty, err)
			}
			var finalWorkspace, finalEpoch string
			var finalGeneration int64
			if err := plane.DB.SQL().QueryRow(`SELECT workspace_id,runtime_epoch,generation FROM review_grants WHERE review_id=? AND run_id=?`, reviewID, runID).Scan(&finalWorkspace, &finalEpoch, &finalGeneration); err != nil || finalWorkspace != workspaceID || finalEpoch != expectedEpoch || finalGeneration != expectedGeneration {
				t.Fatalf("retry changed owner workspace=%q/%q epoch=%q/%q generation=%d/%d err=%v", workspaceID, finalWorkspace, expectedEpoch, finalEpoch, expectedGeneration, finalGeneration, err)
			}
			physical, err = os.ReadDir(plane.DataPaths.Worktrees)
			if err != nil || len(physical) != 2 {
				t.Fatalf("physical roots after retry=%d want=2 err=%v", len(physical), err)
			}
			if tc.fault != "CREATE" {
				var recoveredID, recoveredPath string
				if err := plane.DB.SQL().QueryRow(`SELECT id,path FROM worktrees WHERE id=?`, metadataFailure.failed.ID).Scan(&recoveredID, &recoveredPath); err != nil || recoveredID != metadataFailure.failed.ID || recoveredPath != metadataFailure.failed.Path {
					t.Fatalf("metadata retry changed physical identity got=%s/%s want=%s/%s err=%v", recoveredID, recoveredPath, metadataFailure.failed.ID, metadataFailure.failed.Path, err)
				}
			}
		})
	}
}
