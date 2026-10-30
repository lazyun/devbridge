package probeexecution

import (
	"encoding/json"
	"reflect"

	"github.com/lazyun/devbridge/internal/inspectionresult"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/workset"
)

// These aliases retain the public probeexecution contract while the canonical
// schema/parser now lives in the dependency-neutral inspectionresult package.
type InspectionSource = inspectionresult.InspectionSource
type InspectionFinding = inspectionresult.InspectionFinding
type InspectionRepositoryObservation = inspectionresult.InspectionRepositoryObservation
type InspectionRelationship = inspectionresult.InspectionRelationship
type InspectionReport = inspectionresult.InspectionReport

func InspectionSourceForWorkset(w workset.RuntimeWorkset) InspectionSource {
	return inspectionresult.SourceForWorkset(w)
}
func InspectionReportSchema(source InspectionSource) json.RawMessage {
	return inspectionresult.Schema(source)
}
func ParseInspectionReport(raw []byte, w workset.RuntimeWorkset) (InspectionReport, error) {
	return inspectionresult.Parse(raw, w)
}
func inspectionHash(raw []byte) string { return inspectionresult.Hash(raw) }

// InspectionObservationStatus compares all observed fields, including index,
// local metadata and root identity captured by the shared observer. It never
// cleans a dirty worktree or treats a model's self-report as mutation evidence.
func InspectionObservationStatus(before, after []recoverystatus.WorktreeObservation) string {
	if len(before) == 0 || len(before) != len(after) {
		return "SCOPE_CONFLICT"
	}
	for i, b := range before {
		if !b.Available || !after[i].Available || !reflect.DeepEqual(b, after[i]) {
			return "SCOPE_CONFLICT"
		}
	}
	return "UNCHANGED"
}
