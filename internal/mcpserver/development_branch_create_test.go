package mcpserver

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDevelopmentBranchCreateSchemaIsClosedAndHasNoRequestReceipt(t *testing.T) {
	var schema struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal([]byte(DevelopmentBranchCreateInputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.AdditionalProperties || schema.Properties["requestId"] != nil || !reflect.DeepEqual(schema.Required, []string{"aiSessionId", "repositoryId", "developmentRef", "baseCommit"}) {
		t.Fatalf("create schema=%+v", schema)
	}
	if schema.Properties["projectId"] != nil || schema.Properties["checkout"] != nil || schema.Properties["select"] != nil {
		t.Fatalf("create schema leaks selection or checkout=%+v", schema.Properties)
	}
}
