package featurearea

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
)

func creationReceipt(sessionID, projectID, requestID string) CreationReceipt {
	return CreationReceipt{AISessionID: sessionID, ProjectID: projectID, Command: "FEATURE_AREA_CREATE", RequestID: requestID, PayloadSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ContractVersion: "v1", Prepared: json.RawMessage(`{"repositoryId":"repo-a","model":"model-a"}`)}
}

func TestReceiptedCreationMemoryReplayConflictAndMonotonicProgress(t *testing.T) {
	store := NewMemoryStore()
	ids := 0
	service, err := NewService(store, sessionStub{"session-a": {ID: "session-a", ProjectID: "project-a"}}, zeroTaskCount, func() (string, error) { ids++; return "feature-area-" + string(rune('0'+ids)), nil }, func() time.Time { return time.Date(2026, 9, 13, 1, 2, 3, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	input := CreateInput{AISessionID: "session-a", RequestID: "root-a", Title: "Receipt area", ProblemStatement: "Keep the root request durable."}
	receipt := creationReceipt("session-a", "project-a", "root-a")
	created, saved, err := service.CreateReceipted(context.Background(), input, receipt)
	if err != nil || saved.FeatureAreaID != created.ID {
		t.Fatalf("created=%+v receipt=%+v err=%v", created, saved, err)
	}
	replay, replayReceipt, err := service.ReplayCreation(context.Background(), "session-a", receipt.Command, receipt.RequestID, receipt.PayloadSHA256)
	if err != nil || replay.ID != created.ID || string(replayReceipt.Prepared) != string(receipt.Prepared) {
		t.Fatalf("replay=%+v receipt=%+v err=%v", replay, replayReceipt, err)
	}
	if _, _, err := service.ReplayCreation(context.Background(), "session-a", receipt.Command, receipt.RequestID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); !errors.Is(err, ErrConflict) {
		t.Fatalf("different payload error=%v", err)
	}
	saved.TaskID, saved.ReviewID, saved.RunID, saved.ReviewAdmitted = "task-a", "review-a", "run-a", true
	if err := service.UpdateCreationReceipt(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	saved.RunID = "run-b"
	if err := service.UpdateCreationReceipt(context.Background(), saved); !errors.Is(err, ErrConflict) {
		t.Fatalf("replaced run error=%v", err)
	}
	if ids != 1 {
		t.Fatalf("receipt replay allocated %d FeatureAreas", ids)
	}
}

type sessionStub map[string]aisession.Session

func (s sessionStub) Get(_ context.Context, id string) (aisession.Session, error) {
	value, ok := s[id]
	if !ok {
		return aisession.Session{}, aisession.ErrNotFound
	}
	return value, nil
}

func zeroTaskCount(ctx context.Context, featureAreaID string) (int, error) { return 0, nil }

func TestFeatureAreaExistsBeforeExecutablePlanningState(t *testing.T) {
	now := time.Date(2026, 9, 4, 2, 3, 4, 0, time.UTC)
	store := NewMemoryStore()
	service, err := NewService(store, sessionStub{
		"session-a":             {ID: "session-a", ProjectID: "project-a"},
		"session-a-replacement": {ID: "session-a-replacement", ProjectID: "project-a"},
		"session-b":             {ID: "session-b", ProjectID: "project-b"},
	}, zeroTaskCount, func() (string, error) { return "feature_area-a", nil }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), CreateInput{AISessionID: "session-a", Title: "Fix startup", ProblemStatement: "The application does not start."})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "feature_area-a" || created.ProjectID != "project-a" || created.CreatedByAISessionID != "session-a" || created.Status != StatusDiscovering || created.Title != "Fix startup" || created.ProblemStatement != "The application does not start." {
		t.Fatalf("created FeatureArea=%+v", created)
	}
	if IsTaskActionable(created) {
		t.Fatalf("DISCOVERING Feature Area must not be Task actionable: %+v", created)
	}
	listed, err := service.ListForSession(context.Background(), "session-a-replacement")
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	continued, err := service.GetForSession(context.Background(), "session-a-replacement", created.ID)
	if err != nil || continued.ID != created.ID || continued.CreatedByAISessionID != "session-a" {
		t.Fatalf("replacement-session FeatureArea=%+v err=%v", continued, err)
	}
	if _, err := service.GetForSession(context.Background(), "session-b", created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project read error=%v", err)
	}
	activated, err := service.Activate(context.Background(), "session-a", created.ID, "lifecycle-request")
	if err != nil || activated.Status != StatusActive {
		t.Fatalf("activated=%+v err=%v", activated, err)
	}
	if !IsTaskActionable(activated) {
		t.Fatalf("ACTIVE Feature Area must be Task actionable: %+v", activated)
	}
	listed, err = service.ListForSession(context.Background(), "session-a-replacement")
	if err != nil || len(listed) != 1 {
		t.Fatalf("ACTIVE Feature Area leaked out of workflow list: %+v err=%v", listed, err)
	}
	retired, err := service.Retire(context.Background(), "session-a", created.ID, "retire-request")
	if err != nil || retired.Status != StatusRetiring {
		t.Fatalf("retired=%+v err=%v", retired, err)
	}
	if IsUsable(retired) || IsTaskActionable(retired) {
		t.Fatalf("RETIRED Feature Area must be unusable: %+v", retired)
	}
	listed, err = service.ListForSession(context.Background(), "session-a-replacement")
	if err != nil || len(listed) != 0 {
		t.Fatalf("RETIRED Feature Area leaked into workflow list: %+v err=%v", listed, err)
	}
	if _, err := service.GetForSession(context.Background(), "session-a-replacement", created.ID); !errors.Is(err, ErrRetiring) {
		t.Fatalf("RETIRING Feature Area workflow read error=%v", err)
	}
	stored, err := store.Get(context.Background(), created.ID)
	if err != nil || stored.Status != StatusRetiring || stored.CreatedByAISessionID != "session-a" {
		t.Fatalf("durable retired FeatureArea=%+v err=%v", stored, err)
	}
}

func TestCurrentFeatureAreaDoesNotArbitrarilyChooseAmongUsableFeatureAreas(t *testing.T) {
	now := time.Date(2026, 9, 4, 3, 4, 5, 0, time.UTC)
	store := NewMemoryStore()
	service, err := NewService(store, sessionStub{"session-a": {ID: "session-a", ProjectID: "project-a"}}, zeroTaskCount, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"feature_area-a", "feature_area-b"} {
		stamp := now.Add(time.Duration(index) * time.Minute)
		if err := store.Create(context.Background(), FeatureArea{ID: id, ProjectID: "project-a", CreatedByAISessionID: "session-a", Title: id, ProblemStatement: id, Status: StatusActive, CreatedAt: stamp, UpdatedAt: stamp}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.CurrentForSession(context.Background(), "session-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("multiple usable Feature Areas produced arbitrary current: %v", err)
	}
}

func TestFeatureAreaCreateRejectsUnboundedOrMissingSemanticInput(t *testing.T) {
	service, err := NewService(NewMemoryStore(), sessionStub{"session-a": {ID: "session-a", ProjectID: "project-a"}}, zeroTaskCount, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []CreateInput{
		{AISessionID: "", Title: "Title", ProblemStatement: "Problem"},
		{AISessionID: "session-a", Title: "", ProblemStatement: "Problem"},
		{AISessionID: "session-a", Title: "Title", ProblemStatement: ""},
	} {
		if _, err := service.Create(context.Background(), input); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("input=%+v error=%v", input, err)
		}
	}
}

func TestFeatureAreaLifecycleMatrix(t *testing.T) {
	for _, from := range []Status{StatusDiscovering, StatusActive, StatusDecomposed, StatusRetired} {
		for _, action := range []LifecycleAction{ActionActivate, ActionDecompose, ActionRetire} {
			_, err := LifecycleTarget(FeatureArea{Status: from}, action)
			allowed := from == StatusDiscovering && (action == ActionActivate || action == ActionDecompose) || (from == StatusActive || from == StatusDiscovering) && action == ActionRetire
			if (err == nil) != allowed {
				t.Fatalf("%s / %s: %v", from, action, err)
			}
		}
	}
}
func TestDecomposeRequiresZeroTasksAndReplaysExactChildren(t *testing.T) {
	store := NewMemoryStore()
	count := 1
	service, err := NewService(store, sessionStub{"session-a": {ID: "session-a", ProjectID: "project-a"}}, func(context.Context, string) (int, error) { return count, nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	parent, err := service.Create(ctx, CreateInput{AISessionID: "session-a", Title: "Broad", ProblemStatement: "Capability boundary"})
	if err != nil {
		t.Fatal(err)
	}
	input := DecomposeInput{AISessionID: "session-a", FeatureAreaID: parent.ID, RequestID: "split-1", Children: []ChildDefinition{{Title: "First", ProblemStatement: "First independent capability"}, {Title: "Second", ProblemStatement: "Second independent capability"}}}
	if _, err := service.Decompose(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("Tasks did not block decompose: %v", err)
	}
	count = 0
	result, err := service.Decompose(ctx, input)
	if err != nil || result.Parent.Status != StatusDecomposed || len(result.Children) != 2 || len(result.Relations) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	replay, err := service.Decompose(ctx, input)
	if err != nil || replay.Children[0].ID != result.Children[0].ID || replay.Children[1].ID != result.Children[1].ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	input.Children[0].Title = "Changed"
	if _, err := service.Decompose(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed payload=%v", err)
	}
	for _, child := range result.Children {
		if child.Status != StatusDiscovering || child.ID == parent.ID {
			t.Fatalf("child=%+v", child)
		}
	}
}
