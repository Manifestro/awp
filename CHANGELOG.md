# Changelog

## 0.3.0-alpha.1

- Add a `{mcp_tools_prefixed_csv}` placeholder to the generic `command` adapter, formatting granted MCP tool names as `mcp__<mcp_server>__<tool>,...` for `--allowedTools`-style flags. Found live: Claude Code's headless `-p` resume has no interactive prompt to approve an MCP tool call, so without this the resumed session could wake but not actually call any provider tool.

- Let `permissions grant` (CLI `--mcp-tools`, MCP `grant_permissions`' `mcp_tools`) synthesize a local permission request when the provider never sent its own `permission.request`, instead of requiring every provider to implement that handshake before a session can be granted `runtime.wake` at all. A provider-sent request, when present, still takes precedence.

- Add `awp sessions remove --all` (optionally scoped to `--provider`) to remove every local session binding at once instead of one at a time.
- Add a persistent event store (`~/.config/awp/events.json` by default, `--events-store` to override) that deduplicates redelivered events per `(provider, session_id)`.
- Skip waking the runtime adapter for an `event_id` the daemon already completed, so a provider that resends an already-acknowledged event no longer spends an agent turn; events that were left pending or previously failed are still retried.
- Write a read-only projection of session lifecycle and pending events to `<workspace>/.awp/data.json` after each processed delivery.
- Add `awp sessions pause`/`resume`/`status` to deliberately hold a session's deliveries (e.g. while working on it by hand) without touching the provider connection.
- Detect structurally broken bindings (missing runtime binary, missing workspace) and mark the session `crashed` instead of just failing one event, so the daemon does not keep retrying a binding that cannot work.
- Add a local `awp mcp` stdio MCP server so a resumed agent can inspect why it was woken (`wake_context`, `list_pending_events`, `list_sessions`) and pause/resume its own wake processing (`pause_session`, `resume_session`).
- Stop holding the event store in daemon memory for the whole run; it is now reloaded around every mutation so a concurrent `awp sessions pause/resume` or `awp mcp` call is never silently overwritten by the daemon's next save.
- Add a generic `command` runtime adapter driven by a per-binding argv template (`resume_command`, with placeholders `{runtime_session_id}`, `{workspace}`, `{mcp_server}`, `{mcp_tools_json}`, `{prompt}`) instead of hardcoded per-CLI Go code; the provider event is never substituted into these args, only ever passed as `{prompt}`/stdin.
- Add a `set_awp` MCP tool so a running agent (Claude Code, Codex, or anything else) can register or update its own resume command and runtime session id, instead of a human running `awp sessions bind` by hand or AWP needing a built-in adapter for that runtime.
- Extract the shared process-execution primitive into `internal/adapters/exec`, used by both the Codex and the generic command adapter.
- Add `awp daemon start`/`stop`/`status` to run the daemon as a detached background process with a PID file, instead of requiring a foreground shell or launchd autostart; add `--provider` to `awp daemon` (and `start`) to connect only one provider. While stopped, AWP is not connected at all, so anything a provider sends is neither received nor queued locally.
- Add `configure_provider`, `request_permissions`, `grant_permissions`, `start_daemon`, `stop_daemon`, and `daemon_status` MCP tools, so a whole provider setup (endpoint, token, session binding, permission review and grant, going live) can be done by an agent through MCP alone, with no CLI or `export` required. A token given to `configure_provider` is written to a private 0600 file next to config.json, never into config.json itself; `grant_permissions` never decides what to approve on its own, so the human still reviews what `request_permissions` returns before anything is granted.
- Extract daemon process lifecycle management (start/stop/status, PID file, background spawn) into `internal/daemonctl`, shared by the CLI and the MCP server so both control the exact same process the same way.

## 0.2.0-alpha.1

- Add mandatory provider `permission.request` messages.
- Add local `once`, `binding`, and `provider` permission grants.
- Add permission request hashing, revocation, and a private audit log.
- Restrict each Codex wake to the granted provider MCP tools with one-run config overrides.
- Keep global `~/.codex/config.toml` unchanged.
- Add `awp update check` and verified atomic self-update.
- Add disabled-by-default automatic update policy.
- Update the FastAPI reference provider and provider documentation.

## 0.1.0-alpha.1

- Initial AWP `0.1` wire protocol.
- Multi-provider, multi-session Go client.
- Codex CLI adapter and opt-in macOS autostart.
- Provider-owned FastAPI reference endpoint.
