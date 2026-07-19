# Agent Wake Protocol (AWP)

**Wake dormant AI agent sessions with external events.**

Agent Wake Protocol (AWP) is an open protocol for delivering events from always-on systems to inactive agent sessions such as Codex, Claude Code, and other agent runtimes.

MCP lets an active agent call tools. AWP lets an external system wake the agent when something happens.

AWP is created and developed by the [Manifestro](https://github.com/Manifestro) team. The canonical repository is [Manifestro/awp](https://github.com/Manifestro/awp).

> **Project status:** AWP is an early design draft. The protocol, transports, and APIs are not stable yet. We are developing the standard in public and welcome discussion and contributions.

## Documentation

| Document | Purpose |
| --- | --- |
| [AWP wire protocol](./docs/PROTOCOL.md) | AWP `0.1` envelopes, actions, connection lifecycle, and acknowledgements |
| [Backend implementation guide](./docs/HOW_TO_CREATE_AWP_BACKEND.md) | Persistence, routing, retries, security, scaling, and compatibility requirements |
| [JSON examples](./docs/examples) | Ready-to-use messages for every current protocol action |
| [Conceptual draft](./AWP.md) | Original problem statement, roles, and design direction |
| [Example backend](./example/backend) | Local FastAPI service for development and end-to-end testing |

## Why AWP?

Agent sessions often need to wait for something outside their current execution:

- a customer replies on WhatsApp;
- a pull request receives a new comment;
- a background job completes;
- monitoring detects an incident;
- an email arrives;
- a scheduled time is reached.

Today, these systems can expose tools through MCP or send traditional webhooks. However, a local agent session may be inactive, behind NAT, running on a device with a dynamic IP, and unable to receive an inbound webhook.

AWP introduces a standard delivery path between the system producing the event and the local runtime capable of resuming the agent session.

## How it works

```text
┌──────────────┐       AWP event       ┌─────────────┐
│ Event Server │ ────────────────────▶ │ AWP Service │
│   Sinores    │                       │ queue/relay │
└──────────────┘                       └──────┬──────┘
                                            │
                                  outbound connection
                                            │
                                     ┌──────▼─────┐
                                     │ AWP Client │
                                     └──────┬─────┘
                                            │ resume + event
                                  ┌─────────▼─────────┐
                                  │ Codex/Claude Code │
                                  └───────────────────┘
```

The AWP Client opens an outbound connection to the AWP Service. This means the user's computer does not need a public domain, static IP address, port forwarding, or an inbound firewall rule.

When an event occurs:

1. The Event Server creates an AWP event.
2. The AWP Service authenticates, queues, and routes the event.
3. The AWP Client receives it over an outbound connection.
4. The client finds the associated local session.
5. The client resumes Codex, Claude Code, or another compatible runtime with the event.

## Roles

### AWP Client

The local component installed alongside an IDE or agent runtime.

It is responsible for:

- maintaining an outbound connection to an AWP Service;
- registering local agent session bindings;
- receiving and acknowledging events;
- resuming the correct agent session;
- passing the event to the agent without changing its application data.

### AWP Service

The delivery service between Event Servers and AWP Clients. It behaves like a durable webhook relay for clients that cannot accept public inbound connections.

It is responsible for:

- authenticating servers and clients;
- accepting and routing events;
- storing events while a client is offline;
- retrying unacknowledged deliveries;
- preventing duplicate processing.

### Event Server

An always-on system where events originate.

Examples include Sinores, GitHub integrations, email services, monitoring systems, CI pipelines, and schedulers. An Event Server does not need to understand the internals of Codex or Claude Code.

## Event format

Every AWP `0.1` wire message uses a common envelope:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_01JABC123",
  "action": "event.publish",
  "timestamp": "2026-07-19T12:00:00Z",
  "data": {}
}
```

- `type` and `version` identify the protocol.
- `id` makes the individual protocol message traceable.
- `action` selects the schema and operation.
- `timestamp` is an RFC 3339 timestamp with a timezone.
- `data` contains fields for that action. Application-specific content belongs inside `data.event.data` for publication and delivery actions.

AWP transports `data` without defining the business fields inside it. This keeps the core protocol independent of WhatsApp, GitHub, email, or any other event source.

## Sinores example

[Sinores](https://sinores.net) is an MCP and REST gateway for WhatsApp. An active agent can use Sinores through MCP to send a message and then end its current turn. When the recipient replies, Sinores can create an AWP event:

```json
{
  "type": "awp",
  "version": "0.1",
  "id": "msg_publish_01",
  "action": "event.publish",
  "timestamp": "2026-07-19T12:00:02Z",
  "data": {
    "event_id": "evt_sinores_01",
    "target": {
      "device_id": "dev_macbook_01",
      "session_id": "ses_01JABC123"
    },
    "event": {
      "source": "sinores",
      "name": "message.received",
      "timestamp": "2026-07-19T12:00:02Z",
      "data": {
        "channel_id": "channel_123",
        "from": "+77001234567",
        "message_id": "wa_message_456",
        "text": "Yes, tomorrow at 10 works"
      }
    }
  }
}
```

The local AWP Client resumes the associated session and gives the event to the agent:

```text
New WhatsApp message received through Sinores.
Channel: channel_123
From: +77001234567
Message: Yes, tomorrow at 10 works
```

This creates a bidirectional agent workflow:

```text
Agent ──MCP──▶ Sinores ──▶ WhatsApp
Agent ◀──AWP── Sinores ◀── WhatsApp
```

## Design principles

- **Vendor-neutral:** AWP must work with different agent runtimes and event sources.
- **Local-first:** Clients behind NAT and dynamic IP addresses must work without inbound networking.
- **Durable:** Events should survive temporary client disconnections.
- **Opaque application data:** The protocol transports events without owning their business schema.
- **Secure by default:** Waking a session must never silently grant it additional permissions.
- **At-least-once delivery:** Events use stable identifiers and acknowledgements instead of claiming impossible network-level exactly-once delivery.
- **Adapter-based:** Runtime-specific resume behavior belongs in Codex, Claude Code, and other adapters.

## Scope

AWP aims to standardize:

- event envelopes;
- client and server authentication;
- session and subscription bindings;
- outbound client connections;
- delivery, acknowledgement, retry, and deduplication;
- offline event queues;
- manual and automatic wake policies;
- runtime adapter capabilities.

AWP does not define:

- how an agent reasons about an event;
- which tools an agent may call after waking;
- the internal storage format of agent sessions;
- application-specific fields inside `data`;
- a replacement for MCP or agent-to-agent protocols.

## Relationship to MCP

AWP complements MCP rather than replacing it:

| Protocol | Direction | Purpose |
| --- | --- | --- |
| MCP | Agent → external system | Give an active agent tools, resources, and context |
| AWP | External system → agent session | Deliver an event and resume an inactive agent |

## Current implementation

The repository currently contains:

- a draft AWP `0.1` WebSocket and HTTP protocol;
- a Go client with machine-readable, agent-friendly commands;
- a multi-session daemon using one outbound connection per device;
- a local session registry that never exposes runtime session IDs to the service;
- a Codex CLI adapter using `codex exec resume`;
- reconnect with exponential backoff;
- explicit, reversible macOS autostart;
- an in-memory FastAPI backend for local interoperability tests;
- a production-oriented backend implementation guide.

The FastAPI backend is a development example, not a durable production service. Claude Code, Linux autostart, formal JSON Schemas, and a conformance suite are not implemented yet.

## Go client quick start

Build the client:

```bash
go build -o ./bin/awp ./cmd/awp
```

Configure it using non-interactive commands that Codex or Claude Code can also execute directly:

```bash
./bin/awp config set \
  --service-url wss://awp.example.com/ws \
  --device-id dev_macbook_01 \
  --token-env AWP_TOKEN \
  --json

./bin/awp config show --json
./bin/awp doctor --json

# Bind an opaque AWP session to a local Codex CLI session.
./bin/awp sessions bind \
  --session-id ses_01JABC123 \
  --adapter codex \
  --runtime-session-id 019f79c6-0c42-76a3-8812-8ec8b77d3e66 \
  --workspace /path/to/project \
  --json

# Connect, wake Codex for one delivered event, and exit.
./bin/awp connect \
  --session-id ses_01JABC123 \
  --once \
  --timeout 30s \
  --json
```

For a long-running multi-session client, start the daemon. It loads every binding from the local `sessions.json`, registers them over one WebSocket, routes each delivery by `target.session_id`, and reconnects with exponential backoff:

```bash
./bin/awp daemon --json
```

### Optional autostart

Autostart is explicit and editable; installing the AWP client never enables it automatically. The first implementation uses one per-user macOS `launchd` agent for the multi-session daemon.

```bash
# Enable launch at the next login, but do not start anything now.
./bin/awp autostart enable \
  --json

# Enable or update the definition and also start it now.
./bin/awp autostart enable \
  --start-now \
  --json

# Inspect both the saved definition and current launchd state.
./bin/awp autostart status \
  --json

# Stop the launch agent and remove its autostart definition.
./bin/awp autostart disable \
  --json
```

`autostart enable` copies the token from the configured environment variable into a separate local file with mode `0600`, because `launchd` does not inherit an interactive shell's environment. The token is never embedded in the plist or the main configuration. `autostart disable` intentionally leaves that protected token file in place. Running `enable --start-now` again updates the paths and token, restarts the daemon, and reloads all session bindings.

On platforms without an autostart adapter, run `awp daemon` under systemd, a container, or another process supervisor. Native Linux service management is planned.

The current configuration connects one daemon to one AWP Service while multiplexing any number of event sources and local sessions over that connection. Supporting several independent AWP Service endpoints from one daemon will use named connection profiles and is a separate planned capability.

The bearer token is not written to the configuration file. `token_env` contains only the name of the environment variable that holds it.

The mapping from an AWP `session_id` to the Codex runtime session remains in the local `sessions.json` registry and is never sent to the AWP Service. On delivery, the adapter invokes:

```text
codex exec resume --json <runtime-session-id> -
```

The universal AWP event prompt is passed through stdin. The adapter does not add sandbox bypasses, permission bypasses, model overrides, or other privilege-changing flags.

The main open design questions are:

- how clients and sessions are securely paired and provisioned;
- how publishers receive narrowly scoped authorization;
- how subscriptions and source filters are expressed;
- what default retention, retry, and dead-letter policies should be standardized;
- how manual approval and automatic wake policies are represented on the wire;
- how wake batching and coalescing should work;
- which capabilities need negotiation across protocol versions;
- how conformance is validated across independent clients and services.

## Roadmap

- [x] Define the problem and core roles
- [x] Publish the initial event envelope
- [x] Define initial protocol terminology and identifiers
- [ ] Specify pairing and authentication
- [ ] Specify subscriptions
- [x] Specify initial session bindings
- [x] Specify the WebSocket transport
- [x] Specify initial acknowledgement and retry behavior
- [ ] Create a JSON Schema for AWP events
- [x] Build a local example AWP Service
- [ ] Build a durable reference AWP Service
- [x] Build an AWP Client MVP
- [x] Build a Codex CLI adapter
- [x] Add reconnect/backoff and opt-in macOS autostart
- [x] Add one-connection multi-session daemon
- [ ] Add named profiles for multiple independent AWP Services
- [ ] Build a Claude Code adapter
- [ ] Add native Linux systemd autostart
- [ ] Add the first Sinores integration
- [ ] Publish interoperability tests

## Contributing

AWP is at the stage where architectural feedback is especially valuable. Issues and pull requests may propose:

- protocol terminology;
- event and transport schemas;
- security and authentication models;
- failure and reconnection behavior;
- adapter contracts;
- real-world wake scenarios.

Please treat all current names and schemas as experimental until the first stable specification is published.

- [Open an issue](https://github.com/Manifestro/awp/issues)
- [View pull requests](https://github.com/Manifestro/awp/pulls)
- [Visit Manifestro](https://github.com/Manifestro)

## License

A license has not been selected yet. Until a license file is added, no open-source license is granted by this repository. Selecting and adding an explicit license is required before the first public release.

---

Created by the [Manifestro](https://github.com/Manifestro) team.
