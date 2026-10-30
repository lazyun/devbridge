package consoleapi

import (
	"context"
	"strings"
)

type ProductTaskPage struct {
	Items  []ProductTaskSummary `json:"items"`
	Total  int                  `json:"total"`
	Limit  int                  `json:"limit"`
	Offset int                  `json:"offset"`
}

type ProductTaskFilter struct {
	PageRequest
	Search, FeatureAreaID, Status, Phase, Branch, Kind string
}

// ListProductTasks pages semantic Task facts from the existing product reader.
// Filtering never substitutes a Review instruction or an operation inventory.
func (r *Reader) ListProductTasks(ctx context.Context, projectID string, filter ProductTaskFilter) (ProductTaskPage, error) {
	limit, offset, err := boundedPage(filter.PageRequest)
	if err != nil {
		return ProductTaskPage{}, err
	}
	workspace, err := r.GetProductWorkTab(ctx, projectID, "tasks")
	if err != nil {
		return ProductTaskPage{}, err
	}
	return pageProductTasks(workspace.Tasks, filter, limit, offset), nil
}

func pageProductTasks(tasks []ProductTaskSummary, filter ProductTaskFilter, limit, offset int) ProductTaskPage {
	items := make([]ProductTaskSummary, 0, limit)
	needle, total := strings.ToLower(strings.TrimSpace(filter.Search)), 0
	for _, value := range tasks {
		if filter.FeatureAreaID != "" && value.FeatureAreaID != filter.FeatureAreaID {
			continue
		}
		if filter.Status == "__terminal__" && value.Status != "SUCCEEDED" && value.Status != "FAILED" && value.Status != "CANCELED" {
			continue
		}
		if filter.Status != "" && filter.Status != "__terminal__" && string(value.Status) != filter.Status {
			continue
		}
		if filter.Phase != "" && string(value.Phase) != filter.Phase && string(value.BusinessPhase) != filter.Phase {
			continue
		}
		if filter.Branch != "" {
			match := filter.Branch == "__unknown__" && len(value.DevelopmentBranches) == 0
			for _, branch := range value.DevelopmentBranches {
				if branch == filter.Branch {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		if filter.Kind != "" && filter.Kind != "all" {
			if filter.Kind == "unclassified" {
				if value.WorkKind != "" {
					continue
				}
			} else if string(value.WorkKind) != filter.Kind {
				continue
			}
		}
		if needle != "" && !strings.Contains(strings.ToLower(value.Title), needle) {
			continue
		}
		if total >= offset && len(items) < limit {
			items = append(items, value)
		}
		total++
	}
	return ProductTaskPage{Items: items, Total: total, Limit: limit, Offset: offset}
}
