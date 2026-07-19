# Add AWP to Your Backend

AWP lets your product deliver a new event to a user's AI agent after the agent's original turn has ended. If your product already exposes MCP or an API, add an authenticated WebSocket endpoint beside it:

```text
https://your-product.example/mcp
wss://your-product.example/awp
```

The user's local AWP client opens the connection to your backend. You never need to connect to the user's laptop, and the user does not need a public IP, domain, open port, or webhook server.

This guide implements the minimum interoperable AWP `0.1` flow. For persistence, retry, scaling, and security requirements, use the [production backend guide](https://github.com/Manifestro/awp/blob/main/docs/HOW_TO_CREATE_AWP_BACKEND.md).

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

There is no Manifestro relay or shared AWP server. You operate the endpoint and retain your own events.

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
      "version": "0.1.0-alpha.1"
    },
    "capabilities": {
      "adapters": ["codex"],
      "resume": true
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

The `session_id` is an opaque AWP routing ID. It is not a Codex or Claude Code runtime session ID. Runtime IDs and runtime credentials must remain on the user's machine.

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

## 4. Persist and deliver an event

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

Delivery is **at least once**. Keep `event_id` and `delivery_id` stable across retries so the receiver can deduplicate them. If the device is offline, retain the pending delivery and send it after reconnect and handshake.

## 5. Process acknowledgements

After the local runtime finishes, the client reports the result:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_ack_01",
  "action": "event.ack",
  "timestamp": "2026-07-19T12:00:10Z",
  "data": {
    "delivery_id": "dlv_01JABC123",
    "event_id": "evt_message_456",
    "status": "completed",
    "result": {
      "adapter": "codex"
    }
  }
}
```

Supported statuses:

| Status | Meaning |
| --- | --- |
| `accepted` | Client accepted the delivery for local processing. |
| `completed` | Agent runtime finished successfully. |
| `failed` | Runtime execution failed. Apply your retry policy. |
| `rejected` | Client intentionally refused the event. Do not retry without a policy change. |

Verify that the acknowledged delivery belongs to the authenticated tenant and connected device. An unknown or cross-tenant delivery ID must never reveal another tenant's state.

## 6. Keep the connection alive

Either side may send:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_ping_01",
  "action": "heartbeat.ping",
  "timestamp": "2026-07-19T12:00:30Z",
  "data": {}
}
```

The receiver responds:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_pong_01",
  "action": "heartbeat.pong",
  "timestamp": "2026-07-19T12:00:30Z",
  "data": {
    "reply_to": "msg_ping_01"
  }
}
```

Close stale connections, but keep durable device, session, and pending-delivery records for later reconnects.

## Connect the reference client

Install AWP on macOS or Linux:

```bash
curl -LsSf https://awp.manifestro.io/install.sh | sh
```

Configure your provider:

```bash
export MY_PROVIDER_TOKEN="development-token"

awp config set \
  --provider my-provider \
  --service-url wss://your-product.example/awp \
  --device-id dev_macbook_01 \
  --token-env MY_PROVIDER_TOKEN
```

Bind an existing Codex session:

```bash
awp sessions bind \
  --provider my-provider \
  --session-id ses_support \
  --adapter codex \
  --runtime-session-id <codex-session-id> \
  --workspace /absolute/path/to/project \
  --metadata-json '{"channel_id":"channel_123"}'
```

Validate configuration and start listening:

```bash
awp doctor
awp daemon --json
```

For a short handshake and delivery test:

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

- [Full AWP `0.1` wire protocol](https://github.com/Manifestro/awp/blob/main/docs/PROTOCOL.md)
- [Production backend implementation guide](https://github.com/Manifestro/awp/blob/main/docs/HOW_TO_CREATE_AWP_BACKEND.md)
- [Complete JSON message examples](https://github.com/Manifestro/awp/tree/main/docs/examples)
- [FastAPI reference backend](https://github.com/Manifestro/awp/tree/main/example/backend)
- [AWP releases](https://github.com/Manifestro/awp/releases)

AWP is developed by [Manifestro](https://github.com/Manifestro). Protocol feedback and provider implementations are welcome at [github.com/Manifestro/awp](https://github.com/Manifestro/awp).
