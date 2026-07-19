# Agent Wake Protocol (AWP)

**The event channel that complements MCP.**

MCP lets an active agent call a service. AWP lets that same service deliver a later event back to the agent and resume the correct local session.

```text
Agent ── MCP ──▶ Provider: call tools now
Agent ◀─ AWP ─── Provider: receive events later
```

AWP is created by [Manifestro](https://github.com/Manifestro). The canonical repository is [Manifestro/awp](https://github.com/Manifestro/awp).

> **Status:** runnable MVP and early protocol draft. The Go multi-provider client, multi-session routing, Codex CLI adapter, macOS autostart, and example provider endpoint work end to end. Protocol and local configuration formats are not stable yet.

## Core architecture

There is no central AWP server.

Every product that wants to wake agents exposes its own AWP endpoint beside its MCP endpoint:

```text
Sinores
  MCP: https://sinores.net/mcp
  AWP: wss://sinores.net/awp

Another provider
  MCP: https://provider.example/mcp
  AWP: wss://provider.example/awp
```

The local AWP daemon connects outward to every configured provider:

```text
                           ┌─▶ Sinores AWP
Local AWP daemon ──────────┼─▶ GitHub provider AWP
                           └─▶ Email provider AWP

Local AWP daemon
  ├─ sinores / ses_support  ─▶ Codex runtime session A
  ├─ sinores / ses_sales    ─▶ Codex runtime session B
  └─ github  / ses_project  ─▶ Codex runtime session C
```

Each provider connection is independent. If Sinores is offline, GitHub events can continue to work. Each connection may bind multiple local agent sessions.

The client initiates all WebSocket connections, so the user's computer needs no public domain, static IP, inbound port, or port forwarding.

## Provider responsibilities

An MCP/API provider implementing AWP is responsible for:

- exposing an authenticated `wss://.../awp` endpoint;
- accepting `client.hello` and one or more `session.bind` messages;
- associating its own application resources with opaque AWP session IDs;
- retaining events while a client is offline according to its policy;
- delivering `event.deliver` to the correct device and session;
- handling acknowledgements, retries, deduplication, and heartbeats.

The provider never receives the Codex or Claude runtime session ID. That mapping stays only on the user's device.

## Client responsibilities

The local AWP client is responsible for:

- maintaining one outbound connection per configured provider;
- registering all local bindings relevant to each provider;
- routing by the pair `(provider, session_id)`;
- resuming the correct local runtime through an adapter;
- preserving existing runtime permissions;
- sending `completed`, `failed`, or another valid acknowledgement.

## Relationship to MCP

| Protocol | Direction | Purpose |
| --- | --- | --- |
| MCP | Agent → provider | Call tools, read resources, and perform actions while the agent is active |
| AWP | Provider → agent session | Deliver a later event and resume an inactive local session |

Example with Sinores:

```text
1. Codex calls Sinores through https://sinores.net/mcp.
2. Sinores sends a WhatsApp message.
3. Codex finishes its turn.
4. Sinores later receives a WhatsApp reply.
5. wss://sinores.net/awp delivers message.received.
6. The local daemon resumes the bound Codex session.
```

## Documentation

| Document | Purpose |
| --- | --- |
| [Wire protocol](./docs/PROTOCOL.md) | AWP `0.1` envelopes, connection lifecycle, bindings, delivery, ACKs, and heartbeat |
| [Provider implementation guide](./docs/HOW_TO_CREATE_AWP_BACKEND.md) | How an MCP/API product implements its own AWP backend endpoint |
| [JSON messages](./docs/examples) | Current protocol examples |
| [Conceptual draft](./AWP.md) | Roles, provider-owned endpoint model, and design direction |
| [FastAPI example](./example/backend) | Local example of one provider's AWP endpoint |
| [Release and installer hosting](./docs/RELEASING.md) | GitHub Releases and serving `install.sh` from the Manifestro website |

## Current implementation

The repository contains:

- AWP `0.1` JSON messages over WebSocket and HTTP publication;
- a Go client with agent-friendly JSON output;
- multi-provider configuration;
- provider-scoped local session bindings;
- one independent connection and reconnect loop per provider;
- multiple sessions per provider connection;
- parallel processing across sessions and sequential processing within one session;
- a Codex CLI adapter using `codex exec resume`;
- explicit, reversible macOS daemon autostart;
- a FastAPI example of a provider-owned AWP endpoint.

Not implemented yet:

- Claude Code adapter;
- Linux systemd installer;
- formal JSON Schemas and conformance suite;
- a standardized MCP-to-AWP association operation;
- stable pairing, credential issuance, and subscription APIs.

## Install

On macOS or Linux:

```bash
curl -LsSf https://awp.manifestro.io/install.sh | sh
```

The installer:

- detects `darwin`/`linux` and `amd64`/`arm64`;
- downloads the latest binary from [GitHub Releases](https://github.com/Manifestro/awp/releases);
- verifies the release SHA-256 checksum before extraction;
- installs to `$HOME/.local/bin/awp` without `sudo`.

Install a specific version or directory:

```bash
curl -LsSf https://awp.manifestro.io/install.sh | \
  AWP_VERSION=0.1.0-alpha.1 AWP_INSTALL_DIR=/usr/local/bin sh
```

If the chosen directory requires elevated permissions, create it and grant access separately; the installer never invokes `sudo` itself.

## Build

Requirements are Go and an installed runtime such as Codex CLI.

```bash
go build -o ./bin/awp ./cmd/awp
./bin/awp version --json
```

Use a stable binary location before enabling autostart. The launch agent records the absolute path to the current executable.

## Configure providers

Configuration version `0.2` stores a device ID and a map of provider-owned AWP endpoints. Tokens remain in environment variables.

This is a breaking replacement for the earlier single-service `0.1` local config. Recreate old configs explicitly because AWP cannot safely guess which provider owns an existing binding.

```bash
export SINORES_TOKEN=your-sinores-token
export GITHUB_AWP_TOKEN=your-github-provider-token

./bin/awp config set \
  --provider sinores \
  --service-url wss://sinores.net/awp \
  --device-id dev_macbook_01 \
  --token-env SINORES_TOKEN \
  --json

./bin/awp config set \
  --provider github \
  --service-url wss://github-provider.example/awp \
  --token-env GITHUB_AWP_TOKEN \
  --json

./bin/awp config show --json
./bin/awp doctor --json
```

Generated configuration:

```json
{
  "version": "0.2",
  "device_id": "dev_macbook_01",
  "providers": {
    "sinores": {
      "service_url": "wss://sinores.net/awp",
      "token_env": "SINORES_TOKEN"
    },
    "github": {
      "service_url": "wss://github-provider.example/awp",
      "token_env": "GITHUB_AWP_TOKEN"
    }
  }
}
```

Remove a provider explicitly:

```bash
./bin/awp config remove --provider github --json
```

## Bind local sessions

Bindings are scoped by provider. The same `session_id` may exist under two providers without collision.

```bash
./bin/awp sessions bind \
  --provider sinores \
  --session-id ses_support \
  --adapter codex \
  --runtime-session-id <existing-codex-session-id> \
  --workspace /absolute/path/to/project \
  --metadata-json '{"channel_id":"channel_123"}' \
  --json

./bin/awp sessions bind \
  --provider github \
  --session-id ses_project \
  --adapter codex \
  --runtime-session-id <another-codex-session-id> \
  --workspace /absolute/path/to/project \
  --json

./bin/awp sessions list --json
./bin/awp sessions list --provider sinores --json
```

The local registry contains the runtime session IDs and is saved with mode `0600`. Runtime IDs are never sent to providers.

`metadata-json` is provider-defined association data and is sent in `session.bind`. For example, Sinores may associate `channel_id` with `ses_support`. A provider may instead expose an MCP tool that accepts the opaque AWP `session_id`; the common MCP-to-AWP association operation is not standardized yet.

## Run the daemon

```bash
./bin/awp daemon --json
```

The daemon loads all providers and bindings, then starts independent connection loops:

```text
sinores loop: connect → bind sinores sessions → receive → reconnect
github loop:  connect → bind github sessions  → receive → reconnect
```

Daemon JSON Lines wrap each protocol message with its provider:

```json
{
  "provider": "sinores",
  "message": {
    "type": "awp",
    "version": "0.1",
    "action": "event.deliver"
  }
}
```

For a single connection test:

```bash
./bin/awp connect \
  --provider sinores \
  --session-id ses_support \
  --once \
  --json
```

## Optional macOS autostart

Autostart is opt-in. Installing or building AWP never enables it.

```bash
# Save the launch definition; do not start now.
./bin/awp autostart enable --json

# Save/update it and start the multi-provider daemon now.
./bin/awp autostart enable --start-now --json

./bin/awp autostart status --json
./bin/awp autostart disable --json
```

Because `launchd` does not inherit an interactive shell's environment, `autostart enable` copies every configured provider token into a separate protected `<provider>.token` file. Tokens are not embedded in the plist or main config. Disabling autostart leaves the protected token directory in place.

## Run the local provider example

The FastAPI example behaves as one provider's AWP backend. It is not a shared or central service.

```bash
export AWP_TOKEN=local-dev-token
docker compose -f example/backend/compose.yaml up -d --build

./bin/awp config set \
  --provider example \
  --service-url ws://localhost:8000/awp \
  --device-id dev_macbook_01 \
  --token-env AWP_TOKEN \
  --config ./.awp/config.json \
  --json

./bin/awp sessions bind \
  --provider example \
  --session-id ses_01JABC123 \
  --adapter codex \
  --runtime-session-id <existing-codex-session-id> \
  --workspace /absolute/path/to/project \
  --config ./.awp/config.json \
  --json

AWP_TOKEN=local-dev-token ./bin/awp daemon \
  --config ./.awp/config.json \
  --json
```

Publish the fixture from another terminal:

```bash
curl -X POST http://localhost:8000/events \
  -H 'Authorization: Bearer local-dev-token' \
  -H 'Content-Type: application/json' \
  --data @docs/examples/05-event-publish-sinores.json
```

Expected lifecycle:

```text
provider creates event
  → provider's /awp endpoint sends event.deliver
  → daemon selects (provider, session_id)
  → codex exec resume --json <runtime-session-id> -
  → daemon sends completed or failed event.ack to that provider
```

Stop the example:

```bash
docker compose -f example/backend/compose.yaml down
```

The example keeps all state in memory and loses it on restart. It is for interoperability testing, not production deployment.

## Wire envelope

Every AWP `0.1` message uses:

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

Provider-specific application content belongs in `data.event.data`. The AWP transport preserves it as opaque, untrusted JSON.

## Design principles

- **Provider-owned:** every MCP/API product exposes and operates its own AWP endpoint.
- **Complementary to MCP:** MCP is the active request path; AWP is the later event path.
- **Multi-provider:** one local daemon may connect to many independent products.
- **Provider-scoped sessions:** routing uses `(provider, device_id, session_id)`.
- **Local runtime identity:** vendor runtime IDs never leave the client.
- **Outbound-only client networking:** no inbound port or static client IP.
- **At-least-once delivery:** stable event and delivery IDs plus acknowledgements.
- **Secure by default:** waking a session never adds permissions.

## Roadmap

- [x] Define provider-owned AWP endpoints beside MCP
- [x] Specify the initial AWP `0.1` wire protocol
- [x] Build the Go multi-provider, multi-session daemon
- [x] Build the Codex CLI adapter
- [x] Add reconnect/backoff and opt-in macOS autostart
- [x] Publish a local provider backend example
- [ ] Standardize how an MCP interaction associates resources with an AWP session
- [ ] Specify provider pairing, token issuance, and rotation
- [ ] Create JSON Schemas and a provider/client conformance suite
- [ ] Build the Claude Code adapter
- [ ] Add native Linux systemd management
- [ ] Add the first production Sinores AWP endpoint

## Contributing

Useful contributions include provider implementations, runtime adapters, protocol proposals, security review, JSON Schemas, and interoperability tests.

- [Open an issue](https://github.com/Manifestro/awp/issues)
- [View pull requests](https://github.com/Manifestro/awp/pulls)
- [Visit Manifestro](https://github.com/Manifestro)

## License

A license has not been selected yet. Until a license file is added, no open-source license is granted by this repository.

---

Created by the [Manifestro](https://github.com/Manifestro) team.
