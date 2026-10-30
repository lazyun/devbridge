# Direct File Capability v0.1

> Project-centric update (schema v76): [current contracts and remaining boundaries](project-centric-multi-repository-closure.md). Multi-member Upper Review and v2 NO_START approval are complete. Explicitly started v2 DIRECT_FILE Tasks support exact member permissions, formal results/acceptance and recoverable per-repository Task commits and FeatureArea merges. Coding Agent multi-root execution remains unsupported. Workspace membership never grants access; the legacy single-repository descriptions below remain compatibility behavior.

## Task-owned protocol v2 (schema v76)

A formally approved DIRECT_FILE definition remains NO_START until the user consumes
an exact `EXECUTION / START_DIRECT_FILE` confirmation. Core validates the persisted
CONFIRMING intent and its exact command request; a pending card or arbitrary intent
ID cannot start. `RECOVER_DIRECT_FILE` and `CANCEL_DIRECT_FILE` use that same formal
boundary. Generic enable/disable and `workflowComplete` cannot control v2 grants.
No synthetic Coding Execution, Run, Thread or backend handoff is created.

The immutable revision/approval freezes Repository/member/version and READ plus
WRITE(NONE/PATHS/REPOSITORY). Every file request names its member and current
epoch/generation. All required members must be ready before ACTIVE; omitted or
newly added members have no authority. Each member pins its worktree/root identity,
origin, start HEAD and start snapshot separately. Cross-repository MOVE is refused.
Restart requires explicit recovery, preserving the original members and observations;
GET and old start receipts cannot reactivate closed authorization.

Mutations require a stable request ID; replace/delete/move also require the observed
content digest. Durable intent precedes the file effect. A synced effect journal
supports recovery between filesystem success and SQLite receipt completion. Exact
replay returns the original result without repeating the effect, even if a later
legal operation changed the path. Missing proof remains unresolved and blocks
result/cancel; path existence alone is insufficient. Known pre-effect failures can
finish FAILED without leaving an unknown effect. There is no cross-file transaction.

File operations and result/cancel share the canonical exclusion boundary. Core
collects the full frozen member evidence, checks HEAD and receipt-backed final bytes,
rejects uncontrolled changes, and atomically freezes the result, closes the grant
and advances the Task to ACCEPTANCE_PENDING. Member statements in input are advisory;
Core-observed output is authoritative. Canonical acceptance persists the complete
per-repository delivery intent in the same transaction as Task success. READ-only
and unchanged members are explicit NO_CHANGE with no fabricated commit.

Required RUN verification is unsupported and blocked before start. File readback is
not a test run. Only an explicit user confirmation may select an allowed USER_WAIVED
NONE contract; the acceptance fixture's waiver gives no authority for user projects.
Task commits use exact result paths and a private Git index. FeatureArea merges need
a separate confirmation freezing source, target, expected HEAD and complete pending
Task provenance. Each Git effect has its own receipt; partial/unknown delivery is
recoverable without rollback or switching to a newer branch mapping.

## Legacy single-repository capability

The [unified Review executor contract](unified-review-executors.md) extends this document with
Task-owned UPPER_LLM Reviews, strict read grants, formal report submission and
atomic result/permission/claim closure. Generic Direct File writes and Coding Agent
configuration retain their existing meanings.

Direct File Capability is disabled by default and may only be enabled after explicit user authorization represented by an Upper LLM enable call.

Direct File and writable Coding Agent operations are mutually exclusive for the same canonical worktree.

Direct File mutation supports whole-file creation/replacement only. Missing parent directories for create/move destinations are created automatically inside the canonical worktree.

## Authority and lifecycle

Codex remains the default code modification path. The Upper LLM decides whether the user explicitly authorized direct file operations; Core does not interpret conversation text. Tool discovery does not grant authority.

`direct_file_enable` binds one `aiSessionId`, its immutable Project, an exact `featureAreaId`, Repository, current Workspace and canonical Worktree, and an explicit `workflowId`. Use a fresh workflow ID for every unrelated user request. The capability is process-local, disabled after Core restart, and never saved as a user preference. Duplicate enable does not stack authorization.

The Upper LLM calls enable, performs a contiguous group of file calls, then either sets `workflowComplete: true` on the final file call or calls `direct_file_disable`. Completion revokes the capability automatically, including a failing final operation. Cancellation or finishing a workflow without another file call requires explicit disable. A transport disconnect is not a completion signal: both MCP adapters are stateless. No heartbeat, lease, natural-language completion detector, or persistent lock is introduced. Each file call must present the exact workflow authorization; a different workflow cannot borrow the existing grant.

Core checks the existing FeatureArea claims, active Runs, backend quiescence/recovery and pending Git finalization/submission facts before enable. Enable, file operations and agent admission share the existing SQLite writer serialization boundary. An enabled grant prevents same-area agent admission before any new Run/backend starts; Review keeps the existing same-FeatureArea serialization. Another FeatureArea's independent worktree remains admissible.

## Filesystem contract

The consolidated surface exposes enable/disable, two reads and one closed write tool:

| Tool | Contract |
| --- | --- |
| `direct_file_enable` | Explicit temporary workflow authorization; no caller-supplied filesystem root. |
| `direct_file_disable` | Revoke this workflow's authorization. |
| `file_list` | One exact relative directory, nonrecursive, bounded entries with name/type and truncation metadata. `.` lists the worktree root. |
| `file_read` | One regular file, complete content and byte size, no trimming or newline normalization. |
| `file_write(action="CREATE")` | Whole new file, parents created as necessary, existing target rejected. |
| `file_write(action="REPLACE")` | Whole existing regular file replaced atomically; missing target and directories rejected. |
| `file_write(action="DELETE")` | One existing regular file; no directory recursion or empty-parent cleanup. |
| `file_write(action="MOVE")` | Existing regular file to absent destination in the same worktree; create missing destination parents; no overwrite or cross-filesystem copy fallback. |

Every file operation, including read and list, reject calls without active authorization. Limits are Core constants: 256 KiB read, 256 KiB decoded create/replace content, 256 list entries, 4096 path bytes; scope identifiers are bounded to 128 bytes. Content is UTF-8 by default; base64 carries arbitrary bytes without JSON replacement of invalid UTF-8. Size limits apply to decoded content, inclusive at exactly 256 KiB. Base64 input has a 349528-byte upper bound including necessary padding before allocation; Core decodes and then applies the same 256 KiB content limit used for raw UTF-8. Invalid base64 returns INVALID_FILE_TARGET, and oversized encoded or decoded input returns FILE_TOO_LARGE. No MIME detection is performed.

Paths are relative to the exact canonical worktree selected by Core. Absolute paths, escaping traversal, other FeatureArea worktrees and caller-supplied roots are rejected. A shared resolver validates path components; opened directory capabilities bound filesystem access. The v0.1 symlink policy rejects symlinks in paths; listing may report a symlink name/type without following it. Git administration paths are protected. External IDE/user writes remain a v0.1 boundary; no file revision/CAS or general transaction coordinator is added.

Create writes and syncs a same-directory temporary file and publishes without clobbering an existing destination. Replace writes and syncs a same-directory temporary file before replacing the existing file. Failure cleans temporary files and does not publish partial contents. Move uses the platform's exclusive rename primitive, failing explicitly if unsupported or across filesystems. It does not fall back to copy/delete. Darwin and Linux are supported; file operations fail closed on other platforms. Replacement preserves the regular file’s permission bits.

## Errors and preserved contracts

Errors are typed Core domain errors projected as machine-readable codes: `DIRECT_FILE_CAPABILITY_NOT_ENABLED`, `DIRECT_FILE_CAPABILITY_ACTIVE`, `CODEX_OPERATION_ACTIVE`, `DIRECT_FILE_WORKTREE_UNAVAILABLE`, `INVALID_RELATIVE_PATH`, `PATH_OUTSIDE_WORKTREE`, `SYMLINK_ESCAPE`, `FILE_NOT_FOUND`, `FILE_ALREADY_EXISTS`, `INVALID_FILE_TARGET`, `DESTINATION_ALREADY_EXISTS`, `FILE_TOO_LARGE`, `FILE_OPERATION_FAILED`. Existing destinations use `FILE_ALREADY_EXISTS` consistently; the destination alias is reserved in the domain vocabulary.

Patch, edit, append, text replacement, line mutation, arbitrary shell execution and permission-changing tools are outside this interface. File tools perform no Git commands, staging, commits or ref mutation. Task auto commit and FeatureArea submission retain their existing authority.

Direct File adds no schema changes or new durable records. Task-owned v46, FAST/DEEP, Review 1:1 Run, Task-first presentation and Prompt Neutrality remain unchanged. No Codex instruction builder, final prompt, prompt byte identity or backend authorization ceiling is changed. A small Direct File activity indicator exposes runtime state without offering a default enable button. Production recall host remains `HOST_INTEGRATION_PENDING`.
