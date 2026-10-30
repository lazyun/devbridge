# FeatureArea Review progress projection closure

> Historical implementation evidence. [GPT surface consolidation V1](gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

Baseline: `34516d96edbc90be3499612fd88ada7fff53d286`.

## Production path and findings

| Layer | Production path |
|---|---|
| Durable Review | `reviews.linked_run_id` selects the exact Run; Review start failure is a separate product fact. |
| Ownership | `feature_area_agent_claims` and `AgentConversationStore.State` identify the current operation, independently of historical Run results. |
| Workflow selection | `controlapi.currentWorkflowForSession`: active Review from exact claim; latest admitted Review from ordered history; current Review = active, otherwise latest. Runtimeless legacy admission-failure history remains outside the latest-admitted selector. |
| Shared presentation | `FeatureAreaPresentationForSession` → `projectFeatureAreaPresentation` → `workflowpresentation.ReviewProgressStatus`. |
| MCP / Card | `feature_area_presentation_get` returns the projection as JSON plus `RenderText`; the unified `dev_bridge_ui.html` renders each supplied phase/status directly. |
| Compatibility workflow JSON | `workflow_get` reuses the captured ownership observation and shared Review classifier for current Review state; exact historical detail serialization stays unchanged. |
| Console | `GetFeatureAreaPresentation` delegates to that same facade; HTTP JSON/text use the same projection. |
| Web product view | `GetProductProjectWorkspace` selects exact active Review ownership; `product-app.js` prefers `active_review` over `latest_review`. Active summary status now shares the Review progress classifier. Historical summaries retain their original result. |

The defects were in read-model composition, not Refresh:

1. History was listed before ownership. A newly admitted Review could therefore
   be absent from the history list but present in the claim. The selector silently
   fell back to the old completed Review. Presentation then read ownership a
   second time, allowing current phase/operation and Review progress to describe
   different observations.
2. Review progress mapped `Run.SUCCEEDED` directly to `COMPLETE`, even while an
   active Review claim still owned the area. A start blocker could also be
   overwritten by the subsequent Run status mapping.
3. Recovery projection overwrote Review outcome progress with Agent safety
   status, conflating Review failure with operation recovery requirements.
4. Compatibility workflow JSON independently derived current Review completion
   from the Run, creating another opportunity to disagree with the main projection.

The fix resolves a claim's exact Review/Run if admission crossed the history
read, validates its ownership, preserves the ownership observation in
`CurrentWorkflow`, and reuses it for presentation. Ownership read errors fail
closed instead of selecting historical success. This is one ownership observation,
not a claim that every read in the workflow occurs in one database transaction.

## Current-state semantics

| Exact selected Review facts | REVIEW progress |
|---|---|
| No Review | `PENDING` |
| Active/nonterminal Review, including waiting approval | `RUNNING` |
| Successful Run with active Review owner still present | `RUNNING` |
| Review start blocker | `BLOCKED` |
| Failed / infrastructure-failed Review Run | `FAILED` |
| Successful effective Review, no active Review | `COMPLETE` |
| Canceled / missing exact Run facts | `CANCELED` / `UNKNOWN` |

Active Review wins over latest/history. Historical success cannot supply the
current Review's status, source ID or baseline. A pending runtime Approval remains
visible as a separate decision. Recovery requirements remain in workflow status,
current operation and recovery fields; they do not replace Review progress facts.
Web retains its existing product labels (`REVIEWING`, `AWAITING_AUTHORIZATION`,
and the failed-product `BLOCKED` grouping), while Console presentation JSON/text
and the Card use the shared progress vocabulary above.

## Evidence and validation

Read-only inspection of the running local database observed a new `RUNNING`
Review with an exact Review claim alongside older `SUCCEEDED` Reviews. During
the investigation the new Run became `SUCCEEDED` and its claim was released;
the live Console endpoint then correctly returned `COMPLETE` for that new Review.
The original inconsistent response was not captured, so this later response is
not presented as a replay of the user's original observation.

Regression coverage includes:

- Real SQLite admission between history and ownership reads; exact current/latest
  identity, one ownership observation per presentation, and no silent success on
  ownership-read failure.
- Historical successful Review plus active current Review; successful Run before
  and after claim release; database content fingerprints unchanged by reads.
- Active, collecting, approval, blocked, failed, recovery-required and completed
  states; exact source identity and JSON/text agreement.
- Web current versus historical completion and Card rendering of all five
  requested progress states without inferring completion from later phases.
- Compatibility current workflow remains `REVIEWING`/`RUNNING` until ownership
  release, while exact historical Review detail still reports `SUCCEEDED`.

Final validation:

| Gate | Result |
|---|---|
| `go test ./...` | Passed; 53 test packages |
| `go test -race` for controlapi, consoleapi, workflowpresentation, mcpserver, bridgehttp, localmcp | Passed; MCP/Local repeated after the compatibility-path fix |
| `go vet` for the six affected packages | Passed |
| `go build -o <temporary-output>/ ./cmd/...` | Passed |
| `node --test internal/mcpserver/*ui_test.js` | Passed; 73/73 |
| `node --test internal/bridgehttp/*ui_test.js` | Passed; 45/45 |
| Changed-file `gofmt -l` and `git diff --check` | Passed |

The final diff was checked for alternate completion inference, historical object
rewrites, new write paths and action eligibility. No second state machine or
write path was introduced; existing authoritative action guards remain in force.
No desktop source or frontend resource implementation changed.

No Review, Run, cancellation, recovery or Decision transition protocol is changed.
The running user service and its active work are not restarted or mutated by this
source repair. Deployment of the updated binary is separate from code validation.
