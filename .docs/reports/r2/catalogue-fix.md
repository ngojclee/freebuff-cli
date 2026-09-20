# R2 - freebuff-cli catalogue fix (0.1.1)

Role: Dev CPA
Repo: D:/Python/projects/CPA Plugin/freebuff-cli
Branch: main
Date: 2026-09-20

## Reported symptom

Only one Freebuff model appeared behind CPA:

```
freebuff/google/gemini-2.5-flash-lite
```

## Root cause (measured)

The upstream `free-agents.ts` file changed shape. It used to inline model ids as string
literals inside each `new Set([...])`. It now imports exported constants and references
them:

```ts
import { FREEBUFF_GEMINI_38_FLASH_MODEL_ID } from './freebuff-models'
export const freeAgentModels = {
  'editor-lite': new Set([FREEBUFF_GEMINI_38_FLASH_MODEL_ID]),
}
```

Measured against the live file on 2026-09-20:

| Metric | Value |
| --- | --- |
| agent blocks matched | 44 |
| inline single-quoted model literals | 1 (`google/gemini-2.5-flash-lite`) |
| file size | 48,792 bytes |

The literal-only parser therefore produced exactly one model, which is what CPA published.

## Fix

`freebuff_models.go` now resolves the import graph one level deep:

1. fetch `free-agents.ts`
2. collect the sibling modules it imports (`from './x'`)
3. fetch each module and collect `export const NAME = 'value'` pairs
4. walk each `new Set([...])` body in source order, resolving identifiers through that
   table and keeping inline literals

Everything that still cannot be resolved is skipped rather than guessed at, so a future
shape change degrades to fewer models instead of wrong ones. A module that fails to fetch
is skipped; the whole refresh only fails if the main file fails or yields no agents.

Bounds: at most 12 imported modules and 1 MiB per fetch.

Simulated against the live upstream file, resolution now yields 19 distinct models
instead of 1 (18 resolved constants plus the remaining inline literal).

## Tests added

- `TestParseFreeAgentsResolvesImportedConstants` - identifiers resolve, order preserved
- `TestParseFreeAgentsSkipsUnresolvableIdentifiers` - no constant table means only literals
  survive, never a guess
- `TestExtractImportedModules` - order and dedupe
- `TestParseExportedStringConstantsHandlesLineBreaks` - multi-line values, numeric
  constants ignored
- `TestRegistryRefreshFollowsImportedModules` - end-to-end refresh over httptest serving
  both the main file and the imported module

## Verification

```text
gofmt -l ./src                      -> clean
go vet ./...                        -> clean
go test ./...                       -> ok  github.com/ngojclee/freebuff-cli/src
```

## Not done

No live CPA mutation in this round beyond the artifact and pin, no restart. All parser
tests are offline; the live-file simulation was run out of band against the raw GitHub
file.

## 0.1.2 - resource route fix

The host rejected the dashboard's resource route:

```
pluginhost: plugin freebuff-cli declared invalid resource route /
```

`/` is not a valid resource path, so the dashboard was unreachable. 0.1.2 registers
`/index.html` for the dashboard and exposes `/status`, `/accounts` and `/models` as
read-only resource routes, which also makes them reachable without the management key for
quick checks. The only management route left is `POST /models/refresh`, so the on-demand
network call stays behind management auth; the dashboard no longer offers a button that
could not authenticate.
