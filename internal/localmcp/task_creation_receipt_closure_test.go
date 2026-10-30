package localmcp_test

import (
	"github.com/lazyun/devbridge/internal/mcpserver"
	"sync"
	"testing"
)

func TestFinalTaskCreateConcurrentReceiptReplay(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Concurrent creation receipt")
	in := mcpserver.ExternalTaskCreateInput{AISessionID: sid, FeatureAreaID: area, RepositoryID: f.repositoryID, RequestID: "concurrent-create", Title: "One durable intent", ProblemStatement: "Do not duplicate the semantic Task after response loss.", Constraints: []string{"Keep the scope"}}
	start := make(chan struct{})
	ids := make(chan string, 4)
	errs := make(chan error, 4)
	var wait sync.WaitGroup
	for i := 0; i < 4; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			out, err := mcpserver.TaskCreateCore(f.ctx, f.service, in)
			ids <- out.ID
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for actual := range ids {
		if id == "" {
			id = actual
		}
		if actual == "" || actual != id {
			t.Fatal("duplicate Task", actual, id)
		}
	}
	// Retry on another adapter after dropping every first response.
	replay := callToolMap(t, f.ctx, connectLocalMCP(t, f.libreURL), "task_create", map[string]any{"aiSessionId": sid, "featureAreaId": area, "repositoryId": f.repositoryID, "requestId": in.RequestID, "title": in.Title, "problemStatement": in.ProblemStatement, "constraints": in.Constraints})
	if replay["id"] != id {
		t.Fatal(replay)
	}
	conflict := in
	conflict.Title = "different intent"
	if _, err := mcpserver.TaskCreateCore(f.ctx, f.service, conflict); err == nil {
		t.Fatal("changed payload replay accepted")
	}
	missing := in
	missing.RequestID = ""
	if _, err := mcpserver.TaskCreateCore(f.ctx, f.service, missing); err == nil {
		t.Fatal("requestId-less creation accepted")
	}
	for table, want := range map[string]int{"tasks": 1, "task_create_requests": 1, "reviews": 0, "task_definition_revisions": 1, "execution_proposals": 0} {
		var count int
		if err := f.plane.DB.SQL().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s=%d want=%d", table, count, want)
		}
	}
}
