# MCP response uncertainty: driver crash and real TCP truncation

## What this increment establishes

The [assistant-authored local MCP acceptance](assistant-mcp-resume-20261101.md)
identified an unqualified mailbox window: a request could reach Core, but its
response might not be persisted before the driver died. This increment tested
that precise window and one actual network-response truncation, rather than
adding unrelated crash locations.

Using the unchanged production Core and both MCP adapter binaries from
`115a58bf4d241c622b04556c655c0e12b5c0f548`, a fresh private DataRoot, and a new
disposable repository:

1. **The old ad hoc driver defect was reproduced.** SIGKILL after a completed
   MCP write but before mailbox response persistence caused the restarted driver
   to automatically send the same mutation a second time.
2. **No duplicate Core physical file effect was observed.** The second dispatch
   returned the same original result, while inode, nanosecond mtime, digest,
   file bytes, Git HEAD and status stayed unchanged.
3. **The guarded driver fixed the redispatch defect.** With a durable pre-send
   journal claim, the same process-death window produced OUTCOME_UNKNOWN and
   exactly one mutation POST across restart; it did not fabricate a response.
4. **An actual HTTP response was truncated on a real TCP socket.** Core's
   complete successful response was independently retained before the
   intermediary sent 40 body bytes against a declared 1,351 and closed the
   socket. The real SDK returned `unexpected EOF`. A fresh SDK connection only
   observed the original worker receipt and caused no automatic mutation retry.
5. One **explicit test-directed exact replay**, after independently observing
   the upstream success, returned the same result without replacing the file
   again. This is not a production recommendation to replay unknown requests.

The intentionally injected EOF is not evidence that the historical proxy
request-body lifecycle defect has returned, nor proof of that old failure's
cause. This is a controlled different failure at the response boundary.

## The fix: acceptance-harness journal, not a production worker

`test/mcpmailbox.Journal.Deliver` binds a private journal root and safe label to
the exact original request bytes:

- An exclusive claim is written and synchronized **before** invoking the sender.
- An existing claim without a retained response is UNKNOWN. A fresh process
  does not invoke the sender, even if the old process might have died before
  sending anything. Conservative uncertainty is intentional.
- Changed request bytes under the same label are a conflict, not a retry.
- Partial or corrupt records fail closed. Sender errors, panic, invalid response
  JSON and response-publication failure leave the claim in place.
- Response bytes are synchronized and atomically published. Their exact bytes
  are preserved; replay of a retained response does not call the sender.
- A retained response is only a transport observation. It may contain a remote
  refusal or a transport error; it is **not** proof of business success.

This closes the observed acceptance-driver bug. It does not add a product host,
model launcher, worker resume API or recovery controller. The caller must retain
its original journal, label and request bytes; changing the label or deleting
the journal is not a safe retry mechanism. A trusted private journal directory
is assumed. Malicious roots, power-loss durability, general distributed
exactly-once execution and installed dot qualification are not established.

The historical driver remains in its evidence bundle so the failing behavior is
reproducible. The new bounded driver uses the journal in guarded mode; legacy
mode exists only for the explicit negative control. No production application
source or production binary was changed by this increment.

## Repeatable regression tests

```sh
# Local journal and actual owned-process SIGKILL tests.
go test ./test/mcpmailbox -count=1
go test -race ./test/mcpmailbox -count=10
go vet ./test/mcpmailbox

# Genuine Local MCP TCP truncation with the production outbound fileworker client.
go test ./internal/controlapi \
  -run '^TestFileWorkerClientRealCoreTCPResponseLoss$' -count=1 -v
go test -race ./internal/controlapi \
  -run '^TestFileWorkerClientRealCoreTCPResponseLoss$' -count=1 -v
```

The journal tests cover exact response bytes, conflicting requests, send and
publication failures, 32 concurrent callers, malformed/partial records, a
pre-dispatch claim, and an actual SIGKILL after a subprocess writes one durable
effect. A cold Journal must preserve UNKNOWN without invoking its sender.

The TCP regression uses the existing admitted NONE fixture, real Core services,
production Local MCP HTTP handler, real SDK and a real loopback TCP intermediary.
Bootstrap is through typed fixture services, not claimed to be public HTTP; no
native runtime or model is launched. Unlike the older CallTool-wrapper
response-loss tests, it closes the client socket after sending only part of a
successful upstream response.

It asserts that the actual `fileworker.Client` becomes MutationUnknown and
blocks further file reads, writes and result submission, even if the callback
tries to ignore the error. A new client only performs Observe. Wire counts stay
at one automatic file-write POST. Its one separate explicit exact replay raises
the POST count to two while retaining one COMPLETED mutation receipt, unchanged
formal ledgers, and the same inode, mtime and digest. Unrelated member content,
source checkouts and Git HEADs remain unchanged. The test does not assert that
an unfinished external worker is dead.

Final checks passed: journal unit / race-times-ten / vet, TCP focused-times-three
(6.735s), TCP race (135.58s test / 137.520s package), and controlapi vet. The
final source hashes were rechecked after those runs.

A non-truncating Go-overlay negative control deliberately sent the complete
body. The regression then failed, proving that a normal successful call is not
mistaken for a passed disconnect test. The failure log is retained.

## Retained external-binary evidence

The private `devbridge-mailbox-unknown-evidence` bundle contains:

- `legacy-gap-proof.json`: two automatic POSTs, actual SIGKILL, same original
  response and unchanged post-effect physical snapshot.
- `guarded-gap-proof.json`: one POST, durable claim/no response, cold UNKNOWN,
  unchanged physical snapshot and read-only progress through the restarted driver.
- `socket-loss-proof.json`: upstream success, downstream EOF, one automatic POST,
  one explicit exact replay and unchanged inode/mtime/digest.
- `proxy-wire.jsonl`, the complete original upstream response, `mcp.jsonl`,
  original requests, response files, the unresolved guarded request and journal.
- `observer-*.json`: source and managed file bytes/hashes/stat metadata, HEADs
  and status at each boundary; owned process logs and SIGKILL exit status.
- `report.json`: **28/28** post-run evidence assertions.
- Journal unit, race-times-ten and vet logs in `journal-unit/`.

There are 46 dispatch records and 45 unique request labels: the known legacy
bug accounts for the duplicate. Only 44 mailbox responses exist because the
fixed unknown request deliberately has none. Of the 46 records, two are
`tools/list` and one is public HTTP project registration. Five file-write POSTs
represent three distinct explicit writes: legacy 2, guarded 1 and socket 2
(including its one deliberate replay). These are not five physical effects.

Core and its adapters were never killed or restarted in this increment. Four
owned **driver** SIGKILLs comprise the two response-persistence fault windows,
one idle binary upgrade, and one cold transport restart after the EOF had been
retained. The final four owned processes joined with exit 0.

The final fixture-user cancellation closed the file authorization without
reverting partial file evidence. The Task truthfully remained ACTIVE /
IMPLEMENTING, the worker receipt stayed OUTCOME_UNKNOWN, no formal file result
existed, there was no Execution, and only the original Review Run existed. No
acceptance, delivery commit, merge, deployment or Core-launched model request
occurred; the native-launch tripwire remained absent.

Two acceptance-author mistakes are retained rather than relabeled as product
bugs: an initial v2 read omitted its required request ID; the first socket
oracle expected content from a write result instead of checking its actual
`digest` and `size`. The latter stopped without automatic redispatch. Correcting that assertion from
the original retained response made no MCP call; the remaining cold-observe and
one explicitly requested replay steps then ran as described above. Source files
and Git HEADs remained unchanged apart from the approved managed `payload.txt`
effects.

The separate `devbridge-tcp-response-loss-evidence` bundle holds the repository
TCP regression logs and its negative control. Root `go build ./...` and an
aggregate selection of the existing three wrapper-loss cases plus the new TCP
case passed; the latter completed in 10.471s. Focused and race checks do not
constitute a full repository-suite pass. Do not publish either private bundle
wholesale or use these test-only confirmations as authorization for user work.
