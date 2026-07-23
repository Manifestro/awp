# How to Build an AWP Backend

Status: implementation guide for AWP `0.1` draft  
Audience: engineers adding an AWP endpoint to an MCP/API product
Canonical repository: [Manifestro/awp](https://github.com/Manifestro/awp)

This document describes how a product implements its own backend endpoint compatible with the Agent Wake Protocol (AWP) Go client. It combines wire protocol requirements with storage, delivery, retry, security, and production guidance.

AWP has no required central server. If a product exposes `https://provider.example/mcp`, that same product can expose `wss://provider.example/awp`. The local daemon connects directly and independently to every configured provider.

AWP is runtime-neutral. A provider backend MUST NOT contain Codex-, Claude Code-, or IDE-specific resume logic. Runtime session identifiers and runtime credentials remain on the local AWP Client. The provider may naturally contain its own application logic—for example Sinores understands its WhatsApp events—but AWP transport semantics remain generic.

## 1. Backend responsibilities

An AWP backend is an event endpoint owned by one MCP/API provider:

```text
Provider application ──▶ provider's durable AWP queue
                                      │
Local AWP Client ◀── wss://provider.example/awp
       │
       └── locally resumes Codex, Claude Code, or another runtime
```

Different products operate different endpoints; one provider's events never pass through another's.

The backend MUST:

1. authenticate its AWP Clients and internal event producers;
2. create or accept and validate its own events;
3. persist an event before reporting successful publication;
4. route the event to the target `device_id` and `session_id`;
5. queue events while the client is offline;
6. deliver events over an authenticated WebSocket;
7. process delivery acknowledgements;
8. retry deliveries that have not been safely acknowledged;
9. deduplicate repeated publication requests;
10. preserve the application event payload without interpreting it.

The provider backend MUST NOT:

- receive or store a Codex/Claude runtime session ID;
- execute a runtime itself;
- grant new permissions to a resumed agent;
- require a public IP, domain, or inbound port on the client device;
- interpret `event.data` as trusted agent instructions.

The HTTP `POST /events` used by this repository's FastAPI example represents an internal provider publication boundary for testing. A real provider may enqueue AWP events directly from its application and does not need to expose a public generic `/events` endpoint.

## 2. Protocol and transport

AWP `0.1` uses JSON. The same common envelope is used for HTTP and WebSocket messages:

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

Envelope requirements:

| Field | Requirement |
| --- | --- |
| `type` | MUST equal `awp`. |
| `version` | MUST equal a version supported by the receiver. This guide implements `0.1`. |
| `id` | MUST be a non-empty, sender-generated unique message ID. |
| `action` | MUST be one of the actions valid in the current direction and connection state. |
| `timestamp` | MUST be an RFC 3339 timestamp with a timezone; UTC is strongly recommended. |
| `data` | MUST be a JSON object. Its schema depends on `action`. |

Identifiers are opaque strings. A backend MUST NOT parse business meaning from prefixes such as `dev_`, `ses_`, `evt_`, or `dlv_`.

For WebSocket transport:

- production endpoints MUST use `wss://`;
- each text frame MUST contain exactly one complete JSON message;
- binary frames are unsupported in `0.1` and SHOULD cause a protocol error;
- the provider MUST enforce a configured maximum frame/message size;
- secrets MUST be sent during the HTTP upgrade, not inside AWP messages.

Unknown JSON fields SHOULD be ignored unless accepting them would be unsafe. Missing or invalid required fields MUST be rejected.

## 3. Authentication and tenancy

The example provider backend uses one bearer token for local development. A production provider MUST use independently revocable credentials and MUST scope every database query by tenant or account.

Recommended credential separation:

| Credential | Used by | Suggested scope |
| --- | --- | --- |
| Client token | AWP Client | connect as one tenant/device; bind permitted sessions; acknowledge only its deliveries |
| Internal publisher credential | Provider component | enqueue only provider-owned event sources and permitted targets |
| Admin credential | Control plane | provision, rotate, revoke, inspect, and delete resources |

The reference HTTP form is:

```http
Authorization: Bearer <token>
```

Authentication MUST happen before accepting a WebSocket as an active AWP connection. The provider SHOULD return HTTP `401`/`403` before upgrade when the framework permits it; otherwise it SHOULD close with WebSocket policy code `1008`.

Authorization MUST be checked separately from authentication. Knowing a `device_id`, `session_id`, `event_id`, or `delivery_id` MUST NOT grant access to it.

Production implementations SHOULD:

- store only hashed API tokens when possible;
- use constant-time secret comparison;
- support rotation and revocation;
- rate-limit failed authentication;
- avoid tokens in URLs, query strings, messages, and logs;
- record an authenticated principal on every session, event, and delivery row.

## 4. WebSocket connection lifecycle

### 4.1 Connect and `client.hello`

After the authenticated WebSocket upgrade, the first client message MUST be `client.hello`:

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
      "version": "0.1.0"
    },
    "capabilities": {
      "adapters": ["codex"],
      "resume": true
    }
  }
}
```

The provider SHOULD require this message within 10 seconds. It MUST verify that the authenticated principal owns or may use `device_id`.

If the hello is valid, reply with `server.welcome`:

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

`connection_id` identifies this socket instance, not the persistent device. The provider MUST remove a connection only if the disconnecting socket is still the current socket for that connection/device.

### 4.2 Multiple connections for one device

The provider needs an explicit policy. The `0.1` reference behavior is **newest connection wins**:

1. atomically register the new connection;
2. close the previous connection with code `1012`;
3. leave durable session bindings and pending deliveries intact;
4. deliver pending events through the new connection.

This prevents two clients from processing the same delivery concurrently. A future protocol version may support multiple connection leases, but a `0.1` backend SHOULD NOT invent that behavior silently.

### 4.3 Session binding

After welcome, the client announces an opaque AWP session:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_bind_01",
  "action": "session.bind",
  "timestamp": "2026-07-19T12:00:01Z",
  "data": {
    "session_id": "ses_01JABC123",
    "adapter": "codex",
    "metadata": {
      "label": "Customer support session"
    }
  }
}
```

The provider MUST bind the AWP `session_id` to the authenticated tenant and connected `device_id`. It MAY store the adapter name and metadata for routing and display, but it MUST NOT request the vendor runtime session ID.

Provider-defined `session.bind.metadata` may associate application resources with the AWP session—for example `{ "channel_id": "channel_123" }`. Another valid product design is an MCP tool such as `subscribe_awp` that accepts the opaque `awp_session_id` and a provider resource. Whichever mechanism is used, authorization must prove that the principal may bind both the AWP session and the provider resource. Never accept a Codex/Claude runtime ID.

Reply with:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_bound_01",
  "action": "session.bound",
  "timestamp": "2026-07-19T12:00:01Z",
  "data": {
    "session_id": "ses_01JABC123",
    "status": "active"
  }
}
```

A session already owned by another tenant or device MUST NOT be silently reassigned. Return an error such as `session_conflict`. Rebinding by the same authorized device SHOULD be idempotent.

Bindings SHOULD be durable. An `active` connection is transient; the session identity itself should survive disconnects so the provider can enqueue events while the client is offline.

One connection to this provider may bind multiple sessions. The provider MUST support multiple `session.bind` messages after one `server.welcome`; it SHOULD NOT require or create a WebSocket per session. Events belonging to a different provider travel over that other provider's independent AWP connection.

### 4.4 Permission request

Immediately after `session.bound`, a fully-featured provider SHOULD send `permission.request` for that session, before delivering new or queued events. This message is RECOMMENDED, not required for interoperability: the reference client also supports granting `runtime.wake` (and specific MCP tool names the user already knows) locally, with no message from the provider at all, so a backend that skips this section still works end to end — it just loses the per-permission reasoning, provider-defined tool scoping, and definition-change detection described below and in [PERMISSIONS.md](./PERMISSIONS.md#local-grants-without-a-provider-request).

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_permission_01",
  "action": "permission.request",
  "timestamp": "2026-07-19T15:00:00Z",
  "data": {
    "request_id": "req_example_01",
    "session_id": "ses_01JABC123",
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

Provider requirements:

- every request contains `runtime.wake`;
- IDs and their risk/tool definitions remain stable;
- `mcp_tools` names only tools from this provider's MCP server;
- sensitive actions that require a present user use `interactive-only`;
- the current request is resent after every reconnect and rebind;
- receiving a request is never treated as proof of a local grant.

The reference client hashes each granted definition. Changing a tool mapping, risk, or delegation under an existing ID invalidates that part of the grant. See [`PERMISSIONS.md`](./PERMISSIONS.md).

### 4.5 Heartbeats

Either peer may send:

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

The receiver replies:

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

The provider SHOULD close stale sockets after a documented number of missed heartbeat intervals. WebSocket control-frame ping/pong MAY also be used, but it does not replace the AWP heartbeat when protocol-level liveness is needed.

## 5. Publishing events

If the provider exposes an HTTP boundary between its application and AWP delivery component, the example form is:

```http
POST /events
Authorization: Bearer <publisher-token>
Content-Type: application/json
```

Request body:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_publish_01",
  "action": "event.publish",
  "timestamp": "2026-07-19T12:00:02Z",
  "data": {
    "event_id": "evt_source_01",
    "target": {
      "device_id": "dev_macbook_01",
      "session_id": "ses_01JABC123"
    },
    "event": {
      "source": "example-service",
      "name": "message.received",
      "timestamp": "2026-07-19T12:00:02Z",
      "data": {
        "external_message_id": "message_456",
        "text": "The external event payload is source-defined"
      }
    }
  }
}
```

Required validation:

- envelope fields are valid;
- `action` equals `event.publish`;
- `event_id`, `device_id`, `session_id`, `event.source`, and `event.name` are non-empty;
- both timestamps are valid RFC 3339 timestamps with timezones;
- the publisher is authorized for the target and source;
- the session exists and belongs to the target device;
- message and application payload sizes are within configured limits;
- JSON nesting, strings, and collection sizes are bounded to prevent resource exhaustion.

The provider MUST treat `event.data` as opaque untrusted JSON and preserve it semantically. It MUST NOT execute, template, or evaluate values from the payload.

### 5.1 Persist before responding

The publish operation SHOULD use one database transaction:

1. create or find the event by its idempotency key;
2. verify that an existing event has the same tenant, target, and payload identity;
3. create one delivery record with a new stable `delivery_id`;
4. commit both records;
5. attempt online delivery only after commit;
6. return `202 Accepted`.

Example response:

```json
{
  "event_id": "evt_source_01",
  "delivery_id": "dlv_01JABC123",
  "status": "pending",
  "online": true,
  "duplicate": false
}
```

`online: true` means a live socket was available for an immediate send attempt. It does not mean the runtime completed the event. Only an `event.ack` communicates the client/runtime outcome.

### 5.2 HTTP status codes

Recommended responses:

| Status | Meaning |
| --- | --- |
| `202` | Event was durably accepted, including an idempotent duplicate. |
| `400` | Valid JSON but wrong AWP action or invalid request form. |
| `401` | Missing or invalid authentication. |
| `403` | Principal is authenticated but not authorized for the target/source. |
| `404` | Target session does not exist in the caller's tenant. Do not leak cross-tenant existence. |
| `409` | Session/device mismatch or conflicting reuse of an idempotency key. |
| `413` | Request exceeds the configured size. |
| `422` | Action data fails schema validation. |
| `429` | Publisher rate limit exceeded. |
| `503` | Durable storage or delivery infrastructure is unavailable. |

## 6. Delivery construction and routing

The provider's AWP delivery component creates `event.deliver`; an upstream application component MUST NOT choose `delivery_id` or `attempt`:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_deliver_01",
  "action": "event.deliver",
  "timestamp": "2026-07-19T12:00:03Z",
  "data": {
    "delivery_id": "dlv_01JABC123",
    "event_id": "evt_source_01",
    "target": {
      "device_id": "dev_macbook_01",
      "session_id": "ses_01JABC123"
    },
    "event": {
      "source": "example-service",
      "name": "message.received",
      "timestamp": "2026-07-19T12:00:02Z",
      "data": {
        "external_message_id": "message_456",
        "text": "The external event payload is source-defined"
      }
    },
    "attempt": 1
  }
}
```

The tuple `(tenant_id, device_id, session_id)` is the routing boundary inside one provider. Before every send, the provider MUST verify that the current socket belongs to the same authenticated tenant and device as the delivery.

Before writing an `event.deliver` frame, validate the final wire object—not only the stored event:

- `data.delivery_id` is present and non-empty;
- `data.event_id` is present and non-empty;
- `data.target.device_id` and `data.target.session_id` are present and authorized;
- `data.event.source` and `data.event.name` are present and non-empty;
- `data.event.timestamp` is RFC 3339 and `data.event.data` is an object;
- `data.attempt` is a positive integer.

The IDs are required directly under `data`. A client cannot acknowledge, retry, or deduplicate a delivery without them. The reference Go client closes the connection without sending an ACK when either is missing; repeatedly sending that malformed pending event will therefore create a reconnect loop.

The provider MUST reuse the same `delivery_id` when retrying the same delivery. It SHOULD create a new envelope message `id` and increment `attempt` for each new delivery attempt. The original event contents MUST not change.

Do not hold a global application lock while performing network I/O. Resolve the current connection and claim work atomically, release the lock/transaction, then send. A failed send returns the delivery to retryable state.

## 7. Acknowledgements and state machine

The client reports delivery outcome with `event.ack`:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_ack_01",
  "action": "event.ack",
  "timestamp": "2026-07-19T12:00:10Z",
  "data": {
    "delivery_id": "dlv_01JABC123",
    "event_id": "evt_source_01",
    "status": "completed",
    "result": {
      "adapter": "codex"
    }
  }
}
```

Statuses:

| Status | Meaning | Network redelivery |
| --- | --- | --- |
| `pending` | Internal provider state; not yet safely acknowledged. | Allowed. |
| `accepted` | Client durably accepted responsibility for local processing. | Stop. |
| `completed` | Runtime finished processing successfully. | Stop. |
| `failed` | Client/runtime attempted processing and failed. | Policy-dependent; do not blindly retry forever. |
| `rejected` | Client intentionally refused the event. | Stop until policy changes. |

The reference Go client has no durable local inbox. It therefore skips `accepted` and sends `completed` or `failed` after the runtime process exits. A backend MUST support this valid transition:

```text
pending ───────────────▶ completed
   │
   ├───────────────────▶ failed
   ├───────────────────▶ rejected
   └──▶ accepted ──────▶ completed | failed | rejected
```

The backend MUST validate all of the following before applying an acknowledgement:

- the delivery exists in the authenticated tenant;
- `event_id` matches the stored delivery;
- the connected device owns the delivery;
- the status is recognized;
- the transition does not incorrectly move a terminal delivery backward.

Duplicate acknowledgements for the same or a later terminal state SHOULD be idempotent. Conflicting terminal acknowledgements SHOULD be recorded and rejected rather than silently overwriting history.

The optional `result` is untrusted diagnostic JSON. It MUST be size-limited and MUST NOT be treated as authorization, billing, or executable data.

## 8. Idempotency and deduplication

AWP guarantees at-least-once delivery, not network-level exactly-once execution.

`event_id` is the publication idempotency key. At minimum, enforce a unique constraint on `(tenant_id, event_id)`. When a publisher repeats an event:

- return the original `delivery_id`;
- return the current delivery status;
- set `duplicate: true`;
- do not create another delivery or wake the client again solely because the HTTP request was repeated.

If the same `(tenant_id, event_id)` is reused with a different target or different payload, return `409 Conflict`. Do not silently treat distinct content as the same event.

The backend SHOULD also make acknowledgement handling idempotent by storing a transition history or a monotonic terminal state.

## 9. Offline queue and retries

When a target device has no current connection, publication still succeeds after persistence and remains `pending`.

On a valid `client.hello`, the provider SHOULD schedule pending deliveries for that tenant/device. Ordering policy must be documented. FIFO by creation time is a reasonable default, but strict global ordering is not required by AWP `0.1`.

A production retry record SHOULD contain:

- `attempt_count`;
- `next_attempt_at`;
- `last_attempt_at`;
- `lease_owner` and `lease_expires_at`;
- `last_error_code`;
- terminal status and timestamp;
- retention/expiry timestamp.

Recommended retry behavior:

1. atomically claim a pending delivery with a short lease;
2. verify the device still has a current authenticated connection;
3. increment `attempt` and send `event.deliver`;
4. wait for an acknowledgement deadline;
5. if the socket closes or the deadline expires, release/reschedule the delivery;
6. use exponential backoff with jitter and a configured maximum;
7. stop after expiry or a terminal state and move exhausted deliveries to a dead-letter state/queue.

Retries must survive process restarts. An in-memory `asyncio.Queue`, map, or goroutine/channel is suitable only for the local example backend.

The backend SHOULD define:

- maximum offline retention;
- maximum delivery attempts;
- acknowledgement deadline;
- retry backoff range;
- per-device concurrency;
- whether multiple events for one session may run concurrently;
- operator behavior for dead-letter deliveries.

## 10. Suggested persistent data model

Names are illustrative. SQL implementations can use the following logical records:

### `devices`

| Field | Purpose |
| --- | --- |
| `tenant_id`, `device_id` | Composite identity and ownership boundary. |
| `created_at`, `last_seen_at` | Lifecycle and diagnostics. |
| `revoked_at` | Prevent future connections. |
| `client_name`, `client_version`, `capabilities_json` | Last advertised client information. |

### `sessions`

| Field | Purpose |
| --- | --- |
| `tenant_id`, `session_id` | Unique AWP session identity. |
| `device_id` | Routing destination. |
| `adapter`, `metadata_json` | Non-secret client advertisement. |
| `created_at`, `updated_at`, `disabled_at` | Lifecycle. |

This table MUST NOT contain the Codex/Claude runtime session ID.

### `events`

| Field | Purpose |
| --- | --- |
| `tenant_id`, `event_id` | Unique publication idempotency key. |
| `publisher_id` | Authenticated source principal. |
| `device_id`, `session_id` | Immutable target. |
| `source`, `name`, `event_timestamp` | Queryable event metadata. |
| `payload_json` | Opaque original event data. |
| `payload_hash` | Detect conflicting reuse of `event_id`. |
| `created_at`, `expires_at` | Retention. |

### `deliveries`

| Field | Purpose |
| --- | --- |
| `tenant_id`, `delivery_id` | Stable delivery identity. |
| `event_id` | Associated event. |
| `status` | Pending/accepted/completed/failed/rejected/expired. |
| `attempt_count`, `next_attempt_at` | Retry scheduling. |
| `lease_owner`, `lease_expires_at` | Safe concurrent worker claim. |
| `last_error`, `result_json` | Bounded diagnostics. |
| status timestamps | Audit and latency metrics. |

Recommended constraints include unique `(tenant_id, event_id)`, unique `(tenant_id, delivery_id)`, and foreign keys that retain the tenant boundary.

## 11. Scaling beyond one process

The example FastAPI backend stores connections and deliveries in one process. Do not run it with multiple workers: each worker would have a different map and could not find sockets owned by another worker.

A horizontally scaled backend needs three layers:

1. **durable database** for devices, sessions, events, deliveries, idempotency, and leases;
2. **connection ownership registry** mapping a tenant/device to a live provider-backend instance and connection ID, usually with TTL/heartbeats;
3. **inter-instance notification bus** so an HTTP publisher or queue worker can notify the instance holding the target socket.

Examples of suitable building blocks are PostgreSQL for durable records and Redis/NATS/Kafka for notifications. The notification bus is not the source of truth; loss of a notification must be recoverable by scanning durable pending deliveries.

Use database uniqueness and compare-and-set/lease operations for correctness. Process-local locks alone do not protect a multi-instance deployment.

Backpressure requirements:

- bound per-connection send queues;
- limit in-flight deliveries per device/session;
- disconnect or slow consumers instead of growing memory indefinitely;
- rate-limit publishers by tenant and source;
- set maximum event, result, and metadata sizes;
- expose queue depth and oldest-pending-age metrics.

## 12. Errors

WebSocket protocol errors use the AWP `error` action:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_error_01",
  "action": "error",
  "timestamp": "2026-07-19T12:00:05Z",
  "data": {
    "code": "session_not_found",
    "message": "The target session binding does not exist",
    "reply_to": "msg_publish_01",
    "retryable": false
  }
}
```

Recommended stable error codes:

| Code | Retryable | Meaning |
| --- | --- | --- |
| `invalid_message` | No | Envelope or JSON is invalid. |
| `unsupported_version` | No | No compatible protocol version. |
| `upgrade_required` | No | Client does not advertise the required permission capability. |
| `hello_required` | No | First client action was not `client.hello`. |
| `invalid_hello` | No | Hello action data is invalid. |
| `unauthorized` | No | Credential is missing/invalid or scope is insufficient. |
| `unexpected_action` | No | Action is invalid in this direction/state. |
| `invalid_action_data` | No | Action data fails validation. |
| `session_not_found` | No | No visible target session. |
| `session_conflict` | No | Session cannot be rebound to that device. |
| `delivery_not_found` | No | Delivery is missing, mismatched, or not visible. |
| `rate_limited` | Yes | Caller should retry after a delay. |
| `temporarily_unavailable` | Yes | Transient backend/storage failure. |

`message` is for diagnostics and may change. Integrations SHOULD branch on `code`, not text. `reply_to` SHOULD identify the triggering message. Do not expose internal stack traces, SQL details, credentials, or cross-tenant resource existence.

Malformed or disallowed messages may receive an error while keeping the socket open. Authentication failure, oversized frames, invalid initial hello, or repeated abuse SHOULD close the connection.

## 13. Security checklist

An AWP event can wake an agent, so the backend is part of a security boundary.

- Use TLS for all non-local traffic.
- Separate client and publisher credentials and scopes.
- Enforce tenant ownership on every lookup and update.
- Never accept a runtime session ID from the wire.
- Treat event payloads, metadata, ACK results, and error text as untrusted.
- Never interpolate event content into shell commands.
- Limit JSON size, depth, strings, arrays, metadata, and ACK results.
- Apply rate limits per credential, tenant, device, session, IP, and event source as appropriate.
- Protect against replay with scoped tokens, idempotent event IDs, and optional request signatures/nonces.
- Redact bearer tokens and sensitive application payloads from logs.
- Encrypt sensitive payloads and credentials at rest according to product requirements.
- Audit credential changes, session rebinds, publication, acknowledgement, rejection, and administrative replay.
- Support retention and deletion policies for events and diagnostics.
- Do not interpret event text as trusted system/developer instructions. The AWP Client also marks the event as untrusted when resuming the runtime.

If a provider exposes an internet-facing publication API to trusted integrations, request signing can be added in addition to bearer authentication. A typical design signs the HTTP method, path, timestamp, nonce, and exact body hash, rejects stale timestamps, and stores recent nonces to block replay. Signature details are not yet standardized in AWP `0.1`.

## 14. Observability and operations

Useful metrics:

- active WebSocket connections;
- connected devices by client version;
- published, duplicate, delivered, completed, failed, rejected, expired events;
- pending delivery count and oldest pending age;
- attempts per delivery;
- publish-to-deliver and publish-to-complete latency;
- authentication and authorization failures;
- reconnect rate, heartbeat timeouts, send failures;
- dead-letter count and storage/notification errors.

Logs SHOULD include tenant-safe identifiers such as `connection_id`, `device_id`, `session_id`, `event_id`, `delivery_id`, `message_id`, status, and attempt. Avoid logging the token and default to excluding full `event.data`.

Health endpoints should distinguish:

- **liveness:** the process/event loop is running;
- **readiness:** durable storage and required messaging infrastructure are reachable;
- **diagnostics:** queue/connection counts, protected by authentication when sensitive.

## 15. Minimal implementation order

A practical build order is:

1. envelope and action-data validation;
2. separate client and publisher authentication;
3. authenticated WebSocket with hello/welcome;
4. durable session binding;
5. permission request after binding (recommended; the client can grant `runtime.wake` locally without it, see 4.4);
6. transactional HTTP event publication and idempotency;
7. online `event.deliver`;
8. offline pending queue;
9. ACK validation and terminal states;
10. retry leases, deadlines, backoff, and expiry;
11. heartbeat and stale-connection cleanup;
12. metrics, audit logs, limits, rotation, and horizontal scaling.

The repository's [`example/backend`](../example/backend) demonstrates one provider-owned AWP endpoint using FastAPI and in-memory state. It is intentionally not a central relay and not a production template for persistence or scaling.

## 16. Compatibility test checklist

Before calling a backend AWP `0.1` compatible, test at least:

- valid authentication succeeds; missing/invalid tokens fail;
- non-`client.hello` first messages are rejected;
- unsupported versions are rejected;
- hello receives welcome with matching `device_id`;
- clients without `capabilities.permissions=true` receive `upgrade_required`;
- same-device reconnect replaces or safely supersedes the old socket;
- if your backend sends `permission.request`, every bound session receives it before any delivery, and requests include valid `runtime.wake` and are resent after reconnect;
- session binding is idempotent for the same owner;
- cross-device and cross-tenant session rebinding fails;
- publisher cannot target resources outside its scope;
- event is committed before `202` is returned;
- offline publication is delivered after reconnect;
- duplicate `event_id` returns the original delivery;
- conflicting reuse of `event_id` returns `409`;
- retry reuses `delivery_id` and increments `attempt`;
- valid direct `pending → completed` acknowledgement works;
- mismatched event/delivery/device ACK is rejected;
- duplicate terminal ACK is idempotent;
- terminal deliveries are not redelivered;
- invalid and oversized WebSocket messages are bounded/rejected;
- heartbeat timeout removes only the stale connection;
- restart does not lose sessions, pending deliveries, or idempotency records;
- two provider-backend instances cannot concurrently claim the same delivery;
- application `event.data` survives publish/deliver without semantic changes.

Canonical wire examples are available in [`docs/examples`](./examples), and the concise action reference is in [`docs/PROTOCOL.md`](./PROTOCOL.md).

## 17. Current `0.1` boundaries

AWP `0.1` does not yet standardize:

- device/session provisioning and pairing APIs;
- token issuance and rotation endpoints;
- subscription/filter expressions;
- event batching or wake coalescing;
- multi-device fan-out;
- interactive approval transport for actions marked `interactive-only`;
- publisher request signatures;
- cursor-based delivery replay;
- retention defaults;
- a conformance test suite or JSON Schema package.

Backends may add these features, but extensions MUST preserve the core envelope, MUST remain tenant-safe, and SHOULD be capability-negotiated or namespaced so they do not break `0.1` clients.

The core interoperability rule is simple:

> Each provider persists and routes its own events through its own authenticated AWP endpoint, while runtime-specific identity and execution stay entirely on the local client.
