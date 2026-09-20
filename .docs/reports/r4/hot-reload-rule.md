# R4 - why 0.1.5 never hot loaded

Role: Dev CPA
Date: 2026-09-20
Repo: freebuff-cli (the finding is about CPA's plugin store, not this plugin)

## Symptom the planner reported

`POST /v0/management/plugin-store/freebuff-cli/install?...&version=0.1.5` answered
`restart_required:false`, the file `freebuff-cli-v0.1.5.so` was on disk, but
`/v0/management/plugins` still reported metadata `version:0.1.4` and the Plugin Store
still showed `installed_version:0.1.4, update_available:true`.

## What the log actually shows (measured)

```
16:55:35  pluginstore: plugin installed plugin_id=freebuff-cli version=0.1.4 overwritten=false
16:55:36  pluginhost:  plugin loaded      plugin_id=freebuff-cli version=0.1.4
16:55:36  pluginhost:  plugin hot reloaded plugin_id=freebuff-cli active_version=0.1.4 retired_version=0.1.3

20:09:17  pluginstore: plugin installed plugin_id=codebuddy-cli version=0.7.21 overwritten=true
20:09:17  pluginhost:  plugin loaded      plugin_id=codebuddy-cli version=0.7.21
20:09:18  pluginhost:  plugin hot reloaded plugin_id=codebuddy-cli active_version=0.7.21 retired_version=0.7.20

20:09:18  pluginstore: plugin installed plugin_id=freebuff-cli version=0.1.5 overwritten=true
20:10:45  pluginstore: plugin installed plugin_id=freebuff-cli version=0.1.5 overwritten=true
20:14:32  pluginstore: plugin installed plugin_id=freebuff-cli version=0.1.5 overwritten=true
          (no "plugin loaded" line, no "plugin hot reloaded" line, all three times)
```

## Cause

The 0.1.5 `.so` was **already on disk before the store install**, because this lane
hand-installed it. Every store install then reported `overwritten=true` and produced no
load at all. The codebuddy 0.7.21 install was also `overwritten=true` and still hot
loaded, so "overwritten" by itself is not the discriminator: what differs is that CPA had
never recorded 0.1.5 as its own installed artifact, so there was no in-memory entry to
retire and swap.

The honest limit of this note: the exact internal predicate is not observable from the
log. What is observable, and enough to act on, is the rule below.

## Rule for this deployment

1. Let the **store** write the `.so`. Hand-placing the file for the same version you then
   ask the store to install breaks the hot-reload path.
2. Hand-install only as a fallback when a restart is happening anyway, and in that case
   do not expect the store to be able to hot-swap that version later.
3. `restart_required:false` in the install response is not proof of a reload. The proof is
   the `pluginhost: plugin hot reloaded` line naming the new `active_version`.
4. Keep exactly one `.so` per plugin id in `plugins/linux/amd64/`. Moving older ones to
   `plugins/legacy/` is safe: measured, CPA never loads from that directory.

## Also observed, not this lane's file

`plugins/linux/amd64/` currently holds five `any2api-bridge` artifacts (0.4.5, 0.4.6,
0.4.8, 0.4.9, 0.5.0). That is the same pile-up shape that produced the stale 0.1.3
unloads at 16:57:26. Reported to the planner rather than touched here.

## This round's change

Version 0.1.5 -> 0.1.6 so the store has a new artifact to install, then install it
**through the store only**, and verify by the `plugin hot reloaded` line.
