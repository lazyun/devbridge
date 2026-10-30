package bridgehttp

import (
	"errors"
	"net/http"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/projectregistration"
)

type projectRegistrationInput struct {
	RequestID      string                                `json:"request_id"`
	ProjectID      string                                `json:"project_id,omitempty"`
	Repositories   []projectregistration.RepositoryInput `json:"repositories"`
	Name           string                                `json:"name"`
	RepositoryPath string                                `json:"repository_path"`
}

type registeredRepository struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}
type projectRegistrationOutput struct {
	RequestID      string                 `json:"request_id,omitempty"`
	Repositories   []registeredRepository `json:"repositories"`
	ProjectID      string                 `json:"project_id"`
	RepositoryID   string                 `json:"repository_id"`
	Name           string                 `json:"name"`
	RepositoryPath string                 `json:"repository_path"`
}

func registrationOutput(value projectregistration.Registration) projectRegistrationOutput {
	repos := make([]registeredRepository, 0, len(value.Repositories))
	for _, repo := range value.Repositories {
		repos = append(repos, registeredRepository{ID: repo.ID, Name: repo.Name, Path: repo.Path})
	}
	return projectRegistrationOutput{RequestID: value.RequestID, Repositories: repos,
		ProjectID: value.Project.ID, RepositoryID: value.Repository.ID,
		Name: value.Project.Name, RepositoryPath: value.Repository.Path,
	}
}

func (s *Server) registerProject(w http.ResponseWriter, r *http.Request) {
	var input projectRegistrationInput
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.RegisterProject(r.Context(), projectregistration.Input{RequestID: input.RequestID, ProjectID: input.ProjectID, Repositories: input.Repositories, Name: input.Name, RepositoryPath: input.RepositoryPath})
	if err == nil {
		writeJSON(w, http.StatusCreated, registrationOutput(value))
		return
	}
	var apiErr *controlapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == controlapi.CodeProjectAlreadyRegistered {
		output := registrationOutput(value)
		writeJSON(w, http.StatusConflict, map[string]any{
			"code": apiErr.Code, "message": "Repository is already registered as " + value.Project.Name + ".",
			"project_id": output.ProjectID, "repository_id": output.RepositoryID,
			"name": output.Name, "repository_path": output.RepositoryPath,
		})
		return
	}
	writeError(w, err)
}
