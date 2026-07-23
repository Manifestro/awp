# Agent Wake Protocol (AWP)

**Wake an AI agent when something happens outside its current session.**

[AWP](https://github.com/Manifestro/awp) is an open protocol and local client for delivering external events to Codex, Claude Code, and other agent runtimes. It complements MCP:

```text
MCP: agent    ──request──▶ provider
AWP: provider ──event────▶ agent session
```

An agent can call a provider through MCP, finish its turn, and be resumed later when that provider has something new—such as an incoming message, a completed job, or a changed issue.

> **Project status:** early alpha. The AWP `0.1` protocol, Go client, multi-provider routing, Codex CLI adapter, and macOS autostart are usable. The protocol and configuration may still change.

## Why AWP?

MCP works while an agent is already running. Webhooks work well between public servers, but a developer laptop usually has a dynamic IP, no public domain, and no safe inbound port.

AWP solves the return path with an outbound WebSocket connection:

```text
┌────────────────────────────┐                 ┌─────────────────────────┐
│ Developer machine          │   outbound WSS  │ Provider                │
│                            │◀───────────────▶│                         │
│ AWP client                 │                 │ /mcp                    │
│   └─ runtime adapter       │                 │ /awp                    │
│       └─ Codex session     │                 │ events + durable queue  │
└────────────────────────────┘                 └─────────────────────────┘
```

The client needs no static IP, public domain, port forwarding, or central AWP relay.

## Provider-owned endpoints

AWP is a protocol, not a hosted event broker. Every MCP or API product operates its own authenticated AWP endpoint:

```text
Sinores:          https://sinores.net/mcp
                  wss://sinores.net/awp

Another product: https://provider.example/mcp
                  wss://provider.example/awp
```

The local client can connect to several providers at once. Each connection reconnects independently and can serve multiple agent sessions.

## Install

macOS and Linux, AMD64 and ARM64:

```bash
curl -LsSf https://awp.manifestro.io/install.sh | sh
```

The installer downloads the latest binary from [GitHub Releases](https://github.com/Manifestro/awp/releases), verifies its SHA-256 checksum, and installs it to `~/.local/bin/awp` without `sudo`.

Check the installation:

```bash
awp version
awp help
```

To install a specific version or use another destination:

```bash
curl -LsSf https://awp.manifestro.io/install.sh | \
  AWP_VERSION=0.2.0-alpha.1 AWP_INSTALL_DIR="$HOME/bin" sh
```

## Quick start with Codex

Requirements:

- an AWP provider URL and access token;
- [Codex CLI](https://github.com/openai/codex) installed and authenticated;
- an existing Codex session to resume.

### 1. Add a provider

Tokens are read from environment variables and are not written to the main configuration file.

```bash
export SINORES_TOKEN="your-token"

awp config set \
  --provider sinores \
  --service-url wss://sinores.net/awp \
  --device-id dev_my_macbook \
  --token-env SINORES_TOKEN \
  --mcp-server sinores
```

### 2. Bind an agent session

The AWP session ID is an opaque ID shared with the provider. The Codex runtime session ID stays only on your machine.

```bash
awp sessions bind \
  --provider sinores \
  --session-id ses_support \
  --adapter codex \
  --runtime-session-id <codex-session-id> \
  --workspace /absolute/path/to/project \
  --metadata-json '{"channel_id":"channel_123"}'
```

`metadata-json` is defined by the provider. Sinores might use a channel ID; another provider can use a repository, mailbox, job, or subscription ID.

### 3. Review provider permissions

If the provider sends a `permission.request`, fetch it and grant only what this binding needs:

```bash
awp permissions request \
  --provider sinores \
  --session-id ses_support

awp permissions grant \
  --provider sinores \
  --session-id ses_support \
  --allow runtime.wake,messages.read_new,messages.read_history \
  --scope binding
```

Most providers will not implement that handshake, at least not yet — that's fine. Grant `runtime.wake` (and any of the provider's own MCP tool names you already know) directly instead; AWP records the grant locally without a provider round-trip:

```bash
awp permissions grant \
  --provider sinores \
  --session-id ses_support \
  --allow runtime.wake,messages.read_new \
  --mcp-tools get_new_messages \
  --scope binding
```

AWP stores the grant locally and never edits `~/.codex/config.toml`. On every wake, the runtime adapter creates a one-run MCP tool allowlist from that grant. Ungranted tools remain unavailable to that background invocation. See [the permission model](./docs/PERMISSIONS.md) for details, and [the local grant fallback](./docs/PERMISSIONS.md#local-grants-without-a-provider-request) specifically.

### 4. Validate and run

```bash
awp doctor
awp daemon
```

When the provider sends an event for `ses_support`, AWP runs an isolated command equivalent to:

```bash
codex exec resume \
  --json \
  -c 'mcp_servers.sinores.enabled_tools=["get_new_messages","list_messages"]' \
  -c 'mcp_servers.sinores.tools."get_new_messages".approval_mode="approve"' \
  -c 'mcp_servers.sinores.tools."list_messages".approval_mode="approve"' \
  <codex-session-id> -
```

The event becomes the new message for that Codex session. After execution, the client acknowledges the delivery as completed or failed.

For a one-connection transport test:

```bash
awp connect --provider sinores --session-id ses_support --once --json
```

## Automatic startup

Automatic startup is always opt-in. Installing AWP does not start a background service.

On macOS:

```bash
# Create/update the LaunchAgent without starting it now
awp autostart enable

# Create/update it and start the daemon immediately
awp autostart enable --start-now

awp autostart status
awp autostart disable
```

Provider tokens used by `launchd` are copied into separate protected token files. They are not embedded in the plist. Linux service management is not implemented yet; run `awp daemon` with your preferred process supervisor.

## Updates

Check or install the latest verified GitHub release:

```bash
awp update check
awp update install
```

Automatic updates are opt-in and disabled by default:

```bash
awp update auto enable --interval-hours 24
awp update auto status
awp update auto disable
```

When enabled, the daemon checks at startup whenever the configured interval has elapsed. The updater verifies the release SHA-256 checksum and atomically replaces the current executable. A running daemon continues using its current in-memory version until restarted.

## Multiple providers and sessions

Routing is scoped by `(provider, session_id)`, so the same session name can be used by different products without collision:

```text
sinores / ses_support  ──▶ Codex session A
sinores / ses_sales    ──▶ Codex session B
issues  / ses_support  ──▶ Codex session C
```

Useful management commands:

```bash
awp config show --json
awp config remove --provider issues --json

awp sessions list --json
awp sessions list --provider sinores --json
awp sessions remove --provider sinores --session-id ses_support --json
```

## How delivery works

```text
1. Client connects to the provider's wss://.../awp endpoint.
2. Client authenticates and sends client.hello.
3. Client announces one or more opaque sessions with session.bind.
4. Provider creates or restores an event for a bound session.
5. Provider sends event.deliver.
6. Client acknowledges accepted and invokes the local runtime adapter.
7. Client acknowledges completed or failed.
```

All AWP `0.1` messages use a common envelope:

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

Provider-specific content is carried as opaque JSON inside `data.event.data`.

## Implement AWP in a provider

A provider implementing AWP is responsible for:

- exposing an authenticated `wss://.../awp` endpoint;
- authorizing devices, session bindings, and provider resources;
- retaining events while clients are offline according to its policy;
- delivering events at least once with stable event and delivery IDs;
- handling acknowledgements, retries, deduplication, and heartbeats;
- keeping Codex, Claude Code, and other runtime credentials out of the provider.

Start with the [backend implementation guide](./docs/HOW_TO_CREATE_AWP_BACKEND.md), then run the [FastAPI example](./example/backend) for a local interoperability test.

## Build from source

Requires Go 1.26 or newer:

```bash
git clone https://github.com/Manifestro/awp.git
cd awp
go test ./...
go build -o ./bin/awp ./cmd/awp
./bin/awp version
```

## Documentation

| Document | Contents |
| --- | --- |
| [Provider quickstart](./docs/PROVIDER_QUICKSTART.md) | Website-ready guide for adding AWP to an existing backend |
| [Permission model](./docs/PERMISSIONS.md) | Provider requests, local grants, scopes, audit, and one-run runtime isolation |
| [Protocol specification](./docs/PROTOCOL.md) | AWP `0.1` envelope, handshake, binding, delivery, ACKs, errors, and heartbeat |
| [Backend implementation guide](./docs/HOW_TO_CREATE_AWP_BACKEND.md) | Authentication, persistence, routing, retry, security, and production requirements |
| [JSON examples](./docs/examples) | Complete protocol messages for client and provider implementations |
| [Local FastAPI provider](./example/backend) | Runnable development backend with WebSocket delivery |
| [Release guide](./docs/RELEASING.md) | Binary releases and public installer hosting |
| [Design draft](./AWP.md) | Roles and original protocol direction |

## Security model

- Runtime session IDs remain on the local device and are never sent to providers.
- The client only makes outbound provider connections.
- A provider event grants nothing by itself; only an explicit local AWP grant can authorize wake or provider MCP tools.
- Event payloads are untrusted input and must not be treated as authorization.
- Providers must authorize every device, session, subscription, and delivery independently.
- At-least-once delivery requires idempotent processing and deduplication.

See the [backend security requirements](./docs/HOW_TO_CREATE_AWP_BACKEND.md#13-security-checklist) for production deployments.

## Roadmap

- [x] Initial AWP `0.1` wire protocol
- [x] Multi-provider and multi-session Go client
- [x] Codex CLI runtime adapter
- [x] Reconnect, backoff, acknowledgements, and macOS autostart
- [x] Runnable FastAPI provider example
- [x] Provider-requested permissions with local grants and per-wake Codex isolation
- [x] Verified update checks, self-update, and opt-in automatic updates
- [ ] Standard MCP-to-AWP subscription and pairing flow
- [ ] Token issuance and rotation conventions
- [ ] JSON Schemas and conformance test suite
- [ ] Claude Code runtime adapter
- [ ] Native Linux service management
- [ ] First production Sinores AWP endpoint

## Contributing

AWP is being developed in public by [Manifestro](https://github.com/Manifestro). Protocol proposals, provider implementations, runtime adapters, security reviews, schemas, and interoperability tests are welcome.

- [Issues](https://github.com/Manifestro/awp/issues)
- [Pull requests](https://github.com/Manifestro/awp/pulls)

## License

Copyright 2026 Manifestro.

Licensed under the [Apache License, Version 2.0](./LICENSE).

[awp.manifestro.io](https://awp.manifestro.io) · Apache-2.0
