package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/task"
)

func TestTaskClassificationContextEmptyChangedFilesSerializesAsArray(t *testing.T) {
	value := taskClassificationContextOutput(controlapi.TaskClassificationContext{
		Task:           task.Task{ID: "task-a", ProjectID: "project-a", ModuleID: "module-a", RepositoryID: "repo-a", CodexConfigID: "config-a", ProblemStatement: "problem", Status: task.StatusPlanning},
		Module:         module.Module{ID: "module-a", ProjectID: "project-a"},
		ProjectContext: projectcontext.ProjectContext{ProjectID: "project-a", Status: projectcontext.StatusNoGuidance},
	})
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"changedFiles":[]`) || strings.Contains(string(encoded), `"changedFiles":null`) {
		t.Fatalf("serialized output=%s", encoded)
	}
}
