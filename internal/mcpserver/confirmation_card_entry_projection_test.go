package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	run "github.com/lazyun/devbridge/internal/run"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const liveContentHash = "9b0ddf87d658146faedc28cea9fc8b2f9b0ddf87d658146faedc28cea9fc8b2f"

// liveShapeReviewIntent reproduces the exact authoritative read of the live
// object: a complete, readable, committable project-scope REVIEW/REVISE intent
// with two repositories, no single repositoryId, one REVISE variant, full display
// facts and an empty (never committed) step set. Steps is deliberately the
// natural fresh-prepare shape: empty slice, not nil.
func liveShapeReviewIntent() c.Intent {
	return c.Intent{
		IntentID: "confirm_9b0ddf87d658146faedc28cea9fc8b2f", Version: 1,
		AISessionID: "session", ProjectID: "project", Purpose: c.Review, Action: "REVISE",
		State: "PENDING", ContentHash: liveContentHash, CanResume: false, CurrentUpperReadAvailable: false,
		Steps: []c.Step{},
		Content: c.PrepareInput{
			AISessionID: "session", RequestID: "prepare-request", Purpose: c.Review, Action: "REVISE",
			Variants: []c.Variant{{ID: "revise", Label: "Retry review", Summary: "Re-run the review with the requested changes."}},
			Review: &c.ReviewBody{
				TaskID: "task", FeatureAreaID: "area", ReviewID: "review", DecisionID: "decision",
				ReviewFeedback: "Please reconsider the boundary handling.",
				ReviewPlan:     run.ReviewPlan{SelectedModules: []run.ReviewModuleSelection{}},
			},
		},
	}
}

type showService struct {
	*StaticService
	intent  c.Intent
	err     error
	display *c.DisplayMetadata
}

func (s showService) ReadConfirmation(context.Context, c.ReadInput) (c.Intent, error) {
	if s.err != nil {
		return c.Intent{}, s.err
	}
	return s.intent, nil
}
func (s showService) ConfirmationDisplay(context.Context, c.Intent) *c.DisplayMetadata {
	return s.display
}

func TestPublicProjectionEmitsEmptyStepsArrayNotNil(t *testing.T) {
	for _, steps := range [][]c.Step{nil, {}} {
		intent := liveShapeReviewIntent()
		intent.Steps = steps
		projected := normalizePublicConfirmationResult(c.Result{Intent: &intent})
		raw, err := json.Marshal(projected)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		got, present := decoded["intent"].(map[string]any)["steps"]
		if !present {
			t.Fatalf("steps key missing for input steps=%#v in %s", steps, raw)
		}
		array, ok := got.([]any)
		if !ok || array == nil {
			t.Fatalf("steps must serialize as JSON array [], got %#v (%s)", got, raw)
		}
		if len(array) != 0 {
			t.Fatalf("steps must be empty, got %#v", array)
		}
		if _, invalid, err := publicConfirmationResultOrFallback(c.Result{Intent: &intent}); invalid || err != nil {
			t.Fatalf("canonical live-shape projection is schema-invalid for steps=%#v (err=%v)", steps, err)
		}
		if failure := ValidateConfirmationCardEntryProjection(projected); failure != nil {
			t.Fatalf("canonical live-shape entry rejected: %+v", failure)
		}
	}
}

func TestPublicSchemaCardCollectionsAreNonNullRequiredArrays(t *testing.T) {
	resolved := resolveSchema(t, confirmationResultSchema())
	base := liveShapeReviewIntent()
	valid, _, _ := publicConfirmationResultOrFallback(c.Result{Intent: &base})
	validRaw, _ := json.Marshal(valid)
	var validValue any
	if err := json.Unmarshal(validRaw, &validValue); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(validValue); err != nil {
		t.Fatalf("canonical live-shape result rejected by advertised SDK schema: %v", err)
	}
	// steps:null must now fail the advertised output schema.
	corrupted := validValue.(map[string]any)
	corrupted["intent"].(map[string]any)["steps"] = nil
	if err := resolved.Validate(corrupted); err == nil {
		t.Fatal("steps:null still satisfies the advertised output schema; expected array-only")
	}
}

func containsUserFeedback(message string) bool {
	lowered := strings.ToLower(message)
	return strings.Contains(lowered, "reconsider") || strings.Contains(lowered, "boundary handling")
}

// TestPublicProjectionEmitsEmptyOptionsCollectionsNotNil closes the same
// empty-collection-fold on the OPTIONS envelope that the live object proved for
// intent.steps. The Card validateOptions gate requires Array.isArray on
// projects/repositories/branches/variants, developmentContexts, each row.branches
// and runtimeOptions, so a nil folded to JSON null would block the readOptions()
// commit preflight just as steps:null blocked binding. A legal empty candidate set
// must project as [], never null.
func TestPublicProjectionEmitsEmptyOptionsCollectionsNotNil(t *testing.T) {
	withOptions := func(populated bool) map[string]any {
		dev := []c.DevelopmentContextOption{}
		if populated {
			dev = []c.DevelopmentContextOption{{RepositoryID: "repo", Branches: []c.Choice{{ID: "main", Label: "main"}}}}
		}
		options := c.Options{
			AISessionID: "session", IntentID: "confirm_9b0ddf87d658146faedc28cea9fc8b2f", Version: 1, Purpose: c.Review,
			ProjectID:           "project",
			RepositoryID:        "repo",
			DevelopmentContexts: dev,
		}
		if populated {
			options.Projects = []c.Choice{{ID: "p", Label: "P"}}
			options.Branches = []c.Choice{{ID: "b", Label: "B"}}
			options.Variants = []c.Choice{{ID: "revise", Label: "Retry"}}
		}
		// RuntimeOptions, Repositories, Projects, Branches, Variants left nil in the
		// non-populated case to prove the fold is gone.
		base := liveShapeReviewIntent()
		projected := normalizePublicConfirmationResult(c.Result{Intent: &base, Options: &options})
		raw, err := json.Marshal(projected)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		opts, ok := decoded["options"].(map[string]any)
		if !ok {
			t.Fatalf("options missing from projection: %s", raw)
		}
		for _, key := range []string{"projects", "repositories", "branches", "variants", "developmentContexts", "runtimeOptions"} {
			value, present := opts[key]
			if !present {
				t.Fatalf("options.%s key missing (must never be omitted or null): %s", key, raw)
			}
			array, isArray := value.([]any)
			if !isArray || array == nil {
				t.Fatalf("options.%s must serialize as a JSON array, got %#v in %s", key, value, raw)
			}
		}
		return opts
	}
	empty := withOptions(false)
	for _, key := range []string{"projects", "repositories", "branches", "variants", "developmentContexts", "runtimeOptions"} {
		if got := len(empty[key].([]any)); got != 0 {
			t.Fatalf("options.%s must be empty [], got len %d", key, got)
		}
	}
	populated := withOptions(true)
	row := populated["developmentContexts"].([]any)[0].(map[string]any)
	branches, isArray := row["branches"].([]any)
	if !isArray || branches == nil || len(branches) != 1 {
		t.Fatalf("row.branches must be a populated array of one, got %#v", row["branches"])
	}
	// A populated choices list is copied faithfully, not dropped.
	if populated["projects"].([]any)[0].(map[string]any)["id"] != "p" {
		t.Fatal("populated options.projects not preserved through normalization")
	}
}

// TestPublicSchemaOptionCollectionsAreArrayOnly proves the advertised OutputSchema
// rejects a null option collection instead of letting options.projects:null reach
// the Card validateOptions gate, and that a canonicalized server-legal options
// envelope (nil collections folded to []) still satisfies the array-only schema.
func TestPublicSchemaOptionCollectionsAreArrayOnly(t *testing.T) {
	resolved := resolveSchema(t, confirmationResultSchema())
	options := c.Options{
		AISessionID: "session", IntentID: "confirm_9b0ddf87d658146faedc28cea9fc8b2f", Version: 1, Purpose: c.Review,
		ProjectID:              "project",
		RuntimeInventorySource: "codex",
		Eligibility:            c.IntentEligibility{Available: true, AttemptState: "PENDING", ReceiptState: "NONE"},
	}
	// Every collection left nil to prove the canonicalizer + schema array-only pin.
	normalized := normalizePublicConfirmationResult(c.Result{Options: &options})
	raw, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	var canonical map[string]any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(canonical); err != nil {
		t.Fatalf("canonicalized options rejected by the advertised schema: %v (%s)", err, raw)
	}
	opts := canonical["options"].(map[string]any)
	for _, key := range []string{"projects", "repositories", "branches", "variants", "developmentContexts", "runtimeOptions"} {
		if array, ok := opts[key].([]any); !ok || array == nil {
			t.Fatalf("options.%s must canonicalize to a JSON array, got %#v", key, opts[key])
		}
	}
	// A null collection must violate the array-only advertised schema on every surface.
	for _, key := range []string{"projects", "repositories", "branches", "variants", "developmentContexts", "runtimeOptions", "backendCandidates"} {
		var corrupted map[string]any
		if err := json.Unmarshal(raw, &corrupted); err != nil {
			t.Fatal(err)
		}
		corrupted["options"].(map[string]any)[key] = nil
		if err := resolved.Validate(corrupted); err == nil {
			t.Fatalf("options.%s:null still satisfies the advertised output schema; expected array-only", key)
		}
	}
}

// TestSharedOptionsCanonicalizerMatchesMCPSurface proves MCP and the shared
// confirmation canonicalizer agree on the same option array shape, so the HTTP /
// Console / Workbench / Desktop surfaces (which call NormalizePublicOptions) and
// MCP cannot diverge on Card-required collections.
func TestSharedOptionsCanonicalizerMatchesMCPSurface(t *testing.T) {
	durable := c.Options{
		AISessionID: "session", IntentID: "confirm_x", Version: 1, Purpose: c.Review,
		RuntimeInventorySource: "codex",
		Eligibility:            c.IntentEligibility{Available: true, AttemptState: "PENDING", ReceiptState: "NONE"},
	}
	shared := durable
	c.CanonicalizePublicOptions(&shared)
	sharedRaw, err := json.Marshal(shared)
	if err != nil {
		t.Fatal(err)
	}
	mcpRaw, err := json.Marshal(normalizePublicConfirmationResult(c.Result{Options: &durable}).Options)
	if err != nil {
		t.Fatal(err)
	}
	// The only permitted difference is MCP display-layer failure-text bounding; the
	// array-presence shape (this task's canonicalization contract) must match.
	var sharedValue, mcpValue map[string]any
	json.Unmarshal(sharedRaw, &sharedValue)
	json.Unmarshal(mcpRaw, &mcpValue)
	for _, key := range []string{"projects", "repositories", "branches", "variants", "developmentContexts", "runtimeOptions"} {
		sharedArray, sharedOK := sharedValue[key].([]any)
		mcpArray, mcpOK := mcpValue[key].([]any)
		if !sharedOK || !mcpOK || sharedArray == nil || mcpArray == nil {
			t.Fatalf("options.%s array shape diverges: shared=%#v mcp=%#v", key, sharedValue[key], mcpValue[key])
		}
		if len(sharedArray) != len(mcpArray) {
			t.Fatalf("options.%s length diverges across surfaces: %d vs %d", key, len(sharedArray), len(mcpArray))
		}
	}
}

// TestSharedIntentCanonicalizerMatchesMCPSurface proves the shared non-MCP intent
// canonicalizer (NormalizePublicIntent, which HTTP/Console/Workbench/Desktop call)
// materializes Card-required empty collections identically to the MCP surface, so
// a zero-step / zero-variant intent projects as steps:[] / content.variants:[] on
// every surface and never as JSON null.
func TestSharedIntentCanonicalizerMatchesMCPSurface(t *testing.T) {
	buildIntent := func() *c.Intent {
		return &c.Intent{
			IntentID: "confirm_x", Version: 1, AISessionID: "session", ProjectID: "project",
			Purpose: c.Review, Action: "REVISE", State: "PENDING",
			ContentHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Content:     c.PrepareInput{AISessionID: "session", Purpose: c.Review, Action: "REVISE"},
		}
	}
	// Non-MCP surface: shared NormalizePublicIntent only.
	shared := buildIntent()
	c.NormalizePublicIntent(shared)
	sharedRaw, err := json.Marshal(shared)
	if err != nil {
		t.Fatal(err)
	}
	// MCP surface: the full public normalizer.
	mcpIntent := buildIntent()
	mcpRaw, err := json.Marshal(normalizePublicConfirmationResult(c.Result{Intent: mcpIntent}).Intent)
	if err != nil {
		t.Fatal(err)
	}
	var sharedValue, mcpValue map[string]any
	if err := json.Unmarshal(sharedRaw, &sharedValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mcpRaw, &mcpValue); err != nil {
		t.Fatal(err)
	}
	sharedSteps, sharedStepsOK := sharedValue["steps"].([]any)
	mcpSteps, mcpStepsOK := mcpValue["steps"].([]any)
	if !sharedStepsOK || sharedSteps == nil {
		t.Fatalf("non-MCP surface must serialize intent.steps as [], got %#v", sharedValue["steps"])
	}
	if !mcpStepsOK || mcpSteps == nil {
		t.Fatalf("MCP surface must serialize intent.steps as [], got %#v", mcpValue["steps"])
	}
	sharedVariants, sharedVariantsOK := sharedValue["content"].(map[string]any)["variants"].([]any)
	mcpVariants, mcpVariantsOK := mcpValue["content"].(map[string]any)["variants"].([]any)
	if !sharedVariantsOK || sharedVariants == nil || !mcpVariantsOK || mcpVariants == nil {
		t.Fatalf("content.variants array shape diverges across surfaces: shared=%#v mcp=%#v",
			sharedValue["content"], mcpValue["content"])
	}
}

// TestGoValidatorCanonicalCollectionParityTable has been removed: the Go mount
// gate's accept/reject decisions are now asserted against the shared contract
// vector file (see confirmation_contract_vectors_test.go), the single source of
// truth loaded by BOTH runtimes. A second hand-written Go table is forbidden.

func connectShowService(t *testing.T, service showService) (*sdkmcp.ClientSession, context.Context, context.CancelFunc) {
	t.Helper()
	base, err := NewStaticService(BridgeInfo{ApplicationName: "test", ApplicationVersion: "1", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	service.StaticService = base
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "show-mount", Version: "1"}, nil)
	RegisterConfirmationTools(server, service)
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "show-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, ctx, cancel
}

func showArguments() map[string]any {
	return map[string]any{"aiSessionId": "session", "intentId": "confirm_9b0ddf87d658146faedc28cea9fc8b2f", "version": 1, "purpose": "REVIEW", "contentHash": liveContentHash}
}

func metaResourceURI(meta sdkmcp.Meta) string {
	if ui, ok := meta["ui"].(map[string]any); ok {
		uri, _ := ui["resourceUri"].(string)
		return uri
	}
	return ""
}

// Scenario A + B + C + D of the Host mount gate, through the real MCP transport.
func TestConfirmationShowMountGate(t *testing.T) {
	t.Run("A complete canonical entry mounts the Card", func(t *testing.T) {
		session, ctx, cancel := connectShowService(t, showService{intent: liveShapeReviewIntent()})
		defer cancel()
		result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArguments()})
		if err != nil || result.IsError {
			t.Fatalf("show err=%v isError=%v", err, result.IsError)
		}
		if metaResourceURI(result.Meta) != confirmationResourceURI || result.Meta["emission"] != "CONFIRMATION" {
			t.Fatalf("Card not mounted for canonical entry: meta=%+v", result.Meta)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		var decoded map[string]any
		json.Unmarshal(raw, &decoded)
		if steps, ok := decoded["intent"].(map[string]any)["steps"].([]any); !ok || steps == nil || len(steps) != 0 {
			t.Fatalf("shown steps must be empty JSON array: %s", raw)
		}
	})
	t.Run("B invalid final entry does not mount and returns typed error", func(t *testing.T) {
		// A durable identity fact the canonicalizer must NOT paper over: a malformed
		// content hash. (An empty projectId is legitimate project-scope shape and is
		// accepted by the Card, so it cannot be used to prove the gate rejects.)
		broken := liveShapeReviewIntent()
		broken.ContentHash = "nope"
		session, ctx, cancel := connectShowService(t, showService{intent: broken})
		defer cancel()
		result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArguments()})
		if err != nil {
			t.Fatal(err)
		}
		if metaResourceURI(result.Meta) != "" || result.Meta["emission"] != nil {
			t.Fatalf("Card mounted for invalid entry: meta=%+v", result.Meta)
		}
		if !result.IsError {
			t.Fatal("invalid entry must return an error result")
		}
		raw, _ := json.Marshal(result.StructuredContent)
		var decoded map[string]any
		json.Unmarshal(raw, &decoded)
		if decoded["intent"] != nil {
			t.Fatalf("no intent must be exposed for a rejected entry: %s", raw)
		}
		failure, _ := decoded["failure"].(map[string]any)
		if failure == nil || failure["code"] != "CONFIRMATION_CARD_ENTRY_PROJECTION_INVALID" {
			t.Fatalf("expected typed projection error, got %s", raw)
		}
		if reason, _ := failure["reasonCode"].(string); reason != "CONTENT_HASH_INVALID" {
			t.Fatalf("reasonCode=%v", failure["reasonCode"])
		}
		if message, _ := failure["message"].(string); containsUserFeedback(message) {
			t.Fatalf("rejected entry message leaked user feedback: %s", message)
		}
	})
	t.Run("C confirmation_show business failure does not mount", func(t *testing.T) {
		session, ctx, cancel := connectShowService(t, showService{err: c.NewFailure("STORAGE_UNAVAILABLE", "STORAGE", "RECEIPT_READBACK", "read failed", c.ReadOriginalReceipt, nil)})
		defer cancel()
		result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArguments()})
		if err != nil {
			t.Fatal(err)
		}
		if metaResourceURI(result.Meta) != "" {
			t.Fatalf("Card mounted despite business failure: meta=%+v", result.Meta)
		}
	})
	t.Run("D display enrichment partial still mounts authoritative intent", func(t *testing.T) {
		display := &c.DisplayMetadata{AISessionID: "session", IntentID: "confirm_9b0ddf87d658146faedc28cea9fc8b2f", Version: 1, Purpose: c.Review, ProjectID: "project", Status: "PARTIAL", ObjectNameSource: "UNAVAILABLE", RepositoryCount: 2, TopologyRevision: 3}
		session, ctx, cancel := connectShowService(t, showService{intent: liveShapeReviewIntent(), display: display})
		defer cancel()
		result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArguments()})
		if err != nil || result.IsError {
			t.Fatalf("display partial must not block mount: err=%v isError=%v", err, result.IsError)
		}
		if metaResourceURI(result.Meta) != confirmationResourceURI {
			t.Fatalf("Card must mount despite partial display: meta=%+v", result.Meta)
		}
	})
}
