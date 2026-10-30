# Dev Bridge user guide

Dev Bridge gives AI a durable software-development capability. It coordinates
project understanding, engineering goals, code review, executable Tasks, user
authorization, Codex execution, verification, Git workspaces, Evidence, and
recovery/history.

The user-facing workflow is:

```text
Requirement → FeatureArea → Task → Review → Approval
→ Execution → Implementation → Verification → Task Acceptance
```

Task records your engineering intent before Review. Its title remains your goal.
Review Approval freezes the existing Task's immutable initial definition, then an
explicit start begins development. Each command reports current Task phase and
nextAction. Reviews and delivery cycles can repeat under the same Task.

The Task Card also allows you to explicitly skip Review and proceed to a separate
implementation-plan confirmation. Creating that Task starts no work. AI file Review
is performed by the AI in the initiating conversation; it creates no Codex session.

You choose the Project and authorize meaningful work. Dev Bridge preserves the
engineering context and coordinates the machinery around that work. You do not
need to understand its internal storage objects or lifecycle enums to use it.

[验证统一交给 Codex](codex-verification.md)：GPT 文件实施后的独立验证、命令证据与验收。

## How this documentation is organized

Use this guide for stable rules that affect how you understand or predict Dev
Bridge behavior. Architecture documentation explains implementation and design
rationale. Reference documentation defines exact technical contracts, states,
and APIs. Internal names appear here only when they clarify a user-visible
boundary or help with diagnostics.

## Recommended reading order

Optional enhancements: [Task notifications and Codex history](task-events-and-codex-history.md).
Ordinary conversations remain supported without Work / Cloud or event subscriptions.
For a task that should stay visible while chatting, see [PiP task progress](task-progress-pip.md).

1. [Getting started](getting-started.md) — the shortest path through Dev Bridge.
2. [The development workflow](workflow.md) — the complete product workflow.
3. [Project Context](project-context.md) — reusable project understanding and
   the Project Module Map.
4. [Feature Areas and Reviews](feature-areas-and-reviews.md) — engineering intent, focused
   investigation, repeated Reviews, and continuity.
5. [Built-in agent roles](agent-roles.md) — Reviewer, Implementer, and Verifier.
6. [Decisions and authorization](decisions-and-authorization.md) — what you
   approve at the Review boundary and how execution follows.
7. [Codex conversations](codex-conversations.md) — context boundaries,
   identity, language, and Skills.
8. [DataRoot](data-root.md) — independent local Dev Bridge environments.
9. [Desktop and Web Console](desktop-and-console.md) — the local shell and the
   main product UI.
10. [The Local MCP adapter](local-mcp.md) — the second, independent
    ingress adapter; it and the ChatGPT path may run at the same time.
11. [Canceling an Execution](execution-cancellation.md) — exact cancellation,
    durable outcomes, and workspace preservation.
12. [Terminology](terminology.md) — a concise glossary.

For exact model-facing and internal contracts, see the
[interface reference](../interfaces.md). That reference intentionally includes
details that normal users do not need.

- [GPT 统一工作面板](gpt-work-panel.md)：在同一面板处理创建、审查、实施、验证、恢复和合入。
