# R1 - freebuff-cli phase 1

Role: Dev CPA
Repo: D:/Python/projects/CPA Plugin/freebuff-cli
Branch: main
Date: 2026-09-20

## What this round delivered

The plugin scaffold plus the three Phase 1 subsystems the planner asked for: the Freebuff
upstream client, the free-agent model registry, and the per-account token pool.

### Scaffold

Plugin id `freebuff-cli`, provider key `freebuff`, display name `Freebuff CLI`, logo
`https://freebuff.com/favicon.svg`, private repo
`https://github.com/ngojclee/freebuff-cli`.

Files carried over from codebuddy-cli unchanged: `entry.go`, `main.go`,
`hostcalls_stub.go`, `hostlog.go`, `state.go`, `util.go` (extended with the shared
map/JSON helpers), and the `.github` release pipeline.

### Freebuff upstream client (`freebuff_upstream.go`)

- `POST /api/v1/agent-runs` START and FINISH
- `POST /api/v1/chat/completions`
- `POST|GET|DELETE /api/v1/freebuff/session`
- typed `freebuffError` with a stable code, a retryable flag, and the upstream status;
  upstream text never reaches the message

### Model registry (`freebuff_models.go`)

- fetches and regex-parses `free-agents.ts`, refreshes every `model_refresh_seconds`
- hardcoded fallback map used only when the first fetch fails
- deterministic model -> agent resolution: the lexicographically smallest agent wins, so
  routing does not move between agents across restarts
- keeps the previous catalogue when a refresh fails

### Token pool (`freebuff_pool.go`)

- one pool per account, round-robin selection, cooling accounts skipped
- waiting room: poll until active or `waiting_room_timeout_seconds`; on timeout park the
  account briefly and return a retryable `waiting_room_timeout`
- `disable_cooling` is honoured on every parking path, including the waiting-room timeout
- agent runs reused per agent and rotated on `rotation_interval_seconds`
- `Snapshot()` is the redacted dashboard view

### Supporting pieces

- `storage.go` parses four credential shapes: our own record, the CLI credentials file, a
  flat token export, and a bare token in a `.txt`/`.key`/`.token` file
- `provider.go` implements the auth provider: parse, login (token page + directory watch),
  poll, refresh
- `accountcache.go` remembers every account seen so a request can fall over to a sibling
- `executor.go` / `streaming.go` do the request, the retry policy, and frame-by-frame SSE
  passthrough
- `management.go` serves the dashboard and the JSON routes

## Verification

```text
go build ./src                      -> ok
go vet ./...                        -> clean
go test ./...                       -> ok  github.com/ngojclee/freebuff-cli/src  3.19s
```

Test coverage added this round:

- pool: round robin, cooling skip, all-cooling error, waiting-room poll to active,
  waiting-room timeout is retryable and parks the account, `disable_cooling` never parks,
  snapshot never carries a token
- upstream classification: auth rejected, rate limited, waiting room, session expired,
  bad request, server error
- registry: parser fixture, unknown shape tolerance, deterministic mapping, fallback on an
  unreachable source
- config: prefix round trip, outer provider prefix stripped, foreign ids untouched, empty
  prefix publishes bare ids, codebuff.com upgraded to www.codebuff.com, validation range
- storage: all four credential shapes, foreign documents ignored, auth id stability and
  non-reversibility, storage round trip, auth metadata never carries the token

## Not done on purpose

- No live CPA mutation, no plugin install, no restart. The owner restarts.
- The Anthropic `/v1/messages` lane is out of scope for this round, per the owner.
- Real upstream behaviour is not claimed: every test above uses a scripted stand-in, so
  the waiting-room and agent-run semantics are verified against the documented contract,
  not against codebuff.com.

## Open questions carried forward

1. Does a Freebuff auth token expire? If it does, `RefreshAuth` needs a real probe.
2. Should the executor pre-warm one run per agent on startup, as the reference does?
3. Is `freebuff/` the right prefix in production, or should aliases be added first?
