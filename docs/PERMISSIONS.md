# AWP Permission Model

AWP can wake an unattended agent session. That makes consent part of the protocol, not an optional provider convention.

AWP never edits global Codex or Claude Code configuration. A provider requests semantic permissions, the user grants a subset locally, and the runtime adapter applies only those grants to one wake invocation.

## Lifecycle

```text
client                         provider
  │── session.bind ─────────────▶│
  │◀─ session.bound ─────────────│
  │◀─ permission.request ────────│
  │                              │
user reviews and stores grant locally
  │                              │
  │◀─ event.deliver ─────────────│
  │── local permission check     │
  │── one-run runtime overrides  │
  │── event.ack ────────────────▶│
```

A fully-featured provider sends `permission.request` for every bound session before delivering events for it, and that request MUST include `runtime.wake`. A client MUST NOT wake a runtime without a matching local grant. Sending `permission.request` is recommended, not required for a provider to be usable — see [Local grants without a provider request](#local-grants-without-a-provider-request).

## Requested permission

```json
{
  "id": "messages.read_new",
  "title": "Read new messages",
  "description": "Read messages received after binding",
  "risk": "read",
  "delegation": "background",
  "mcp_tools": ["get_new_messages"]
}
```

Fields:

| Field | Rule |
| --- | --- |
| `id` | Stable semantic identifier. `runtime.wake` is defined by AWP; other IDs are provider-defined. |
| `title` | Short text shown to the user. |
| `description` | Optional explanation of data and effects. |
| `risk` | `runtime`, `read`, `write`, or `sensitive`. |
| `delegation` | `background` or `interactive-only`. |
| `mcp_tools` | Exact tools on this provider's own MCP server needed by the permission — never the runtime's tools; see [Runtime independence](./PROVIDER_QUICKSTART.md#runtime-independence-you-never-need-to-know-what-agent-the-user-runs). |

`runtime.wake` MUST have risk `runtime`, delegation `background`, and no MCP tools.

Payments, destructive deletion, publication, account administration, and similar sensitive operations SHOULD be `interactive-only`. The background daemon refuses to grant those permissions.

## Local grants

Grant scopes:

| Scope | Lifetime |
| --- | --- |
| `once` | Consumed by the next authorized wake for this binding. |
| `binding` | Applies to this provider and AWP session. Recommended. |
| `provider` | Applies to matching permission definitions for every session of this provider. |

The client hashes every requested permission definition when it is granted. If the provider later changes its risk, delegation, or MCP tool mapping under the same ID, that permission stops being authorized until the user reviews it again.

Permission state is stored locally with file mode `0600`. Runtime session IDs, tokens, event contents, and MCP results are not written to the permission audit log.

## Local grants without a provider request

Most providers will not implement `permission.request`, at least not right away. Requiring every provider to build that handshake before any of its users could be woken at all would make AWP harder to adopt for no real safety benefit — the actual safety property is "a human approved this permission," not "a provider phrased the ask in a specific message."

So `GrantPermissions` no longer requires a prior `permission.request` from the provider. When none exists yet for a `(provider, session_id)`, the client synthesizes one locally (`request_id: "local"`) covering exactly the permission IDs and MCP tool names it was asked to grant, then grants it the normal way. If the provider *does* send a real `permission.request` first, that one is used instead — a provider-authored request always takes precedence over the local fallback.

What you give up with a local grant, compared to a real `permission.request`:

- no per-permission `title`/`description`/`risk` from the provider — the human sees only the bare ID they typed;
- one flat `mcp_tools` list shared by everything granted in that call, instead of the provider's own per-permission scoping;
- no definition-change detection, since there is no provider-authored definition to hash and compare against later.

`runtime.wake` is still always required, still cannot be granted with MCP tools attached, and everything else in this document — scopes, hashing, `0600` storage, one-run isolation — applies identically whether the request came from the provider or was synthesized locally.

```bash
# No permission.request has ever arrived for this session — grants locally:
awp permissions grant \
  --provider sinores \
  --session-id ses_support \
  --allow runtime.wake,messages.read_new \
  --mcp-tools get_new_messages \
  --scope binding
```

The same thing through the local MCP server (see [User commands](#user-commands) below) is the `grant_permissions` tool, with `allow` and `mcp_tools` arguments — this is what lets an agent set up a whole provider without your backend ever implementing the permission handshake.

## One-run invocation isolation

For every wake, the runtime adapter creates an explicit allowlist scoped to that one process — never the user's global runtime configuration. The built-in Codex adapter does this with one-run config overrides:

```bash
codex exec resume \
  --json \
  -c 'mcp_servers.sinores.enabled_tools=["get_new_messages"]' \
  -c 'mcp_servers.sinores.tools."get_new_messages".approval_mode="approve"' \
  <runtime-session-id> -
```

These `-c` values apply to that Codex process only. AWP does not write `~/.codex/config.toml`. Ungranted tools from the provider are excluded from `enabled_tools`, even when the user's global configuration would otherwise approve them.

A binding registered for any other runtime (via `set_awp`'s `resume_command`, see [`docs/PROVIDER_QUICKSTART.md`](./PROVIDER_QUICKSTART.md)) gets the same granted tool list through the `{mcp_tools_json}` placeholder instead — the generic `command` adapter substitutes it into whatever argv that runtime's own resume invocation needs, so isolation is not specific to Codex.

The configured MCP server name defaults to the AWP provider name and can be set explicitly:

```bash
awp config set \
  --provider sinores \
  --service-url wss://sinores.net/awp \
  --token-env SINORES_TOKEN \
  --mcp-server sinores
```

For an event-only provider that has no MCP server, configure `--mcp-server none`. Such a provider request must not map granted permissions to MCP tools.

## User commands

Every command below exists both as a CLI subcommand and as a tool on the local `awp mcp` server (`claude mcp add awp -- awp mcp`), so a user's agent can drive the whole flow without them touching a terminal. `provider`/`session_id` are optional on the MCP tools — omitted, they resolve to the one binding already registered for the current workspace.

Fetch the provider's request, if it sends one, before starting the background daemon:

```bash
awp permissions request --provider sinores --session-id ses_support
```

MCP: `request_permissions` (`provider`, `session_id`, `timeout_seconds`).

Review and grant only what is needed — with a provider request already on file, or synthesized locally if not (see [above](#local-grants-without-a-provider-request)):

```bash
awp permissions pending --provider sinores

awp permissions grant \
  --provider sinores \
  --session-id ses_support \
  --allow runtime.wake,messages.read_new,messages.read_history \
  --scope binding
```

MCP: `grant_permissions` (`allow`, `mcp_tools`, `scope`).

Inspect and revoke:

```bash
awp permissions list --provider sinores
awp permissions audit

awp permissions revoke \
  --provider sinores \
  --session-id ses_support \
  --permissions messages.read_history
```

Revocation affects the next wake. It does not terminate an already running runtime process. `revoke`/`list`/`audit` are CLI-only today; a paused session (`pause_session`/`awp sessions pause`) is the MCP-reachable way to stop a binding from waking without changing its grants.

## Provider requirements, if you implement `permission.request`

- Request the smallest useful set of permissions.
- Use stable semantic IDs and exact MCP tool names.
- Send the request after `session.bound` and before pending deliveries.
- Resend the current request after reconnect and rebind.
- Never request tools belonging to another provider.
- Never treat a permission request as proof that the user granted it.
- Continue enforcing authentication and authorization inside every MCP tool.
- Do not encode tokens, secrets, runtime IDs, or event payloads in permission metadata.

If you do not implement it, the only requirement on your side is the same one that already applies to every provider: continue enforcing authentication and authorization inside your own MCP tools regardless of what AWP granted locally — a local grant authorizes the *client* to invoke your tool during a wake, it says nothing about what your backend should trust.
