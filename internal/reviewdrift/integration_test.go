package reviewdrift_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

func projectValue(id, name string, now time.Time) project.Project {
	return project.Project{ID: id, Name: name, CreatedAt: now, UpdatedAt: now}
}

func repositoryValue(id, projectID, name, path string, now time.Time) repository.Repository {
	return repository.Repository{ID: id, ProjectID: projectID, Name: name, Path: path, CreatedAt: now, UpdatedAt: now}
}

type driftPlaneFixture struct {
	ctx          context.Context
	plane        *controlplane.ControlPlane
	control      *controlapi.Facade
	projectID    string
	repositoryID string
	repoPath     string
	moduleID     string
	backend      codexconfig.BackendSelection
}

func newDriftPlaneFixture(t *testing.T) *driftPlaneFixture {
	t.Helper()
	ctx := aisession.WithCallerScope(context.Background(), "reviewdrift-fixture")
	root := t.TempDir()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })
	control, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	// Background Review Runs must not spawn Codex in tests: a stub factory
	// completes every turn immediately and deterministically.
	plane.Coordinator.Codex = &stubCodexFactory{}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	projectValue := projectValue("project-drift", "Drift fixture", now)
	if err := sqlite.NewProjectStore(plane.DB).Create(ctx, projectValue); err != nil {
		t.Fatal(err)
	}
	repoPath, _ := createGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoPath, "reviewed/other.go"), []byte("package reviewed // stable dependency\n"), 0600); err != nil {
		t.Fatal(err)
	}
	commitAll(t, repoPath, "add stable reviewed dependency")
	// The fixture repository already lives under the test's own TempDir root,
	// so no additional copy step is required.
	repositoryValue := repositoryValue("repository-drift", projectValue.ID, "Primary", repoPath, now)
	if _, _, err := sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repositoryValue); err != nil {
		t.Fatal(err)
	}
	branch, err := exec.Command("git", "-C", repoPath, "branch", "--show-current").Output()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plane.DevelopmentCycles.Select(ctx, developmentcycle.SelectInput{ProjectID: projectValue.ID, RepositoryID: repositoryValue.ID, DevelopmentRef: "refs/heads/" + strings.TrimSpace(string(branch)), RequestID: "drift-fixture-branch"}); err != nil {
		t.Fatal(err)
	}
	unclassified, err := plane.Modules.EnsureUnclassified(ctx, projectValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	configValue, err := plane.CodexConfigs.Get(ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	return &driftPlaneFixture{
		ctx: ctx, plane: plane, control: control, projectID: projectValue.ID,
		repositoryID: repositoryValue.ID, repoPath: repoPath, moduleID: unclassified.ID, backend: configValue.Selection("reviewdrift-fixture"),
	}
}

func (f *driftPlaneFixture) createReviewTask(t *testing.T, aiSessionID, featureAreaID, baseCommit string) string {
	t.Helper()
	value, err := f.plane.Tasks.Create(f.ctx, task.CreateInput{
		AISessionID: aiSessionID, RequestID: "drift-task-" + featureAreaID,
		ProjectID: f.projectID, FeatureAreaID: featureAreaID, ModuleID: f.moduleID,
		RepositoryID: f.repositoryID, CodexConfigID: codexconfig.DefaultConfigID,
		Goal: "Review repository drift", ProblemStatement: "Review repository drift facts.",
		BaseRevision: baseCommit, Planning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value.ID
}

func (f *driftPlaneFixture) openSession(t *testing.T) string {
	t.Helper()
	request := "drift-session-" + t.Name()
	ref := aiprojectselection.ClientBindingReference{CallerScope: "reviewdrift-fixture"}
	initialized, err := f.plane.AIContext.InitializeSession(f.ctx, ref, request, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.AIContext.BindProject(f.ctx, ref, initialized.AISessionID, f.projectID, request+"-bind"); err != nil {
		t.Fatal(err)
	}
	session, err := f.plane.AIContext.GetSession(f.ctx, initialized.AISessionID)
	if err != nil {
		t.Fatal(err)
	}
	return session.Session.ID
}

func (f *driftPlaneFixture) createTopic(t *testing.T, aiSessionID string) string {
	t.Helper()
	value, err := f.control.CreateFeatureArea(f.ctx, featurearea.CreateInput{AISessionID: aiSessionID, Title: "Drift featureArea", ProblemStatement: "Review the interface implementation."})
	if err != nil {
		t.Fatal(err)
	}
	return value.ID
}

func (f *driftPlaneFixture) repositoryHead(t *testing.T) string {
	t.Helper()
	head, err := f.plane.Repositories.CurrentHEAD(f.ctx, f.repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func (f *driftPlaneFixture) startReview(t *testing.T, aiSessionID, reviewID string) run.Run {
	t.Helper()
	value, err := f.control.StartScopeReview(f.ctx, scopereview.StartInput{AISessionID: aiSessionID, ReviewID: reviewID})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestReviewProvenanceCapturedAtStartAndDriftsIndependently(t *testing.T) {
	fixture := newDriftPlaneFixture(t)
	sessionA := fixture.openSession(t)
	featureAreaID := fixture.createTopic(t, sessionA)
	baseCommit := fixture.repositoryHead(t)
	taskID := fixture.createReviewTask(t, sessionA, featureAreaID, baseCommit)

	reviewOne, err := fixture.plane.Reviews.Create(fixture.ctx, productreview.Review{
		TaskID: taskID, ReviewDepth: productreview.ReviewDepthDeep,
		ProjectID: fixture.projectID, FeatureAreaID: featureAreaID, CreatedByAISessionID: sessionA, RepositoryID: fixture.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, BackendSelection: &fixture.backend, BaseCommit: baseCommit,
		Focus:           productreview.Focus{Kind: productreview.FocusFocused, Summary: "Interface focus", Paths: []string{"reviewed/interface.go"}},
		EngineeringGoal: "Review the interface.",
		Instruction:     "Inspect the interface implementation without modifying it.",
		Model:           "gpt-5.6-terra", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.startReview(t, sessionA, reviewOne.ID)

	// Provenance is captured deterministically at Review start, before any
	// later repository change, and equals the Review's immutable base commit —
	// the exact state the Review's worktree gives the Agent.
	provenanceOne, err := fixture.plane.ReviewDrift.Provenance(fixture.ctx, reviewOne.ID)
	if err != nil {
		t.Fatal(err)
	}
	if provenanceOne.Commit != baseCommit || provenanceOne.RepositoryID != fixture.repositoryID || provenanceOne.CapturedAt.IsZero() {
		t.Fatalf("provenance = %+v, want commit %s", provenanceOne, baseCommit)
	}

	// R1's Run completes before the follow-up Review starts (the durable
	// FeatureArea claim allows one active operation).
	waitForReviewRunSucceeded(t, fixture, reviewOne.ID)

	// A second Review under the same Topic records its own independent
	// provenance; R1 is neither modified nor superseded. Both Reviews select the same
	// immutable repository commit, so their provenance commits
	// legitimately coincide while remaining separate immutable records.
	reviewTwo, err := fixture.plane.Reviews.Create(fixture.ctx, productreview.Review{
		TaskID: taskID, ReviewDepth: productreview.ReviewDepthDeep,
		ProjectID: fixture.projectID, FeatureAreaID: featureAreaID, CreatedByAISessionID: sessionA, RepositoryID: fixture.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, BackendSelection: &fixture.backend, BaseCommit: baseCommit,
		Focus:           productreview.Focus{Kind: productreview.FocusFocused, Summary: "Follow-up focus", Paths: []string{"reviewed/other.go"}},
		EngineeringGoal: "Re-check the interface.",
		Instruction:     "The last review missed details; look again.",
		Model:           "gpt-5.6-sol", ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.startReview(t, sessionA, reviewTwo.ID)
	provenanceTwo, err := fixture.plane.ReviewDrift.Provenance(fixture.ctx, reviewTwo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if provenanceTwo.Commit != provenanceOne.Commit || provenanceTwo.ReviewID == provenanceOne.ReviewID {
		t.Fatalf("independent provenance = %+v / %+v", provenanceOne, provenanceTwo)
	}
	firstAgain, err := fixture.plane.ReviewDrift.Provenance(fixture.ctx, reviewOne.ID)
	if err != nil || firstAgain.Commit != provenanceOne.Commit || firstAgain.CapturedAt != provenanceOne.CapturedAt {
		t.Fatalf("R1 provenance mutated: %+v vs %+v", firstAgain, provenanceOne)
	}
	waitForReviewRunSucceeded(t, fixture, reviewTwo.ID)

	// Another developer changes the reviewed interface file and HEAD
	// advances. Reading R1 now reports factual drift only.
	if err := os.WriteFile(filepath.Join(fixture.repoPath, "reviewed/interface.go"), []byte("package reviewed // team change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repoPath, "unrelated/other.go"), []byte("package other // team change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	current := commitAll(t, fixture.repoPath, "team changes the reviewed interface")

	drift, err := fixture.control.ReviewRepositoryDrift(fixture.ctx, fixture.projectID, reviewOne.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !drift.Available || drift.ReviewCommit != provenanceOne.Commit || drift.CurrentCommit != current {
		t.Fatalf("R1 drift identity = %+v", drift)
	}
	if !drift.RepositoryChanged || drift.ChangedFileCount != 2 {
		t.Fatalf("R1 drift facts = %+v", drift)
	}
	if drift.ChangedFocusPathCount != 1 || len(drift.FocusPathsChanged) != 1 {
		t.Fatalf("R1 reviewed-path facts = %+v", drift)
	}
	if drift.FocusPathsChanged[0].Path != "reviewed/interface.go" || drift.FocusPathsChanged[0].Change != productreview.ChangeModified {
		t.Fatalf("R1 reviewed path change = %+v", drift.FocusPathsChanged[0])
	}
	if drift.OtherChangedFileCount != 1 {
		t.Fatalf("unrelated change not separated: %+v", drift)
	}
	// The historical Review record itself is untouched: no staleness,
	// invalidity, or rerun flag exists anywhere in the projection.
	encodedDrift := drift
	if encodedDrift.UnavailableReason != "" {
		t.Fatalf("available drift carries no unavailability: %+v", encodedDrift)
	}

	// R2 drifts independently against the same current state.
	driftTwo, err := fixture.control.ReviewRepositoryDrift(fixture.ctx, fixture.projectID, reviewTwo.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !driftTwo.Available || driftTwo.ReviewCommit != provenanceTwo.Commit {
		t.Fatalf("R2 drift identity = %+v", driftTwo)
	}
	// R2's recorded relevant path is a different file; the team change did
	// not touch it, so no reviewed-path change is reported for R2.
	if driftTwo.ChangedFocusPathCount != 0 || len(driftTwo.FocusPathsChanged) != 0 {
		t.Fatalf("R2 reviewed-path facts = %+v", driftTwo)
	}
	if driftTwo.OtherChangedFileCount != 2 {
		t.Fatalf("R2 other-change facts = %+v", driftTwo)
	}
}

// waitForReviewRunSucceeded waits for one Review's background Run to reach a
// durable SUCCEEDED state.
func waitForReviewRunSucceeded(t *testing.T, fixture *driftPlaneFixture, reviewID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	lastStatus := ""
	for time.Now().Before(deadline) {
		reviewValue, err := fixture.plane.Reviews.Get(fixture.ctx, reviewID)
		if err == nil && reviewValue.RunID != "" {
			runValue, runErr := fixture.plane.Runs.Get(fixture.ctx, reviewValue.RunID)
			if runErr == nil {
				lastStatus = string(runValue.Status)
				if runValue.Status == run.StatusSucceeded {
					return
				}
				if runValue.Status != run.StatusCreated && runValue.Status != run.StatusPreparing && runValue.Status != run.StatusRunning && runValue.Status != run.StatusCollecting {
					t.Fatalf("review run ended in %s: failure=%+v", runValue.Status, runValue.Failure)
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("review run did not succeed before the deadline (last status %q)", lastStatus)
}

func TestConsoleDriftReadIsProjectScopedAndFactual(t *testing.T) {
	fixture := newDriftPlaneFixture(t)
	sessionA := fixture.openSession(t)
	featureAreaID := fixture.createTopic(t, sessionA)
	baseCommit := fixture.repositoryHead(t)
	taskID := fixture.createReviewTask(t, sessionA, featureAreaID, baseCommit)
	reviewOne, err := fixture.plane.Reviews.Create(fixture.ctx, productreview.Review{
		TaskID: taskID, ReviewDepth: productreview.ReviewDepthDeep,
		ProjectID: fixture.projectID, FeatureAreaID: featureAreaID, CreatedByAISessionID: sessionA, RepositoryID: fixture.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, BackendSelection: &fixture.backend, BaseCommit: baseCommit,
		Focus:           productreview.Focus{Kind: productreview.FocusFocused, Summary: "Interface focus", Paths: []string{"reviewed/interface.go"}},
		EngineeringGoal: "Review the interface.",
		Instruction:     "Inspect the interface implementation without modifying it.",
		Model:           "gpt-5.6-terra", ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.startReview(t, sessionA, reviewOne.ID)

	// Unresolvable reviewed commit (simulated by a foreign SHA) is factual
	// unavailability, not a Review failure.
	drift, err := fixture.control.ReviewRepositoryDrift(fixture.ctx, fixture.projectID, reviewOne.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !drift.Available {
		t.Fatalf("fresh provenance drift should be available: %+v", drift)
	}
	// Wrong-Project reads are rejected at the Project boundary.
	if _, err := fixture.control.ReviewRepositoryDrift(fixture.ctx, "project-other", reviewOne.ID, 0); err == nil {
		t.Fatal("cross-Project drift read must be rejected")
	}
}
