# R5 - host-backed accounts, payload decode fix, console parity

Role: Dev CPA
Repo: D:/Python/projects/CPA Plugin/freebuff-cli
Branch: main
Date: 2026-09-20
Version: 0.1.6 -> 0.1.7

## 1. A real bug, found by the first end-to-end smoke test

A chat completion through CPA came back in 6 ms:

```json
{"error":{"code":"invalid_request","message":"the request carried no model","retryable":false,"status":400,"type":"freebuff_error"}}
```

The client had sent a model. Cause: the host's `ExecutorRequest.Payload` and
`StorageJSON` are `[]byte`, so over the JSON RPC they arrive **base64 encoded**.
`parseExecutorRequest` read them as plain strings, which produced a body that
parsed as nothing, so both `req.Model` and `payloadModel(req.Payload)` came back
empty. Every real request would have failed this way.

`decodeHostBytes` now base64-decodes with a plain-text fallback, matching the
pattern the any2api bridge already uses in production. Also collapsed the two
attribute-bag spellings (`auth_attributes` and `attributes`) into one lookup.

Tests: `TestParseExecutorRequestDecodesBase64Payload`,
`TestDecodeHostBytesAcceptsBothShapes`,
`TestResolveModelFallsBackToThePayload`.

## 2. Accounts now come from the gateway, not the plugin's memory

`/v0/resource/plugins/freebuff-cli/accounts` returned `{"accounts":[]}` right
after a reload. The list was built from an in-memory cache that only fills when
`ParseAuth` or an executor request touches a credential, so a hot reload emptied
it until traffic came back.

`hostAccountViews()` asks `host.auth.list` and filters to this provider by
`provider`/`type`, falling back to the file-name prefix. The cache stays only as
a fallback for a build without the host callback.

The row type is deliberately narrow: `label`, `name`, `status`, `active`,
`source`, `runtime_only`, `prefix`. `TestAccountsPayloadHasNoCredentialFields`
asserts every key is on an allow list and that no credential word appears in the
JSON at all.

## 3. Console parity with the CodeBuddy plugin

Both consoles now use the same card order and the same vocabulary:

```
State -> Actions -> Accounts -> Published models -> Upstream catalogue
```

Actions sits second on both because the CodeBuddy console cannot read anything
without the management key; putting it last on one page and second on the other
would have been the divergence, not the card itself.

`TestDashboardSectionsMatchTheFreebuffConsoleOrder` in the codebuddy-cli repo and
`dashboardSections` here pin the same list on both sides.

## 4. Cookies: not needed, and here is the evidence

Every upstream call this plugin makes authenticates with a bearer token:

- `POST /api/v1/agent-runs` (START and FINISH)
- `POST /api/v1/chat/completions`
- `POST|GET|DELETE /api/v1/freebuff/session`

All of them send `Authorization: Bearer <authToken>`, and the owner's own probe
against `GET /api/v1/freebuff/session` returned `200` with the account's tier and
freebucks using only that header. No cookie, no session jar, no CSRF token.

The one place a browser session is involved is *obtaining* the token. Checked
`https://freebuff.llm.pm/` unauthenticated: 17,003 bytes, not an SPA shell, it
mentions `authToken`, and renders **zero** UUID-shaped values. So the token is
fetched by client-side code after login, and a cookie-to-token exchange would
have to replay that undocumented flow. That is the same class of fragility that
made the CodeBuddy cookie login unreliable, and this plugin already has a
supported import path for the credential: the CLI's
`~/.config/manicode/credentials.json`, a flat token export, or a bare token file.

Decision: **no cookie support.** The per-account auth file model already covers
multi-account use, and adding cookies would add a credential to store and log-proof
for no capability we currently need. If the token ever stops being obtainable from
the CLI file, revisit with a capture of what the page actually calls.

## Verification

```text
gofmt -l src        -> clean
go vet ./...        -> clean
go test ./...       -> ok
```

## Not done

No hand-install of the artifact, on purpose: the previous round showed that
placing the file before the store installs it makes the reload unreliable. The
release is published and the store Update should hot-load it.
