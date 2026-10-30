# Built-in agent roles

Dev Bridge v0.1 separates three responsibilities so that understanding,
changing, and proving software are not treated as the same operation.

```text
Reviewer    = understand
Implementer = change
Verifier    = prove
```

These are AgentRoles used only to select execution configuration such as backend,
model, reasoning effort, and runtime profile. They are not prompt authors, Codex
Skills, or model-conversation identities.

## Reviewer

The Reviewer understands the relevant production implementation well enough to
support the engineering decision and formation of a Task.

The complete GPT-authored Review instruction tells the backend what to investigate
and what constraints to follow. Dev Bridge forwards it verbatim. It does not prepend
Reviewer identity, require `repository_*` capability use, classify command/path text,
or police worktree mutation to enforce semantic role behavior. Project/Repository
containment and genuine authorization boundaries remain structural controls.

Each Review is an independent durable operation under the FeatureArea. It uses
the canonical AgentConversation; a role does not select a new conversation.

## Implementer

The Implementer configuration performs the confirmed Task. The backend receives only the frozen
implementation instruction, changes only the authorized Execution scope, and
works in the managed Execution workspace/worktree.

The Implementer does not own final verification authority. Execution uses its FeatureArea canonical conversation, while its own immutable
definition, prepared scope and authorization determine permitted work.

## Verifier

The Verifier configuration independently checks the completed implementation against the
verification specification. Depending on that specification, it may run tests,
static analysis, lint, Git assertions, diff checks, and semantic checks.

Verification must leave the implementation Git tree unchanged. When verification
requires Codex semantic work, it uses the FeatureArea conversation with the frozen verification instruction.
Deterministic verification evidence retains its separate authority.

## Example separation

```text
Review #1     -> Reviewer conversation A -> understanding
Execution #1 -> Implementer conversation B -> implementation
Verification -> Verifier conversation C -> independent proof
```

This separation does not prevent Dev Bridge from preserving durable Feature Area,
Task, workspace, Evidence, and history across the operations.
