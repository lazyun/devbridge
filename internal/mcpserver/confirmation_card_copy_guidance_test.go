package mcpserver_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// §10: the outer copy is the last thing that can make a prepared-but-uncommitted
// replacement look like a recreated Review or a shown Card. These assertions run
// against the advertised tool contract and live server instructions of the real
// adapter, not against a constant copied into this file.

func advertisedTools(t *testing.T) map[string]string {
	t.Helper()
	_, _, baseURL := startTestServer(t, readyService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "confirmation-copy-guidance", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if session.InitializeResult().Instructions != mcpserver.ChatGPTConfirmationInstructions {
		t.Fatal("live server instructions diverged from the contract constant")
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptions := map[string]string{}
	for _, tool := range listed.Tools {
		descriptions[tool.Name] = tool.Description
	}
	return descriptions
}

func requirePhrases(t *testing.T, surface, text string, phrases []string) {
	t.Helper()
	for _, phrase := range phrases {
		if !strings.Contains(text, phrase) {
			t.Fatalf("%s copy omits %q\ngot: %s", surface, phrase, text)
		}
	}
}

func TestPrepareToolCopyForbidsPreCommitSuccessorClaims(t *testing.T) {
	descriptions := advertisedTools(t)
	// The five purpose Confirmation prepare tools, named explicitly so a purpose
	// surface that disappears cannot quietly drop out of the copy contract. Other
	// *_prepare names belong to unrelated read-only eligibility surfaces.
	for _, name := range []string{"project_setup_prepare", "feature_area_prepare", "task_prepare", "review_prepare", "execution_prepare"} {
		description, ok := descriptions[name]
		if !ok {
			t.Fatalf("%s is not advertised", name)
		}
		requirePhrases(t, name, description, []string{
			"Persist only an immutable pending confirmation intent",
			"sourceIntentId replaces only a still-pending prior intent",
			"Until confirmation_commit succeeds, report only that this confirmation intent is prepared",
			"never state that the Task or Review was recreated, that a successor Review exists, that re-review started, or that the Card was shown",
			"Only a committed intent with durable successor identities may be described as a created successor Review",
		})
		// The prepare surface may never itself tell the model to announce success.
		for _, forbidden := range []string{"successor Review has been created", "the Card is now showing"} {
			if strings.Contains(description, forbidden) {
				t.Fatalf("%s copy asserts an uncommitted result: %q", name, forbidden)
			}
		}
	}
}

func TestConfirmationShowCopyDescribesItsOwnCardInstance(t *testing.T) {
	descriptions := advertisedTools(t)
	show, ok := descriptions["confirmation_show"]
	if !ok {
		t.Fatal("confirmation_show is not advertised")
	}
	requirePhrases(t, "confirmation_show", show, []string{
		"showing or refreshing is a pure read",
		"Each intent is its own Card instance",
		"devbridge/presentationInstanceId is derived from aiSessionId, intentId, version and contentHash only",
		"never rebinds a previously shown Card or inherits its business state",
		"Without an explicit Host display acknowledgement, return this tool result only and do not state that the Card was shown",
		"Subsequent phases must not replace this Card's operation identity",
	})
}

func TestServerInstructionsCarryTheReplacementCardCopyRules(t *testing.T) {
	requirePhrases(t, "server instructions", mcpserver.ChatGPTConfirmationInstructions, []string{
		"No formal Task/Review/Execution exists merely because a creation intent was prepared",
		"A sourceIntentId replacement yields a separate pending intent with its own identity and never revives or rebinds the superseded confirmation",
		"Before confirmation_commit succeeds, report only this intent's identity, frozen state and counts",
		"never state a recreated Task or Review, an existing successor Review, a started re-review, or that a Card was shown",
		"Each intent is its own Card instance, addressed by the devbridge/presentationInstanceId derived from aiSessionId, intentId, version and contentHash",
		"an old Card stays historical and uncommittable",
	})
	// §13/§14: the text-Continue route is spelled out on the same surface the Card
	// uses, including the fixed outer copy and the two sentences it may never become.
	requirePhrases(t, "server instructions", mcpserver.ChatGPTConfirmationInstructions, []string{
		"Explicit user text 'Continue confirmation <intentId> version <version> contentHash <contentHash>' names one exact intent and is routed through the same confirmation_get and confirmation_commit services the Card uses",
		"First read confirmation_get with that exact five-field identity (aiSessionId, intentId, version, purpose, contentHash)",
		"A recorded REJECTED_NOT_CONSUMED whose retryDisposition is REPREPARE_REQUIRED means report the typed rejection verbatim and prepare the replacement with confirmation_reprepare; never call commit on it",
		"Call confirmation_commit only when that exact read shows PENDING, no recorded submission, options available and the identity unchanged",
		"never downgrade a typed rejection to recorded=false, OUTCOME_UNKNOWN or READ_ORIGINAL_RECEIPT",
		"If no confirmation_commit tool is discoverable on this surface, return the actual CONFIRMATION_COMMIT_TOOL_UNAVAILABLE condition and state no outcome at all",
		"本次重新审查确认未被消费。原因：冻结的审查处置状态已发生变化。需要重新准备新的确认。",
		"新的重新审查确认已准备",
		"only a committed replacement with durable successor identity may be described as successor Review 已创建并开始准备",
		"Never describe either as 当前结果未知 or 无法确认是否已提交",
	})
}

func TestServerInstructionsMatchCurrentCompletionAndFileVerification(t *testing.T) {
	requirePhrases(t, "current server workflow", mcpserver.ChatGPTConfirmationInstructions, []string{
		"the primary choices are CONTINUE_TASK, COMPLETE_TASK and DEFER_TASK",
		"the Task completes only after every merge succeeds",
		"ACCEPT is legacy acceptance-only compatibility",
		"DIRECT_FILE implementation with RUN verification requires an explicit Codex verification backend and runtime configuration",
		"The Core starts that authorized independent read-only verification after file_implementation_result_submit",
		"The unified panel may use ui/message or sendFollowUpMessage",
		"never automatically resend a message with an unknown delivery outcome",
		"prepare a fresh MERGE_FEATURE_AREA confirmation",
	})
	for _, obsolete := range []string{"File modes carry no Codex configuration", "No ui/message, sendFollowUpMessage", "No implicit waiver or automatic Codex follow-up is allowed"} {
		if strings.Contains(mcpserver.ChatGPTConfirmationInstructions, obsolete) {
			t.Errorf("obsolete public workflow rule: %s", obsolete)
		}
	}
}
