# Review observability and mounted Card live update

The current business schema is v46. Presentation reads, disclosure state and MAIN
leases do not write business SQLite or participate in workflow finalization.

## Review activity and tokens

Review activity uses the existing v40/v41 official current-item snapshot. The
current activity is collapsed initially, with a native button (click, Enter,
Space, `aria-expanded`, `aria-controls`) controlling the bounded text. Local
expansion survives ordinary drawer refresh, and a different FeatureArea/Review/
Task or drawer close resets it. Terminal views say “最后活动” and “已结束”. Waiting
summaries precede the disclosure. Activity diagnostics remain collapsed and omit
the agent-message text already available in the activity disclosure.

The Review detail and its owning semantic Task use Review → `linked_run_id` →
existing Run token accounting. Shared operation presentation binds Review Run
facts to that exact link, without creating a REVIEW pseudo-Task.
Console `latest_run.usage`, Review `usage`, and the Task tab all use
`operationUsage` of that exact Run. A newer unrelated Run cannot replace it.
Only provided counts are shown, including optional context window; no derived
non-cached counts are added. AVAILABLE has exact values, PENDING says 统计中,
UNAVAILABLE (including missing history) says 不可用, never fabricated zero.
Token availability does not determine Run/Review terminal status.

## Mounted Card lifecycle

An expanded component uses the existing local 5-second timeout only while its
selected Review, Implementation, Verification or commit is running. A collapsed
entry observes its exact OperationTarget using operation_get(include:[]) every
15 seconds, at most 12 times; a terminal observation triggers one exact workspace
readback. Pending/ready/terminal state does not itself keep a poller running.
There is no token-specific timer or additional Web global poll.

Mounted reads use `dev_bridge_card_get` and, when branch inventory is needed,
`workspace_get(include=["BRANCHES"])`. Session, Project, exact target, generation
and source sequence fence responses. Independent read errors stay explicit;
failed refreshes keep stale data visible while dangerous actions remain disabled.
Collapsed historical entries do not create their own workspace poller. No read
invokes show or emits another UI resource.

Refresh cancels the timeout; completion schedules at most one next timeout.
There is no polling timeout while a read is in flight. Manual refresh may
supersede a pending read; its newer read sequence fences the older response.
Feature/Task navigation cancels the previous context, reads the new selection,
and reevaluates eligibility. Teardown clears the timeout and RPC timers.
Hidden documents pause; visible documents immediately read and then reevaluate.
Approval/user-input waits, stable terminal states, no selection and no active
work stop polling. Cancellation/retirement convergence takes precedence over
old user-wait observations. Actions pause polling and reevaluate after readback.

## MAIN presentation policy

`project_selection_show` is the only unbound entry. `workspace_show` requires a
valid Session and stable presentationRequestId; `details_show` reads an exact
object. Ordinary workspace entries default COLLAPSED, while an explicit user
request to show/open uses EXPANDED. In-component expansion performs a fresh
exact read and does not ask GPT or create another card.

workspaceIdentity identifies the bound Session/Project; entryIdentity identifies
one requested entry. Equal request identity and arguments retain the first
resolved scope and re-read a complete current initialization response; changed
arguments conflict. Business snapshots and old approval authority are not cached. Different replies may have
different entries. The server cannot infer all host turns, so the one-entry-per-reply
rule is an instruction to GPT. No session-wide ALREADY_SHOWN lease blocks later
replies. Host visibility stays UNCONFIRMED until there is host evidence.

All data reads, writes and readback remain SILENT. Entry retries retain exact
selection and complete identity. Each card owns local navigation and drafts;
server remembered selection cannot move a historical entry. Full-component
Loading covers INTERACTIVE navigation/refresh/action readback and blocks pointer,
keyboard and duplicate submissions. BACKGROUND reads retain focus, popups,
scrolling and compatible drafts, with lightweight stale/error feedback.
Hidden and destroyed components stop polling and fence late responses; collapsed
components use only the bounded exact-operation observer described above.
Existing exact Review completion-event deduplication remains intact. A collapsed
observer may request follow-up only for the completion it actually observed;
ordinary expansion or remount does not fabricate a new completion event.

The existing mounted Review-completion notice can request semantic continuation
through supported MCP Apps communication. It is neither approval nor a guarantee
of delivery while the component is unmounted. No new background GPT wake service
is introduced. See the [current interface contract](gpt-surface-consolidation-v1.md).

The same-call fresh show snapshot initializes the production component directly.
Missing, older-than-30-second, inconsistent or failed-partition snapshots instead
cause an exact INTERACTIVE read. Historical decision origins remain separate
from current navigation. ui/update-model-context carries bound Session/Project
and accurate selection; its ACK is distinct from ui/message follow-up acceptance,
and neither proves GPT executed. Failed context delivery does not trigger an
automatic long user-message compensation. The manual result-interpretation
control remains available with explicit host-capability degradation.

See the [flow closure report](../validation/chatgpt-card-presentation-flow-closure.md)
for executed automated gates and the still-NOT_RUN real ChatGPT evaluation set.
