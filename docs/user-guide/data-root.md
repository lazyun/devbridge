# DataRoot

One DataRoot is one independent local Dev Bridge environment. It contains the
state and operational material managed by Dev Bridge, including:

- durable state;
- managed worktrees;
- Evidence;
- logs;
- runtime state; and
- artifacts.

Your Project repositories remain external to the DataRoot. A DataRoot stores
Dev Bridge's environment around those repositories; it is not the repository
container.

## Opening and switching environments

An empty directory becomes a fresh local Dev Bridge environment. An existing,
valid DataRoot reopens its existing environment.

Switching DataRoot does not migrate, copy, merge, or delete anything from the
previous DataRoot. Different DataRoots do not share local Bridge state. When
you switch back to a previous valid DataRoot, its own durable state and history
remain there.

This makes DataRoot a useful isolation boundary, but also means that selecting
a different one is not a synchronization operation. If information appears
absent after a switch, first confirm which DataRoot is active.

The product meaning above is stable. Exact command-line flags or integration
mechanics are intentionally outside this guide until they are part of the
released interface.
