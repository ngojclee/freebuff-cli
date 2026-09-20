# R3 - same-origin plugin icon

Role: Dev CPA
Repo: D:/Python/projects/CPA Plugin/freebuff-cli
Branch: main
Date: 2026-09-20

## Why

The management panel renders a provider mark as a plain `<img>` source. The plugin declared
`https://freebuff.com/logo-icon.png`, so the mark only appeared when the operator's browser
could reach freebuff.com. When it could not, the Auth Files tab fell back to a letter.

## What changed

- `src/assets/icon.png` caches the vendor mark in the repository (source
  `https://freebuff.com/logo-icon.png`, fetched 2026-09-20, PNG, 5969 bytes).
- `src/icon.go` embeds it and serves it at `/v0/resource/plugins/freebuff-cli/icon` with
  `image/png` and a one day cache.
- `/icon` is registered as a resource route, so it needs no management key and resolves
  same-origin with the panel.
- `logoURL` (pluginapi.Metadata.Logo) now points at that same-origin path.
- `registry.json` keeps the absolute vendor URL: the plugin store lists the plugin before
  it is installed, and at that point the plugin's own route does not exist yet.
- Version bumped to 0.1.3 in `src/version.go`, `Makefile` and `registry.json`.

This is compatible with upstream PR #427 (`feat(auth-files): show plugin OAuth provider
branding`): once that lands, the Auth Files tabs pick the declared logo up automatically.

## Tests

- `TestIconRouteServesTheVendorMark` - 200, exact image content type, real bytes
- `TestIconRouteIsRegisteredAsAResource` - reachable without the management key, and never
  registered as `/` which the host rejects
- `TestLogoURLIsSameOrigin` - guards against drifting back to a vendor URL

## Verification

```text
gofmt -l ./src ./.github/scripts   -> clean
go vet ./...                       -> clean
go test ./...                      -> ok
go test -race -count=1 ./src       -> ok (golang:1.26-bookworm on CT101)
```

Artifact: `freebuff-cli-v0.1.3.so`, sha256
`9671861e32f7c20c131384aa4a8f5a09f274b95d9d331ac643d19411d3a5fc5a`.

## Stop lines

No CPA restart. No secret in the repo or the log. The mark is a vendor brand asset, not a
credential.
