package recoverystatus

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/inspectionresult"
	"github.com/lazyun/devbridge/internal/workset"
)

func inspectionAssessmentFixture(t *testing.T) (Report, FactualObservation) {
	t.Helper()
	_, f := factFixture(t)
	w := f.Workset
	w.ProtocolVersion = workset.RuntimeProtocolVersionProjectInspection
	w.OwnerKind = workset.RuntimeOwnerProjectInspection
	w.ProjectID = "project"
	w.FeatureAreaID = ""
	w.InspectionOwnerTaskID = "inspection-task"
	w.InspectionOperationID = "probe"
	w.RunID = "source-run"
	w.WorkspaceID = "inspection-workspace"
	w.ContentHash = "inspection-workset-hash"
	for i := range w.Members {
		w.Members[i].Write = workset.WriteScope{Kind: "NONE"}
		w.Members[i].IntendedReadScope = workset.ReadScope{Kind: "REPOSITORY"}
		f.Before[i].ChangedPaths = []string{}
		f.After[i].ChangedPaths = []string{}
	}
	s := Source{ProjectID: w.ProjectID, OwnerKind: "PROJECT_INSPECTION", OwnerID: w.InspectionOperationID, TaskID: w.InspectionOwnerTaskID, OperationKind: "INSPECTION", OperationID: w.InspectionOperationID, RunID: w.RunID, TurnID: "source-turn", RuntimeWorksetHash: w.ContentHash, WorkspaceID: w.WorkspaceID}
	result := inspectionresult.InspectionReport{
		SchemaVersion: "project-inspection-v1", Source: inspectionresult.SourceForWorkset(w), Status: "SUCCEEDED", Summary: "Inspection completed",
		Findings: []inspectionresult.InspectionFinding{},
		RepositoryObservations: []inspectionresult.InspectionRepositoryObservation{
			{RepositoryAlias: "app", Summary: "App inspected", RelevantPaths: []string{}},
			{RepositoryAlias: "sdk", Summary: "SDK inspected", RelevantPaths: []string{}},
		},
		BlockedReasons: []string{}, Relationships: []inspectionresult.InspectionRelationship{}, RecommendedNextAction: "NONE",
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	r := Report{Version: InspectionReportVersion, SourceOperationKind: s.OperationKind, SourceOperationID: s.OperationID, SourceRunID: s.RunID, SourceTurnID: s.TurnID, OverallStatus: "COMPLETED", Summary: "Inspection result reconciled", CompletedGoals: []GoalObservation{{GoalID: "approved-goal", Status: "COMPLETED", EvidenceSummary: "every member inspected"}}, RemainingGoals: []GoalObservation{}, RepositoryObservations: []RepositoryObservation{{RuntimeAlias: "app", ChangedPaths: []string{}, Completion: "COMPLETED", VerificationState: "NOT_RUN"}, {RuntimeAlias: "sdk", ChangedPaths: []string{}, Completion: "COMPLETED", VerificationState: "NOT_RUN"}}, VerificationObservations: []VerificationObservation{}, UnresolvedIssues: []string{}, RecommendedNextAction: "FINALIZE", InspectionResult: raw}
	reportBytes, _ := json.Marshal(r)
	f.Source, f.Workset, f.OwnerResult = s, w, append([]byte(nil), raw...)
	f.RecoveryRunID, f.RecoveryTurnID, f.ReportID, f.ReportHash = "recovery-run", "recovery-turn", "report", Hash(reportBytes)
	f.OwnerContractValidated, f.OwnerContractEvidenceHash = true, Hash([]byte("receipt-before-after-result"))
	return r, f
}

func replaceInspectionResult(t *testing.T, r *Report, f *FactualObservation, edit func(*inspectionresult.InspectionReport)) {
	t.Helper()
	var result inspectionresult.InspectionReport
	if err := json.Unmarshal(r.InspectionResult, &result); err != nil {
		t.Fatal(err)
	}
	edit(&result)
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	r.InspectionResult, f.OwnerResult = raw, append([]byte(nil), raw...)
	reportBytes, _ := json.Marshal(*r)
	f.ReportHash = Hash(reportBytes)
}

func TestInspectionAssessmentBindsImmutableOwnerEvidence(t *testing.T) {
	r, f := inspectionAssessmentFixture(t)
	a, err := Assess(r, f)
	if err != nil || a.EngineeringDisposition != "RECOVERED_COMPLETE" || a.Consistency != "CONSISTENT" {
		t.Fatalf("assessment: %+v %v", a, err)
	}
	if a.RecoveryTurnID != f.RecoveryTurnID || a.InspectionResultHash != Hash(r.InspectionResult) || a.RuntimeWorksetHash != f.Workset.ContentHash || a.OwnerObservationHash != ownerObservationHash(f.Before, f.After) {
		t.Fatalf("immutable bindings missing: %+v", a)
	}
	for _, tc := range []struct {
		name string
		edit func(*Report, *FactualObservation)
	}{
		{"missing-turn", func(_ *Report, f *FactualObservation) { f.RecoveryTurnID = "" }},
		{"owner-result-bytes-mismatch", func(_ *Report, f *FactualObservation) { f.OwnerResult = append(f.OwnerResult, ' ') }},
		{"workspace-mismatch", func(_ *Report, f *FactualObservation) { f.Source.WorkspaceID = "other-workspace" }},
		{"missing-owner-evidence", func(_ *Report, f *FactualObservation) { f.OwnerContractEvidenceHash = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr, ff := inspectionAssessmentFixture(t)
			tc.edit(&rr, &ff)
			got, err := Assess(rr, ff)
			if err != nil || got.EngineeringDisposition == "RECOVERED_COMPLETE" || !contains(got.ReasonCodes, "INSPECTION_RECOVERY_BINDING_MISSING") {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestInspectionAssessmentRejectsInvalidMemberAndPathEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*inspectionresult.InspectionReport)
	}{
		{"missing-member", func(result *inspectionresult.InspectionReport) {
			result.RepositoryObservations = result.RepositoryObservations[:1]
		}},
		{"outside-read-scope", func(result *inspectionresult.InspectionReport) {
			result.Findings = []inspectionresult.InspectionFinding{{RepositoryAlias: "app", Path: "/outside", Summary: "invalid"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, f := inspectionAssessmentFixture(t)
			replaceInspectionResult(t, &r, &f, tc.edit)
			a, err := Assess(r, f)
			if err != nil || a.EngineeringDisposition == "RECOVERED_COMPLETE" || !contains(a.ReasonCodes, "INSPECTION_RESULT_INVALID") {
				t.Fatalf("%+v %v", a, err)
			}
		})
	}
}

func TestInspectionAssessmentBlockedRecommendationDisposition(t *testing.T) {
	for _, tc := range []struct {
		recommendation string
		want           string
	}{
		{"INSPECT_MORE", "RECOVERED_PARTIAL"},
		{"REQUEST_USER_DECISION", "USER_DECISION_REQUIRED"},
		{"CREATE_FEATURE_AREA", "REVISION_REQUIRED"},
	} {
		t.Run(tc.recommendation, func(t *testing.T) {
			r, f := inspectionAssessmentFixture(t)
			r.OverallStatus = "PARTIAL"
			r.CompletedGoals = []GoalObservation{}
			r.RemainingGoals = []GoalObservation{}
			replaceInspectionResult(t, &r, &f, func(result *inspectionresult.InspectionReport) {
				result.Status, result.BlockedReasons, result.RecommendedNextAction = "BLOCKED", []string{"follow-up required"}, tc.recommendation
			})
			a, err := Assess(r, f)
			if err != nil || a.EngineeringDisposition != tc.want || a.Consistency != "CONSISTENT" {
				t.Fatalf("%+v %v", a, err)
			}
		})
	}
	t.Run("no-inspection-recommendation-is-not-partial", func(t *testing.T) {
		r, f := inspectionAssessmentFixture(t)
		r.OverallStatus, r.CompletedGoals, r.RemainingGoals = "PARTIAL", []GoalObservation{}, []GoalObservation{}
		replaceInspectionResult(t, &r, &f, func(result *inspectionresult.InspectionReport) {
			result.Status, result.BlockedReasons, result.RecommendedNextAction = "BLOCKED", []string{"follow-up required"}, "NONE"
		})
		a, err := Assess(r, f)
		if err != nil || a.EngineeringDisposition == "RECOVERED_PARTIAL" || !contains(a.ReasonCodes, "INSPECTION_PARTIAL_CONTINUATION_REQUIRED") {
			t.Fatalf("%+v %v", a, err)
		}
	})
}

func TestFeatureAreaAssessmentRetainsV1Bytes(t *testing.T) {
	r, f := factFixture(t)
	a, err := Assess(r, f)
	if err != nil || a.EngineeringDisposition != "RECOVERED_COMPLETE" {
		t.Fatalf("%+v %v", a, err)
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"recoveryTurnId", "inspectionResultHash", "runtimeWorksetHash", "ownerObservationHash"} {
		if strings.Contains(string(raw), field) {
			t.Fatalf("legacy assessment gained %s: %s", field, raw)
		}
	}
	legacy := struct {
		ID                        string    `json:"id"`
		Source                    Source    `json:"sourceOperation"`
		RecoveryRunID             string    `json:"recoveryRunId"`
		ReportID                  string    `json:"reportId"`
		FactualObservationHash    string    `json:"factualObservationHash"`
		ReportHash                string    `json:"reportHash"`
		Consistency               string    `json:"consistency"`
		EngineeringDisposition    string    `json:"engineeringDisposition"`
		SourceResponseDisposition string    `json:"sourceResponseDisposition"`
		ReasonCodes               []string  `json:"reasonCodes"`
		AvailableActions          []string  `json:"availableActions"`
		CreatedAt                 time.Time `json:"createdAt"`
	}{a.ID, a.Source, a.RecoveryRunID, a.ReportID, a.FactualObservationHash, a.ReportHash, a.Consistency, a.EngineeringDisposition, a.SourceResponseDisposition, a.ReasonCodes, a.AvailableActions, a.CreatedAt}
	want, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(want) || Hash(raw) != Hash(want) {
		t.Fatalf("legacy assessment bytes/hash changed:\n got=%s\nwant=%s", raw, want)
	}
}
