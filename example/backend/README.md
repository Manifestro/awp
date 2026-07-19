# AWP example backend

A protocol-aware FastAPI implementation of an AWP Service MVP. It exists for interoperability development and local end-to-end tests with the Go client.

The example implements:

- authenticated WebSocket connections at `/ws`;
- `client.hello` and `server.welcome`;
- `session.bind` and `session.bound`;
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
  --service-url ws://localhost:8000/ws \
  --device-id dev_macbook_01 \
  --config /tmp/awp-example.json \
  --json
```

Bind the public AWP session identifier to a local Codex CLI session. Replace the runtime ID and workspace with real local values:

```bash
awp sessions bind \
  --config /tmp/awp-example.json \
  --session-id ses_01JABC123 \
  --adapter codex \
  --runtime-session-id 019f79c6-0c42-76a3-8812-8ec8b77d3e66 \
  --workspace /path/to/project \
  --json
```

Connect the client and register the bound AWP session with the service:

```bash
awp connect \
  --config /tmp/awp-example.json \
  --session-id ses_01JABC123 \
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

The service returns `202 Accepted` with a delivery identifier. If the device is online, it immediately sends `event.deliver`; otherwise the event remains in the in-memory pending queue until that device reconnects.

## Security

`AWP_TOKEN` protects both the WebSocket handshake and `POST /events`. The default `local-dev-token` is only for localhost development. Always provide a strong secret and TLS in any shared environment.
