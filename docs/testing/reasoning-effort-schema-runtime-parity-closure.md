> Historical validation record. Its checkpoint and screenshots are not the current architecture or interface contract; see [final v0.1](../architecture/task-execution-v0.1.md).

> Historical implementation evidence. [GPT surface consolidation V1](../architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# REASONING_EFFORT_SCHEMA_RUNTIME_PARITY_CLOSURE

Current Task/Execution authority and automatic approved Implementation follow the [final v0.1 model](../architecture/task-execution-v0.1.md).

Result: **REASONING_EFFORT_SCHEMA_RUNTIME_PARITY_CLOSURE_READY** (implementation and isolated fresh-runtime validation).

Scope: GPT `featureArea_review_start` reasoning-effort input discovery versus
current runtime admission. This does not redesign model configuration, change
the enabled efforts, or alter other workflows.

## Baseline and fresh observation

- Development branch: `dev_20260908_1913`; clean working tree.
- Starting HEAD: `802345570a2312a44cdae6e2a1384e4db1d11c30`; no baseline advance.
- Independent branch: `codex/reasoning-effort-schema-runtime-parity`.
- Worktree: `/private/tmp/devbridge-reasoning-parity`.
- Connected running service: `0.1.0-dev+g8e4b9d2`, ready. It is older than the
  checked-out development HEAD and was not replaced during this task.
- Read-only Desktop configuration inspection: enabled efforts were
  `low, medium, high`.
- A fresh HTTP `tools/list` from the running GPT endpoint advertised
  `low, medium, high, xhigh` for the Review input.
- A read-only `workflow_get` using an existing session on the same running
  endpoint returned `availableReasoningEfforts = [low, medium, high]`.

The earlier conclusion that the drift was closed was incorrect: it compared
the static universe to code-level support, not fresh discovery to the configured
runtime subset. This observation reproduces the mismatch without creating a
Review in the user's live environment.

## Ownership trace

1. The vendored Codex v2 protocol defines `ReasoningEffort` as a non-empty
   string; model metadata has `supportedReasoningEfforts`. This protocol shape
   does not authorize every value for the running Bridge instance. Bridge does
   not derive its configured allowlist from `model/list` on this path.
2. Bridge's v0.1 closed universe in `internal/codexruntime/allowlist.go` includes
   `low, medium, high, xhigh`. Desktop `AllowedReasoningEfforts` selects the
   enabled subset and sends it to Core's `-allowed-reasoning-efforts` flag.
3. Core parses configuration into `ControlPlane.CodexRuntime`; the shared
   control facade receives it as `ExecutionAllowlist`. Workflow projection
   exposes the same runtime's enabled efforts as `availableReasoningEfforts`.
4. Before this fix, GPT registration passed `externalReviewStartSchema`, whose
   input enum was a static four-value universe, directly to the MCP SDK.
5. `StartTopicReviewCore` calls `validateModelSelection`, which reads the
   service's `ExecutionAllowlist()` and rejects a disabled effort with
   `REASONING_EFFORT_NOT_ALLOWED` before durable Review admission.

The configured runtime subset owns admission and input discovery. The fix
projects that authority into the GPT Review schema; it does not enable `xhigh`
merely because the protocol and persistence can represent it.

## Change

GPT server construction now derives the Review input schema from the same
`ExecutionAllowlist()` used by admission. Only its `reasoningEffort` input enum
is replaced. Missing or invalid runtime authority fails server construction
instead of advertising a guessed universe. The existing configuration lifecycle
restarts Core when the enabled list changes, so each server registers its
capability snapshot before fresh `initialize` / `tools/list`.

The static template, output schemas (which can describe historical `xhigh`
records), model fields, Task schema, Local MCP schema and admission guards are
preserved. There is no new protocol/model discovery, fallback effort, mutable
workflow state or automatic capability expansion. The cancellation transport
test wrapper now embeds its existing concrete service so runtime capability
access is forwarded alongside the other service methods.

Disabled inputs now fail GPT schema validation before the handler. The existing
runtime guard remains necessary and retains `REASONING_EFFORT_NOT_ALLOWED` for
calls that reach the core directly. The old transport test was updated for this
earlier rejection point; the new parity test also exercises the core guard.

## Evidence

The new parity regression was run with the old static registration and failed
as expected for `[low, medium, high]` and `[xhigh, low]`: fresh `tools/list`
returned all four values. It passes with the dynamic registration.

For three configurations (three values, four values, reordered subset), tests:

- discover the schema on fresh MCP connections before Project session binding;
- compare the enum exactly, including order, with runtime configuration and
  `workflow_get` capability projection;
- call Review start for every universe value through the real MCP transport;
- persist admitted Reviews with the exact enabled effort, using the existing
  durable admission fixture rather than launching external Codex work;
- reject disabled values both at the schema boundary and via direct core
  admission, with no Review created and no operation started;
- reject missing/invalid schema authority without guessing defaults.

A separately built Core was launched against temporary DataRoots with both
three-value and four-value CLI configurations. Fresh MCP `initialize` and
`tools/list` against `/_adapters/gpt/mcp` returned exactly the configured Review
efforts in both cases. The Task input remained unchanged. Both Core processes
shut down normally and their temporary DataRoots were removed.

## Validation

| Gate | Result |
|---|---|
| Old static registration + new parity regression | Expected FAIL; drift reproduced |
| Parity/admission, authority and configured workflow regression | PASS |
| Targeted MCP parity `go test -race` | PASS |
| Fresh compiled Core, three-value and four-value configurations | PASS |
| `go build ./cmd/devbridge` | PASS |
| `go vet ./internal/mcpserver` | PASS |
| `go test ./... -count=1` | PASS; 54 packages with tests |
| Changed Go files `gofmt` / `git diff --check` | PASS |

Validation uses a temporary Go build cache and local loopback/process tests.
No live Review was started, no external Codex execution was claimed, and no
model/effort configuration was changed. The already-running `g8e4b9d2` service
still needs the new binary and a restart before its fresh catalog can reflect
this change; an existing GPT conversation's cached tool declarations are not
updated by committing code.
