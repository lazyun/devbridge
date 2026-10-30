package controlapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/mcpserver"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The replacement-intent Card lifecycle enters through the production ChatGPT MCP
// adapter: the real SQLite confirmation store, the actual prepare/get/options/show
// tools, every receiving middleware (including the presentation middleware that
// owns Card resource emission) and the embedded Card resource. No Card internal
// function and no facade shortcut is called from here.
type cardLifecycleStack struct {
	t        *testing.T
	fixture  *upperLifecycleFixture
	ctx      context.Context
	session  *sdkmcp.ClientSession
	sequence int
	lastRaw  string
}

func newCardLifecycleStack(t *testing.T) *cardLifecycleStack {
	t.Helper()
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	submitLifecycleApprovalReady(t, f, "approval-for-"+shortCardTestName(t))
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "card-instance-lifecycle-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := mcpserver.New(mcpserver.Config{}, service)
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(adapter.Handler())
	t.Cleanup(host.Close)
	ctx, cancel := context.WithTimeout(f.ctx, 60*time.Second)
	t.Cleanup(cancel)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "card-instance-lifecycle-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: host.URL + mcpserver.MCPPath, HTTPClient: host.Client(), DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &cardLifecycleStack{t: t, fixture: f, ctx: ctx, session: session}
}

func shortCardTestName(t *testing.T) string {
	name := t.Name()
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(strings.TrimPrefix(name, "test"))
}

func (s *cardLifecycleStack) nextID(prefix string) string {
	s.sequence++
	return fmt.Sprintf("%s-%s-%d", prefix, shortCardTestName(s.t), s.sequence)
}

// call is the raw transport boundary: it returns the actual SDK result, so the
// test can assert Card resource emission and identity meta, not only the payload.
func (s *cardLifecycleStack) call(name string, arguments map[string]any) (*sdkmcp.CallToolResult, c.Result) {
	s.t.Helper()
	result, err := s.session.CallTool(s.ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		s.t.Fatalf("%s transport error: %v", name, err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		s.t.Fatalf("%s structured content: %v", name, err)
	}
	contents, _ := json.Marshal(result.Content)
	s.lastRaw = string(raw) + " content=" + string(contents) + " isError=" + fmt.Sprint(result.IsError)
	var out c.Result
	if err := json.Unmarshal(raw, &out); err != nil {
		s.t.Fatalf("%s envelope %s: %v", name, raw, err)
	}
	return result, out
}

// prepareReviewDecision enters one pending Review-decision confirmation through
// review_prepare. Replacements use the formal stale-source writer, preserving
// the original identity and atomically superseding it with a replacement receipt.
//
// The arguments are built by hand rather than marshalled from c.PrepareInput: the
// advertised REVISE wire contract forbids the plan fields ReviewDepth and
// ReviewPlan, which a Go struct marshal would emit as their zero values.
func (s *cardLifecycleStack) prepareReviewDecision(requestID, sourceIntentID string) c.Intent {
	s.t.Helper()
	arguments := map[string]any{
		"aiSessionId": s.fixture.session,
		"requestId":   requestID,
		"purpose":     string(c.Review),
		"action":      "REVISE",
		"review": map[string]any{
			"taskId":         s.fixture.task,
			"featureAreaId":  s.fixture.area,
			"reviewId":       s.fixture.review,
			"decisionId":     wp.DecisionID(s.fixture.review),
			"reviewFeedback": "Re-review the exact formal result and address its unresolved findings.",
		},
		"variants": []any{map[string]any{
			"id":      "revise",
			"label":   "同一任务重新审查",
			"summary": "Re-review the same Task after the revised decision",
		}},
	}
	tool := "review_prepare"
	if sourceIntentID != "" {
		source, err := s.fixture.plane.Confirmations.Get(s.fixture.ctx, s.fixture.session, sourceIntentID)
		if err != nil {
			s.t.Fatal(err)
		}
		s.fixture.appendTopologyMember(s.t, s.nextID("replacement-member"))
		tool = "confirmation_reprepare"
		arguments = map[string]any{"aiSessionId": s.fixture.session, "sourceIntentId": source.IntentID, "sourceVersion": source.Version, "sourceContentHash": source.ContentHash, "requestId": requestID}
	}
	_, result := s.call(tool, arguments)
	if result.Intent == nil {
		s.t.Fatalf("review_prepare(%s) returned no intent: %s", requestID, s.lastRaw)
	}
	return *result.Intent
}

// prepareReplacement prepares the successor intent for a still-pending prior one
// and returns both intents as the durable store reads them after the replacement
// transaction has committed.
func (s *cardLifecycleStack) prepareReplacement() (c.Intent, c.Intent) {
	s.t.Helper()
	prior := s.prepareReviewDecision(s.nextID("prepare-prior"), "")
	replacement := s.prepareReviewDecision(s.nextID("prepare-replacement"), prior.IntentID)
	_, priorRead := s.read(prior, "")
	_, replacementRead := s.read(replacement, "")
	if priorRead.Intent == nil || replacementRead.Intent == nil {
		s.t.Fatalf("replacement pair not readable after prepare: prior=%s replacement=%s", s.lastRaw, jsonCard(replacementRead.Intent))
	}
	return *priorRead.Intent, *replacementRead.Intent
}

// show, read and options all address one exact intent. The public read identity is
// (aiSessionId, intentId, version, purpose, contentHash); they take the whole intent
// so a lifecycle probe can never be expressed as a hash-free lookup (§7).
func (s *cardLifecycleStack) show(intent c.Intent, presentationHandle string) (*sdkmcp.CallToolResult, c.Result) {
	s.t.Helper()
	arguments := map[string]any{"aiSessionId": s.fixture.session, "intentId": intent.IntentID, "version": intent.Version, "purpose": string(intent.Purpose), "contentHash": intent.ContentHash}
	if presentationHandle != "" {
		arguments["requestId"] = presentationHandle
	}
	return s.call("confirmation_show", arguments)
}

func (s *cardLifecycleStack) read(intent c.Intent, requestID string) (*sdkmcp.CallToolResult, c.Result) {
	s.t.Helper()
	arguments := map[string]any{"aiSessionId": s.fixture.session, "intentId": intent.IntentID, "version": intent.Version, "purpose": string(intent.Purpose), "contentHash": intent.ContentHash}
	if requestID != "" {
		arguments["requestId"] = requestID
	}
	return s.call("confirmation_get", arguments)
}

func (s *cardLifecycleStack) options(intent c.Intent) (*sdkmcp.CallToolResult, c.Result) {
	s.t.Helper()
	return s.call("confirmation_options_get", map[string]any{"aiSessionId": s.fixture.session, "intentId": intent.IntentID, "version": intent.Version, "purpose": string(intent.Purpose), "contentHash": intent.ContentHash})
}

func (s *cardLifecycleStack) commit(intent c.Intent, requestID string) (*sdkmcp.CallToolResult, c.Result) {
	s.t.Helper()
	return s.call("confirmation_commit", map[string]any{
		"aiSessionId": intent.AISessionID, "intentId": intent.IntentID, "version": intent.Version,
		"purpose": string(intent.Purpose), "action": intent.Action, "requestId": requestID,
		"contentHash": intent.ContentHash, "variantId": "revise", "selection": map[string]any{},
	})
}

// countRows reads one scalar from the fixture database. Every lifecycle assertion
// about "no extra object" goes through the durable rows, never a response field.
func (s *cardLifecycleStack) countRows(query string, args ...any) int {
	s.t.Helper()
	var count int
	if err := s.fixture.plane.DB.SQL().QueryRow(query, args...).Scan(&count); err != nil {
		s.t.Fatalf("count %q: %v", query, err)
	}
	return count
}

func (s *cardLifecycleStack) scalar(query string, args ...any) string {
	s.t.Helper()
	var value string
	if err := s.fixture.plane.DB.SQL().QueryRow(query, args...).Scan(&value); err != nil {
		s.t.Fatalf("scalar %q: %v", query, err)
	}
	return value
}

// optionalScalar reads a nullable column, reporting the empty string when absent.
func (s *cardLifecycleStack) optionalScalar(query string, args ...any) string {
	s.t.Helper()
	var value *string
	if err := s.fixture.plane.DB.SQL().QueryRow(query, args...).Scan(&value); err != nil {
		s.t.Fatalf("optional scalar %q: %v", query, err)
	}
	if value == nil {
		return ""
	}
	return *value
}

func metaString(result *sdkmcp.CallToolResult, key string) string {
	if result == nil || result.Meta == nil {
		return ""
	}
	value, _ := result.Meta[key].(string)
	return value
}

func metaNumber(result *sdkmcp.CallToolResult, key string) float64 {
	if result == nil || result.Meta == nil {
		return 0
	}
	value, _ := result.Meta[key].(float64)
	return value
}

func cardResourceURI(result *sdkmcp.CallToolResult) string {
	if result == nil || result.Meta == nil {
		return ""
	}
	ui, _ := result.Meta["ui"].(map[string]any)
	value, _ := ui["resourceUri"].(string)
	return value
}

func outputTemplate(result *sdkmcp.CallToolResult) string {
	if result == nil || result.Meta == nil {
		return ""
	}
	value, _ := result.Meta["openai/outputTemplate"].(string)
	return value
}

// derivedInstanceID recomputes the §4 instance identity from frozen intent fields
// only, independently of the server helper.
func derivedInstanceID(intent c.Intent) string {
	return c.ValueHash([]any{intent.AISessionID, intent.IntentID, intent.Version, intent.ContentHash})
}

func intentState(result c.Result) string {
	if result.Intent == nil {
		return "<no intent>"
	}
	return result.Intent.State
}

func requireMountedCard(t *testing.T, result *sdkmcp.CallToolResult, intent c.Intent) {
	t.Helper()
	if result.IsError {
		t.Fatalf("show(%s) was rejected instead of mounted: %s", intent.IntentID, result.Content)
	}
	if uri := cardResourceURI(result); uri != confirmationCardResourceURI {
		t.Fatalf("show(%s) attached Card resource %q", intent.IntentID, uri)
	}
	if template := outputTemplate(result); template != confirmationCardResourceURI {
		t.Fatalf("show(%s) output template %q != static resource", intent.IntentID, template)
	}
	if got := metaString(result, "devbridge/intentId"); got != intent.IntentID {
		t.Fatalf("show(%s) meta intentId %q", intent.IntentID, got)
	}
	if got := metaNumber(result, "devbridge/version"); got != float64(intent.Version) {
		t.Fatalf("show(%s) meta version %v", intent.IntentID, got)
	}
	if got := metaString(result, "devbridge/contentHash"); got != intent.ContentHash {
		t.Fatalf("show(%s) meta contentHash %q", intent.IntentID, got)
	}
	if got := metaString(result, "devbridge/aiSessionId"); got != intent.AISessionID {
		t.Fatalf("show(%s) meta aiSessionId %q", intent.IntentID, got)
	}
	if got, want := metaString(result, "devbridge/presentationInstanceId"), derivedInstanceID(intent); got != want {
		t.Fatalf("show(%s) presentationInstanceId %q != derived-from-frozen-identity %q", intent.IntentID, got, want)
	}
}

const confirmationCardResourceURI = "ui://devbridge/confirmation/v14"

// rawEnvelope returns the exact public structuredContent the Host received, so a
// captured fixture is byte-identical to what the Card decodes.
func rawEnvelope(result *sdkmcp.CallToolResult) map[string]any {
	if result == nil || result.StructuredContent == nil {
		return nil
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return nil
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	return envelope
}

// captureCardInstanceFixtures writes the two production show envelopes, their
// options envelopes and their identity meta to DEVBRIDGE_CARD_INSTANCE_FIXTURE_DIR
// for check-in as the shared Host/Card instance-lifecycle fixture. Same
// env-gated evidence convention as the startup wire test; a normal run writes
// nothing.
func captureCardInstanceFixtures(t *testing.T, supersededShow, replacementShow *sdkmcp.CallToolResult, supersededOptions, replacementOptions *sdkmcp.CallToolResult, superseded, replacement c.Intent) {
	t.Helper()
	dir := os.Getenv("DEVBRIDGE_CARD_INSTANCE_FIXTURE_DIR")
	if dir == "" {
		return
	}
	instance := func(show *sdkmcp.CallToolResult, options *sdkmcp.CallToolResult, intent c.Intent) map[string]any {
		return map[string]any{
			"entry": rawEnvelope(show),
			"options": map[string]any{
				"entry": rawEnvelope(options),
			},
			"meta": map[string]any{
				"resourceUri":            cardResourceURI(show),
				"outputTemplate":         outputTemplate(show),
				"aiSessionId":            metaString(show, "devbridge/aiSessionId"),
				"intentId":               metaString(show, "devbridge/intentId"),
				"version":                metaNumber(show, "devbridge/version"),
				"contentHash":            metaString(show, "devbridge/contentHash"),
				"presentationInstanceId": metaString(show, "devbridge/presentationInstanceId"),
				"componentVersion":       metaString(show, "devbridge/componentVersion"),
				"mainScriptSHA256":       metaString(show, "devbridge/mainScriptSHA256"),
			},
			"state":                    intent.State,
			"intentId":                 intent.IntentID,
			"version":                  intent.Version,
			"contentHash":              intent.ContentHash,
			"presentationInstanceId":   derivedInstanceID(intent),
			"expectCommittableVariant": "",
		}
	}
	supersededCard := instance(supersededShow, supersededOptions, superseded)
	supersededCard["expectCommittableVariant"] = ""
	replacementCard := instance(replacementShow, replacementOptions, replacement)
	replacementCard["expectCommittableVariant"] = "revise"
	file := map[string]any{
		"_meta": map[string]any{
			"schema":               "devbridge/confirmation-card-instance-lifecycle/v5",
			"singleSourceOfTruth":  "These envelopes are the exact production structuredContent and _meta of confirmation_show and confirmation_options_get, captured over the real Streamable HTTP MCP wire, the production middleware stack and the real SQLite store. The Go mount-gate guard and the Card JavaScript instance-lifecycle replay load this SAME file. Never hand-edit an envelope; re-capture with DEVBRIDGE_CARD_INSTANCE_FIXTURE_DIR.",
			"capturedBy":           "internal/controlapi/confirmation_card_instance_lifecycle_test.go",
			"staticResourceUri":    confirmationCardResourceURI,
			"instanceIdentityRule": "presentationInstanceId = canonicalHash([aiSessionId, intentId, version, contentHash]); lineage (sourceIntentId) is never part of Card instance identity.",
			"pair":                 "superseded is the prior Review-decision intent replaced by sourceIntentId; replacement is the new PENDING intent for the same Task/Review/Decision.",
		},
		"superseded":  supersededCard,
		"replacement": replacementCard,
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "confirmation_card_instance_lifecycle_v5.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured shared Card instance fixture at %s", path)
}

// G1, G2, G3, G4, G6, G8, G9: after one replacement prepare, the old intent stays
// a SUPERSEDED Card and the new intent mounts its own PENDING Card on the same
// static resource, each carrying its own frozen identity and its own state.
func TestConfirmationReplacementIntentMountsItsOwnCardInstance(t *testing.T) {
	s := newCardLifecycleStack(t)
	prior, replacement := s.prepareReplacement()
	if prior.State != "SUPERSEDED" {
		t.Fatalf("prior intent state=%s after replacement prepare", prior.State)
	}
	if replacement.State != "PENDING" {
		t.Fatalf("replacement intent state=%s", replacement.State)
	}
	if replacement.Content.SourceIntentID != prior.IntentID {
		t.Fatalf("replacement lineage=%q, want prior %q", replacement.Content.SourceIntentID, prior.IntentID)
	}
	if replacement.IntentID == prior.IntentID || replacement.ContentHash == prior.ContentHash {
		t.Fatalf("replacement reused prior identity: %+v %+v", prior, replacement)
	}

	// §12 baseline: the superseded row as the replacement prepare left it.
	priorRaw := s.scalar(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, prior.IntentID)

	priorShowResult, priorShow := s.show(prior, "")
	replacementShowResult, replacementShow := s.show(replacement, "")
	requireMountedCard(t, priorShowResult, *priorShow.Intent)
	requireMountedCard(t, replacementShowResult, *replacementShow.Intent)

	if priorShow.Intent.State != "SUPERSEDED" {
		t.Fatalf("G3 prior Card state=%s", priorShow.Intent.State)
	}
	if replacementShow.Intent.State != "PENDING" {
		t.Fatalf("G4 replacement Card state=%s", replacementShow.Intent.State)
	}
	if len(replacementShow.Intent.Steps) != 0 || replacementShow.Intent.Submission != nil {
		t.Fatalf("G6 replacement Card carries prior/commit state: steps=%d submission=%s", len(replacementShow.Intent.Steps), jsonCard(replacementShow.Intent.Submission))
	}
	if priorShow.Intent.Submission != nil {
		t.Fatalf("G6 prior Card gained a submission from the replacement flow: %s", jsonCard(priorShow.Intent.Submission))
	}
	// G2: one shared static resource, two distinct business instances.
	if cardResourceURI(priorShowResult) != cardResourceURI(replacementShowResult) {
		t.Fatalf("G2 two Card resources for one component version")
	}
	if priorInstance, replacementInstance := metaString(priorShowResult, "devbridge/presentationInstanceId"), metaString(replacementShowResult, "devbridge/presentationInstanceId"); priorInstance == replacementInstance {
		t.Fatalf("G2 both intents derived the same presentation instance %q", priorInstance)
	}
	// G4: the replacement Card offers the same-Task re-review; G3: the old one does not.
	priorOptionsResult, priorOptions := s.options(prior)
	replacementOptionsResult, replacementOptions := s.options(replacement)
	captureCardInstanceFixtures(t, priorShowResult, replacementShowResult, priorOptionsResult, replacementOptionsResult, prior, replacement)
	if priorOptionsResult.IsError {
		t.Fatalf("G3 superseded options read became an error instead of closed capability: %s", s.lastRaw)
	}
	if replacementOptionsResult.IsError || replacementOptions.Options == nil {
		t.Fatalf("G4 replacement options rejected: %s", s.lastRaw)
	}
	if !replacementOptions.Options.Eligibility.Available {
		t.Fatalf("G4 replacement Card shows no confirm action: eligibility=%+v", replacementOptions.Options.Eligibility)
	}
	if !hasAvailableVariant(replacementOptions.Options.Variants, "revise") {
		t.Fatalf("G4 replacement Card has no available REVISE variant: %s", jsonCard(replacementOptions.Options.Variants))
	}
	if priorOptions.Options == nil || priorOptions.Options.Eligibility.Available {
		t.Fatalf("G3 superseded Card still presents a confirm action: %+v", priorOptions.Options)
	}
	if hasAvailableVariant(priorOptions.Options.Variants, "revise") {
		t.Fatalf("G3 superseded Card exposes an available REVISE variant")
	}
	// The display of each Card names its own intent and the same Task/Review.
	if priorShow.Display == nil || replacementShow.Display == nil {
		t.Fatalf("missing live display: prior=%s replacement=%s", jsonCard(priorShow.Display), jsonCard(replacementShow.Display))
	}
	if priorShow.Display.IntentID != prior.IntentID || replacementShow.Display.IntentID != replacement.IntentID {
		t.Fatalf("display identity crossed intents: %s / %s", priorShow.Display.IntentID, replacementShow.Display.IntentID)
	}
	if priorShow.Display.TaskID != s.fixture.task || replacementShow.Display.TaskID != s.fixture.task {
		t.Fatalf("replacement display left the original Task: %s vs %s", priorShow.Display.TaskID, replacementShow.Display.TaskID)
	}
	if priorShow.Display.ReviewID != s.fixture.review || replacementShow.Display.ReviewID != s.fixture.review || replacementShow.Display.DecisionID != wp.DecisionID(s.fixture.review) {
		t.Fatalf("replacement display moved to another Review/Decision")
	}

	// G9: re-showing the same identity is idempotent and creates nothing.
	before := s.countRows(`SELECT COUNT(*) FROM confirmation_intents`)
	repeatResult, repeat := s.show(replacement, "")
	requireMountedCard(t, repeatResult, *repeat.Intent)
	if got := metaString(repeatResult, "devbridge/presentationInstanceId"); got != metaString(replacementShowResult, "devbridge/presentationInstanceId") {
		t.Fatalf("G9 same-intent refresh produced a new presentation instance")
	}
	if repeat.Intent.State != "PENDING" || repeat.Intent.ContentHash != replacement.ContentHash {
		t.Fatalf("G9 same-intent refresh changed frozen state: %s", jsonCard(repeat.Intent))
	}
	if after := s.countRows(`SELECT COUNT(*) FROM confirmation_intents`); after != before {
		t.Fatalf("G9 same-intent refresh created another confirmation: %d -> %d", before, after)
	}
	// G8: an old Card is never rebound - the prior intent still reads SUPERSEDED
	// after the replacement has been shown.
	lateMeta, lateEnvelope := s.show(prior, "")
	if lateEnvelope.Intent.State != "SUPERSEDED" {
		t.Fatalf("G8 prior Card entry was rebound to the replacement: %s", jsonCard(lateEnvelope.Intent))
	}
	requireMountedCard(t, lateMeta, *lateEnvelope.Intent)

	// §12: the old intent is immutable history. Any later change in this pair may only
	// update the replacement Card; it must not rewrite A and must not conjure a third
	// confirmation without an explicit prepare call.
	if a, b := jsonCard(priorShow.Intent), jsonCard(lateEnvelope.Intent); a != b {
		t.Fatalf("§12 the superseded intent was rewritten by the replacement flow:\nbefore=%safter=%s", a, b)
	}
	if priorShow.Intent.ContentHash != lateEnvelope.Intent.ContentHash || priorShow.Display.IntentID != lateEnvelope.Display.IntentID {
		t.Fatalf("§12 the superseded Card's frozen identity or display moved")
	}
	if total := s.countRows(`SELECT COUNT(*) FROM confirmation_intents WHERE intent_id IN (?,?)`, prior.IntentID, replacement.IntentID); total != 2 {
		t.Fatalf("§12 a third confirmation appeared in this pair without an explicit prepare: %d", total)
	}
	if a := s.scalar(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, prior.IntentID); a != priorRaw {
		t.Fatalf("§12 the superseded intent row was rewritten:\nbefore=%safter=%s", priorRaw, a)
	}
}

func hasAvailableVariant(variants []c.Choice, id string) bool {
	for _, variant := range variants {
		if variant.ID == id {
			return variant.Available
		}
	}
	return false
}

func jsonCard(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("<unmarshalable: %v>", err)
	}
	return string(raw)
}

// G7: one presentation handle may not carry two different intents. The Host is
// refused a mis-reuse instead of silently receiving B inside A's Card, and
// neither intent loses anything.
func TestConfirmationShowRefusesCrossIntentPresentationHandleReuse(t *testing.T) {
	s := newCardLifecycleStack(t)
	prior, replacement := s.prepareReplacement()
	const handle = "card-handle-shared"
	firstResult, first := s.show(prior, handle)
	requireMountedCard(t, firstResult, *first.Intent)

	rejectedResult, rejected := s.show(replacement, handle)
	if !rejectedResult.IsError || rejected.Intent != nil || rejected.Failure == nil {
		t.Fatalf("G7 cross-intent handle reuse was not rejected: %s", s.lastRaw)
	}
	if rejected.Failure.Code != "CONFIRMATION_CARD_INSTANCE_REUSE_REJECTED" {
		t.Fatalf("G7 rejection code=%s reason=%s", rejected.Failure.Code, rejected.Failure.ReasonCode)
	}
	if uri := cardResourceURI(rejectedResult); uri != "" {
		t.Fatalf("G7 rejected show still attached Card resource %q", uri)
	}
	if template := outputTemplate(rejectedResult); template != "" {
		t.Fatalf("G7 rejected show still claimed output template %q", template)
	}
	// A keeps its own Card, B is not lost: an independent handle mounts it.
	if priorMeta, priorEnvelope := s.show(prior, handle); priorEnvelope.Intent.State != "SUPERSEDED" {
		t.Fatalf("G7 rejected reuse damaged prior Card: %s", jsonCard(priorEnvelope.Intent))
	} else {
		requireMountedCard(t, priorMeta, *priorEnvelope.Intent)
	}
	independentMeta, independent := s.show(replacement, "card-handle-independent")
	requireMountedCard(t, independentMeta, *independent.Intent)
	if independent.Intent.State != "PENDING" {
		t.Fatalf("G7 replacement intent lost after a rejected reuse: %s", jsonCard(independent.Intent))
	}
	if got, want := metaString(independentMeta, "devbridge/presentationInstanceId"), derivedInstanceID(*independent.Intent); got != want {
		t.Fatalf("G7 independent mount identity %q != %q", got, want)
	}
	if count := s.countRows(`SELECT COUNT(*) FROM confirmation_intents WHERE intent_id IN (?,?)`, prior.IntentID, replacement.IntentID); count != 2 {
		t.Fatalf("G7 reuse rejection changed the durable pair count to %d", count)
	}
}

// G5, §6: sourceIntentId is lineage data. Every current operation resolves on the
// intent's own identity, and a superseded identity can neither read nor consume
// the replacement's state.
func TestConfirmationSourceIntentIdIsLineageOnly(t *testing.T) {
	s := newCardLifecycleStack(t)
	prior, replacement := s.prepareReplacement()

	// Reads address the target intent by its own id; the lineage field never
	// redirects a read to the replacement.
	_, priorRead := s.read(prior, "")
	if priorRead.Intent == nil || priorRead.Intent.IntentID != prior.IntentID {
		t.Fatalf("§6 read of the prior intent returned another intent: %s", jsonCard(priorRead.Intent))
	}
	if priorRead.Intent.Content.SourceIntentID != "" {
		t.Fatalf("prior intent reported replacement lineage: %q", priorRead.Intent.Content.SourceIntentID)
	}
	_, replacementRead := s.read(replacement, "")
	if replacementRead.Intent == nil || replacementRead.Intent.IntentID != replacement.IntentID {
		t.Fatalf("§6 read of the replacement intent failed: %s", s.lastRaw)
	}
	if replacementRead.Intent.Content.RequestID == priorRead.Intent.Content.RequestID {
		t.Fatalf("§6 replacement intent carried the prior prepare request: %q", replacementRead.Intent.Content.RequestID)
	}
	// The lineage field is the only place A's id appears in B's entry, and B's
	// identity fields are B's own.
	entry := replacementRead.Intent
	if entry.AISessionID != replacement.AISessionID || entry.Version != replacement.Version || entry.ContentHash != replacement.ContentHash {
		t.Fatalf("§6 replacement entry identity drifted: %s", jsonCard(entry))
	}
	// The prior intent cannot consume the replacement: its options are closed and
	// a commit addressed to it is rejected without touching B or the Task.
	rejectedCommit, rejectedEnvelope := s.commit(prior, s.nextID("commit-prior"))
	if !rejectedCommit.IsError || rejectedEnvelope.Failure == nil {
		t.Fatalf("§6 superseded intent was committable: %s", s.lastRaw)
	}
	if rejectedEnvelope.Intent != nil && rejectedEnvelope.Intent.State == "COMPLETED" {
		t.Fatalf("§6 superseded commit reported completion: %s", jsonCard(rejectedEnvelope.Intent))
	}
	if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != 1 {
		t.Fatalf("§6 superseded commit created a successor Review: %d", reviews)
	}
	if runs := s.countRows(`SELECT COUNT(*) FROM runs WHERE task_id=?`, s.fixture.task); runs != 1 {
		t.Fatalf("§6 superseded commit created a successor Run: %d", runs)
	}
	if current := s.optionalScalar(`SELECT current_review_id FROM tasks WHERE id=?`, s.fixture.task); current != s.fixture.review {
		t.Fatalf("§6 superseded commit moved Task.currentReviewId to %q", current)
	}
	if _, still := s.read(replacement, ""); still.Intent == nil || still.Intent.State != "PENDING" || still.Intent.Submission != nil {
		t.Fatalf("§6 prior commit leaked into the replacement Card: %s", s.lastRaw)
	}
	// A's contentHash cannot commit B: the commit must carry B's own hash.
	misbound := prior
	misbound.IntentID = replacement.IntentID
	misboundResult, misboundEnvelope := s.commit(misbound, s.nextID("commit-misbound"))
	if !misboundResult.IsError || misboundEnvelope.Failure == nil {
		t.Fatalf("§6 prior contentHash committed the replacement intent: %s", s.lastRaw)
	}
	if _, still := s.read(replacement, ""); still.Intent.State != "PENDING" {
		t.Fatalf("§6 misbound commit consumed the replacement: %s", jsonCard(still.Intent))
	}
	// A receipt only ever reads back inside its own intent. The prior intent's
	// prepare request cannot surface a recorded receipt on the replacement Card:
	// the commit-request ledger is keyed by (Session, request) and cross-checked
	// against the requested intent, so the most a foreign request can produce is
	// an unrecorded UNKNOWN marker that echoes the requested identity.
	priorPrepareRequest := priorRead.Intent.Content.RequestID
	if _, leaked := s.read(replacement, priorPrepareRequest); leaked.Intent != nil && leaked.Intent.Submission != nil {
		submission := leaked.Intent.Submission
		if submission.Request.IntentID != replacement.IntentID {
			t.Fatalf("§6 foreign request %q surfaced another intent's receipt: %s", priorPrepareRequest, jsonCard(submission))
		}
		if submission.Recorded || submission.Outcome != c.Unknown {
			t.Fatalf("§6 foreign request %q surfaced a recorded receipt: %s", priorPrepareRequest, jsonCard(submission))
		}
	}
	if _, own := s.read(prior, priorPrepareRequest); own.Intent != nil && own.Intent.Submission != nil && own.Intent.Submission.Recorded {
		t.Fatalf("§6 a prepare request surfaced as a recorded commit receipt: %s", jsonCard(own.Intent.Submission))
	}
}

// G10, G11, G12, G13: prepare only readies a confirmation; the successor Review,
// Run and start authority appear exactly once, and only after commit B. A late
// old-Card refresh cannot disturb them, and a lost commit response replays against
// the original request instead of submitting twice.
func TestConfirmationReplacementSuccessorReviewAppearsOnlyAfterCommit(t *testing.T) {
	s := newCardLifecycleStack(t)
	prior, replacement := s.prepareReplacement()

	// Pre-commit: the prepare stage may claim nothing but a pending confirmation.
	if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != 1 {
		t.Fatalf("G10 successor Review existed before commit: %d", reviews)
	}
	if runs := s.countRows(`SELECT COUNT(*) FROM runs WHERE task_id=?`, s.fixture.task); runs != 1 {
		t.Fatalf("G10 successor Run existed before commit: %d", runs)
	}
	if current := s.optionalScalar(`SELECT current_review_id FROM tasks WHERE id=?`, s.fixture.task); current != s.fixture.review {
		t.Fatalf("G10 Task.currentReviewId moved before commit: %q", current)
	}
	if starts := s.countRows(`SELECT COUNT(*) FROM feature_area_review_start_requests WHERE review_id<>?`, s.fixture.review); starts != 0 {
		t.Fatalf("G10 a successor start authority existed before commit: %d", starts)
	}
	if chain := s.countRows(`SELECT COUNT(*) FROM review_re_review_requests WHERE previous_review_id=?`, s.fixture.review); chain != 0 {
		t.Fatalf("G10 a successor re-review chain existed before commit: %d", chain)
	}
	_, preparedShow := s.show(replacement, "")
	if len(preparedShow.Intent.Steps) != 0 || preparedShow.Intent.Submission != nil {
		t.Fatalf("G10 prepared Card already carried successor business state: %s", jsonCard(preparedShow.Intent))
	}

	// Commit B once: exactly one successor Review, its own ordinal, one Run and
	// one start authority, all on the same Task.
	commitRequestID := s.nextID("commit-replacement")
	commitResult, commitEnvelope := s.commit(replacement, commitRequestID)
	if commitResult.IsError || commitEnvelope.Intent == nil || commitEnvelope.Intent.State != "COMPLETED" {
		t.Fatalf("G11 replacement commit failed: %s", s.lastRaw)
	}
	if commitEnvelope.Intent.IntentID != replacement.IntentID || commitEnvelope.Intent.ContentHash != replacement.ContentHash {
		t.Fatalf("G11 commit completed another intent: %s", jsonCard(commitEnvelope.Intent))
	}
	reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task)
	if reviews != 2 {
		t.Fatalf("G11 successor Review count=%d", reviews)
	}
	successor := s.scalar(`SELECT id FROM reviews WHERE task_id=? AND id<>?`, s.fixture.task, s.fixture.review)
	if successor == "" {
		t.Fatal("G11 no successor Review row")
	}
	if ordinal := s.countRows(`SELECT COUNT(*) FROM reviews WHERE id=? AND ordinal=2 AND task_id=?`, successor, s.fixture.task); ordinal != 1 {
		t.Fatalf("G11 successor Review %s is not ordinal 2 of the same Task", successor)
	}
	if runs := s.countRows(`SELECT COUNT(*) FROM runs WHERE task_id=?`, s.fixture.task); runs != 2 {
		t.Fatalf("G11 successor Run count=%d", runs)
	}
	if starts := s.countRows(`SELECT COUNT(*) FROM feature_area_review_start_requests WHERE review_id=? AND outcome='ACCEPTED'`, successor); starts != 1 {
		t.Fatalf("G11 successor start-authority/auto-start rows=%d, want exactly one accepted hand-off", starts)
	}
	if chain := s.countRows(`SELECT COUNT(*) FROM review_re_review_requests WHERE decision_id=? AND previous_review_id=? AND successor_review_id=? AND task_id=?`, wp.DecisionID(s.fixture.review), s.fixture.review, successor, s.fixture.task); chain != 1 {
		t.Fatalf("G11 the successor chain is not the single same-Task re-review of the original Review: %d", chain)
	}
	successorRun := s.scalar(`SELECT successor_run_id FROM review_re_review_requests WHERE previous_review_id=?`, s.fixture.review)
	if owned := s.countRows(`SELECT COUNT(*) FROM runs WHERE id=? AND task_id=?`, successorRun, s.fixture.task); owned != 1 {
		t.Fatalf("G11 successor Run %s is not the single Run of the same Task", successorRun)
	}
	if current := s.optionalScalar(`SELECT current_review_id FROM tasks WHERE id=?`, s.fixture.task); current != successor {
		t.Fatalf("G11 Task.currentReviewId=%q, want successor %q", current, successor)
	}

	// G13: a lost commit response replays on the ORIGINAL request id and records
	// nothing new.
	replayResult, replay := s.commit(replacement, commitRequestID)
	if replayResult.IsError || replay.Intent == nil || replay.Intent.State != "COMPLETED" {
		t.Fatalf("G13 replay of the lost commit response failed: %s", s.lastRaw)
	}
	if replay.Intent.Submission == nil || replay.Intent.Submission.Request.RequestID != commitRequestID {
		t.Fatalf("G13 replay returned a different request: %s", jsonCard(replay.Intent.Submission))
	}
	if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != 2 {
		t.Fatalf("G13 replay duplicated the successor Review: %d", reviews)
	}
	if runs := s.countRows(`SELECT COUNT(*) FROM runs WHERE task_id=?`, s.fixture.task); runs != 2 {
		t.Fatalf("G13 replay duplicated the successor Run: %d", runs)
	}
	if starts := s.countRows(`SELECT COUNT(*) FROM review_re_review_requests WHERE previous_review_id=?`, s.fixture.review); starts != 1 {
		t.Fatalf("G13 replay duplicated the successor re-review chain: %d", starts)
	}

	// G12: the old Card may still be refreshed, but it stays history and cannot
	// disturb the replacement or the successor.
	latePriorResult, latePrior := s.show(prior, "")
	requireMountedCard(t, latePriorResult, *latePrior.Intent)
	if latePrior.Intent.State != "SUPERSEDED" || latePrior.Intent.Submission != nil {
		t.Fatalf("G12 late prior refresh changed history state: %s", jsonCard(latePrior.Intent))
	}
	_, latePriorOptions := s.options(prior)
	if latePriorOptions.Options == nil || latePriorOptions.Options.Eligibility.Available {
		t.Fatalf("G12 late prior refresh became committable: %+v", latePriorOptions.Options)
	}
	latePriorCommit, latePriorCommitEnvelope := s.commit(prior, s.nextID("commit-prior-late"))
	if !latePriorCommit.IsError || latePriorCommitEnvelope.Failure == nil {
		t.Fatalf("G12 late prior commit was accepted: %s", s.lastRaw)
	}
	if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != 2 {
		t.Fatalf("G12 late prior commit added a Review: %d", reviews)
	}
	if current := s.optionalScalar(`SELECT current_review_id FROM tasks WHERE id=?`, s.fixture.task); current != successor {
		t.Fatalf("G12 late prior commit moved the successor pointer: %q", current)
	}
	// B remains the authoritative record of what happened, addressed by its own
	// request id only.
	_, receipt := s.read(replacement, commitRequestID)
	if receipt.Intent == nil || receipt.Intent.Submission == nil || receipt.Intent.Submission.Request.RequestID != commitRequestID {
		t.Fatalf("G13 exact receipt readback lost: %s", s.lastRaw)
	}
	if count := s.countRows(`SELECT COUNT(*) FROM confirmation_intents WHERE intent_id IN (?,?)`, prior.IntentID, replacement.IntentID); count != 2 {
		t.Fatalf("the lifecycle produced %d confirmations for this pair, want exactly the prior and replacement", count)
	}
}

// rawCall is the goroutine-safe transport boundary: it reports through returned
// errors instead of the test handle, which concurrent callers must not touch.
func (s *cardLifecycleStack) rawCall(name string, arguments map[string]any) (*sdkmcp.CallToolResult, c.Result, error) {
	result, err := s.session.CallTool(s.ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return nil, c.Result{}, fmt.Errorf("%s transport: %w", name, err)
	}
	var out c.Result
	if result.StructuredContent != nil {
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			return nil, c.Result{}, fmt.Errorf("%s structured content: %w", name, err)
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, c.Result{}, fmt.Errorf("%s envelope: %w", name, err)
		}
	}
	return result, out, nil
}

type cardMountProbe struct {
	requestID   string
	intentID    string
	state       string
	instanceID  string
	resourceURI string
	isError     bool
	failureCode string
	err         string
}

// probeShow mounts one intent through a concurrent goroutine and reports exactly
// what the Host would have received, so a torn or crossed Card instance cannot hide.
func (s *cardLifecycleStack) probeShow(intent c.Intent, handle string) cardMountProbe {
	probe := cardMountProbe{requestID: handle, intentID: intent.IntentID}
	result, envelope, err := s.rawCall("confirmation_show", map[string]any{
		"aiSessionId": s.fixture.session, "intentId": intent.IntentID, "version": intent.Version,
		"purpose": string(intent.Purpose), "contentHash": intent.ContentHash, "requestId": handle,
	})
	if err != nil {
		probe.err = err.Error()
		return probe
	}
	probe.isError = result.IsError
	probe.resourceURI = cardResourceURI(result)
	probe.instanceID = metaString(result, "devbridge/presentationInstanceId")
	if envelope.Failure != nil {
		probe.failureCode = envelope.Failure.Code
	}
	if envelope.Intent != nil {
		probe.state = envelope.Intent.State
	}
	return probe
}

func concurrent(count int, run func(index int) error) []error {
	var wg sync.WaitGroup
	errs := make([]error, count)
	start := make(chan struct{})
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			errs[index] = run(index)
		}(index)
	}
	close(start)
	wg.Wait()
	return errs
}

// G15: two Card instances, one shared presentation handle and one in-flight commit
// are exercised concurrently. A replacement never crosses into the old Card's
// identity, and the successor objects still appear exactly once.
func TestConfirmationConcurrentCardInstancesStayIsolated(t *testing.T) {
	s := newCardLifecycleStack(t)
	prior, replacement := s.prepareReplacement()
	const sharedHandle = "concurrent-shared-handle"

	// Phase 1: independent handles mount concurrently on both intents.
	independent := make([]cardMountProbe, 8)
	requestIDs := make([]string, len(independent))
	for index := range requestIDs {
		requestIDs[index] = fmt.Sprintf("concurrent-%s-%d", prior.IntentID[:16], index)
	}
	for _, err := range concurrent(len(independent), func(index int) error {
		target := replacement
		if index%2 == 0 {
			target = prior
		}
		probe := s.probeShow(target, requestIDs[index])
		independent[index] = probe
		return nil
	}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	wantInstance := map[string]string{prior.IntentID: derivedInstanceID(prior), replacement.IntentID: derivedInstanceID(replacement)}
	for index, probe := range independent {
		if probe.err != "" {
			t.Fatalf("probe %d: %s", index, probe.err)
		}
		if probe.isError || probe.failureCode != "" {
			t.Fatalf("probe %d was refused: %+v", index, probe)
		}
		if probe.resourceURI != confirmationCardResourceURI {
			t.Fatalf("probe %d mounted no Card resource: %+v", index, probe)
		}
		if probe.instanceID != wantInstance[probe.intentID] {
			t.Fatalf("probe %d derived instance %q for intent %q", index, probe.instanceID, probe.intentID)
		}
		if want := map[string]string{prior.IntentID: "SUPERSEDED", replacement.IntentID: "PENDING"}[probe.intentID]; probe.state != want {
			t.Fatalf("probe %d shows %s inside a %s Card: %+v", index, want, probe.state, probe)
		}
	}

	// Phase 2: one handle, two different intents at once. Exactly one Card is bound
	// and the other intent is refused; the winner keeps its own instance afterwards.
	shared := make([]cardMountProbe, 2)
	targets := []c.Intent{prior, replacement}
	for _, err := range concurrent(2, func(index int) error {
		shared[index] = s.probeShow(targets[index], sharedHandle)
		return nil
	}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	var accepted, rejected []cardMountProbe
	for _, probe := range shared {
		switch {
		case probe.err != "":
			t.Fatal(probe.err)
		case probe.isError:
			rejected = append(rejected, probe)
		default:
			accepted = append(accepted, probe)
		}
	}
	if len(accepted) != 1 || len(rejected) != 1 {
		t.Fatalf("one presentation handle served %+v", shared)
	}
	if rejected[0].failureCode != "CONFIRMATION_CARD_INSTANCE_REUSE_REJECTED" {
		t.Fatalf("handle competition rejection code=%q", rejected[0].failureCode)
	}
	if rejected[0].resourceURI != "" {
		t.Fatalf("the refused reuse still attached a Card resource: %+v", rejected[0])
	}
	winner := prior
	if accepted[0].intentID == replacement.IntentID {
		winner = replacement
	}
	if refresh := s.probeShow(winner, sharedHandle); refresh.isError || refresh.instanceID != accepted[0].instanceID {
		t.Fatalf("the winning intent lost its own handle: %+v vs %+v", refresh, accepted[0])
	}

	// Phase 3: committing the replacement while the old Card is read and refreshed.
	const readers = 6
	readerRequests := make([]string, readers)
	for index := range readerRequests {
		readerRequests[index] = s.nextID("concurrent-old-reader")
	}
	oldReads := make([]cardMountProbe, readers)
	commitRequests := make([]string, readers)
	for index := range commitRequests {
		commitRequests[index] = s.nextID("concurrent-old-commit")
	}
	oldCommits := make([]string, readers)
	var commitErr string
	successorRequestID := s.nextID("concurrent-successor-commit")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, envelope, err := s.rawCall("confirmation_commit", map[string]any{
			"aiSessionId": replacement.AISessionID, "intentId": replacement.IntentID, "version": replacement.Version,
			"purpose": string(replacement.Purpose), "action": replacement.Action, "requestId": successorRequestID,
			"contentHash": replacement.ContentHash, "variantId": "revise", "selection": map[string]any{},
		})
		if err != nil {
			commitErr = err.Error()
			return
		}
		if envelope.Intent == nil || envelope.Intent.State != "COMPLETED" {
			commitErr = fmt.Sprintf("successor commit did not complete: %+v", envelope.Intent)
		}
	}()
	for _, err := range concurrent(readers, func(index int) error {
		oldReads[index] = s.probeShow(prior, readerRequests[index])
		if _, _, err := s.rawCall("confirmation_options_get", map[string]any{"aiSessionId": prior.AISessionID, "intentId": prior.IntentID, "version": prior.Version, "purpose": string(prior.Purpose), "contentHash": prior.ContentHash}); err != nil {
			return err
		}
		_, envelope, err := s.rawCall("confirmation_commit", map[string]any{
			"aiSessionId": prior.AISessionID, "intentId": prior.IntentID, "version": prior.Version,
			"purpose": string(prior.Purpose), "action": prior.Action, "requestId": commitRequests[index],
			"contentHash": prior.ContentHash, "variantId": "revise", "selection": map[string]any{},
		})
		if err != nil {
			oldCommits[index] = err.Error()
			return nil
		}
		if envelope.Intent != nil && envelope.Intent.State == "COMPLETED" {
			return fmt.Errorf("the superseded intent completed while its replacement was committing")
		}
		oldCommits[index] = "REJECTED"
		return nil
	}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if commitErr != "" {
		t.Fatal(commitErr)
	}
	for index, probe := range oldReads {
		if probe.err != "" {
			t.Fatalf("old reader %d: %s", index, probe.err)
		}
		if probe.state != "SUPERSEDED" {
			t.Fatalf("old reader %d saw the replacement state: %+v", index, probe)
		}
		if probe.instanceID != wantInstance[prior.IntentID] {
			t.Fatalf("old reader %d lost its own Card instance: %+v", index, probe)
		}
	}
	for index, disposition := range oldCommits {
		if disposition != "REJECTED" {
			t.Fatalf("old commit %d was not cleanly rejected: %s", index, disposition)
		}
	}
	if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != 2 {
		t.Fatalf("concurrency changed the successor Review count to %d", reviews)
	}
	if runs := s.countRows(`SELECT COUNT(*) FROM runs WHERE task_id=?`, s.fixture.task); runs != 2 {
		t.Fatalf("concurrency changed the successor Run count to %d", runs)
	}
	if count := s.countRows(`SELECT COUNT(*) FROM confirmation_intents WHERE intent_id IN (?,?)`, prior.IntentID, replacement.IntentID); count != 2 {
		t.Fatalf("concurrency created another confirmation: %d", count)
	}
}
