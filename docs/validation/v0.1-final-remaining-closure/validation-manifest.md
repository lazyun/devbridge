# Validation manifest — v0.1 final remaining closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](../README.md)。 本报告整理前的原文及同期证据保存在[批次归档](../archive/v0.1-final-remaining-closure.md)中。

This is **validated working tree based on `3d251564089935d402a444bb8265be3e134e2cf0` with patch identity `05462f78985085c299b5d2d3dfb03004fdbdf16f7579125df7bebe61ce83123c`**. It is not clean-HEAD validation.

Branch: `dev_20260908_1913`. Initial status was clean. Schema: v45. No commit, push, tag or release.

The exact final patch is [working-tree.patch](../archive/v0.1-final-remaining-closure.md#file-e1267894140b1953), generated with `git diff --binary HEAD`; SHA256 is `05462f78985085c299b5d2d3dfb03004fdbdf16f7579125df7bebe61ce83123c`. The five untracked source/test files are additional validated inputs, not part of that Git patch.

| Untracked source/test file | Filesystem mode (decimal) | SHA256 |
| --- | --- | --- |
| `internal/localmcp/final_catalog_test.go` | `420` | `3b00eeb19c408ca410c4a3b758089680cde3c9cbc6912a7aa67a132e070df933` |
| `internal/localmcp/task_acceptance_parity_test.go` | `420` | `51b48dcbbbfdca7425bb4e8e3cea8c7d97bbc1a69c7532a35012bf4d4f142d99` |
| `internal/localmcp/task_creation_receipt_closure_test.go` | `420` | `2f0a7c4a2697781c7e0cdb1e96fe95ea6b286853c8c16662e5e87b08a88a02b9` |
| `internal/workflowpresentation/selected_operation.go` | `420` | `e23008a106b10415c5aa8ab4897d3a418cd393d2e3815c0ed297a8d1df182a1f` |
| `internal/workflowpresentation/selected_operation_test.go` | `420` | `425342709598cc7779d4ae5ab31917fdf4abb7ab6c89bb649937eebda5bff750` |

[source-before.json](../archive/v0.1-final-remaining-closure.md#file-b0b99f279811e301) and [source-after.json](../archive/v0.1-final-remaining-closure.md#file-d21142ce49eda6dc) are identical. They contain SHA256/mode/deletion/working-tree classification for every tracked and untracked source file, excluding this evidence directory only. Their canonical source content SHA256 is `f8e5c06c4cb084c97508a3dcaecb1cb01db84e17a4350974ff6e9ee269f27a7f`. All final tests began after the before snapshot; the after snapshot was taken after the last test and source freeze.

## Final validation

| Command group | Final result | Evidence |
| --- | --- | --- |
| Full affected Go packages (13) | 672 top-level pass, 5 skip, 0 fail; including subtests 1617 pass | [go-tests.log](../archive/v0.1-final-remaining-closure.md#file-43a8c53973f7b2c8) |
| Acceptance focused (3 packages) | 15 top-level pass; including subtests 33 pass | [acceptance-focused.log](../archive/v0.1-final-remaining-closure.md#file-f6cbdae4000a0db9) |
| Targeted race (4 packages) | 14 top-level pass; including subtests 28 pass | [race-tests.log](../archive/v0.1-final-remaining-closure.md#file-4529185aceb0b2c6) |
| Node Card/Web suites | 370 tests: 366 pass, 0 fail, 4 skip | [node-tests.log](../archive/v0.1-final-remaining-closure.md#file-2d0a10247b4ee927) |
| Migration | 20 top-level pass; including subtests 42 pass | [migration-tests.log](../archive/v0.1-final-remaining-closure.md#file-e0bb925581798b2c) |
| Prompt byte/SHA | 9 top-level pass; including subtests 57 pass | [prompt-byte-identity.log](../archive/v0.1-final-remaining-closure.md#file-650400ce83484a1a) |
| Production registration tools/list | GPT 53 / Local 42, PASS | [tool-catalog.txt](../archive/v0.1-final-remaining-closure.md#file-2f67fea958c94d9f) |
| go vet (13 packages) | PASS | [go-vet.txt](../archive/v0.1-final-remaining-closure.md#file-70b0ee196dce3d8d) |
| git diff --check | PASS | [diff-check.txt](../archive/v0.1-final-remaining-closure.md#file-030a4d2ae3ed8df0) |

All command arguments, working directory, UTC start and exit code are in each log and matching `*-result.json`. [test-summary.json](../archive/v0.1-final-remaining-closure.md#file-cc5f5e1640dca626) records exact counts, package outcomes and skip names. The runner used for these commands is preserved as [run-validation.py](../archive/v0.1-final-remaining-closure.md#file-3a6101fd55dc4dd2); its temporary execution location was `/private/tmp/devbridge-final-validation.py`.

Go used `GOCACHE=/private/tmp/devbridge-go-cache`, `GOPROXY=off`, `GOSUMDB=off`. Test servers were isolated loopback fixtures; SQLite and fake backends were temporary. No real Coding Agent, live DB, external network or user device/browser profile was used. Other existing worktrees were listed read-only and were not modified. The default sandbox cannot bind test loopback sockets, so the explicitly authorized fixture runs used tool sandbox escalation. No approval rejection occurred.

[diagnostics/first-combined](../archive/v0.1-final-remaining-closure.md) preserves a prior non-final combination. Its Go failure exposed fixture calls that lacked the newly mandatory requestId; fixtures were corrected, then **every final validation group above was rerun**. Those diagnostic logs are not final passing evidence.

## Complete changed/untracked content seal

[modified-files.txt](../archive/v0.1-final-remaining-closure.md#file-06bee61a8b346800) and [git-status.txt](../archive/v0.1-final-remaining-closure.md#file-ba449914b011dbb5) enumerate every final tracked change and untracked file, including this manifest, logs, catalogs, scripts, report and snapshots. No untracked file is silently omitted from the delivered content identity.

A final full-content receipt is generated **outside** the repository at:

`/private/tmp/devbridge-v0.1-final-remaining-closure-content.json`

The receipt records the full starting HEAD, branch, tracked patch SHA256, exact final git status and path/mode/kind/byte-count/SHA256 for **every tracked changed file and every untracked file**, including all files under this evidence directory. It takes two identical captures before sealing. Unchanged tracked files are anchored by the full base HEAD. The receipt lives outside the repository so it can hash this manifest/report/status and every other evidence file without a circular self-hash. Its content identity and receipt file SHA256 are delivered in the final response; it is an integrity receipt, not a claim of a commit or signature.

Recompute or verify using the preserved [seal-content.py](../archive/v0.1-final-remaining-closure.md#file-c07f03633962821b):

```sh
python3 docs/validation/v0.1-final-remaining-closure/seal-content.py /private/tmp/devbridge-v0.1-final-remaining-closure-content.json
python3 docs/validation/v0.1-final-remaining-closure/seal-content.py --verify /private/tmp/devbridge-v0.1-final-remaining-closure-content.json
```

Any later source or evidence edit changes this receipt. Keep the external receipt together with this directory when archiving the delivery. A later commit can be checked against the recorded patch plus untracked source hashes; the receipt additionally covers all evidence.

## Scope and gaps

The [38-item final report](final-report.md) records F1–F6 closure, preserved architecture and evidence limitations. [static-audit.txt](../archive/v0.1-final-remaining-closure.md#file-fc203cb5061b172c) explains executable versus inert/historical hits. Actual production GPT and Local tools/list schemas were exported through production registration code with an isolated service fixture; this is runtime registration evidence, not real Codex execution.

No SCOPE_CONFLICT remains. Five Go opt-in real/candidate runtime tests and four Node candidate tests remain skipped; no skipped test is counted as passed. Production recall remains **EXTERNAL_INTEGRATION_BLOCKED / HOST_INTEGRATION_PENDING**. The existing Card recall hidden-epoch policy is unchanged.
