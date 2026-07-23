# Agent Wake Protocol (AWP)

**Wake an AI agent when something happens outside its current session.**

AWP is an open protocol and local client for delivering external events to Claude Code, Codex, and other agent runtimes. It complements MCP:

```text
MCP: agent    ──request──▶ provider
AWP: provider ──event────▶ agent session
```

An agent calls a provider through MCP, finishes its turn, and gets resumed later when that provider has something new — an incoming message, a completed job, a changed issue.

> **Project status:** early alpha. The protocol, client, MCP server, and both the Codex and generic (Claude Code, or any other CLI) adapters are usable. Configuration may still change between releases.

## Why AWP?

MCP works while an agent is already running. Webhooks work between public servers, but a developer's laptop usually has a dynamic IP, no public domain, and no safe inbound port.

AWP solves the return path with an outbound WebSocket connection:

```text
┌────────────────────────────┐                 ┌─────────────────────────┐
│ Your machine                │   outbound WSS  │ Provider                │
│                            │◀───────────────▶│                         │
│ AWP client                 │                 │ /mcp                    │
│   └─ runtime adapter       │                 │ /awp                    │
│       └─ agent session     │                 │ events + durable queue  │
└────────────────────────────┘                 └─────────────────────────┘
```

No static IP, public domain, port forwarding, or central relay needed. Each provider (Sinores, or anything else exposing an AWP endpoint) runs its own `wss://.../awp`; the client can hold several such connections open at once, each serving one or more agent sessions.

## Install

macOS and Linux, AMD64 and ARM64:

```bash
curl -LsSf https://awp.manifestro.io/install.sh | sh
```

Verifies the SHA-256 checksum and installs to `~/.local/bin/awp` without `sudo`.

```bash
awp version
awp help
```

## Quickest way to connect: through MCP

If your agent runtime supports MCP, this is the whole setup — no CLI required. Add the local server once:

```bash
claude mcp add awp -- awp mcp
```

(Codex config is TOML, add to `~/.codex/config.toml` instead: `[mcp_servers.awp]` / `command = "awp"` / `args = ["mcp"]`.)

Then just tell your agent what to do — for example: *"Connect this session to `<provider>` at `wss://.../awp` with token `<token>`, and wake this same session whenever something arrives."* It calls, in order:

1. **`configure_provider`** — endpoint + token. The token goes into a private `0600` file next to config, never into the shared config file.
2. **`set_awp`** — registers this exact session (its own runtime id and resume command) to wake later.
3. **`request_permissions`** / **`grant_permissions`** — if the provider sent a permission request, review and grant it; if not, grant `runtime.wake` and the specific provider tools directly. Either way a human still approves the underlying tool call.
4. **`start_daemon`** — connects and starts waiting for events. **`stop_daemon`** turns it back off; while stopped, nothing is received or queued.

Other useful tools: `wake_context` (why was I woken, what's still pending), `list_pending_events`, `list_sessions`, `pause_session`/`resume_session` (hold a session's deliveries without touching the provider connection), `daemon_status`.

## Quickest way to connect: CLI

Same steps, run by hand:

```bash
export SINORES_TOKEN="your-token"

awp config set --provider sinores --service-url wss://sinores.net/awp \
  --device-id dev_my_macbook --token-env SINORES_TOKEN

awp sessions bind --provider sinores --session-id ses_support \
  --adapter codex --runtime-session-id <codex-session-id> \
  --workspace /absolute/path/to/project --metadata-json '{"channel_id":"channel_123"}'

# If the provider sent a permission.request:
awp permissions request --provider sinores --session-id ses_support
awp permissions grant --provider sinores --session-id ses_support \
  --allow runtime.wake,messages.read_new --scope binding

# If it didn't — grants locally, no round-trip:
awp permissions grant --provider sinores --session-id ses_support \
  --allow runtime.wake,messages.read_new --mcp-tools get_new_messages --scope binding

awp doctor
awp daemon start --provider sinores
```

`metadata-json` is provider-defined — a channel, repo, mailbox, or job ID. `awp daemon start` runs detached with a PID file (`awp daemon stop`/`status` to manage it); plain `awp daemon` runs in the foreground. `awp connect --once --timeout 30s --json` does a one-shot handshake-and-delivery test.

## How a wake actually runs

When an event arrives for a bound session, AWP builds and runs a one-shot, isolated invocation of the registered runtime — never touching global config:

```bash
codex exec resume --json \
  -c 'mcp_servers.sinores.enabled_tools=["get_new_messages"]' \
  -c 'mcp_servers.sinores.tools."get_new_messages".approval_mode="approve"' \
  <runtime-session-id> -
```

Only the tools actually granted for that session are enabled, and only for that one process. A binding registered through `set_awp` with a custom `resume_command` (e.g. for Claude Code) gets the same granted-tool list through a `{mcp_tools_prefixed_csv}`/`{mcp_tools_json}` placeholder instead of hardcoded flags — any runtime can register itself this way without AWP needing built-in support for it.

The event itself is passed to the runtime as untrusted external data, with an explicit instruction not to treat it as trusted system input — same as any external content a running agent might read.

## Safety properties

- Runtime session IDs and credentials never leave your machine; a provider only ever sees an opaque session ID it assigned itself.
- A provider event grants nothing by itself. Only an explicit local grant — reviewed by a human, once — authorizes a wake or a specific tool.
- Redelivery of an already-completed event is deduplicated locally and does not wake the runtime again or spend a turn.
- A session can be paused (held, still queuing events) or found structurally broken (`crashed`, e.g. missing binary/workspace) without losing what arrived while it was down.
- While the daemon is stopped, nothing is received or queued — a provider's own retry/offline policy governs what happens to events sent during that time.

## Multiple providers and sessions

Routing is scoped by `(provider, session_id)`, so the same session name can be reused across providers without collision:

```bash
awp sessions list --json
awp sessions remove --provider sinores --session-id ses_support --json
awp sessions remove --all --provider sinores --json   # drop every binding for one provider
```

## Automatic startup (macOS)

Opt-in; installing AWP does not start a background service.

```bash
awp autostart enable [--start-now]
awp autostart status
awp autostart disable
```

Provider tokens used by `launchd` live in separate protected files, never in the plist. Linux: run `awp daemon start` under your own process supervisor.

## Updates

```bash
awp update check
awp update install
awp update auto enable --interval-hours 24   # opt-in, disabled by default
```

## Build from source

Requires Go 1.26+:

```bash
git clone https://github.com/Manifestro/awp.git && cd awp
go test ./...
go build -o ./bin/awp ./cmd/awp
```

## Documentation

| Document | Contents |
| --- | --- |
| [Provider quickstart](./docs/PROVIDER_QUICKSTART.md) | Add an AWP endpoint to an existing backend |
| [Permission model](./docs/PERMISSIONS.md) | Requests, local grants, scopes, one-run isolation |
| [Protocol specification](./docs/PROTOCOL.md) | Envelope, handshake, binding, delivery, ACKs, errors, heartbeat |
| [Backend implementation guide](./docs/HOW_TO_CREATE_AWP_BACKEND.md) | Persistence, routing, retry, security, scaling |
| [JSON examples](./docs/examples) | Complete protocol messages |
| [Local FastAPI reference provider](./example/backend) | Runnable backend for interoperability testing |

## License

Apache-2.0. See [LICENSE](./LICENSE).

- [Issues](https://github.com/Manifestro/awp/issues) · [Pull requests](https://github.com/Manifestro/awp/pulls)
