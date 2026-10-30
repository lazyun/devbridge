# Codex platform defaults policy causal verification — 2026-09-16

> **Current product disposition — CODEX_SANDBOX_HARDENING_DEFERRED.** This
> document preserves the original investigation, controls, failures, hashes,
> and candidate work. Under Trusted Executor Model v1 these observations are
> development and operations diagnostics rather than Coding admission
> requirements. Dev Bridge still passes the backend's supported workspace
> permission fields and never auto-selects unrestricted access, but it does not
> claim a hostile OS boundary against a malicious Coding Agent. Production
> admission is decided by exact RuntimeWorkset/Client capability,
> managed-worktree provenance, per-member result evidence, Verification,
> Acceptance, and delivery gates.

**CODEX_OFFLINE_ISOLATION_CAUSE_UNRESOLVED** remains the local causal status.
The fixed source establishes a specific policy-construction defect candidate:
`:minimal/read` includes independent temporary-directory read/write grants, while
path DENY narrows dynamic allow rules rather than emitting a final path deny.
A local single-factor A/B/A has **not** been obtained. This is now a source-level
mechanism finding, not an unspecified “platform problem”.

| Evidence dimension | Current result |
| --- | --- |
| Mechanism | **Established in fixed source; local causal effect not proved** |
| Installed helper's final SBPL/definitions | **Not directly obtained** |
| Minimal candidate | **Source patch prepared; not built or behaviorally verified** |
| Production Coding | **Runtime adapter implemented; full integration closure pending** |

## Current baseline and preserved scope

Start: branch `dev_20260908_1913`, HEAD
`fd8a6afeb213972e8aed00ff23f090ae058b8530`, schema v76, **191 dirty files**,
all unstaged, empty index. Byte snapshot:
`/private/tmp/devbridge-platform-baseline-s6h091e5` (1,754 files).
The current increment is measured against this snapshot, not HEAD. Final counts
and file hashes are in [increment evidence](evidence/codex-platform-defaults/increment.json).
End: **202 unstaged dirty files**, empty index, same branch/HEAD/schema; **15 current
changes (4 existing, 11 new)** and no removals relative to starting bytes.
No production Go, planner/wire, schema, runtime admission or Client code changed.
No live database/DataRoot/config/credentials, installed binary, real model,
Thread/Turn, UI, devices or official-repository Git mutation occurred.
Public downloads were isolated and separate from the offline probe; no helper
permission escalation or retry was attempted in this increment.

## Independent-terminal evidence incorporated without rewriting it

用户报告从独立终端运行；上传记录已核验；启动来源来自用户说明，不是脚本自动取得的进程祖先证明。

The exact original directory was available:
`/private/tmp/devbridge-independent-20260916-223513-45476`.
Original `report.json` SHA-256 was rechecked as
`780fc0bc39bcc284dc53820754e05c58d229f8c4532f9521e13a318461418e9b`.
The [portable raw archive](evidence/codex-platform-defaults/independent-evidence.json)
preserves report/controller and all six config/payload/result byte strings with
hashes, including the old `independentLaunchContext=EVIDENCE_PENDING` field.
That field is a constant in the old controller, not launch-origin detection.

Baseline/repeat/canonical-alias/tmpdir-only each allowed the app read and also
allowed the excluded temporary target. Normal-target allowed app and denied the
excluded ordinary-directory target. Undefined-profile never entered the helper.
Thus **10 actual reads: 6 matches, 4 unexpected successes**; no writes or full
72-case acceptance is inferred. Before/after target identity and bytes match in
these read controls. This experiment was not rerun.

The named tar archive was **not reacquired**, so its stated SHA-256
`a2c7902a9f3c578057962954c610e848d95208220e838990e7a73b17ab02b5ce`
was not reverified. The four named companion review materials were not reacquired;
the exact original output directory supplied the verified records. No broad HOME
search or Mac `/mnt/data` assumption was used.

## Fixed source and installed native identity

Official annotated tag `rust-v0.149.0` resolves through tag object
`a4e15bf371341b067c8278d3b70b1a8c7b3d793e` to immutable commit
`758ef40f50c1a458425c7cfbf1eb12cbc07af0b0`. The API reports an unsigned tag;
this is a checked tag/commit relation, not a signed build attestation.
[Official release](https://github.com/openai/codex/releases/tag/rust-v0.149.0).

The official `codex-aarch64-apple-darwin.tar.gz` has SHA-256
`0cef4f9629af7b6bcc4b4de2adb63337d1e77a00a811e66281da5356e3e74fc6`,
matching the release asset digest. Its extracted native is **byte-identical** to
the installed native: SHA-256
`f4a74117b8142cda581c95ff753abf4508b5636d89682c1ed77e4a9249af8963`.
The installed launcher remains
`134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477`;
new actual version output is `codex-cli 0.149.0`.
Both installed and extracted binaries return exit 1 from `codesign --verify --strict`
with “invalid signature (code or signature have been modified)”. Nothing was
re-signed or replaced. Byte identity with the published asset does not prove a
reproducible source build; no build attestation was acquired. Downloaded native
was inspected but never executed.

[Fixed-source evidence](evidence/codex-platform-defaults/fixed-source-evidence.json)
contains original release/tag metadata, source archive digest, full relevant source
files and all three static SBPL files, with original paths and SHA-256. Sources
are preserved with upstream LICENSE/NOTICE. No current-main source substitutes.
The prior 404s were wrong paths; correct fixed paths are `sandboxing/src/seatbelt.rs`
and `cli/src/debug_sandbox.rs`.

## Input → intermediate fact → rule → observed boundary

All line references below refer to that immutable commit. The original exact
profile uses `:root=deny`, `:minimal=read`, tmp selectors/path denies, explicit
app/sdk permissions, metadata READ and exact scratch WRITE, with network disabled.

| Input | Intermediate / generated rule | Intended boundary and current evidence |
| --- | --- | --- |
| `:minimal = read` | `include_platform_defaults()` checks restricted kind, non-full-disk read and readable Minimal; appends full platform SBPL | Independent four global tmp READ/WRITE grants exist in source. Their local causal effect is not yet isolated. |
| `:root = deny` | Root resolves to the cwd volume root; root is deliberately filtered from unreadable roots | Base `(deny default)` plus explicit grants supplies restricted behavior; no final blanket root deny that could erase legal subroots. |
| `/absolute/path = deny` | `get_unreadable_roots_with_cwd` resolves/canonicalizes effective denies | Nested unreadable paths become `require-not literal/subpath` inside dynamic READ allows; dynamic WRITE uses readonly subpaths. Not an independent final deny over platform grants. |
| `:tmpdir = deny`, `:slash_tmp = deny` | Absolute nonempty process TMPDIR or Unix `/tmp`; same resolved-path deny branch | Does not switch off the independently included `/tmp`, `/private/tmp`, `/var/tmp`, `/private/var/tmp` SBPL rules. Independent TMPDIR-only control still reads the excluded tmp object. |
| app/sdk READ, app PATHS WRITE, metadata READ, exact scratch WRITE | Read/write root parameters, write readonly exclusions and metadata protection expressions | Correct dynamic masks alone do not constrain a separate platform tmp WRITE allow. Original 17 failed writes remain observations, not all causally attributed. |
| glob DENY (not used in original profile) | Only `GlobPattern` enters `get_unreadable_globs_with_cwd`; emitted anchored regex READ/WRITE denies plus ancestor unlink protection | Different code path; supports translated escaping/globs and canonical static-prefix variants. No glob candidate or coverage certification was run. |

TOML parses in `config/src/permissions_toml.rs:224–242`; the actual keys compile in
`core/src/config/permissions.rs:522–603,778–791`, including the four named special
paths. Config selection/materialization is in `core/src/config/mod.rs:3459–3484`;
workspace root expansion is in protocol permissions `953–1000`. The relevant
permission methods are `845–857`, `1133–1150`, `1265–1311`, `1429+`, `1660–1698`.
[Fixed permission source](https://github.com/openai/codex/blob/758ef40f50c1a458425c7cfbf1eb12cbc07af0b0/codex-rs/protocol/src/permissions.rs).

The CLI route is `cli/src/main.rs:1644` → `run_command_under_seatbelt` →
`run_command_under_sandbox` → the normal config parser/effective profile →
`create_seatbelt_command_args` (`debug_sandbox.rs:380–409`). That wrapper selects
**Process**, not FileSystemHelper. `SandboxManager::new` also defaults Process;
`exec-server/process_sandbox.rs` uses it, whereas `exec-server/fs_sandbox.rs:118–124`
selects `for_file_system_helpers`. Both share platform tmp defaults. Process adds
an extra `/Applications` READ policy; FileSystemHelper does not. The CLI probe
cannot establish actual FS-tool-channel enforcement.

Full assembly in `sandboxing/src/seatbelt.rs:897–943` is: **base → dynamic READ →
dynamic WRITE → network → conditional platform defaults → Process Applications
READ → glob DENY → protected-ancestor unlink DENY**. Then `-p`, the whole joined
policy, all `-Dkey=path` definitions, `--` and command are passed to absolute
`/usr/bin/sandbox-exec`. `spawn_debug_sandbox_child` clears the environment,
applies the generated child map and network/sandbox flags, and uses configured cwd.
[Fixed generator](https://github.com/openai/codex/blob/758ef40f50c1a458425c7cfbf1eb12cbc07af0b0/codex-rs/sandboxing/src/seatbelt.rs).

This audit includes the **whole relevant composition**, not only tmp lines:

- Base starts closed, grants process operations, restricted sysctl/IPC/device
  resources and PTY extensions. It has no broad tmp filesystem grant.
- Dynamic READ/WRITE contain parameterized approved roots and their local
  exclusions; WRITE also protects metadata and root/ancestor unlink operations.
- The network generator selects policy by network/proxy/unix-socket inputs; this
  probe supplies restricted network and no managed proxy/unix socket overrides.
  The enabled-network static file is archived but is not claimed to be active.
- Platform defaults include system libraries/frameworks and executable mapping,
  preferences/configuration reads, devices, metadata traversal, mach/IPC services,
  local syslog, **four tmp subtree READ/WRITE allows**, and app sandbox extension
  consumption. These extension and broad runtime grants remain relevant limitations
  even after removing the four tmp grants.
- Glob denies and protected-directory unlink denies occur after those defaults.
  No assertion that “all Seatbelt denies lose to allows” follows from this code.

## Policy acquisition and bounded new-factor experiment

There is no supported full-policy export in the inspected installed help or fixed
CLI source. `--log-denials` collects denial logs, not complete SBPL/definitions;
`sandbox-state-json` is input, not policy output. No invented flag, PATH hijack,
process injection or system-helper replacement was used.

The [provenance ledger](evidence/codex-platform-defaults/policy-provenance.json)
explicitly separates **installed actual output**, **original fixed-generator
output**, **observation-build output**, and **modified experimental policy**.
**None of those four complete-policy outputs was obtained this round.** The full
static fragments and generator source are archived; they are not presented as a
final generated policy. Exact installed CLI argv/config/payload/cwd/environment,
native identity and target before/after effects are retained in
[baseline raw evidence](evidence/codex-platform-defaults/installed-baseline-evidence.json).
The embedded controller is the immutable version actually run, before subsequent
review hardening; current source is separately hashed in the increment manifest.
The inner Seatbelt SBPL and `-D` definitions remain the precise missing evidence.

An [observation-only patch](evidence/codex-platform-defaults/observe-cli-policy.patch)
now exports the original parser/generator's full argv, policy cwd, child cwd,
complete test-owned child environment, filesystem facts and network flags before
spawn. It rejects network/proxy configuration before proxy/logger startup,
creates a new mode-0600 output, and exits without running the command. It does
not generate replacement SBPL. Standard `cargo`/`rustc` lookup and explicit
common paths are unavailable here; no Rust tools/dependencies were installed.
Patch application dry-run succeeds, but **compilation and export are not done**.

The one new installed invocation used fresh fixtures and the original permission
mapping with allowed app READ, scratch WRITE, excluded tmp READ and WRITE. Result:
**A: exit 71, `sandbox_apply: Operation not permitted`, entered=false, 0 actual
operations; B and A′: NOT_RUN**. None of four cases is PASS. No second startup,
permission escalation or repetition of the old independent experiment followed.

[New-factor controller](../../scripts/probe_codex_platform_defaults.py) provides
the bounded manual continuation: export original A once from an isolated build,
replay A, delete exactly the four whole tmp grant lines in B, replay byte-identical
A′. All other policy bytes, definitions, payload, cwd, environment, target inode,
starting bytes and write markers stay fixed. Duplicate/missing grant lines,
wrong network/profile/command/cwd/environment or failed controls stop or invalidate
it. The known fixed-source arg0 PATH prefix is permitted only inside the new
CODEX_HOME's alias directory and is retained unchanged for all three replays.
Both allowed controls must work and both excluded read/write cases must reverse
and recover before a mechanism result is reported. Exporter hash and claimed
commit are recorded, but source binding is explicitly **SELF_REPORTED_NOT_BUILD_VERIFIED**;
a caller-supplied exporter is not a build attestation.

This continuation is a **system Seatbelt policy mechanism experiment**, not the
installed helper being repaired. There is no remove-`:minimal` multi-variable
experiment, new runtime grant, glob blacklist, global tmp deny or rewritten
expected outcome. [Exact isolated build/manual instructions](evidence/codex-platform-defaults/README.md)
cover this new factor only. The 11 passing Python tests are controller regressions
(including mocked A/B/A), never runtime isolation evidence.

## Candidate, coverage and exact remaining gates

[Minimal source candidate](evidence/codex-platform-defaults/remove-implicit-temp-grants.patch)
removes four unconditional temporary subtree grants from
`restricted_read_only_platform_defaults.sbpl`, keeping all other platform/runtime
and dynamic rules. No excluded target is enumerated. Legal tmp members and exact
scratch should continue through their explicit dynamic grants; that is a source
expectation, **not yet runtime verified**. The patch affects both Process and
FileSystemHelper platform defaults globally and can affect tools that formerly
relied on arbitrary tmp access. Such tools must use approved scratch; arbitrary
tmp access must not be restored as an implicit permission.

A proposed upstream generator regression checks both profiles, absence of the
four static grants and retained dynamic temporary workspace read/write parameters.
It does not count `sandbox_apply` failure as successful denial. Neither this Rust
regression nor the patch has compiled here. Existing upstream `assert_seatbelt_denied`
accepts sandbox startup failure; it was deliberately not used as acceptance evidence.

No Dev Bridge mapping defect was demonstrated. No existing configuration was
verified to remove only those implicit grants while preserving all intended
boundaries. Removing Minimal also removes runtime resources; adding runtime roots
would introduce extra variables. This round does not claim that every possible
existing configuration is mathematically impossible.

The source finding explains how a separate tmp allow can reopen **both read and
write classes** despite dynamic exclusions, consistent with the independent reads
and original matrix. It does **not** causally assign every one of the 9 read and
17 write failures. App sandbox extensions and remaining platform/runtime grants
need separate boundary review. No write cause is declared proved from read-only
independent evidence.

Original matrix remains **46/72 matched; 26 unexpected successes**. Candidate
matrix is **NOT_RUN**, because no executable verified candidate exists. Added
alias/symlink/new-after-policy cases are **NOT_RUN**, not SKIP/PASS. The old 72 IDs,
layouts and expectations are unchanged. Seven execution fixture repairs remain
inherited PASS; they and unrelated Go packages were not rerun. The two extra old
failures remain inherited and outside this increment.

Before Client/runtime binding: obtain an auditable isolated build and real complete
policy/definitions; run effective positive-control read/write A/B/A; verify the
candidate with all original 72 cases and separately counted boundary aliases,
external symlinks and newly created targets; verify necessary runtime exemptions,
readonly/PATHS/metadata/scratch behavior and no inherited-extension overgrant.
Then separately establish other tool channels and same-Thread narrowing. Even a
complete offline candidate pass would not authorize production or claim real E2E.

Retained: **PARTIAL_IMPLEMENTATION**, **TRUSTED_EXECUTOR_MODEL_V1**,
**CODEX_SANDBOX_HARDENING_DEFERRED**, **REAL_CODING_AGENT_E2E_NOT_RUN**,
**SAFE_REPOSITORY_UNLINK_CLOSED**,
**MANUAL_DESKTOP_ACCEPTANCE_PENDING**, **MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING**.
The production Client/workset path is no longer blocked; the narrower milestone
is **MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED**.

---

## Historical prior increment (184 → 191 dirty files)

The remainder preserves the preceding investigation. Its source-correspondence
and independent-evidence pending statements are superseded by the current section;
its original raw records and the 72-case failures remain unchanged.

### Prior diagnosis and execution fixture repair — 2026-09-16

**CODEX_OFFLINE_ISOLATION_CAUSE_UNRESOLVED**. The cause is narrowed to target
location/object class in this host's official helper invocation. The complete
matrix still fails: **46 matching expectations, 26 unexpected successful accesses**.
No multi-repository Coding production permission was enabled.

## Baseline and scope

Branch `dev_20260908_1913`, HEAD `fd8a6afeb213972e8aed00ff23f090ae058b8530`,
empty index, **184 inherited dirty files**, schema **v76**. Starting byte copies
and SHA-256 manifest: `/private/tmp/devbridge-isolation-baseline-xpbzc920/`
(1,750 files). Increment accounting compares these bytes, not historical HEAD.

Changes are limited to the offline diagnostic runner, its regression tests,
execution test fixtures and documentation/evidence. The pure experimental planner,
wire DTOs, capability admission and evidence identity were inspected; no defect
attributable to those production modules was established, so they were not changed.
No database migration, live DataRoot/SQLite/runtime operation, global configuration,
credentials, binary upgrade, real model, app-server Thread/Turn, UI or device action
was performed. Git initialization/add/commit is confined to new probe/test-owned
repositories. No official repository stage/commit/merge/push/reset/clean/rebase.

## Preserved evidence and exact identity

The portable [raw evidence bundle](evidence/codex-offline-isolation-v2.json) preserves
original report bytes and six TOMLs, the new complete matrix and its exact profiles
and payloads, the final minimal controls, and the restricted-context startup failure.
Each embedded file includes its original source path and SHA-256. The matrix has
`caseMapping` for all 72 old and new IDs, with identical expected outcomes. The
actual final controller source is also embedded. Temporary fixture trees were
removed only after external observations were captured; retained evidence is not
an assertion that those trees still exist.

Original report was re-read, not inferred from a previous document:
`/private/tmp/devbridge-coding-offline-final/report.json`, SHA-256
`4baa0c74f7a32b14d6b069c0b3455c0a3cfefa73bb2bd293cd603018f9d07b55`.
All six original TOML hashes match that report. Original timestamps, per-operation
return codes and content/inode evidence were **not recorded**; they are not
retroactively invented.

Installed version is `codex-cli 0.149.0`, Darwin 25.6.0 arm64. The inspected launcher
and native identities still match the original evidence:

- Launcher: `/Users/phoenix/.nvm/versions/node/v23.6.0/bin/codex`, resolving to
  `/Users/phoenix/.nvm/versions/node/v23.6.0/lib/node_modules/@openai/codex/bin/codex.js`;
  SHA-256 `134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477`.
- Native: `/Users/phoenix/.nvm/versions/node/v23.6.0/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex`;
  SHA-256 `f4a74117b8142cda581c95ff753abf4508b5636d89682c1ed77e4a9249af8963`.

The actual installed `sandbox --help` identifies `-P` as named permissions selection
and `-C` as the resolution/command working directory. No legacy `sandbox_mode`,
`sandboxPolicy`, `--sandbox-state-json`, runtimeWorkspaceRoots or managed-config
switch is supplied to the helper. The explicit isolated TOML bytes and hashes are
recorded; **complete effective configuration is not introspected**. Invalid `-P`
is rejected, which proves name lookup, not every policy's effective enforcement.
Exact binary-to-source revision correspondence remains **UNVERIFIED**. Neither
current main-branch source nor current documentation was substituted for 0.149.0
implementation evidence. Existing generated schema evidence is retained separately
in [capability evidence](coding-runtime-capability-evidence.md).

## Probe defects corrected without changing expectations

The old wrapper discarded read content and all operation errors. A nonzero cat or
redirect result became DENY regardless of whether the cause was permission denial,
missing target or missing executable. Successful writes had no external effect
check. It reused modified files across policy groups and could corrupt its own
Git config after a failed denial. These are evidence defects; they do not prove
that the old 26 unexpected successes were false positives.

Probe v2 now records stable `layout/policy/operation:role` IDs, timestamp, source and
controller/payload hashes, launcher/native identity, actual argv and cwd, isolated
environment, exact supplied config/profile hashes, canonical path/device/inode,
pre-existence/content and poststate. Per-case fields inherit binary/config/time
identity from their enclosing helper and report. Reads must return the generated
unique marker exactly. The external controller checks each write against its
unique written marker. Every group retains successful allowed access controls.

A confirmed denial requires an existing target, entered helper, completed command,
permission error naming the exact target, empty output and (for writes) unchanged
content/inode. Other errors remain `COMMAND_FAILED_OTHER`, `TOOL_UNAVAILABLE`,
`COMMAND_NOT_EXECUTED`, `FIXTURE_INVALID`, `HELPER_STARTUP_FAILED` or
`RESULT_INCONSISTENT`; none counts as a denial pass. A platform error that omits the
path is non-confirming, even if it might have been a real denial. Duplicate/missing
records and stdout/rc/effect contradictions fail explicitly. No `|| true` is used.

Fixtures distinguish app/sdk, excluded backend, scratch, configuration and evidence.
Excluded repository aliases resolve to the recorded object. Explicit canonical grant
ancestors are recorded for each target. `.git/.codex/.agents` remain protected, and
all writes target pre-existing fixture files. Matrix files are reset before each
policy; no reset touches immutable Git objects. An initial v2 reset attempted to
rewrite a readonly test Git object and aborted before any matrix helper ran
(`/private/tmp/devbridge-isolation-matrix-final/report.json`). That fixture defect
was fixed and has a regression test; the aborted run is not a capability result.

The helper payload exits zero after reporting every command status; the external
controller determines the safety verdict and exits **1** for the 26 mismatches.
Helper rc=0 is therefore never presented as isolation success. Six Python tests
cover evidence failures, content/effect contradictions, controls, the 72-ID shape,
and readonly Git reset exclusion.

## Final six-group matrix and the original 26 failures

Final raw source: `/private/tmp/devbridge-isolation-matrix-verified/report.json`.
All six helpers entered, all positive controls passed, and all 72 target operations
executed. **26 ALLOW_MATCH + 20 DENY_MATCH + 26 ACCESS_SUCCEEDED_WHEN_DENIED**.
There are no helper-startup, missing-target, tool, parsing or unexpected-denial
failures in this final matrix. Every original case has the same outcome in v2.

| Layout | Policy | Total | Match | Unexpected access success | Exact failing operations/roles |
| --- | --- | ---: | ---: | ---: | --- |
| workspace-local | both-write | 13 | 11 | 2 | read/write excluded-private-tmp |
| workspace-local | mixed-paths | 13 | 11 | 2 | read/write excluded-private-tmp |
| workspace-local | read-only | 13 | 11 | 2 | read/write excluded-private-tmp |
| private-tmp | both-write | 11 | 6 | 5 | read absent, read symlink; write absent, git-metadata, parent |
| private-tmp | mixed-paths | 11 | 4 | 7 | preceding five + write app-parent, sdk |
| private-tmp | read-only | 11 | 3 | 8 | preceding seven + write app-path |
| **Total** | | **72** | **46** | **26** | **9 reads, 17 writes** |

These are different failed boundaries: 9 excluded/alias reads, 6 excluded repository
writes, 3 metadata writes, 3 outside-member parent writes, and 5 writes disallowed by
member/PATHS policy. The corresponding workspace-local targets are denied; their
exact rc/error or poststate is in the bundle. The original full matrix remains the
representative write/metadata/PATHS reproduction. The smallest causal experiment
selects one excluded read; it does **not** establish one common cause for all 26.

## Minimal controls and limits of the causal conclusion

Final minimal source: `/private/tmp/devbridge-isolation-minimal-final/report.json`.
The helper reads one allowed member marker and one excluded marker. The normal and
temporary excluded files have identical bytes/hash. Across baseline/repeat/alias/
normal-target controls, the config, profile, payload, cwd, environment and binary
are identical. Only the target argument changes in alias/normal-target controls.
The normal-target control changes the addressed object/location; it does not claim
to keep that object's inode constant. The alias control does keep the inode constant.

| Control | Only intentional change from baseline | Observation |
| --- | --- | --- |
| baseline | none | app read succeeds; excluded `/private/tmp` read succeeds |
| repeat | none | identical outcome and content |
| canonical-alias | target spelling `/tmp` | same canonical device/inode, still succeeds |
| normal-target | excluded target object/path | same content, exact-path permission error; denial confirmed |
| tmpdir-only | TMPDIR to normal scratch/tmp | excluded temporary target still succeeds |
| undefined-profile | `-P not-a-profile` | profile lookup fails before payload; not an isolation result |

The explicit profile denies `:root`, `:tmpdir`, `:slash_tmp`, `/tmp` and `/private/tmp`;
its positive app/sdk/scratch roots do not contain the excluded temporary target.
The minimal result excludes content-marker mistakes, alias spelling alone and
TMPDIR alone as sufficient explanations **in this invocation context**. It shows
normal and temporary target differences while holding the former matrix's cwd,
scratch and profile-root differences constant. It does not establish whether
special/minimal policy generation, other loaded configuration, native helper
behavior or inherited host sandbox extensions cause those differences.

A default restricted launch failed before `HELPER_ENTERED` with Seatbelt exit 71,
`sandbox_apply: Operation not permitted`. Its results are preserved and classified
as startup failures. Subsequent offline helper processes used approved sandbox
escalation solely because nested Seatbelt could not start. The official helper
still applies the unchanged denial profile. **Neither launch is an independent
terminal context**. Clearing the child environment and closing inherited file
descriptors do not erase inherited sandbox extensions. Independent parent context
is **EVIDENCE_PENDING**; no all-platform, all-tool or upstream-confirmed bug claim
is made. Once this boundary was reached, no extra policy matrix was added.

## Smallest next independent control

Run the self-contained standard-library Python controller from an independently
opened terminal. It touches only two new `.devbridge-offline-workset-*` trees and
a new evidence directory, cleans only those two fixture trees, and retains evidence.
It does not call a model, app-server, network client or global configuration writer.
The script's `--dry-run` was run locally; it confirms these boundaries and a 30-second
per-subprocess timeout. Runtime filesystem effects and the unique content checks
are then produced by the actual non-dry run.

```sh
cd /Users/phoenix/Documents/devbridge
python3 scripts/probe_codex_workset.py \
  --executable /Users/phoenix/.nvm/versions/node/v23.6.0/bin/codex \
  --native-binary /Users/phoenix/.nvm/versions/node/v23.6.0/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex \
  --fixture-parent /Users/phoenix/Documents/devbridge \
  --output "/private/tmp/devbridge-independent-$(date +%Y%m%d-%H%M%S)" \
  --mode minimal --sandbox --dry-run
```

Remove only `--dry-run` to execute. The output directory must not already exist.
Return the generated report and its retained per-control config/payloads. Exit 1 is
expected when denied access succeeds (and for the intentional undefined-profile
control); inspect the per-control classifications rather than treating aggregate
exit alone as the answer. The agent did not open Terminal or run this independent
context on the user's behalf. A different result is a parent-context correlation,
not by itself proof of which extension/implementation mechanism is responsible.

## Execution fixtures and retained production gates

**LEGACY_EXECUTION_FIXTURES_REPAIRED**: all seven specified tests pass.
Execution fixture validation and final increment accounting are recorded in the
[current execution report](project-centric-multi-repository-execution-report.md).
These are fake-backend protocol regressions, not multi-repository permission proof.

Required statuses remain:

- **PARTIAL_IMPLEMENTATION**
- **MULTI_REPOSITORY_CODING_RUNTIME_BLOCKED**
- **REAL_CODING_AGENT_E2E_NOT_RUN**
- **SAFE_REPOSITORY_UNLINK_CLOSED**
- **MANUAL_DESKTOP_ACCEPTANCE_PENDING**
- **MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING**

The experimental profile planner is not connected to the actual Client. Audit
identity is not durable Execution runtime binding. Separate helper invocations do
not prove same-Thread start/resume/turn narrowing, prior grant revocation, built-in
file/patch tools or external channels. Production confirmation still rejects before
reservation/claim/handoff, with coordinator checks before dispatch. No waiver,
unsupported-mode substitution, single-repository fallback, empty Run or fake PASS
was introduced.

Further implementation requires a proven exact permission path for the selected
host and every enabled tool channel; frozen launch/config/schema/profile and member
root identity bound durably before reservation and revalidated before dispatch;
same-Thread narrowing; per-member start/end observations after quiescence; required
repository-qualified Verification.RUN and Coding Review; Coding-owned acceptance
into v76 delivery and the response-loss/recovery gates. Those are subsequent work,
not completed by this diagnostic or by seven repaired legacy regressions.
