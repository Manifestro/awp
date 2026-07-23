# Add AWP to Your Backend

AWP lets your product deliver a new event to a user's AI agent after the agent's original turn has ended. If your product already exposes MCP or an API, add an authenticated WebSocket endpoint beside it:

```text
https://your-product.example/mcp
wss://your-product.example/awp
```

The user's local AWP client opens the connection to your backend. You never need to connect to the user's laptop, and the user does not need a public IP, domain, open port, or webhook server.

This guide implements the minimum interoperable AWP `0.1` flow, and — separately — exactly what to tell your own users to do once your endpoint exists. For persistence, retry, scaling, and security requirements beyond the minimum, use the [production backend guide](./HOW_TO_CREATE_AWP_BACKEND.md).

## What you are building

Your backend has two internal responsibilities:

1. **Connection service** — authenticates AWP clients, binds their opaque session IDs, and maintains WebSocket connections.
2. **Event producer** — your existing application code that creates events, stores them, and targets a bound AWP session.

```text
Your application                          User's machine

new message / job / issue
          │
          ▼
persist event + delivery
          │
          ▼
wss://your-product.example/awp ─────────▶ AWP client
                                             │
                                             ▼
                                        agent session
```

There is no Manifestro relay or shared AWP server. You operate the endpoint and retain your own events. Your backend never needs to know which agent runtime the user is running (Claude Code, Codex, or anything else) — that choice, and everything runtime-specific, stays entirely on the user's machine. See [Runtime independence](#runtime-independence-you-never-need-to-know-what-agent-the-user-runs) below.

## Protocol at a glance

Every WebSocket frame contains one UTF-8 JSON object with this envelope:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_01JABC123",
  "action": "event.deliver",
  "timestamp": "2026-07-19T12:00:00Z",
  "data": {}
}
```

Required fields:

| Field | Meaning |
| --- | --- |
| `type` | Always `awp`. |
| `version` | Protocol version. This guide uses `0.1`. |
| `id` | Unique ID for this protocol message. |
| `action` | Message operation, such as `client.hello`. |
| `timestamp` | RFC 3339 timestamp in UTC. |
| `data` | Object defined by the selected action. |

Receivers should ignore unknown fields unless processing them would be unsafe.

## 1. Expose an authenticated WebSocket

Accept connections at `/awp` over TLS. The reference client sends its provider token during the WebSocket HTTP upgrade:

```http
GET /awp HTTP/1.1
Upgrade: websocket
Authorization: Bearer <provider-token>
```

Return HTTP `401` or `403`, or close with WebSocket policy violation code `1008`, when authentication fails. Never accept credentials in a query string or AWP message.

In production, tokens should be independently revocable and scoped to one tenant or account. Authentication identifies the principal; your backend must still authorize every device, session, and provider resource.

## 2. Complete the handshake

The first client message is `client.hello`:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_hello_01",
  "action": "client.hello",
  "timestamp": "2026-07-19T12:00:00Z",
  "data": {
    "device_id": "dev_macbook_01",
    "client": {
      "name": "awp-go",
      "version": "0.3.0-alpha.1"
    },
    "capabilities": {
      "adapters": ["codex", "claude-code"],
      "resume": true,
      "permissions": true
    }
  }
}
```

Validate the envelope, protocol version, and non-empty `device_id`. Associate the device with the authenticated tenant, then respond with `server.welcome`:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_welcome_01",
  "action": "server.welcome",
  "timestamp": "2026-07-19T12:00:00Z",
  "data": {
    "device_id": "dev_macbook_01",
    "connection_id": "conn_01JABC123",
    "heartbeat_interval_seconds": 30,
    "max_message_bytes": 65536
  }
}
```

The returned `device_id` must exactly match the hello. A client may reconnect with the same device ID; define a consistent policy for replacing or rejecting an older connection.

`capabilities.adapters` lists what the local client can run — this is informational only. Do not brand your integration around one adapter name: the same client may resume Claude Code today and Codex tomorrow, and `capabilities.adapters` can even list runtimes the client doesn't have hardcoded support for at all (see [Runtime independence](#runtime-independence-you-never-need-to-know-what-agent-the-user-runs)).

## 3. Accept session bindings

After the welcome, the client announces one or more local agent sessions:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_bind_01",
  "action": "session.bind",
  "timestamp": "2026-07-19T12:00:01Z",
  "data": {
    "session_id": "ses_support",
    "adapter": "codex",
    "metadata": {
      "channel_id": "channel_123"
    }
  }
}
```

The `session_id` is an opaque AWP routing ID. It is not a Codex or Claude Code runtime session ID. Runtime IDs and runtime credentials must remain on the user's machine — you will never see one.

The `metadata` object is yours to define. It can associate the binding with a channel, repository, mailbox, job, or another resource owned by your product. Before saving it, prove that the authenticated tenant may access both the device and that resource.

Persist the binding and confirm it:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_bound_01",
  "action": "session.bound",
  "timestamp": "2026-07-19T12:00:01Z",
  "data": {
    "session_id": "ses_support",
    "status": "active"
  }
}
```

One connection may bind multiple sessions. Do not create a WebSocket per session.

## 4. Permission request — recommended, not required

After `session.bound`, a fully-featured provider sends `permission.request` declaring what it needs before any queued or live delivery for that session:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_permission_01",
  "action": "permission.request",
  "timestamp": "2026-07-19T15:00:00Z",
  "data": {
    "request_id": "req_your_product_01",
    "session_id": "ses_support",
    "permissions": [
      {
        "id": "runtime.wake",
        "title": "Wake this agent session",
        "risk": "runtime",
        "delegation": "background",
        "mcp_tools": []
      },
      {
        "id": "messages.read_new",
        "title": "Read new messages",
        "risk": "read",
        "delegation": "background",
        "mcp_tools": ["get_new_messages"]
      }
    ]
  }
}
```

The client stores this as a pending request. Only the user (or their agent, on their behalf) can create a local grant. A provider cannot grant itself permissions by sending an event or changing the request.

**You do not have to implement this to be usable.** Most providers won't, at least at first, and that is fine: the AWP client can grant `runtime.wake` (and any provider MCP tool names the user already knows about) directly and locally, with no message from you at all — see `grant_permissions` in [step 6](#6-recommended-tell-your-users-to-connect-through-mcp). What you get by implementing `permission.request` properly:

- **Per-permission reasoning**: your `title`/`description`/`risk` show up to the human reviewing the grant, instead of a bare permission ID they typed themselves.
- **Precise, provider-defined tool scoping**: you declare exactly which of *your own* MCP tools each permission maps to; the local grant fallback instead applies one flat `mcp_tools` list across everything the human allows in one call.
- **Change detection**: the client hashes each requested permission definition. If you later change its risk, delegation, or tool mapping under the same ID, the old grant stops authorizing it until the human reviews it again. A locally-authored grant has no such provider-side definition to compare against.

If you do implement it: resend the current request after every reconnect and rebind, and if a permission definition changes, remember the old local grant no longer authorizes it.

## Runtime independence: you never need to know what agent the user runs

Nothing in `permission.request`, `session.bind`, or anywhere else in this protocol tells you whether the user is running Claude Code, Codex, or something else — and you never need to ask. Two things make that possible:

1. **`mcp_tools` names your own MCP server's tools, not the runtime's.** When you request `"mcp_tools": ["get_new_messages"]`, that is a tool on *your* MCP server (the same one you'd expose at `/mcp`) — it has nothing to do with which agent CLI resumes the session.
2. **Translating a grant into an actual resume invocation is entirely the client's job**, via whatever runtime adapter is configured locally. Codex has one hardcoded resume invocation; a generic `command` adapter lets the user (or their agent) register *any* other runtime — Claude Code, or something you've never heard of — with its own resume command template, using the exact same permission grant your `permission.request` produced. You will never see that command, and it can change without anything on your side changing.

Practically: design and document your `permission.request` payload purely in terms of your own product's capabilities ("read new messages," "send a reply," "read payment history"). Never gate a permission or an event on an `adapter` value, and never add runtime-specific fields to `event.data` or `metadata` — if you find yourself wanting to, that's a sign the logic belongs on the client, not in your backend.

## 5. Persist and deliver an event

When your application receives something relevant, first create a durable event and delivery record. Then send `event.deliver` to the active connection for the target device:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_deliver_01",
  "action": "event.deliver",
  "timestamp": "2026-07-19T12:00:03Z",
  "data": {
    "delivery_id": "dlv_01JABC123",
    "event_id": "evt_message_456",
    "target": {
      "device_id": "dev_macbook_01",
      "session_id": "ses_support"
    },
    "event": {
      "source": "your-product",
      "name": "message.received",
      "timestamp": "2026-07-19T12:00:02Z",
      "data": {
        "channel_id": "channel_123",
        "from": "+77001234567",
        "message_id": "message_456",
        "text": "Yes, tomorrow at 10 works"
      }
    },
    "attempt": 1
  }
}
```

AWP does not interpret `event.data`; its schema belongs to your product. Treat it as untrusted input and avoid including secrets the agent does not need.

The following fields are mandatory in every delivery:

```text
data.delivery_id
data.event_id
data.target.device_id
data.target.session_id
data.event.source
data.event.name
data.event.timestamp
data.event.data
data.attempt
```

In particular, `delivery_id` and `event_id` must be direct children of `data`. If either is missing, the reference client cannot produce an ACK, rejects the message, and reconnects. If your server keeps replaying the malformed pending event, the client will remain in a reconnect loop.

Delivery is **at least once**. Keep `event_id` and `delivery_id` stable across retries so the receiver can deduplicate them. If the device is offline, retain the pending delivery and send it after reconnect and handshake.

**The client also deduplicates on its own, so a sloppy retry is not catastrophic — but do not rely on it.** The reference client keeps a local record per `(provider, session_id, event_id)`; if it already reported `completed` for that `event_id`, a resend is acknowledged immediately without waking the runtime again — you will not burn the user's agent quota just because your retry logic double-sent something. It does *not* protect against reusing the same `event_id` for genuinely different content, or against a redelivery arriving as a *new* `delivery_id` for old content — `event_id` identity is still yours to keep stable and correct.

## 6. Recommended: tell your users to connect through MCP

Everything above is what *your backend* needs to implement. Separately, you need to tell *your users* how to connect their agent to it. As of AWP `0.3`, the recommended path does not require your users to touch a terminal at all: the AWP client ships a local MCP server (`awp mcp`) that any MCP-capable agent (Claude Code, Codex, or anything else) can drive directly. Point your own onboarding docs at this flow instead of a CLI walkthrough.

Tell your user to add AWP as an MCP server once (their agent runtime's own config, not something your product needs to touch):

```bash
claude mcp add awp -- awp mcp
```

Then tell the user to say something like *"Connect this session to \<your product\> using this URL and token, and wake this same session whenever something arrives"* and hand their agent:

- your `service_url` (e.g. `wss://your-product.example/awp`);
- a bearer token scoped to their account.

Their agent then does the rest by calling, in order:

1. **`configure_provider`** — `{provider, service_url, token}`. Writes your endpoint into local config and the token into a private `0600` file next to it — never into a shared config file, never logged.
2. **`set_awp`** — `{provider, session_id, runtime_session_id, resume_command}`. Registers *this* conversation as the one AWP should resume. `session_id` is whatever opaque ID your backend wants to use for this binding (a channel ID works fine); your backend does not need to have seen it before this point — the very first `session.bind` your endpoint receives introduces it.
3. **`request_permissions`** — if you implemented `permission.request`, this connects briefly and returns what you asked for, for the human to review. **`grant_permissions`** — if you did not implement it, the agent calls this directly instead with `allow` (permission ids, always including `runtime.wake`) and `mcp_tools` (your MCP server's tool names, if any) — no round-trip to you required. Either way, a human still has to approve the underlying MCP tool call, so this is not a way for the agent to silently grant itself anything.
4. **`start_daemon`** — begins actually connecting to your endpoint and waiting for events. **`stop_daemon`** / **`daemon_status`** let the user turn this off and on; while stopped, your `event.deliver` is not received at all — whatever your own retry/offline-queue policy does with that is between you and step 5 above.

A user who wants to see this step by step, or who is not using an MCP-capable agent, can do the same thing by hand:

```bash
curl -LsSf https://awp.manifestro.io/install.sh | sh

export MY_PROVIDER_TOKEN="development-token"

awp config set \
  --provider my-provider \
  --service-url wss://your-product.example/awp \
  --device-id dev_macbook_01 \
  --token-env MY_PROVIDER_TOKEN

awp sessions bind \
  --provider my-provider \
  --session-id ses_support \
  --adapter codex \
  --runtime-session-id <codex-session-id> \
  --workspace /absolute/path/to/project \
  --metadata-json '{"channel_id":"channel_123"}'

# If you implemented permission.request:
awp permissions request --provider my-provider --session-id ses_support
awp permissions grant --provider my-provider --session-id ses_support \
  --allow runtime.wake,messages.read_new --scope binding

# If you did not (grants locally, no round-trip to your backend):
awp permissions grant --provider my-provider --session-id ses_support \
  --allow runtime.wake,messages.read_new --mcp-tools get_new_messages --scope binding

awp doctor
awp daemon start --provider my-provider
```

For a short handshake and delivery test instead of running the daemon continuously:

```bash
awp connect \
  --provider my-provider \
  --session-id ses_support \
  --once \
  --timeout 30s \
  --json
```

## Minimal backend state

An in-memory prototype can start with maps, but a production provider should persist at least:

| Record | Minimum fields |
| --- | --- |
| Device | tenant, device ID, credential identity, last seen |
| Session | tenant, device ID, session ID, adapter, metadata |
| Event | tenant, event ID, source, name, payload, created time |
| Delivery | tenant, delivery ID, event ID, target, status, attempt, retry time |
| Connection | ephemeral mapping from tenant/device to active socket |

Use a unique constraint for provider event IDs and transactional event-plus-delivery creation. In a multi-instance deployment, keep durable state in a database and use a broker or notification channel to reach the process holding the active socket.

## Production checklist

Before exposing AWP publicly:

- use `wss://` and reject insecure production connections;
- authenticate during the WebSocket upgrade;
- scope every query by tenant and authorize every binding and delivery;
- never request or store runtime session IDs;
- persist events before acknowledging publication;
- queue events while clients are offline;
- implement stable IDs, retry backoff, and idempotency;
- enforce message-size, connection, and rate limits;
- redact tokens and sensitive event payloads from logs;
- support token revocation and rotation;
- monitor active connections, queue age, retry counts, and ACK latency;
- test reconnects, duplicate delivery, restarts, and cross-tenant isolation.

## Run the reference backend

The AWP repository includes a FastAPI provider example:

```bash
git clone https://github.com/Manifestro/awp.git
cd awp
export AWP_TOKEN=local-dev-token
docker compose -f example/backend/compose.yaml up --build
```

The example implements authentication, handshake, session binding, event delivery, acknowledgements, heartbeat, offline delivery, and duplicate event detection. It stores state in memory and is intended for local interoperability testing—not production deployment.

## Next steps

- [Full AWP `0.1` wire protocol](./PROTOCOL.md)
- [Production backend implementation guide](./HOW_TO_CREATE_AWP_BACKEND.md)
- [Permission model, including the local grant fallback](./PERMISSIONS.md)
- [Complete JSON message examples](./examples)
- [FastAPI reference backend](../example/backend)
- [AWP releases](https://github.com/Manifestro/awp/releases)

AWP is developed by [Manifestro](https://github.com/Manifestro). Protocol feedback and provider implementations are welcome at [github.com/Manifestro/awp](https://github.com/Manifestro/awp).
