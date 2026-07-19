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

Providers MUST send `permission.request` for every bound session before delivering events for it. The request MUST include `runtime.wake`. A client MUST NOT wake a runtime without a matching local grant.

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
| `mcp_tools` | Exact tools on this provider's configured Codex MCP server needed by the permission. |

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

## Codex invocation isolation

For every wake, the Codex adapter creates an explicit allowlist:

```bash
codex exec resume \
  --json \
  -c 'mcp_servers.sinores.enabled_tools=["get_new_messages"]' \
  -c 'mcp_servers.sinores.tools."get_new_messages".approval_mode="approve"' \
  <runtime-session-id> -
```

These `-c` values apply to that Codex process only. AWP does not write `~/.codex/config.toml`. Ungranted tools from the provider are excluded from `enabled_tools`, even when the user's global configuration would otherwise approve them.

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

Fetch the provider request before starting the background daemon:

```bash
awp permissions request \
  --provider sinores \
  --session-id ses_support
```

Review and grant only what is needed:

```bash
awp permissions pending --provider sinores

awp permissions grant \
  --provider sinores \
  --session-id ses_support \
  --allow runtime.wake,messages.read_new,messages.read_history \
  --scope binding
```

Inspect and revoke:

```bash
awp permissions list --provider sinores
awp permissions audit

awp permissions revoke \
  --provider sinores \
  --session-id ses_support \
  --permissions messages.read_history
```

Revocation affects the next wake. It does not terminate an already running runtime process.

## Provider requirements

- Request the smallest useful set of permissions.
- Use stable semantic IDs and exact MCP tool names.
- Send the request after `session.bound` and before pending deliveries.
- Resend the current request after reconnect and rebind.
- Never request tools belonging to another provider.
- Never treat a permission request as proof that the user granted it.
- Continue enforcing authentication and authorization inside every MCP tool.
- Do not encode tokens, secrets, runtime IDs, or event payloads in permission metadata.
