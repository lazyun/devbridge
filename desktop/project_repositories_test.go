package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPRepositoryUnlinkUsesCoreConfirmationAndClosedCommit(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}
		switch requests {
		case 1:
			if request.Method != http.MethodGet || request.URL.Path != "/v1/repository-unlink/list" || request.URL.Query().Get("project_id") != "project" || request.URL.Query().Get("include_historical") != "true" {
				t.Fatalf("list request = %s %s", request.Method, request.URL.String())
			}
			response.Body = io.NopCloser(strings.NewReader(`{"projectId":"project","repositories":[{"repositoryId":"repo","registrationState":"ACTIVE"},{"repositoryId":"old","registrationState":"UNLINKED","historyAvailable":true}]}`))
		case 2:
			if request.Method != http.MethodPost || request.URL.Path != "/v1/repository-unlink/prepare" {
				t.Fatalf("prepare request = %s %s", request.Method, request.URL.String())
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != `{"project_id":"project","repository_id":"repo","request_id":"request"}` {
				t.Fatalf("prepare body = %s", body)
			}
			response.Body = io.NopCloser(strings.NewReader(`{"repositoryId":"repo","repositoryName":"source","repositoryPath":"/source","unlinkEligibility":{"projectId":"project","repositoryId":"repo","registrationState":"ACTIVE","registrationGeneration":7,"projectTopologyRevision":11,"canUnlink":true,"blockers":[],"informationalReferences":[]},"confirmation":{"purpose":"REPOSITORY_UNLINK","requestId":"request","projectId":"project","repositoryId":"repo","expectedRegistrationGeneration":7,"expectedTopologyRevision":11,"consequences":["Local Repository files will not be deleted.","Managed worktrees will not be deleted.","Relink and cross-Project transfer are not supported in v1."]}}`))
		case 3:
			if request.Method != http.MethodPost || request.URL.Path != "/v1/repository-unlink/commit" {
				t.Fatalf("commit request = %s %s", request.Method, request.URL.String())
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != `{"expected_registration_generation":7,"expected_topology_revision":11,"project_id":"project","reason":"operator confirmed","repository_id":"repo","request_id":"request"}` {
				t.Fatalf("commit body = %s", body)
			}
			response.Body = io.NopCloser(strings.NewReader(`{"repositoryId":"repo","repositoryName":"source","repositoryPath":"/source","unlinkEligibility":{"projectId":"project","repositoryId":"repo","registrationState":"UNLINKED","registrationGeneration":8,"projectTopologyRevision":12,"canUnlink":false,"blockers":[],"informationalReferences":[]}}`))
		default:
			t.Fatalf("unexpected request %d", requests)
		}
		return response, nil
	})}
	api := newHTTPProjectCoreAPI(client).(*httpProjectCoreAPI)
	list, err := api.ListProjectRepositories(context.Background(), "http://127.0.0.1:8081", "project")
	if err != nil || len(list.Repositories) != 2 || list.Repositories[0].RegistrationState != "ACTIVE" || list.Repositories[1].RegistrationState != "UNLINKED" || !list.Repositories[1].HistoryAvailable {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
	confirmation, err := api.PrepareRepositoryUnlink(context.Background(), "http://127.0.0.1:8081", RepositoryUnlinkPrepareRequest{RequestID: "request", ProjectID: "project", RepositoryID: "repo"})
	if err != nil || confirmation.Purpose != "REPOSITORY_UNLINK" || confirmation.ExpectedGeneration != 7 || confirmation.ExpectedTopologyRevision != 11 || len(confirmation.Consequences) != 3 || confirmation.Eligibility == nil || !confirmation.Eligibility.CanUnlink {
		t.Fatalf("confirmation = %+v, err = %v", confirmation, err)
	}
	confirmation.Reason = "operator confirmed"
	committed, err := api.CommitRepositoryUnlink(context.Background(), "http://127.0.0.1:8081", confirmation)
	if err != nil || committed.RepositoryID != "repo" || committed.ExpectedGeneration != 8 || committed.ExpectedTopologyRevision != 12 || committed.Eligibility == nil || committed.Eligibility.RegistrationState != "UNLINKED" {
		t.Fatalf("committed = %+v, err = %v", committed, err)
	}
	if requests != 3 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestHTTPRepositoryUnlinkRejectsMismatchedCoreConfirmation(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"repositoryId":"repo","unlinkEligibility":{"projectId":"project","repositoryId":"repo"},"confirmation":{"purpose":"REPOSITORY_UNLINK","requestId":"other","projectId":"project","repositoryId":"repo","expectedRegistrationGeneration":1,"expectedTopologyRevision":1,"consequences":[]}}`))}, nil
	})}
	api := newHTTPProjectCoreAPI(client).(*httpProjectCoreAPI)
	if _, err := api.PrepareRepositoryUnlink(context.Background(), "http://127.0.0.1:8081", RepositoryUnlinkPrepareRequest{RequestID: "request", ProjectID: "project", RepositoryID: "repo"}); err == nil {
		t.Fatal("mismatched Core confirmation was accepted")
	}
}
