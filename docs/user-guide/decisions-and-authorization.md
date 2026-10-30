# Decisions and authorization

Review Approval resolves the current Task review gate:

- APPROVE freezes the initial immutable revision and makes implementation ready.
- REVISE requires a new Review under the same Task.
- REJECT cancels the Task.

Configured GPT and upper LLM adapters are trusted to represent user intent.
GPT MCP, Local MCP, Card and Web can resolve the same Decision. Bridge checks the
exact Project/session, FeatureArea, Review result, current version, lifecycle
and request identity. It does not require proof of a physical human click.

After approval, explicitly start the Task with execution_start.
Material scope conflict stops the Execution and requires a new semantic decision;
the immutable approved definition cannot change silently.

Reuse requestId for a retry after response loss. A durable receipt identifies
the exact decision, option and result; replay returns that receipt and current
workflow guidance. A superseding Review or RETIRE invalidates an old approval
action. Guidance is observational and never resolves a Decision automatically.

Codex may still request a runtime permission during Review,
Implementation or Verification. These retain their specific allowed choices,
transport acknowledgement, cancellation and recovery semantics. They cannot
authorize an earlier missing Review or Task approval.

The main Task Card uses the same Core decisions as MCP, Local MCP and Web.
The standalone runtime-permission Card has no TaskPlan confirmation or Execution
authorization action. Host security confirmation remains separate from Bridge's
durable business decision.
