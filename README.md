# Agent Wake Protocol (AWP)

**Wake dormant AI agent sessions with external events.**

Agent Wake Protocol (AWP) is an open protocol for delivering events from always-on systems to inactive agent sessions such as Codex, Claude Code, and other agent runtimes.

MCP lets an active agent call tools. AWP lets an external system wake the agent when something happens.

AWP is created and developed by the [Manifestro](https://github.com/Manifestro) team. The canonical repository is [Manifestro/awp](https://github.com/Manifestro/awp).

> **Project status:** AWP is an early design draft. The protocol, transports, and APIs are not stable yet. We are developing the standard in public and welcome discussion and contributions.

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

The smallest AWP event envelope is:

```json
{
  "type": "awp",
  "data": {}
}
```

- `type` identifies the payload as an AWP event.
- `data` contains application-specific JSON supplied by the Event Server.

AWP transports `data` without defining the business fields inside it. This keeps the core protocol independent of WhatsApp, GitHub, email, or any other event source.

## Sinores example

[Sinores](https://sinores.net) is an MCP and REST gateway for WhatsApp. An active agent can use Sinores through MCP to send a message and then end its current turn. When the recipient replies, Sinores can create an AWP event:

```json
{
  "type": "awp",
  "data": {
    "source": "sinores",
    "event": "message.received",
    "channel_id": "channel_123",
    "from": "+77001234567",
    "message_id": "wa_message_456",
    "text": "Yes, tomorrow at 10 works"
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

## Current work

The conceptual draft is being developed in [AWP.md](./AWP.md). The first client/server wire format and ready-to-use JSON fixtures are documented in [docs/PROTOCOL.md](./docs/PROTOCOL.md) and [`docs/examples`](./docs/examples) in the canonical [Manifestro/awp](https://github.com/Manifestro/awp) repository.

An early Go client foundation is also available. Its configuration commands are non-interactive and support stable JSON output so coding agents can configure and inspect the client directly:

```bash
awp config set \
  --service-url wss://awp.example.com/ws \
  --device-id dev_macbook_01 \
  --token-env AWP_TOKEN \
  --json

awp config show --json
awp doctor --json

# Connect, acknowledge one delivered event, and exit.
awp connect \
  --session-id ses_01JABC123 \
  --adapter codex \
  --once \
  --timeout 30s \
  --json
```

The bearer token is not written to the configuration file. `token_env` contains only the name of the environment variable that holds it.

The main open design questions are:

- how an event securely identifies its target client and session;
- how a session subscribes to events;
- how pairing and authentication work;
- how WebSocket delivery and reconnection work;
- how delivery acknowledgements and retries work;
- how long offline events remain queued;
- how manual and automatic wake policies are expressed;
- how runtime permissions are preserved when a session resumes.

## Roadmap

- [x] Define the problem and core roles
- [x] Publish the initial event envelope
- [ ] Define protocol terminology and identifiers
- [ ] Specify pairing and authentication
- [ ] Specify subscriptions and session bindings
- [ ] Specify the WebSocket transport
- [ ] Specify acknowledgement and retry behavior
- [ ] Create a JSON Schema for AWP events
- [ ] Build a reference AWP Service
- [ ] Build an AWP Client
- [ ] Build Codex and Claude Code adapters
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
