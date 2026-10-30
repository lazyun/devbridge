package formalapproval

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/task"
	"strings"
	"testing"
)

func TestFormalDraftParentIsExplicitAndCarriesNoExecutionAuthority(t *testing.T) {
	s := approvalSnapshot(t)
	old, _ := json.Marshal(s.Fence)
	if strings.Contains(string(old), "parentSchema") {
		t.Fatal("legacy v99 serialization changed")
	}
	draft := task.Definition{Version: 1, ProjectID: "project", FeatureAreaID: "area", WorkSpec: task.WorkSpec{Goal: "authored requirement", Constraints: []string{"original"}}}
	raw, hash, err := task.EncodeDefinition(draft)
	if err != nil {
		t.Fatal(err)
	}
	schema := DraftParentSchema()
	s.Fence.ParentSchema = &schema
	s.Fence.ParentDefinitionJSON = json.RawMessage(raw)
	s.Fence.ParentRevisionHash = hash
	if err = s.Validate(); err != nil {
		t.Fatalf("legitimate draft parent: %v", err)
	}
	if _, err = DecodeDefinition([]byte(raw), hash, s.Fence.Schema); err == nil {
		t.Fatal("draft became approved output")
	}
	if _, err = DecodeParentDefinition([]byte(raw), hash, nil, s.Fence.Schema); err == nil {
		t.Fatal("implicit legacy draft promoted")
	}
	for name, mutate := range map[string]func(*task.Definition){"approval": func(d *task.Definition) { d.ReviewApprovalID = "decision" }, "instruction": func(d *task.Definition) { d.WorkSpec.CodexInstruction = "execute" }, "executor": func(d *task.Definition) { d.WorkSpec.ImplementationMode = task.ImplementationCodingAgent }, "verification": func(d *task.Definition) { d.VerificationSpec.Mode = task.VerificationModeRun }} {
		t.Run(name, func(t *testing.T) {
			d := draft
			mutate(&d)
			r, h, e := task.EncodeDefinition(d)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = DecodeParentDefinition([]byte(r), h, &schema, s.Fence.Schema); e == nil {
				t.Fatal("draft authority accepted")
			}
		})
	}
}
