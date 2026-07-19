# Changelog

## 0.2.0-alpha.1

- Add mandatory provider `permission.request` messages.
- Add local `once`, `binding`, and `provider` permission grants.
- Add permission request hashing, revocation, and a private audit log.
- Restrict each Codex wake to the granted provider MCP tools with one-run config overrides.
- Keep global `~/.codex/config.toml` unchanged.
- Add `awp update check` and verified atomic self-update.
- Add disabled-by-default automatic update policy.
- Update the FastAPI reference provider and provider documentation.

## 0.1.0-alpha.1

- Initial AWP `0.1` wire protocol.
- Multi-provider, multi-session Go client.
- Codex CLI adapter and opt-in macOS autostart.
- Provider-owned FastAPI reference endpoint.
