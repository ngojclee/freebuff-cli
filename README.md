# freebuff-cli

A CLIProxyAPI plugin that brings [Freebuff](https://freebuff.com) free agents into the
gateway as a normal auth provider and executor, the same shape as the Antigravity, Codex
and Devin providers.

## What it does

- **Accounts as auth files.** One Freebuff auth token per account, stored the way every
  other provider stores credentials, so CPA's own scheduler rotates across them.
- **Waiting room handling.** The free tier queues accounts. The plugin polls the session
  until the account is admitted or `waiting_room_timeout_seconds` elapses, then parks that
  account briefly and returns a retryable error instead of hanging the request.
- **Agent runs.** Each request is attached to an upstream agent run. Runs are reused and
  rotated on `rotation_interval_seconds` (default 6h) rather than opened per request.
- **Catalogue refresh.** The free-agent catalogue is fetched from the upstream
  `free-agents.ts` source every `model_refresh_seconds` (default 6h) and merged with the
  operator's pinned list. A failed refresh keeps the previous list.
- **Namespaced models.** Models publish as `freebuff/<model>`, so they never collide with
  another provider's identical bare id. The executor strips the namespace before calling
  upstream. Set `model_alias_prefix` to empty to publish bare ids.
- **Streaming passthrough.** Upstream SSE frames are forwarded frame by frame, so a client
  sees progress as it happens.

## Install

1. Build or download `freebuff-cli-v<version>.so` and place it in
   `/home/Docker/CLIProxyAPI/plugins/linux/amd64/` (mode `0755`).
2. Add the plugin config:

```yaml
plugins:
  configs:
    freebuff-cli:
      enabled: true
      auth_dir: /root/.cli-proxy-api
      model_alias_prefix: freebuff
      upstream_base_url: https://www.codebuff.com
      waiting_room_timeout_seconds: 120
      store:
        version: 0.1.0
        release-tag: v0.1.0
```

3. Load it: apply the config through the CPA management API, or restart `cli-proxy-api`
   once. Dropping the `.so` on disk is not enough on its own.

## Getting an auth token

Two ways, both supported:

- **Web.** Sign in at <https://freebuff.llm.pm>; the page shows your auth token. Save it
  into the auth directory as a plain `.txt` file, or as
  `{"type":"freebuff","auth_token":"...","email":"you@example.com"}`.
- **CLI.** Install the Freebuff CLI (`npm i -g freebuff`), sign in, then copy
  `authToken` out of `~/.config/manicode/credentials.json`
  (`%USERPROFILE%\.config\manicode\credentials.json` on Windows). The plugin reads that
  file shape directly.

In the management UI, **OAuth Login -> Freebuff CLI** opens the token page and polls the
auth directory, so the flow is: open the page, copy the token, save the file, and the
login completes on its own.

## Configuration reference

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Master switch. |
| `priority` | `0` | Default credential priority; an auth file's own `priority` overrides it. |
| `auth_dir` | `/root/.cli-proxy-api` | Where the plugin watches for auth tokens. |
| `model_prefix` | empty | Prefix written onto the plugin's auth record. |
| `model_alias_prefix` | `freebuff` | Published namespace. Empty publishes bare ids. |
| `model_label` | empty | Override for the auth label. |
| `upstream_base_url` | `https://www.codebuff.com` | Freebuff backend origin. |
| `http_proxy` | empty | Proxy for outbound traffic. |
| `request_timeout_seconds` | `900` | Bound on one complete request. |
| `rotation_interval_seconds` | `21600` | How long one agent run is reused. |
| `model_refresh_seconds` | `21600` | Catalogue refresh interval. |
| `waiting_room_timeout_seconds` | `120` | How long a request may wait in the free-tier queue. |
| `disable_cooling` | `false` | Mirror of CPA's switch; still returns a retryable error, never parks an account. |
| `models` | empty | Extra model ids to publish alongside the discovered catalogue. |

## Dashboard

The plugin serves its own page at
`/v0/resource/plugins/freebuff-cli/` with the current state, the published and upstream
model lists, and a catalogue refresh button. A JSON view is available at
`/v0/resource/plugins/freebuff-cli/status`, `/accounts`, and `/models`.

None of these routes ever return an auth token.

## Security

- Auth tokens are credentials. They are read from the auth directory or CPA's auth store,
  used to sign upstream requests, and never logged, never returned by the dashboard, and
  never written anywhere except the auth file CPA owns.
- Error messages carry a classification code, not upstream text.

## Credits

The upstream protocol was worked out by reading
[Freebuff2API](https://github.com/Quorinex/Freebuff2API) (MIT, Copyright (c) 2026
Quorinex): the agent-run lifecycle, the free-tier session endpoint, the
`codebuff_metadata` shape, and the `free-agents.ts` catalogue source. This plugin is an
independent implementation written for the CLIProxyAPI plugin ABI; it does not share code
with that project.

## License

MIT. See [LICENSE](LICENSE).
