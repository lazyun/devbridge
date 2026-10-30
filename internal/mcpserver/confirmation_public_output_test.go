package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNormalizePublicConfirmationResultNormalizesNestedHistoricalFailures(t *testing.T) {
	empty := c.Failure{Code: "HISTORICAL_REJECTION", Source: "STORAGE", Stage: "BUSINESS_STEP", Message: "", NextAction: c.ReadOriginalReceipt}
	input := c.Result{
		Failure: &empty,
		Intent: &c.Intent{
			Submission: &c.Submission{Failure: &empty},
			Steps:      []c.Step{{Failure: &empty}},
		},
		Options: &c.Options{
			Variants: []c.Choice{{ID: "revise", Label: "Revise", Failures: []c.Failure{empty}}},
			DevelopmentContexts: []c.DevelopmentContextOption{{
				Branches: []c.Choice{{ID: "main", Label: "main", Failures: []c.Failure{empty}}},
			}},
		},
	}

	out := normalizePublicConfirmationResult(input)
	for _, failure := range []*c.Failure{
		out.Failure,
		out.Intent.Submission.Failure,
		out.Intent.Steps[0].Failure,
		&out.Options.Variants[0].Failures[0],
		&out.Options.DevelopmentContexts[0].Branches[0].Failures[0],
	} {
		if failure.Message == "" {
			t.Fatal("public failure message remains empty")
		}
		if failure.ReasonCode == "" {
			t.Fatal("public failure reason code remains empty")
		}
	}
	if input.Failure.Message != "" || input.Intent.Steps[0].Failure.Message != "" || input.Options.Variants[0].Failures[0].Message != "" {
		t.Fatal("public normalization mutated immutable source receipt facts")
	}
}

func TestConfirmationDisplayMetadataSchemaSupportsProjectScopedReviewWithoutRepositoryIdentity(t *testing.T) {
	metadata := map[string]any{
		"aiSessionId": "session", "intentId": "intent", "version": 1, "purpose": "REVIEW", "projectId": "project",
		"taskId": "task", "reviewId": "review", "decisionId": "decision", "featureAreaId": "area",
		"projectName": "Project", "taskName": "Task", "featureAreaName": "Area", "reviewOrdinal": 1,
		"repositoryCount": 2, "topologyRevision": 7, "decisionPurpose": "INVALID_REVIEW_RESULT_RESOLUTION",
		"presentationIssue": "DISPLAY_METADATA_PARTIAL", "status": "PARTIAL", "objectNameSource": "CURRENT_AUTHORIZED_OBJECT",
	}
	if err := resolveSchema(t, confirmationDisplayMetadataSchema()).Validate(metadata); err != nil {
		t.Fatalf("project-scoped review display is not schema-valid: %v", err)
	}
	if _, exists := metadata["repositoryId"]; exists {
		t.Fatal("project-scoped display must structurally omit repositoryId")
	}
}

func TestReviewPrepareSchemaRejectsCorePreparedStaleFacts(t *testing.T) {
	schema := resolveSchema(t, confirmationPrepareSchema(c.Review))
	input := map[string]any{
		"aiSessionId": "session", "requestId": "request", "purpose": "REVIEW", "action": "REVISE",
		"review":   map[string]any{"taskId": "task", "featureAreaId": "area", "reviewId": "review", "decisionId": "decision", "reviewFeedback": "Please retry."},
		"variants": []any{map[string]any{"id": "revise", "label": "Retry", "summary": "Retry the exact review"}},
	}
	if err := schema.Validate(input); err != nil {
		t.Fatalf("canonical re-review input rejected: %v", err)
	}
	input["review"].(map[string]any)["preparedTopologyRevision"] = 7
	if err := schema.Validate(input); err == nil {
		t.Fatal("client-injected prepared topology revision was accepted")
	}
}

type confirmationOutputSchemaService struct{ *StaticService }

func (confirmationOutputSchemaService) ReadConfirmation(context.Context, c.ReadInput) (c.Intent, error) {
	return c.Intent{}, c.NewFailure("HISTORICAL_REJECTION", "STORAGE", "RECEIPT_READBACK", "", c.ReadOriginalReceipt, nil)
}

func (confirmationOutputSchemaService) ConfirmationOptions(context.Context, c.OptionsInput) (c.Options, error) {
	return c.Options{}, c.NewFailure("VARIANT_NO_LONGER_AVAILABLE", "CORE", "CONFIRMATION_PREFLIGHT", "", c.PrepareNewVersion, nil)
}

func (confirmationOutputSchemaService) CommitConfirmation(context.Context, c.CommitInput) (c.Intent, error) {
	return c.Intent{}, c.NewFailure("PROJECT_TOPOLOGY_CHANGED", "CORE", "UNIQUE_CONSUMPTION", "", c.PrepareNewVersion, nil)
}

func TestConfirmationToolsReturnSchemaValidTypedFailuresForEmptyInternalMessages(t *testing.T) {
	base, err := NewStaticService(BridgeInfo{ApplicationName: "test", ApplicationVersion: "1", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "confirmation-output-contract", Version: "1"}, nil)
	RegisterConfirmationTools(server, confirmationOutputSchemaService{StaticService: base})
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "confirmation-output-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := make(map[string]*sdkmcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	read := map[string]any{"aiSessionId": "session", "intentId": "intent", "version": 1, "purpose": "REVIEW", "contentHash": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "requestId": "request"}
	commit := map[string]any{"aiSessionId": "session", "intentId": "intent", "version": 1, "purpose": "REVIEW", "requestId": "request", "action": "REVISE", "contentHash": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "variantId": "variant", "selection": map[string]any{}}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"confirmation_get", read},
		{"confirmation_show", read},
		{"confirmation_options_get", read},
		{"confirmation_commit", commit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var output any
			if err := json.Unmarshal(encoded, &output); err != nil {
				t.Fatal(err)
			}
			if err := resolveSchema(t, tools[tc.name].OutputSchema).Validate(output); err != nil {
				t.Fatalf("actual SDK advertised output schema rejected typed failure: %v; output=%s", err, encoded)
			}
			object := output.(map[string]any)
			failure := object["failure"].(map[string]any)
			if failure["message"] == "" {
				t.Fatal("empty internal failure message leaked to public tool output")
			}
		})
	}
}
