package controlapi

import c "github.com/lazyun/devbridge/internal/confirmation"

// Validate the entire submitted set before any command receipt is begun. A row
// is an exact repository/ref target, never a session-wide repository choice.
func validateProjectSetupSelection(intent c.Intent, selected c.Selection, options c.Options) error {
	projectAllowed := false
	for _, p := range options.Projects {
		if p.ID == selected.ProjectID && p.Available {
			projectAllowed = true
		}
	}
	if !projectAllowed {
		return invalid("select an exact available Project")
	}
	switch intent.Action {
	case "SELECT_PROJECT":
		if selected.RepositoryID != "" || selected.DevelopmentRef != "" || len(selected.DevelopmentContexts) > 0 {
			return invalid("SELECT_PROJECT accepts only Project identity")
		}
		return nil
	case "CONFIGURE_DEVELOPMENT_CONTEXT":
		if selected.ProjectID != intent.ProjectID || selected.RepositoryID != "" || selected.DevelopmentRef != "" || len(selected.DevelopmentContexts) == 0 || len(selected.DevelopmentContexts) > 128 || !options.RepositoryInventoryComplete {
			return invalid("exact bound Project and complete repository options required")
		}
		seen := map[string]bool{}
		for _, row := range selected.DevelopmentContexts {
			if row.RepositoryID == "" || seen[row.RepositoryID] {
				return invalid("duplicate or missing Repository identity")
			}
			seen[row.RepositoryID] = true
			available := false
			for _, candidate := range options.DevelopmentContexts {
				if candidate.RepositoryID == row.RepositoryID && candidate.Available {
					for _, branch := range candidate.Branches {
						if branch.ID == row.DevelopmentRef && branch.Available {
							available = true
						}
					}
				}
			}
			if !available {
				return invalid("selected Repository development branch is unavailable")
			}
		}
		return nil
	case "SELECT_PROJECT_BRANCH":
		if len(selected.DevelopmentContexts) > 0 {
			return invalid("legacy action cannot accept an expanded repository set")
		}
		hasRepo, hasBranch := false, false
		for _, r := range options.Repositories {
			if r.ID == selected.RepositoryID && r.Available {
				hasRepo = true
			}
		}
		for _, b := range options.Branches {
			if b.ID == selected.DevelopmentRef && b.Available {
				hasBranch = true
			}
		}
		if hasRepo && hasBranch {
			return nil
		}
	}
	return invalid("select an exact available Project, Repository and local branch")
}
