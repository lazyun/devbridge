# CHATGPT_FEATURE_AREA_CARD_UX_CLOSURE

> Historical implementation evidence. [GPT surface consolidation V1](gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

Historical closure. Its two-resource presentation was superseded by
[unified workspace Card navigation](dev-bridge-unified-workspace-card-navigation-closure.md).

Baseline: actual `dev_20260908_1913` HEAD was
`4815d64e678b1766e38b4886f428656a77a6b733`
(`fix: align feature area review progress projection`), identical to the requested
baseline. Implementation uses independent branch
`codex/chatgpt-feature-area-card-ux-closure`.

## Emission contract

| Policy | Entry | Behavior |
|---|---|---|
| MAIN | `feature_area_card_show`, `development_context_card_show` | Explicit main conversation Card with fresh projection |
| SILENT | All commands and auxiliary reads, including `feature_area_presentation_get` and app-only `development_context_presentation_get` | Structured/text facts; no main resource or output template |
| DETAILS | `feature_area_details_show` | Explicit separate Details presentation; no main Card |

One user-visible intent selects at most one MAIN endpoint across both card types.
Use it for first entry / explicit open, explicit status, Refresh, recall, or the
completion of an explicitly authorized production action. Supporting Project and
branch queries, compatibility workflow reads, Review polling, exact Decision and
Execution reads, history, internal reprojection and response-loss reconciliation
remain SILENT. Creating/preparing a Task or starting a Review does not itself
insert a Card.

## Current Card interaction

`development_context_card_show` supports the first entry before an AI session
exists. The pre-session show returns an unbound presentation; the mounted Card
reads inventory through the separate SILENT `project_list` tool and renders an
explicit Project selector. It does not infer or auto-select a Project. Only the
user's selection invokes the existing authoritative `project_open`; the Card
then receives the Project-bound Development Cycle and branch projection. The
Card does not retain Project state.

FeatureArea progress is a responsive mechanical Stepper over the authoritative
projection: complete, current/running, pending, blocked, failed, canceled and
unknown/recovery-required remain distinct. It does not classify Review state.
The Card records **Last refreshed** only after a successful authoritative
projection read: initial mount, successful Refresh, successful remount, and
fresh reprojection after an action all advance it. Failed reads leave that time
unchanged and display stale/refresh-failed state; domain `updatedAt` is not a
replacement.

The Details backend/read model remains available through the hidden, silent
`feature_area_details_get`; explicit `feature_area_details_show` is its only
template-bound entry point. The FeatureArea Card currently hides its default
`View details` entry. DETAILS remains a separate presentation and never emits a main Card.

For chat-driven production actions the presentation sequence is mutation, exact
authoritative subject readback, then one explicit show (which reads a fresh
projection). Existing mounted Card actions retain their established mutation,
exact readback and silent fresh projection sequence, updating the same Card.
Mounted Refresh and remount likewise use the silent get endpoint. Details retains
its independent presentation when explicitly invoked; it is not a default
FeatureArea Card entry. Neither action handlers nor authoritative readback
protocols change.

The endpoint split is deliberate: host resource routing is declared in tool
metadata, so a result flag or optional SILENT argument on a card-bound tool would
not reliably prevent mounting. Only show tools advertise a main resource. No
session-wide suppression cache, workflow flag, new receipt, or inferred intent ID
is introduced. MCP does not supply a trusted user-intent boundary; the caller must
honor the once-per-intent show contract. Repeated explicit show calls can still
create repeated host items, and later intentional recalls remain available.

Command projection enrichment and structured result contracts remain intact.
An enrichment failure still cannot turn a committed command into a failed
mutation. Local MCP remains resource-free. Decision, Cancellation, Recovery,
Review lifecycle, and all other authoritative workflow semantics are unchanged.

## Validation

Transport tests exercise command + repeated silent projection reads + one show,
projection/text parity, later explicit recall, and resource metadata. Fresh HTTP
catalog tests require only the two show endpoints to bind main Cards. Existing
DOM tests exercise action exact readback, response loss, refresh, and Details.
Real ChatGPT host acceptance is not claimed.

Executed gates:

- `go test ./...`: passed (including fresh GPT/Local catalogs and workflow regression).
- `node --test internal/mcpserver/*ui_test.js`: 73/73 passed.
- `go test -race ./internal/mcpserver ./internal/localmcp`: passed.
- `go vet ./internal/mcpserver`: passed.
- `go build -o <temporary-output>/ ./cmd/...`: passed.
- `node scripts/build-chatgpt-ui.mjs <temporary-output>`: three self-contained assets passed.
- `git diff --check`: passed.

The previous cancellation test expected the mutation itself to mount a Card. Its
presentation assertion now requires no resource/template; all original
cancellation parity and replay checks remain in place.
