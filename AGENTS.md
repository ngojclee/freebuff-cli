# freebuff-cli Agent Rules

## Shared workflow (owner standard)

- Work directly on `main` in this checkout. No worktrees, no feature branches unless the
  planner explicitly asks.
- Preserve inherited dirty files. Never revert work you did not make.
- Stage only your own paths; never `git add -A` in a shared checkout.
- After changes: build, test, commit, push to `origin main`.
- Write a short file-first report under `.docs/reports/<round>/`.
- Never print, commit, or log secrets, cookies, tokens, or refresh tokens.

## Build and runtime

- Build the plugin with the local/self-hosted runtime, not GitHub-hosted Actions, when
  Actions minutes are limited.
- Keep GitHub Actions to tag-triggered releases only.

## CPA plugin release checklist

1. Bump the version in `src/version.go`, `Makefile`, and `registry.json`.
2. Build the Linux amd64 shared object:
   `CGO_ENABLED=1 go build -buildvcs=false -trimpath -buildmode=c-shared -ldflags "-s -w -X main.pluginVersion=<ver>" -o freebuff-cli-v<ver>.so ./src`
3. Install it to `/home/Docker/CLIProxyAPI/plugins/linux/amd64/` with mode `0755`; keep
   exactly one `.so` per plugin id (move older ones out of the plugins directory).
4. Set `plugins.configs.freebuff-cli.store.version` and `release-tag` in
   `/home/Docker/CLIProxyAPI/config.yaml` to the installed version.
5. Load it: either apply the config through the CPA management API so the host runs its
   plugin reload, or restart `cli-proxy-api` once. Writing the `.so` and `config.yaml` on
   disk is not enough on its own.
6. Verify the log line `plugin loaded plugin_id=freebuff-cli version=<ver>`, that the
   plugin reports `configured`, that `GET /v1/models` shows `freebuff/<model>`, and that
   one real chat completion succeeds.

## Verification

```bash
go build ./src
go vet ./...
go test ./...
```
