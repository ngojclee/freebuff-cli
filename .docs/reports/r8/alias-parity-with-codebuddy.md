# R8 - Freebuff alias parity with CodeBuddy

## Defect

CPA showed both the alias and the original Freebuff id:

```
deepseek-flash
freebuff/deepseek/deepseek-v4.1-flash
```

CodeBuddy did not have this problem because its model registration differs in two
ways:

1. It does not set `DisplayName` or `Name` on `ModelInfo`, so the management UI
   has no gray subtitle to render.
2. Its `model.static` response omits ids that have an OAuth alias. CPA's executor
   path publishes the provider catalogue under a second model client that never
   receives aliases; leaving the original there republishes it even when Keep
   original is off.

Freebuff set `DisplayName` and `Name` to the internal namespaced id and returned
the same catalogue for `model.static`, `model.register`, and `model.for_auth`.

## Change

- Freebuff now registers only the stable `ID`, matching CodeBuddy.
- Added the CodeBuddy-style `model.static` filtering: ids present in the
  provider's `OAuthModelAlias` table are omitted from the static catalogue.
- `model.register` and `model.for_auth` still return the full catalogue so CPA
  can apply the operator's alias and Keep original decision on the per-auth path.
- Added regression tests for aliased, unaliased, foreign-provider, identity and
  malformed alias inputs.

## Verification

- `gofmt` passed.
- `go test ./...` passed.
