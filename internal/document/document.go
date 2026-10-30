// Package document owns explicitly requested durable Project knowledge.
// Documents record GPT-authored text and do not represent engineering work,
// execution, or authorization.
package document

import "time"

type Document struct {
	ID                   string
	ProjectID            string
	FeatureAreaID        string
	CreatedByAISessionID string
	Title                string
	Content              string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}
