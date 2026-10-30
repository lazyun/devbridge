package module

import "time"

type Module struct {
	ID              string
	ProjectID       string
	Key             string
	Name            string
	Description     string
	SystemOwned     bool
	ParentID        *string
	ProductionFacts []ProductionFacts
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ResponsibilitySource keeps validated repository facts distinct from a
// concise responsibility derived during bounded Project Context discovery.
type ResponsibilitySource string

const (
	ResponsibilityGuidance   ResponsibilitySource = "GUIDANCE"
	ResponsibilityPackageDoc ResponsibilitySource = "PACKAGE_DOCUMENTATION"
	ResponsibilityDerived    ResponsibilitySource = "DERIVED"
)

// ProductionFacts is the repository-owned portion of one durable Project
// Module Map entry. Paths are always repository-relative and validated before
// persistence. One logical Module may have facts in more than one Repository.
type ProductionFacts struct {
	RepositoryID         string
	RepositoryName       string
	Responsibility       string
	ResponsibilitySource ResponsibilitySource
	ProductionRoots      []string
	Entrypoints          []string
	RelatedModules       []string
}

// ContextModuleInput is the typed replacement contract used only by bounded
// Project Context bootstrap. Internal Module IDs never cross this boundary.
type ContextModuleInput struct {
	Key             string
	Name            string
	Description     string
	ProductionFacts []ProductionFacts
}

const (
	UnclassifiedKey  = "_unclassified"
	UnclassifiedName = "Unclassified / 暂未分类"
)

func clone(value Module) Module {
	if value.ParentID != nil {
		parentID := *value.ParentID
		value.ParentID = &parentID
	}
	value.ProductionFacts = cloneProductionFacts(value.ProductionFacts)
	return value
}

func cloneProductionFacts(values []ProductionFacts) []ProductionFacts {
	result := make([]ProductionFacts, len(values))
	for i, value := range values {
		result[i] = value
		result[i].ProductionRoots = append([]string(nil), value.ProductionRoots...)
		result[i].Entrypoints = append([]string(nil), value.Entrypoints...)
		result[i].RelatedModules = append([]string(nil), value.RelatedModules...)
	}
	return result
}
