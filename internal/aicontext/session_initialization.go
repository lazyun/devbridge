package aicontext

import (
	"context"
	"sort"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
)

type ProjectIdentity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type InitializationResult struct {
	AvailableProjectsComplete  bool                      `json:"availableProjectsComplete"`
	AISessionID                string                    `json:"aiSessionId"`
	State                      string                    `json:"state"`
	BoundProjectID             *string                   `json:"boundProjectId"`
	Project                    *ProjectIdentity          `json:"project,omitempty"`
	AvailableProjects          []ProjectIdentity         `json:"availableProjects"`
	AvailableProjectsStatus    string                    `json:"availableProjectsStatus"`
	AvailableProjectsReadError string                    `json:"availableProjectsReadError,omitempty"`
	Receipt                    *aisession.RequestReceipt `json:"receipt,omitempty"`
}
type ProjectBindingResult struct {
	Context           SessionContext           `json:"-"`
	AISessionID       string                   `json:"aiSessionId"`
	BoundProjectID    string                   `json:"boundProjectId"`
	State             string                   `json:"state"`
	IsNewBinding      bool                     `json:"isNewBinding"`
	Receipt           aisession.RequestReceipt `json:"receipt"`
	PreparationStatus string                   `json:"preparationStatus"`
	PreparationError  string                   `json:"preparationError,omitempty"`
}
type initializingSessionService interface {
	InitializeSession(context.Context, aiprojectselection.ClientBindingReference, string, string) (aisession.Initialization, error)
	BindProject(context.Context, aiprojectselection.ClientBindingReference, string, string, string) (aisession.Session, aisession.RequestReceipt, bool, error)
}

func (s *Service) InitializeSession(ctx context.Context, ref aiprojectselection.ClientBindingReference, requestID, sessionID string) (InitializationResult, error) {
	provider, ok := s.sessions.(initializingSessionService)
	if !ok {
		return InitializationResult{}, aisession.ErrInitializationRequired
	}
	current, err := provider.InitializeSession(ctx, ref, requestID, sessionID)
	if err != nil {
		return InitializationResult{}, err
	}
	result := InitializationResult{AISessionID: current.AISessionID, State: current.State, BoundProjectID: current.BoundProjectID, AvailableProjects: []ProjectIdentity{}, AvailableProjectsStatus: "NOT_APPLICABLE", AvailableProjectsComplete: true, Receipt: current.Receipt}
	if current.BoundProjectID != nil {
		value, err := s.projects.Get(ctx, *current.BoundProjectID)
		if err != nil {
			return result, err
		}
		result.Project = &ProjectIdentity{ID: value.ID, Name: value.Name}
		return result, nil
	}
	// A single full identity read avoids silently truncating at the ordinary
	// project_list page limit or treating repository read failures as no projects.
	inventory, err := s.projects.List(ctx)
	if err != nil {
		result.AvailableProjectsComplete = false
		result.AvailableProjectsStatus = "UNAVAILABLE"
		result.AvailableProjectsReadError = "PROJECT_INVENTORY_UNAVAILABLE"
		return result, nil
	}
	sort.Slice(inventory, func(i, j int) bool {
		if inventory[i].Name == inventory[j].Name {
			return inventory[i].ID < inventory[j].ID
		}
		return inventory[i].Name < inventory[j].Name
	})
	result.AvailableProjectsStatus = "AVAILABLE"
	if len(inventory) > MaxListLimit {
		result.AvailableProjectsComplete = false
		result.AvailableProjectsStatus = "UNAVAILABLE"
		result.AvailableProjectsReadError = "PROJECT_INVENTORY_INCOMPLETE"
		inventory = inventory[:MaxListLimit]
	}
	for _, value := range inventory {
		result.AvailableProjects = append(result.AvailableProjects, ProjectIdentity{ID: value.ID, Name: value.Name})
	}
	return result, nil
}
func (s *Service) BindProject(ctx context.Context, ref aiprojectselection.ClientBindingReference, sessionID, projectID, requestID string) (ProjectBindingResult, error) {
	if sessionID == "" {
		return ProjectBindingResult{}, aisession.ErrInitializationRequired
	}
	provider, ok := s.sessions.(initializingSessionService)
	if !ok {
		return ProjectBindingResult{}, aisession.ErrInitializationRequired
	}
	session, receipt, created, err := provider.BindProject(ctx, ref, sessionID, projectID, requestID)
	if err != nil {
		return ProjectBindingResult{}, err
	}
	result := ProjectBindingResult{Context: SessionContext{Session: session}, AISessionID: session.ID, State: aisession.ProjectBound, BoundProjectID: session.ProjectID, IsNewBinding: created, Receipt: receipt, PreparationStatus: "NOT_REPEATED"}
	if created {
		result.Context, err = s.finishNewSession(ctx, session)
		if err != nil {
			result.PreparationStatus = "FAILED"
			result.PreparationError = "PROJECT_PREPARATION_INCOMPLETE"
			return result, nil
		}
		result.PreparationStatus = "COMPLETED"
		return result, nil
	}
	// Pure readback never repeats preparation. An absent context after an earlier
	// failure remains visible, while the committed Session and receipt survive.
	value, err := s.GetSession(ctx, session.ID)
	if err != nil {
		result.PreparationError = "PROJECT_CONTEXT_UNAVAILABLE"
		return result, nil
	}
	result.Context = value
	return result, nil
}
