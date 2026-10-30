package probeexecution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestCanonicalInspectionOptionsPreserveFrozenInstruction(t *testing.T) {
	prompt := "  exact UTF-8 检查\nno appended prose\n"
	s := CanonicalState{Spec: CanonicalSpec{Probe: contextprobe.Probe{CodexInstruction: prompt}, Model: "frozen-model", ReasoningEffort: "high", ServiceTier: "priority"}, Run: run.Run{ID: "r", ThreadID: "thread"}, Workset: workset.RuntimeWorkset{PrimaryMemberID: "sdk", Members: []workset.RuntimeMemberBinding{{MemberID: "app", CanonicalRoot: "/managed/app"}, {MemberID: "sdk", CanonicalRoot: "/managed/sdk"}}}, Schema: []byte(`{"type":"object","additionalProperties":false}`)}
	cwd, binding, options := inspectionOptions(s)
	if cwd != "/managed/sdk" || options.Prompt != prompt || len(options.OutputSchema) != 0 || options.Model != "frozen-model" || options.ReasoningEffort != "high" || options.ServiceTier == nil || *options.ServiceTier != "priority" || len(binding.RuntimeWorkspaceRoots) != 2 || len(binding.WritableRoots) != 0 {
		t.Fatalf("frozen options changed: %+v %+v", binding, options)
	}
	s.Run.TurnID = "historical-turn"
	_, _, legacy := inspectionOptions(s)
	if string(legacy.OutputSchema) != string(s.Schema) {
		t.Fatal("legacy dispatched inspection contract lost")
	}
	s.Run.ResultProtocol = run.NaturalResultProtocol
	_, _, modern := inspectionOptions(s)
	if len(modern.OutputSchema) != 0 {
		t.Fatal("natural rejoin restored a schema")
	}
}

func inspectionReportFixture() (workset.RuntimeWorkset, InspectionReport) {
	w := workset.RuntimeWorkset{ProjectID: "project", InspectionOwnerTaskID: "inspection-task", InspectionOperationID: "probe", RunID: "run", ContentHash: "hash", Members: []workset.RuntimeMemberBinding{{RepositoryAlias: "app", IntendedReadScope: workset.ReadScope{Kind: "PATHS", Paths: []string{"src"}}}, {RepositoryAlias: "sdk", IntendedReadScope: workset.ReadScope{Kind: "REPOSITORY"}}}}
	r := InspectionReport{SchemaVersion: "project-inspection-v1", Source: InspectionSourceForWorkset(w), Status: "SUCCEEDED", Summary: "Repository-qualified observations", Findings: []InspectionFinding{{RepositoryAlias: "app", Path: "src/main.go", Summary: "Entry point"}}, RepositoryObservations: []InspectionRepositoryObservation{{RepositoryAlias: "app", Summary: "App", RelevantPaths: []string{"src/main.go"}}, {RepositoryAlias: "sdk", Summary: "SDK", RelevantPaths: []string{"README.md"}}}, BlockedReasons: []string{}, Relationships: []InspectionRelationship{{FromRepositoryAlias: "app", ToRepositoryAlias: "sdk", Summary: "Uses SDK"}}, RecommendedNextAction: "CREATE_FEATURE_AREA"}
	return w, r
}
func TestCanonicalInspectionReportStrictIdentityAndScope(t *testing.T) {
	w, r := inspectionReportFixture()
	raw, _ := json.Marshal(r)
	if _, err := ParseInspectionReport(raw, w); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*InspectionReport)
	}{
		{"wrong-owner", func(r *InspectionReport) { r.Source.InspectionOperationID = "other" }},
		{"missing-member", func(r *InspectionReport) { r.RepositoryObservations = r.RepositoryObservations[:1] }},
		{"unapproved-path", func(r *InspectionReport) { r.Findings[0].Path = "secrets/key" }},
		{"path-traversal", func(r *InspectionReport) { r.Findings[0].Path = "src/../key" }},
		{"unknown-repository", func(r *InspectionReport) { r.RepositoryObservations[0].RepositoryAlias = "other" }},
		{"missing-required-list", func(r *InspectionReport) { r.BlockedReasons = nil }},
		{"unsupported-status", func(r *InspectionReport) { r.Status = "IMPLEMENTATION_COMPLETE" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v InspectionReport
			_ = json.Unmarshal(raw, &v)
			tc.edit(&v)
			b, _ := json.Marshal(v)
			if _, err := ParseInspectionReport(b, w); err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
	for _, bad := range []string{"plain prose", string(raw) + "{}", strings.Replace(string(raw), `"summary":`, `"summary":"duplicate","summary":`, 1), strings.Replace(string(raw), `"schemaVersion":`, `"extra":true,"schemaVersion":`, 1)} {
		if _, err := ParseInspectionReport([]byte(bad), w); err == nil {
			t.Fatal("ambiguous report accepted")
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(InspectionReportSchema(InspectionSourceForWorkset(w)), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatal("schema not strict")
	}
}
func TestCanonicalInspectionObservationDetectsEveryMutation(t *testing.T) {
	b := recoverystatus.WorktreeObservation{RuntimeAlias: "app", RepositoryID: "repo", MemberID: "member", WorktreeID: "tree", CanonicalRoot: "/managed/root", RootIdentity: "root", HEAD: "head", Snapshot: "snapshot", DiffDigest: "diff", MetadataDigest: "metadata", Branch: "branch", ChangedPaths: []string{}, Available: true, State: "NO_CHANGE"}
	if InspectionObservationStatus([]recoverystatus.WorktreeObservation{b}, []recoverystatus.WorktreeObservation{b}) != "UNCHANGED" {
		t.Fatal("equal observations conflict")
	}
	for _, tc := range []struct {
		name string
		edit func(*recoverystatus.WorktreeObservation)
	}{
		{"source-file", func(o *recoverystatus.WorktreeObservation) { o.Snapshot = "changed" }},
		{"head", func(o *recoverystatus.WorktreeObservation) { o.HEAD = "changed" }},
		{"index-or-config", func(o *recoverystatus.WorktreeObservation) { o.MetadataDigest = "changed" }},
		{"branch", func(o *recoverystatus.WorktreeObservation) { o.Branch = "changed" }},
		{"root", func(o *recoverystatus.WorktreeObservation) { o.RootIdentity = "changed" }},
		{"missing", func(o *recoverystatus.WorktreeObservation) { o.Available = false }},
		{"member", func(o *recoverystatus.WorktreeObservation) { o.MemberID = "changed" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := b
			tc.edit(&a)
			if InspectionObservationStatus([]recoverystatus.WorktreeObservation{b}, []recoverystatus.WorktreeObservation{a}) != "SCOPE_CONFLICT" {
				t.Fatal("mutation ignored")
			}
		})
	}
}
