package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

type DesktopRepository struct {
	RepositoryID           string                    `json:"repositoryId"`
	RepositoryName         string                    `json:"repositoryName"`
	RepositoryPath         string                    `json:"repositoryPath"`
	RegistrationState      string                    `json:"registrationState"`
	RegistrationGeneration int64                     `json:"registrationGeneration"`
	Availability           string                    `json:"availability"`
	DevelopmentRef         string                    `json:"developmentRef"`
	DevelopmentRefStatus   string                    `json:"developmentRefStatus"`
	StatusCode             string                    `json:"statusCode,omitempty"`
	CurrentUsage           string                    `json:"currentUsage,omitempty"`
	UnlinkEligibility      *DesktopUnlinkEligibility `json:"unlinkEligibility,omitempty"`
	UnlinkedAt             string                    `json:"unlinkedAt,omitempty"`
	HistoryAvailable       bool                      `json:"historyAvailable"`
	AvailableActions       []string                  `json:"availableActions"`
}

// DesktopUnlinkEligibility is rendered exactly as Core supplied it. Desktop
// never derives unlink safety from an operation count or local filesystem.
type DesktopUnlinkEligibility struct {
	ProjectID               string                   `json:"projectId"`
	RepositoryID            string                   `json:"repositoryId"`
	RegistrationState       string                   `json:"registrationState"`
	RegistrationGeneration  int64                    `json:"registrationGeneration"`
	ProjectTopologyRevision int64                    `json:"projectTopologyRevision"`
	CanUnlink               bool                     `json:"canUnlink"`
	Blockers                []DesktopUnlinkBlocker   `json:"blockers"`
	InformationalReferences []DesktopUnlinkReference `json:"informationalReferences"`
	EvaluatedAt             string                   `json:"evaluatedAt"`
}

type DesktopUnlinkBlocker struct {
	BlockerKind       string `json:"blockerKind"`
	EntityType        string `json:"entityType"`
	EntityID          string `json:"entityId"`
	OwnerType         string `json:"ownerType"`
	OwnerID           string `json:"ownerId"`
	Status            string `json:"status"`
	ReasonCode        string `json:"reasonCode"`
	UserFacingSummary string `json:"userFacingSummary"`
}

type DesktopUnlinkReference struct {
	EntityType string `json:"entityType"`
	EntityID   string `json:"entityId"`
	Summary    string `json:"summary"`
}
type DesktopRepositoryList struct {
	ProjectID    string               `json:"projectId"`
	Repositories []DesktopRepository  `json:"repositories"`
	Error        *DesktopProjectError `json:"error,omitempty"`
}

// RepositoryUnlinkPrepareRequest is a closed Desktop-to-Core request. It does
// not allow a client to supply outcome, force, blockers or a replacement
// topology revision.
type RepositoryUnlinkPrepareRequest struct {
	RequestID    string `json:"requestId"`
	ProjectID    string `json:"projectId"`
	RepositoryID string `json:"repositoryId"`
}

type DesktopRepositoryUnlinkConfirmation struct {
	Purpose                  string                    `json:"purpose"`
	RequestID                string                    `json:"requestId"`
	ProjectID                string                    `json:"projectId"`
	RepositoryID             string                    `json:"repositoryId"`
	RepositoryName           string                    `json:"repositoryName"`
	RepositoryPath           string                    `json:"repositoryPath"`
	ExpectedGeneration       int64                     `json:"expectedRegistrationGeneration"`
	ExpectedTopologyRevision int64                     `json:"expectedTopologyRevision"`
	Reason                   string                    `json:"reason"`
	Consequences             []string                  `json:"consequences"`
	Eligibility              *DesktopUnlinkEligibility `json:"eligibility,omitempty"`
	Error                    *DesktopProjectError      `json:"error,omitempty"`
}

type repositoryUnlinkCoreAPI interface {
	PrepareRepositoryUnlink(context.Context, string, RepositoryUnlinkPrepareRequest) (DesktopRepositoryUnlinkConfirmation, error)
	CommitRepositoryUnlink(context.Context, string, DesktopRepositoryUnlinkConfirmation) (DesktopRepositoryUnlinkConfirmation, error)
}

func (c *httpProjectCoreAPI) ListProjectRepositories(ctx context.Context, endpoint, projectID string) (DesktopRepositoryList, error) {
	target, err := coreURL(endpoint, "/v1/repository-unlink/list?project_id="+url.QueryEscape(projectID)+"&include_historical=true")
	if err != nil {
		return DesktopRepositoryList{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return DesktopRepositoryList{}, err
	}
	response, err := c.client.Do(req)
	if err != nil {
		return DesktopRepositoryList{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return DesktopRepositoryList{}, decodeCoreError(response.Body, response.StatusCode)
	}
	var result DesktopRepositoryList
	if err = decodeCoreJSON(response.Body, &result); err != nil {
		return result, err
	}
	if result.Repositories == nil {
		result.Repositories = []DesktopRepository{}
	}
	return result, nil
}
func (a *App) ListProjectRepositories(projectID string) DesktopRepositoryList {
	result := DesktopRepositoryList{ProjectID: projectID, Repositories: []DesktopRepository{}}
	if !a.coreReadyForProjects() {
		result.Error = &DesktopProjectError{Code: desktopCodeCoreUnavailable, Message: "Dev Bridge Core is not ready."}
		return result
	}
	ctx, endpoint, api := a.projectRequestContext()
	reader, ok := api.(interface {
		ListProjectRepositories(context.Context, string, string) (DesktopRepositoryList, error)
	})
	if !ok {
		result.Error = &DesktopProjectError{Code: desktopCodeUnsupportedOperation, Message: "Repository inventory unavailable."}
		return result
	}
	loaded, err := reader.ListProjectRepositories(ctx, endpoint, projectID)
	if err != nil {
		result.Error = mapDesktopProjectError(err, false)
		return result
	}
	if loaded.ProjectID != projectID {
		result.Error = &DesktopProjectError{Code: desktopCodeCoreError, Message: "Repository inventory Project identity mismatch."}
		return result
	}
	return loaded
}

func (c *httpProjectCoreAPI) PrepareRepositoryUnlink(ctx context.Context, endpoint string, input RepositoryUnlinkPrepareRequest) (DesktopRepositoryUnlinkConfirmation, error) {
	payload, err := json.Marshal(map[string]string{"request_id": input.RequestID, "project_id": input.ProjectID, "repository_id": input.RepositoryID})
	if err != nil {
		return DesktopRepositoryUnlinkConfirmation{}, err
	}
	target, err := coreURL(endpoint, "/v1/repository-unlink/prepare")
	if err != nil {
		return DesktopRepositoryUnlinkConfirmation{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return DesktopRepositoryUnlinkConfirmation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return DesktopRepositoryUnlinkConfirmation{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return DesktopRepositoryUnlinkConfirmation{}, decodeCoreError(response.Body, response.StatusCode)
	}
	var value struct {
		RepositoryID      string                   `json:"repositoryId"`
		RepositoryName    string                   `json:"repositoryName"`
		RepositoryPath    string                   `json:"repositoryPath"`
		UnlinkEligibility DesktopUnlinkEligibility `json:"unlinkEligibility"`
		Confirmation      *struct {
			Purpose                  string   `json:"purpose"`
			RequestID                string   `json:"requestId"`
			ProjectID                string   `json:"projectId"`
			RepositoryID             string   `json:"repositoryId"`
			ExpectedGeneration       int64    `json:"expectedRegistrationGeneration"`
			ExpectedTopologyRevision int64    `json:"expectedTopologyRevision"`
			Consequences             []string `json:"consequences"`
		} `json:"confirmation"`
	}
	if err := decodeCoreJSON(response.Body, &value); err != nil {
		return DesktopRepositoryUnlinkConfirmation{}, err
	}
	if value.Confirmation == nil || value.Confirmation.Purpose != "REPOSITORY_UNLINK" || value.Confirmation.RequestID != input.RequestID || value.Confirmation.ProjectID != input.ProjectID || value.Confirmation.RepositoryID != input.RepositoryID {
		return DesktopRepositoryUnlinkConfirmation{}, errors.New("repository unlink confirmation identity mismatch")
	}
	confirmation := value.Confirmation
	return DesktopRepositoryUnlinkConfirmation{Purpose: confirmation.Purpose, RequestID: confirmation.RequestID, ProjectID: confirmation.ProjectID, RepositoryID: confirmation.RepositoryID, RepositoryName: value.RepositoryName, RepositoryPath: value.RepositoryPath, ExpectedGeneration: confirmation.ExpectedGeneration, ExpectedTopologyRevision: confirmation.ExpectedTopologyRevision, Consequences: append([]string(nil), confirmation.Consequences...), Eligibility: &value.UnlinkEligibility}, nil
}

func (c *httpProjectCoreAPI) CommitRepositoryUnlink(ctx context.Context, endpoint string, input DesktopRepositoryUnlinkConfirmation) (DesktopRepositoryUnlinkConfirmation, error) {
	payload, err := json.Marshal(map[string]any{"request_id": input.RequestID, "project_id": input.ProjectID, "repository_id": input.RepositoryID, "expected_registration_generation": input.ExpectedGeneration, "expected_topology_revision": input.ExpectedTopologyRevision, "reason": input.Reason})
	if err != nil {
		return DesktopRepositoryUnlinkConfirmation{}, err
	}
	target, err := coreURL(endpoint, "/v1/repository-unlink/commit")
	if err != nil {
		return DesktopRepositoryUnlinkConfirmation{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return DesktopRepositoryUnlinkConfirmation{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return DesktopRepositoryUnlinkConfirmation{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return DesktopRepositoryUnlinkConfirmation{}, decodeCoreError(response.Body, response.StatusCode)
	}
	var value struct {
		RepositoryID      string                   `json:"repositoryId"`
		RepositoryName    string                   `json:"repositoryName"`
		RepositoryPath    string                   `json:"repositoryPath"`
		UnlinkEligibility DesktopUnlinkEligibility `json:"unlinkEligibility"`
	}
	if err := decodeCoreJSON(response.Body, &value); err != nil {
		return DesktopRepositoryUnlinkConfirmation{}, err
	}
	return DesktopRepositoryUnlinkConfirmation{RequestID: input.RequestID, ProjectID: input.ProjectID, RepositoryID: value.RepositoryID, RepositoryName: value.RepositoryName, RepositoryPath: value.RepositoryPath, ExpectedGeneration: value.UnlinkEligibility.RegistrationGeneration, ExpectedTopologyRevision: value.UnlinkEligibility.ProjectTopologyRevision, Eligibility: &value.UnlinkEligibility}, nil
}

func (a *App) PrepareRepositoryUnlink(input RepositoryUnlinkPrepareRequest) DesktopRepositoryUnlinkConfirmation {
	if !a.coreReadyForProjects() {
		return DesktopRepositoryUnlinkConfirmation{Error: &DesktopProjectError{Code: desktopCodeCoreUnavailable, Message: "Dev Bridge Core is not ready."}}
	}
	ctx, endpoint, api := a.projectRequestContext()
	service, ok := api.(repositoryUnlinkCoreAPI)
	if !ok {
		return DesktopRepositoryUnlinkConfirmation{Error: &DesktopProjectError{Code: desktopCodeUnsupportedOperation, Message: "Repository unlink is unavailable in this Core."}}
	}
	value, err := service.PrepareRepositoryUnlink(ctx, endpoint, input)
	if err != nil {
		value.Error = mapDesktopProjectError(err, false)
	}
	return value
}

func (a *App) CommitRepositoryUnlink(input DesktopRepositoryUnlinkConfirmation) DesktopRepositoryUnlinkConfirmation {
	if !a.coreReadyForProjects() {
		return DesktopRepositoryUnlinkConfirmation{Error: &DesktopProjectError{Code: desktopCodeCoreUnavailable, Message: "Dev Bridge Core is not ready."}}
	}
	ctx, endpoint, api := a.projectRequestContext()
	service, ok := api.(repositoryUnlinkCoreAPI)
	if !ok {
		return DesktopRepositoryUnlinkConfirmation{Error: &DesktopProjectError{Code: desktopCodeUnsupportedOperation, Message: "Repository unlink is unavailable in this Core."}}
	}
	value, err := service.CommitRepositoryUnlink(ctx, endpoint, input)
	if err != nil {
		value.Error = mapDesktopProjectError(err, false)
	}
	return value
}
