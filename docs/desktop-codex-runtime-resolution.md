# Desktop Codex runtime resolution

Starting main: `6abd2d942b29fc62636a809ade66b9a5903d592a`.

The previous Desktop probe resolved Codex and supplied the launcher's directory
on PATH, but discarded that path. Desktop's `codexExecutable` was never passed
to Core. The SQLite default Codex configuration has an empty `codex_path`, so
Review/Execution reached `startAppServer("")` and launched bare
`codex`, without the probe's environment adjustment.

Desktop now writes its current probe selection atomically to
`codex-selection.json` beside Desktop runtime state. It records the configured
input, selected absolute executable and any probe error. Core receives that
file through `DEVBRIDGE_CODEX_SELECTION_FILE` and reads it for each new runtime.
Existing SQLite Codex configurations and historical Review/Run rows are not
rewritten. Changing the Desktop executable remains possible while Core runs;
existing app-server processes keep their original executable.

Resolution precedence:

1. An explicit per-Codex-configuration path (or explicitly injected Core path)
   takes precedence and is never substituted on failure.
2. For the empty/default `codex` choice, a Desktop selection file is authoritative.
   Missing, corrupt, failed or non-executable selections fail precisely; no PATH
   fallback occurs. A resolved path stays pinned across restarts until the
   configured Desktop input changes.
3. Without a Desktop selection, an explicit command/path is resolved directly.
   Only empty/default `codex` may discover via PATH, then the existing deterministic
   candidates: bundled ChatGPT Codex on macOS, `~/.local/bin`, `~/.codex/bin`,
   `~/.volta/bin`, and NVM paths in reverse directory-name order.

`internal/codexlaunch` owns resolution, validation and executable-directory PATH
augmentation. It does not source shell profiles or launch a login shell. The
probe checks bounded version output and calls the actual `codex.Start` initialize
handshake without a Thread or Turn. Runtime launch checks that `exec.Cmd.Path`
is the selected absolute path. `GET /v1/runtime/codex` reports Core's selection
source/path/error; Desktop's status guard rejects an old or differently configured
Core and detects a selected file removed after probing. Core readiness remains
independent of Codex errors. Runtime failures continue through existing Run failure
persistence, including the exact path and cause.

Tests cover a Finder-like PATH and sibling interpreter, production control-plane
Review startup with the probed absolute executable, deleted/non-executable paths,
explicit override precedence, corrupt/failed selection persistence, old/different
Core mismatch, and fresh Review/Run identities with historical failure preservation.
The development runtime-probe command uses the same selection handoff and guard.

No AgentConversation, FeatureArea, Review, Execution, epoch, recovery,
Host Tool Contract or Development Cycle behavior is redesigned.
