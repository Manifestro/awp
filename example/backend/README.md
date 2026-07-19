# AWP example backend

A protocol-aware FastAPI example showing how an MCP or API provider can add its own AWP endpoint. It exists for interoperability development and local end-to-end tests with the Go client; it is not a central AWP relay.

This is a local transport example. Before implementing a durable production provider endpoint, read the full [backend implementation guide](../../docs/HOW_TO_CREATE_AWP_BACKEND.md).

The example implements:

- authenticated WebSocket connections at `/awp`;
- `client.hello` and `server.welcome`;
- `session.bind` and `session.bound`;
- mandatory `permission.request` before delivery;
- authenticated `event.publish` at `POST /events`;
- `event.deliver` routing by `device_id` and `session_id`;
- in-memory offline delivery queues;
- `event_id` deduplication;
- `event.ack` processing;
- heartbeat ping/pong;
- one active connection per device.

This is a single-process reference backend. Its connections, session bindings, and delivery queue are intentionally stored in memory and are lost after restart.

## Run

From `example/backend`:

```bash
AWP_TOKEN=local-dev-token docker compose up --build
```

The API documentation is available at <http://localhost:8000/docs> and health information at <http://localhost:8000/health>.

## Configure the Go client

```bash
export AWP_TOKEN=local-dev-token

awp config set \
  --provider example \
  --service-url ws://localhost:8000/awp \
  --device-id dev_macbook_01 \
  --token-env AWP_TOKEN \
  --mcp-server none \
  --config /tmp/awp-example.json \
  --json
```

Bind the public AWP session identifier to a local Codex CLI session. Replace the runtime ID and workspace with real local values:

```bash
awp sessions bind \
  --config /tmp/awp-example.json \
  --provider example \
  --session-id ses_01JABC123 \
  --adapter codex \
  --runtime-session-id 019f79c6-0c42-76a3-8812-8ec8b77d3e66 \
  --workspace /path/to/project \
  --json
```

Fetch the provider request and grant only permission to wake. The example advertises illustrative message tools, but it does not implement an MCP server:

```bash
awp permissions request \
  --config /tmp/awp-example.json \
  --provider example \
  --session-id ses_01JABC123

awp permissions grant \
  --config /tmp/awp-example.json \
  --provider example \
  --session-id ses_01JABC123 \
  --allow runtime.wake \
  --scope binding
```

Start the daemon. It registers every locally bound AWP session over one device connection:

```bash
awp daemon \
  --config /tmp/awp-example.json \
  --once \
  --json
```

## Publish an event

After `ses_01JABC123` has been bound to `dev_macbook_01`:

```bash
curl -X POST http://localhost:8000/events \
  -H 'Authorization: Bearer local-dev-token' \
  -H 'Content-Type: application/json' \
  --data @../../docs/examples/05-event-publish-sinores.json
```

The example provider returns `202 Accepted` with a delivery identifier. If the device is online, it immediately sends `event.deliver`; otherwise the event remains in the in-memory pending queue until that device reconnects.

## Security

`AWP_TOKEN` protects both the WebSocket handshake and `POST /events`. The default `local-dev-token` is only for localhost development. Always provide a strong secret and TLS in any shared environment.
