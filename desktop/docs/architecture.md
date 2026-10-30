# Desktop boundary

`Dev Bridge Desktop -> local process + HTTP boundaries -> Dev Bridge Core/MCP + Local MCP Adapter`

Desktop is the local operations plane: it supervises its own child processes,
reports readiness/build identity, exposes Desktop process logs, and embeds the
Core-served Console. It does not own or duplicate Dev Bridge domain state.

`DataRoot` is the sole normal Core-managed storage root. Desktop persists only
the selected path in OS application configuration and passes the same
`-data-root` value to managed Core, MCP, and Local MCP Adapter children. Core validates and
initializes the marker and derived `state`, `worktrees`, `evidence`, `logs`,
`runtime`, and `artifacts` directories. Desktop does not parse the marker or
touch SQLite.

Changing the selection is a stop/save/start operation for MANAGED children.
There is no hot switch or migration. EXTERNAL services retain independent
ownership and may be backed by an unknown DataRoot.

## Local MCP Adapter

Desktop starts and supervises the Local MCP Adapter
(`devbridge-local-mcp`) as a third managed runtime component after Core and
MCP. The adapter binds loopback only (`127.0.0.1:8090` by default); Desktop
never passes `-allow-non-loopback` and never configures a non-loopback
listener. A compatible ready adapter that Desktop did not start is shown as
EXTERNAL and left untouched; an occupied endpoint that is not a compatible
adapter is CONFLICT and is never terminated.

The adapter does not depend on the OpenAI Tunnel or ChatGPT connectivity, and
its failure never tears down the managed Core/MCP stack. Local MCP-capable AI
clients connect to the adapter at `http://127.0.0.1:8090/mcp`; it is not
exposed to LAN or public networks in normal product use.
