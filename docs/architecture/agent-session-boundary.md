# Agent boundary — native compatibility and prospective cooperative execution

This implements a neutral session boundary, capability-selected prospective
launches, and an explicit cooperative trusted policy. It does not claim a second
natively isolated/qualified runtime or live-provider verification.

## Implemented boundary

`internal/agent` owns Core's session contract, observed turn result and optional
thread naming, resume, structured output, turn-start callback, interruption,
usage-owner, runtime-binding and host-tool configuration capabilities. The
coordinator and supervisor consume this contract. Configuration methods are
exported so an adapter outside `execution` can actually implement them; they are
not package-private lookalikes that fail Go interface assertions.

`internal/codexagent` adapts the existing Codex client to that contract. It owns
thread/turn option translation, command-evidence and token-usage translation,
terminal-error wrapping, and the existing Codex approval and rejoin extensions.
The protocol client is injected, so these translations can be tested without a
process, provider, or model call. `execution.CodexSession`, `CodexTurnResult` and
`TaskExecutionError` remain compatibility aliases for existing callers.

## Prospective cooperative launch

`agent.Factory.StartAgent` and `agent.LaunchRequest` provide the neutral launch
contract for explicitly registered prospective adapters. Core selects the exact
frozen `AgentID` from the registry; it never silently falls back to Codex. The
legacy native factory/checkpoint path remains separate.

Cooperative confirmation also freezes `AgentConfigFingerprint` from the configured
registry inventory. The Pi fingerprint binds its configured launcher, resolved
entry-file path/bytes, defaults and owned arguments. Core and Pi reject drift
before dispatch, and the policy and launch receipt bind that same fingerprint.
This identifies configured launch inputs, not transitive interpreter/package
dependencies or native runtime qualification.

New confirmations may explicitly select `COOPERATIVE_TRUSTED_V1`; empty policy
retains historical native semantics. This choice is not inferred from the old
project `TrustedExecution` flag, and cannot reinterpret a historical
`TASK_SANDBOX_V1` workset, native qualification, or same-turn recovery.

Core seals an `agentpolicy.Envelope` containing exact operation identity,
engineering scope, source roots, network policy and explicit scratch roots. Its
system-context text is distinct from the byte-identical frozen task instruction.
Source-readonly verification may write only to its explicitly recorded scratch;
this is cooperative policy, not host enforcement. The exact envelope is stored
as a diagnostic artifact **before** adapter launch. A matching
`LAUNCH_CONFIGURED` receipt with actual launcher/arguments is recorded before the
first task prompt. That receipt proves neither model consumption, task success,
user-decision consumption, nor native isolation.

The Pi adapter implements this separate system-context channel and exact
accepted-prompt/terminal/interrupt identity handling. It does not advertise
native permissions, Core host tools, resume, same-turn recovery or reliable
usage. Optional Core services do not turn these absent features into universal
requirements for a fresh cooperative run. An existing canonical thread still
requires exact agent/policy/config continuity and genuine resume capability.
Pi is currently fresh-threadless-only: preflight and durable admission reject
existing canonical threads and plans requiring a later verification Run, before
reservation/claim effects. Review → implementation → verification continuity is
not implemented for Pi.

The new selection fields are optional. Additive storage columns retain empty
legacy defaults; historical records and old hash material are not rewritten.
The current UI/model inventory is still Codex-shaped. Cooperative dispatch to an
unregistered adapter (including Codex without a separate cooperative policy
channel) is refused rather than silently dropping the policy.

## Capability selection and normalized outcomes

Core builds a registry from configured adapters' explicit `AgentDescriptor`
declarations. Native selection retains the frozen executor kind and requires applicable
capabilities (authorization ceiling, service tier, qualified launch, runtime
binding, host tools, cancellation/start acknowledgement, thread resume, and
same-turn recovery) **before** transport
creation. Missing capability is a refusal, never automatic fallback to another
agent. Required session methods are checked again before thread effects. These
declarations do not replace qualification, frozen backend fingerprints, runtime
ownership, or exact receipt evidence. Legacy undescribed injected factories
retain their existing method-based compatibility path; this is not an assertion
that arbitrary third-party factories are qualified.

Terminal failure/interruption is translated to `agent.TerminalError`, preserving
the native cause and exact thread/turn identity. Core accepts it only for the
matching nonempty interaction and a recognized terminal state. Transport loss
never becomes a terminal observation. Success remains the existing successful
turn contract; command output remains canonical `run.ObservedCommand` evidence.
Legacy raw Codex terminal errors are recognized at the compatibility boundary.
No adapter decides analysis, review acceptance or user acceptance.
Cooperative read-only Review checks source snapshots, root identity, HEAD/ref and
logical index identity in every frozen member before/after execution; a
mutation fails with `REVIEW_MUTATED_WORKTREE`, retains its Git evidence and does
not roll back the agent's changes. This is outcome checking, not a sandbox.

## Preserved behavior and authority

- Prompt/schema bytes, model/reasoning/service-tier selection, usage owner and
  frozen runtime bindings pass through unchanged. There is no new prompt wrapper,
  backend fallback, capability inference, or reconstruction from mutable history.
- Commands remain observed evidence, separate from the agent's final claim.
  Missing usage remains unknown. Existing rejoin behavior does not synthesize
  usage from a resumed observation.
- A terminal executor error keeps its original protocol error chain. Transport
  failure is not converted into task completion.
- Same-turn rejoin calls the existing `RejoinTurn`, never `StartTurn`. Its exact
  original thread, turn and restored runtime owner remain intact. Completion is
  still not evidence that an UNKNOWN user response was consumed.
- `DefaultCodexFactory`, its concrete recovery assertion, the private App Server
  ownership/cleanup shim, qualification checks, checkpoint serialization, and
  durable handoff/history/receipt ownership remain unchanged in `execution`.
- The existing trusted-agent premise remains: this contract is not a hostile
  agent sandbox. Passing a runtime binding neither grants new authority nor
  claims host-level isolation. Existing permissions and admission checks are not
  weakened or replaced.
- No historical field rename or frozen-record rewrite is performed. Optional
  prospective fields use additive empty-default storage. Unsupported Pi
  capabilities are refused rather than inferred from legacy no-op methods or
  prompt rewriting; native qualification is not claimed.

## Remaining concrete coupling

1. The legacy/native factories still take `codexconfig.Config`, Codex
   authorization ceilings and Core-private checkpoint/rejoin values. Prospective
   cooperative launches have a neutral contract; advanced native launch/recovery
   has not been fully extracted from Core.
2. App Server launch, qualified executable verification, recovery observation,
   process ownership, and cleanup still use Codex/App Server types and the
   concrete `DefaultCodexFactory` guard. Extracting them must preserve the exact
   durable ownership checks, not substitute a broad capability boolean.
3. Interactive approval requests/decisions, dynamic tool handlers and restored
   runtime rejoin remain Codex-specific extensions. A neutral tool *description*
   is not a neutral tool-delivery or response-receipt protocol.
4. Runtime selection/configuration, probe execution, UI/API names and historical
   persistence retain existing Codex naming. Neutral results deliberately reuse
   canonical Core `run` evidence/accounting types, whose attribution model still
   contains existing Codex usage details.
5. No additional backend (including Pi) has native platform qualification.
   Pi cooperative behavior has deterministic local protocol tests, not live
   provider acceptance. No generic dot Agent transport/adapter is installed. The separate
   [Upper Review pull worker](upper-review-pull-worker.md) only coordinates
   already-authorized UPPER_LLM participation through the existing MCP surface;
   it does not launch a cloud task or claim native Agent parity. Satisfying the small
   `agent.Session` interface alone does not establish structured output,
   continuity, cancellation, approvals, accounting, or recovery parity.

Future work should extract one of these remaining seams with explicit
capabilities and end-to-end recovery tests; simply renaming the legacy factories
would not complete the abstraction.
