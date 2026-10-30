package bridgehttp

import (
	"context"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
)

func createPlanningTaskForArea(t *testing.T, f consoleFixture, areaID, title string) task.Task {
	t.Helper()
	value, err := f.plane.Tasks.Create(context.Background(), task.CreateInput{AISessionID: f.sessionID, RequestID: nextBridgeTaskCreateRequestID(), ProjectID: f.projectID, FeatureAreaID: areaID, ModuleID: f.moduleID, RepositoryID: f.repositoryID, CodexConfigID: f.configID, BaseRevision: f.baseCommit, ProblemStatement: title, Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
