# Codex conversations, identity, language, and Skills

Dev Bridge connects two different interaction boundaries and maps them
internally. Normal use does not require you to manage their identifiers.

## Identity boundary

On the ChatGPT side, an **AISession** is the current ChatGPT interaction context
bound to one explicitly selected Project:

```text
GPT <-> Dev Bridge -> AISession -> selected Project
```

An AISession does not own the Feature Area. Replacing it does not recreate the Feature Area;
a later session can explicitly select the same Project and continue a usable
existing Feature Area. How Dev Bridge records session provenance is internal.

On the Codex side, a Codex conversation is the model context used for an
operation. Dev Bridge refers to its interaction identity internally as Thread
and Turn:

```text
Dev Bridge <-> Codex -> conversation (Thread / Turn internally)
```

Codex does not use the GPT AISession identity, and GPT does not need Codex
Thread or Turn identity. Dev Bridge maps the two sides.

## Conversation lifecycle

FeatureArea owns one canonical current AgentConversation. Review,
Implementation and Verification may continue it through separate Runs and Turns.
A durable claim permits only one Agent operation at a time for that FeatureArea;
different FeatureAreas have independent contexts. Workspace contains environment
facts and does not select or own the Thread. Tasks have no context-source option.

Use agent_context_reset only when the FeatureArea is idle and usable. It preserves
the old conversation as history and creates a successor whose next operation
starts a fresh Thread. UNKNOWN work can converge through exact owner cancellation and quarantine, or agent_operation_recover,
which fences the exact old operation and leaves evidence intact. Terminal or
read-only legacy FeatureAreas cannot create future context. Project Discovery
remains separate from FeatureArea context.

The AgentConversation is the canonical FeatureArea-owned context lineage; a
backend Thread is only the adapter's continuation handle for that lineage.
Review can continue from the canonical latest safe end. v0.1 does not support
resuming Review from an arbitrary historical boundary. Operation-completion
delivery reports an already-finished operation, and a user Decision callback
submits authorization input; neither mechanism is context continuation.

## Language boundary

Dev Bridge-generated machine and system communication uses canonical English.
This includes Server Instructions, tool descriptions, system constraints,
runtime policy, errors and event names, and the Bridge-generated Codex
envelope.

User- or GPT-authored engineering content is passed through as provided. Dev
Bridge does not automatically translate or normalize the language of the
engineering request. English Bridge constraints combined with a Chinese user
request are therefore valid.

The human-facing Desktop and Web Console support Simplified Chinese and English
localization.

## Skills and project guidance

A Codex Skill is an optional specialized capability or instruction package. It
is not an AgentRole. Bridge-managed Codex Runs do not inherit arbitrary
user-global Skills or plugins by default. This reduces ambient instruction
pollution and makes execution more reproducible, predictable, and diagnosable.

Project-owned guidance such as `AGENTS.md` is different from a user-global
Skill. It describes durable rules belonging to the repository or Project.

An explicit opt-in mechanism for additional Skills may be possible in a future
release, but v0.1 does not promise a user-facing Skill allowlist.
