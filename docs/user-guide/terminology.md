# Terminology

Current Task/Execution authority and explicit implementation start follow the [final v0.1 model](../architecture/task-execution-v0.1.md).

## User-facing product terms

| Term | Meaning |
|---|---|
| Project | The durable software-development boundary explicitly selected by the user. |
| Project Context | Reusable understanding of a Project's guidance and production architecture. |
| Project Module Map | The reusable map of major production modules, responsibilities, roots, entrypoints, and relationships produced by Project Context. |
| Feature Area | A Project-owned engineering goal that can outlive a ChatGPT session. |
| Review | A read-only engineering operation owned by an existing Task. Its complete GPT-authored instruction is sent to the trusted backend verbatim. |
| Document | Explicitly requested durable Project knowledge authored through GPT, optionally associated with a Feature Area. It is not engineering work or authorization. |
| Task | Durable engineering intent created before Review; owns status and business phase. Approval freezes its initial immutable implementation definition. |
| Execution | One delivery cycle under a Task revision, with at most one Implementation Run and one Verification Run. |
| Verification | Independent checking of the completed implementation against the verification specification. |
| Result | The terminal user-facing outcome, including implementation, verification, important Evidence, and failures. |
| Evidence | Bridge-observed facts, such as Git changes and command results, used to support the Result. |
| DataRoot | One independent local Dev Bridge environment containing Bridge-managed state and operational material. |
| Web Console | The main product and business UI. |
| Desktop | The local operations and native-integration shell around Dev Bridge. |

## Roles and capabilities

**AgentRole** means the execution configuration being selected. The built-in v0.1
roles are Reviewer, Implementer, and Verifier; role metadata never authors prompt prose.

**Reviewer** understands. **Implementer** changes. **Verifier** proves.

A **Codex Skill** is an optional specialized Codex capability or instruction
package. A Skill is not an AgentRole, and arbitrary user-global Skills are not
inherited by Bridge-managed Codex Runs by default.

`AGENTS.md` is long-lived repository or Project guidance. It is neither a
Skill nor Feature Area or Task state, and it does not replace Project Context.

## Internal or future terms

| Term | Visibility | Meaning |
|---|---|---|
| AISession | Internal boundary that may appear in diagnostics | A real Bridge interaction context initialized without a Project, then bound once after explicit selection. The same ID is retained; it does not own Feature Areas or prove ChatGPT conversation identity. |
| Codex Thread / Turn | Internal identity that may appear in diagnostics | Codex conversation and interaction identity. It is not a Feature Area, Task, Review, or AISession. |
| Run | Internal diagnostic concept | An execution or evidence checkpoint beneath the user-facing workflow. |
| Workspace / worktree | Usually operational or diagnostic | The managed Git environment used for authorized work. |
| ExecutorProfile | Future concept | A planned execution configuration selecting provider, model, reasoning, and runtime. It is not a v0.1 user choice. |

This guide does not define an ExecutorProfile schema or promise multi-provider
configuration. Those are later-release concerns.
