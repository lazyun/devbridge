# MCP dependency direction

Core/domain packages must not acquire a dependency on the MCP transport layer.
That boundary includes **outgoing clients**, not only incoming server adapters.

## Exact transport packages

The architecture test explicitly classifies these existing package directories:

| Exact directory under `internal/` | Role |
| --- | --- |
| `mcp` | Shared MCP transport support |
| `mcpserver` | GPT-facing MCP adapter |
| `localmcp` | Local MCP adapter |
| `upperworker` | Injected, single-run outbound UPPER MCP client |
| `fileworker` | Injected, single-run outbound DIRECT_FILE MCP client |

The clients may import the MCP SDK and existing domain data contracts. They do
not become inward-facing Core services merely because they reside under
`internal/`. Production Core/domain code must not import either client to start
work implicitly. The current client graph uses `directfile`, `review`, `task` and
`workset` contracts; no production inward-facing package imports either client.
Their host injection and explicit identity/authority requirements remain those
in the [UPPER client](upper-review-worker-client.md) and
[DIRECT_FILE client](direct-file-worker-client.md) contracts.

## Enforced guard

`TestDomainPackagesDoNotDependOnMCP` scans production Go imports in the entire
`internal/` source tree. Only the **exact containing package directories** in the
table are transport exceptions. The scanner does not skip their descendants and
does not exempt directory basenames or names beginning with `mcp`.

Every other production package is protected against:

- imports from the `github.com/modelcontextprotocol` namespace;
- imports of any of the five exact internal transport package roots;
- imports of descendants of those transport roots.

Thus `controlapi -> upperworker` and `storage/sqlite -> fileworker` are forbidden,
just like domain-to-MCP-server imports. A directory such as `mcpdomain`,
`controlapi/mcpserver`, or `upperworker/child` does not inherit an SDK exemption.
Any newly classified adapter package requires an explicit architecture change.

The scan includes platform-specific and build-tagged source rather than only the
current platform's compiled graph. Only `_test.go` files are exempt, allowing
black-box integration tests to exercise real adapters without creating a
production dependency. Missing roots, walk errors and malformed production
imports fail the test instead of silently returning an empty result.

Fixture tests prove both allowed edges and rejected reverse edges, exact package
classification, lookalikes, nested directories, descendant imports, test-file
exceptions and error reporting. They test the scanner's actual returned
violations, not a separately implemented imitation of its policy.

## Why this is a separate correction

The broader qualification run for the [file-result projection correction](../testing/assistant-authored-local-mcp.md)
exposed five SDK imports in the already-published outbound clients. The old guard
classified every non-server internal package as domain, while exempting any
nested directory whose name began with `mcp`. Simply suppressing the five errors
would leave a path for Core to import an outbound client and indirectly depend on
MCP. The new classification corrects the false positives **and strengthens the
reverse-dependency and exact-directory checks**.

This increment changes architecture tests/documentation only. It changes no
Core authority, worker behavior, protocol, verification, acceptance or deployment.
It does not erase the separate full SQLite/Local MCP package timeouts or establish
a full-root-suite pass.

```sh
go test ./internal/mcpserver -run 'TestDomainPackagesDoNotDependOnMCP|TestMCPArchitecture' -count=1
```

## Qualification

The complete MCP server package passed in **107.285s** after the correction.
The focused actual-tree scan, 40 boundary cases and two scanner-error cases
passed ordinarily (**0.162s**) and with the race detector (**2.125s**). Root build
and vet passed; the architecture test's source hash was stable, and all six
previous projection-correction Go hashes remained unchanged. Independent review
approved the stricter boundary and its negative oracles. These results do not
turn the earlier SQLite/Local MCP broad-suite timeouts into passes.
