# Confirmation typed rejection, readback and stale-intent validation

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-e5ce8863911b9316)。

Opening baseline was branch `dev_20260908_1913`, HEAD
`3f3309d15a8b06a973db90ea213a79972330ed3e`, schema v87 and an empty index.
The opening non-ignored byte manifest is
`/private/tmp/confirmation-typed-opening.sha256` (1,956 entries), with SHA-256
`e2af3e69a5681b9555f4b0ef7bdcece8d293e9da5db3affb5f52033cb7523dec`.

## Public result contract

Confirmation business outcome, durable receipt, public projection and Host
transport are separate layers. Public projection normalizes every top-level,
submission, step and option failure. A missing historical message is filled
from its reason code or with the stable non-sensitive fallback `The
confirmation command did not complete successfully.` The durable receipt and
its hash are not rewritten. Machine decisions use outcome, disposition, code
and reason code rather than the display message.

The MCP handlers normalize and validate their final structured value against
the same output schema advertised by the real MCP SDK. Projection failure uses
a conforming `CONFIRMATION_PUBLIC_PROJECTION_INVALID` result and cannot erase a
previously recorded business receipt or emerge as an unclassified Host
transport error.

`REJECTED_NOT_CONSUMED` states that the command was rejected before effects;
`COMMITTED` identifies consumed work; `OUTCOME_UNKNOWN` makes no consumption
claim and never enables automatic redispatch. Commit, get and show all use the
same normalized receipt projection.

## Stale-intent and Card contract

Review confirmations freeze the Project topology revision and an ordered
development-context fingerprint. Options expose a typed eligibility result.
The Card rereads exact options before allocating a request ID or hashing and
sending a commit. Unavailable variants enter `STALE` without dispatch. The
commit transaction independently rechecks topology, development context,
current Review, Task phase and unresolved Decision before recording
consumption or creating business objects. A race records a durable typed
`REJECTED_NOT_CONSUMED` receipt with zero successor effects.

Once a request has crossed dispatch, transport uncertainty retains its exact
request ID and payload hash and permits readback only. A recorded COMMITTED or
REJECTED receipt overrides an older persisted `outcomeUnknown` marker after a
Card remount. Exact replay cannot allocate another successor.

## Project-scope display

Project-scope and multi-repository Review confirmations structurally omit an
empty `repositoryId`. Reader references likewise omit empty identities. Display
metadata resolves Project, Task, Feature Area, Review ordinal, Decision purpose,
repository count and topology revision without inventing a single Repository.
Presentation enrichment is optional: safe Project/Task fallbacks and a typed
presentation issue do not alter eligibility, disposition or receipt readback.

## Read-only recovery for the diagnosed live request

After the corrected runtime is deployed, recovery starts only with exact
`confirmation_get` for intent
`confirm_42a8f0d1dee22ff70da2ba4730eab2d4`, version 1, request
`confirm_2d1bc830-4b8d-435a-be79-fa265b6e52ee`. `COMMITTED` means follow the
recorded successor identities; `REJECTED_NOT_CONSUMED` or a typed stale result
permits a new Core prepare; `OUTCOME_UNKNOWN` remains readback/cancel/wait only.
The original request must never be redispatched based on the absence of a
visible successor.

All automated fixtures use test-owned databases, repositories, sessions and
Host doubles. They do not read or mutate the diagnosed live DataRoot, Task,
Review, Decision, intent or request and do not call a real model.

## Verification

| Command | Result |
| --- | --- |
| Focused actual MCP SDK output-schema suite | PASS for get, show, options and commit, including nested empty historical failure messages |
| Focused topology/readback/replay/conflict integration suite | PASS |
| Focused confirmation/topology/receipt race suite | PASS |
| `go test` for confirmation, controlapi, mcpserver, bridgehttp and SQLite packages | PASS |
| `go test ./... -count=1 -timeout=8m` | PASS with the default Root package matrix |
| `go test ./... -count=1 -timeout=8m` in `desktop` | PASS with the default Desktop package matrix |
| Root and Desktop `go vet ./...` | PASS |
| `node --test --test-reporter=spec internal/bridgehttp/*_test.js` | PASS, 116/116 |
| `node --test --test-reporter=spec internal/mcpserver/*_test.js` | PASS, 156/156 defaults; four established opt-in snapshots skipped |
| `npm test` in `desktop/frontend` | PASS, 52/52 |
| `npm run build` in `desktop/frontend` | PASS |
| `node scripts/build-chatgpt-ui.mjs /private/tmp/devbridge-confirmation-typed-card-build` | PASS, two self-contained assets |
| `git diff --check` | PASS |

The opening-to-final manifest comparison contains only the production,
test and documentation files listed by this closure. No unrelated JavaScript
or Card asset changed.
