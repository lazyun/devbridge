# Coding workset capability evidence — 2026-09-16

Latest policy: **TRUSTED_EXECUTOR_MODEL_V1** and
**CODEX_SANDBOX_HARDENING_DEFERRED**. The retained diagnostic remains
**CODEX_OFFLINE_ISOLATION_CAUSE_UNRESOLVED** as an operational hardening fact,
not a Coding v1 admission blocker. The
[platform-defaults diagnosis](codex-offline-isolation-diagnosis.md) now establishes
the fixed-source mechanism: Minimal adds independent tmp grants; path DENY only
narrows dynamic grants. Installed native matches the official fixed release asset
byte-for-byte; this is not a reproducible source-build attestation. Full installed
policy remains unobserved, the new helper start was blocked, and the source
candidate/observation patches are unbuilt (Rust unavailable). Candidate matrix is
NOT_RUN; original **46/72 matches, 26 unexpected accesses** remain. Independent
raw reads are verified as **6/10 matches, 4 violations**, with origin user-reported.
That conclusion described the earlier diagnostic increment. The current
production adapter now serializes the exact multi-root binding on start, resume,
and turn, and the Coordinator binds it from the v77 RuntimeWorkset before
dispatch. Hostile filesystem denial remains deferred. The complete
production-facade Implementation, Verification, Acceptance, delivery, response-
loss and restart-recovery chain now passes, as does two-member read-only Coding
Review with mutation rejection. Current narrow release status is
**MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED**; project status remains
**PARTIAL_IMPLEMENTATION** and the real configured Codex smoke remains opt-in and
not run.

## Exact inspected installation

| Fact | Observation |
| --- | --- |
| Existing Dev Bridge adapter | Codex 0.149.0 App Server subset; no separately advertised schema protocol version |
| Default executable selection | `/Users/phoenix/.nvm/versions/node/v23.6.0/bin/codex`; `DEVBRIDGE_CODEX_SELECTION_FILE` unset in this task environment |
| Resolved launcher | `/Users/phoenix/.nvm/versions/node/v23.6.0/lib/node_modules/@openai/codex/bin/codex.js` |
| Launcher SHA-256 | `134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477` |
| Native binary | `@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex`, relative to the launcher package's parent node_modules |
| Native SHA-256 | `f4a74117b8142cda581c95ff753abf4508b5636d89682c1ed77e4a9249af8963` |
| Actual offline version output | `codex-cli 0.149.0` |
| Platform | Darwin 25.6.0 / arm64; official helper uses macOS Seatbelt |
| Stable schema manifest SHA-256 | `dbd37ddb8617392419ce16c4428f031afd91094f6343ccbe34deaf2c3f021b9e` |
| Experimental schema manifest SHA-256 | `5dabe6b7f3e2533da1a340e9beeab2f2f1b059bc09973e386d10f56d8727c401` |

Schema-manifest hashes cover the sorted relative-path→SHA-256 map encoded as compact
JSON, not a mutable version string. The native path was resolved by inspecting the
installed npm launcher's package-selection code and matching installed platform
package; no binary was installed, upgraded or replaced. This establishes this task's
**default resolver candidate**, not the selected backend of an uninspected live
SQLite configuration. Explicit configured Codex paths and Desktop selection-file
errors remain authoritative and must never silently fall back to this candidate.

Historical pre-source-check note (superseded by the current diagnosis): the version label alone did not establish a source commit. The official GitHub URLs
`openai/codex/blob/rust-v0.149.0/codex-rs/cli/src/sandbox.rs` and
`.../core/src/seatbelt.rs` returned 404 during investigation; corresponding source
revision/build reproducibility remains **UNVERIFIED**. No current main-branch fields
were substituted for binary-generated schema.

## What the binary's schemas actually say

Both `app-server generate-json-schema --out DIR` and the same command with
`--experimental` succeeded offline. The installed generator's own help was read;
TypeScript generation also succeeded. Stable and experimental outputs are distinct.

- Experimental `thread/start`, `thread/resume` and `turn/start` accept a named
  `permissions` string and `runtimeWorkspaceRoots`.
- `permissions` is mutually exclusive with thread `sandbox` or turn `sandboxPolicy`.
  Named profiles and legacy sandbox fields must not be sent together.
- Thread start/resume responses expose `activePermissionProfile` with only `id`
  and `extends`. This is provenance, **not a content digest or proof of enforcement**.
- `initialize` reports user-agent, platform and Codex home, not exact-workset
  capability negotiation. Experimental fields require `experimentalApi` opt-in.
- `outputSchema`, dynamic tools, permission requests and command/file approvals
  exist in the actual protocol. Their presence does not establish repository
  isolation for every enabled tool or revoke prior session permissions.

The official [App Server documentation](https://learn.chatgpt.com/docs/app-server)
confirms named-profile selection and the mutually exclusive legacy fields.
The official [permission-profile documentation](https://learn.chatgpt.com/docs/permissions)
describes restricted filesystem entries and separately controlled non-command
channels. These pages describe an evolution path; the installed generated schemas
remain the version-specific field authority.

## Offline helper method and observed counterexamples

Reproducible runner: `scripts/probe_codex_workset.py`.
All subprocesses receive isolated HOME/CODEX_HOME/TMPDIR, an allowlisted environment,
no credential variables, and no inherited Git config. The runner only calls version,
schema generation, test-owned Git and `codex sandbox -P exact`. It does **not** start
an App Server session, Thread, turn or model. Profile networking is disabled; test
payloads contain no network commands. Fixture Git branches, baselines and same-named
file contents differ across app/sdk/backend. Only directories created by that
invocation are removed. Evidence remains outside the repository.

A profile explicitly denied `:root`, `:tmpdir`, `:slash_tmp`, `/tmp` and
`/private/tmp`, allowed `:minimal` reads, the exact app/sdk permissions and one
scratch directory, and protected each member's `.git`, `.codex` and `.agents`.
No entire HOME or source-parent permission was requested.

| Helper layout and policy | Expected checks | Matched expectations | Failed denial expectations |
| --- | ---: | ---: | ---: |
| Workspace-local repos, app+sdk write | 13 | 11 | 2 |
| Workspace-local repos, app PATHS / sdk read-only | 13 | 11 | 2 |
| Workspace-local repos, app+sdk read-only | 13 | 11 | 2 |
| `/private/tmp` repos, app+sdk write | 11 | 6 | 5 |
| `/private/tmp` repos, app PATHS / sdk read-only | 11 | 4 | 7 |
| `/private/tmp` repos, app+sdk read-only | 11 | 3 | 8 |
| **Total** | **72** | **46** | **26** |

Workspace-local member checks successfully denied absent-member reads/writes,
symlink escapes, out-of-scope paths and Git metadata writes. However, the same
process could read/write a separate excluded probe repository in `/private/tmp`
even with explicit absolute denies. Locating the members in `/private/tmp` also
allowed writes to readonly members and metadata. Therefore every complete helper
suite correctly exits nonzero. No negative expectation was removed to get a pass.

An initial nested invocation failed at `sandbox_apply: Operation not permitted`.
The later helper calls used approved sandbox escalation for these offline test
processes only. An undefined profile was rejected, confirming profile-name lookup;
that is not proof of the selected policy's effects. The observed temporary-directory
exceptions may involve the helper, platform policy or inherited host sandbox
extensions; the root cause is **not established**. This is a failed gate for this
host/configuration, not a claim about every installation or every platform.

Each phase uses a fresh helper process. It **does not test same-Thread start/resume/
turn narrowing**, persisted session grants, built-in patch/file tools, MCP/connectors,
hooks, or extra-permission requests. Those dimensions remain UNVERIFIED. Root
replacement enforcement for a live Coding Run also remains untested.

Raw evidence:

- `/private/tmp/devbridge-coding-offline-final/report.json`: exact argv/environment,
  version, all schema file hashes, config digests and all helper outputs.
- `/private/tmp/devbridge-coding-offline-final/*-*.toml`: exact tested profiles.
- `/private/tmp/devbridge-coding-g0-rgtmr2ku/`: initial help/schema probes and controls.

Reproduction (on the same audited host; does not imply a permission grant):

```sh
python3 scripts/probe_codex_workset.py \
  --executable /Users/phoenix/.nvm/versions/node/v23.6.0/bin/codex \
  --native-binary /Users/phoenix/.nvm/versions/node/v23.6.0/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex \
  --output /private/tmp/NEW-PROBE-OWNED-DIRECTORY \
  --fixture-parent /Users/phoenix/Documents/devbridge --sandbox
```

## Production call-chain gaps and precise admission boundary

| Dimension | Existing production path | Current evidence / missing work |
| --- | --- | --- |
| Binary/config binding | `codexlaunch.Runtime` → `startAppServer`; `codexconfig.Config.Fingerprint` covers config identity/path | Default candidate measured offline; launch-time binary/schema/profile content is not frozen into a multi-member Run |
| Initialization | `codex.Start` → initialize → initialized | Experimental opt-in currently follows dynamic tools; no exact-workset negotiation exists |
| Thread start/resume | `Client.StartThread` / `ResumeThread` serialize cwd/config | Current production client does not install/bind an immutable exact permission profile; new DTOs are preparatory only |
| Turn policy | `Client.StartTurn` sends legacy `sandboxPolicy` | Writable roots/cwd do not establish excluded-member read isolation; named path is not activated |
| Multiple read/write roots and PATHS | New pure plan preserves frozen members/scopes | Selected normal-directory helper checks pass; complete temporary-directory boundary fails |
| Thread permission narrowing | Canonical FeatureArea Thread reused | Only separate-process helper profiles tested; prior grants/cache revocation unverified |
| Metadata and root identity | Existing single-worktree collector | Metadata denied in normal-directory helper, failed in tmp layout; no live multi-member root finalization proof |
| All tool/approval channels | `approvalBroker`, `dynamicToolBroker`, inherited process/config environment | Existing coarse READ_ONLY/WORKSPACE_WRITE ceiling lacks exact member/path ceiling across all enabled channels |
| Structured output | Existing implementation/verification schemas and command-item evidence | Transport schema presence is supported; complete per-member Coding evidence is not implemented |
| Implementation / Verification | `Coordinator.PrepareRun`, `PrepareReservedExecution`, `ExecuteRun` reject exact targets | Admission remains blocked before dispatch; no new positive Coding Run or required cross-repository RUN verification was executed |
| Coding Review | Scoped review admission and `prepareProductReview` | Same exact-workset gate remains closed; Upper Review path is separate and preserved |
| Acceptance / delivery | Coding acceptance uses existing execution result; v76 member intent helper consumes Direct File evidence | Coding-specific complete member observations are not yet bound into accepted v76 intents; no fake Direct File journals added |

The new capability matrix reports production adapter gaps, not speculative facts
about an arbitrary binary. The offline evidence identity binds config fingerprint,
binary/launcher/version/schema/platform/profile contents and independent dimensions;
checksums detect drift and preserve audit identity, **not authenticity or permission**.
No caller-supplied evidence can enable production admission.

Next implementation requires a proven complete official permission path for the
selected host, all enabled channels and same-Thread narrowing; exact runtime binding
before reservation and revalidation before dispatch; Coding-owned per-member
start/end evidence after quiescence; repository-qualified required Verification.RUN
checks; and canonical acceptance into the existing v76 delivery interface. No new
sandbox, alternative backend, hidden Thread, waiver, fake completion or runtime
upgrade was used to bypass these requirements.
