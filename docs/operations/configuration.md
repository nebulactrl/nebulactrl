---
title: Advanced settings
description: "Timer overrides (NEBULACTRL_MESH_LINK_GRACE, NEBULACTRL_REPLICA_BROKEN_AFTER, NEBULACTRL_UPTIME_INTERVAL): what they change, their defaults, and why you normally leave them alone."
section: Reference
order: 30
---

# Advanced settings

The everyday control-plane settings (database URL, secret key, public URL, MCP, log level) are in [the control plane](../installing/control-plane.md). This page lists the **advanced timers**: product timeouts that exist for safety and that you almost never need to change. They exist so the integration suite does not have to wait ten real minutes for a grace period to expire. **Leave them unset in production**: the defaults are the tested, documented behavior.

## Advanced timers

Every timer is read at start (a restart applies a change) from an environment variable holding a Go duration such as `30s`, `90s`, `10m` or `1h30m`; the control plane reads all of them, the agent also reads `NEBULACTRL_UPTIME_INTERVAL`. An unset or empty variable keeps the default. A value that is not a duration, or is shorter than `1s`, stops the start with a message that names the variable.

| Variable | Default | What it controls |
|---|---|---|
| `NEBULACTRL_MESH_LINK_GRACE` | `10m` | How long a cross-server link grant whose variable reference is gone stays before the repair removes it. A reference that is removed and added back, or a redeploy in between, keeps its link within this window. A canvas edge's grant is removed at once, whatever this says. The grace is remembered in memory only, so a control-plane restart starts the window again. See [Mesh](mesh.md) |
| `NEBULACTRL_REPLICA_BROKEN_AFTER` | `2m` | How long a database replica may be out of streaming before it is marked `broken`. The agent polls a replica every 30 seconds, so keep this above `30s`, or a single missed poll marks a healthy replica broken. See [Database replicas](database-replicas.md) |
| `NEBULACTRL_UPTIME_INTERVAL` | `30s` | How often agents probe each service for [uptime](uptime.md). The control plane reads it to decide when a service that sent no check for three intervals plus 30 seconds becomes `unknown`. **The agent reads the same variable** (flag `--uptime-interval`, at least `1s`; an agent that is started with a shorter value refuses to start) for its own probe period. Set it to the same value on both, or on neither: a control plane that expects 30 s while the agent probes every 2 s only waits longer than needed for `unknown`, and one that expects 2 s while the agent probes every 30 s marks a healthy service `unknown` after 36 s. A shorter interval makes alerts and the live state quicker and writes more rollup rows; it is a test and diagnostic knob |

Rules of thumb when you do change one:

- **Shorten with care.** A grace that is shorter than the interval at which its condition is re-checked removes things that would have recovered by themselves. `NEBULACTRL_MESH_LINK_GRACE` is re-checked by the repair job every 5 minutes, so on a real install a value below `5m` only makes the removal happen at the next repair run, not sooner.
- **Do not lengthen to hide a problem.** A replica that is `broken` for two minutes is not streaming; waiting longer does not fix the cause.
- The values are not stored in the database and do not appear in the settings screens.

## What the test suite sets

`tests/integration` starts every control plane with short values (`tests/integration/harness_timers_test.go`): `NEBULACTRL_MESH_LINK_GRACE=30s`, `NEBULACTRL_REPLICA_BROKEN_AFTER=60s` and `NEBULACTRL_UPTIME_INTERVAL=2s` (the harness agent probes every 2 s as well). The same variable in the environment of `tests/run.sh` overrides the short value, for example `NEBULACTRL_MESH_LINK_GRACE=10m ./tests/run.sh full -run '^TestMeshCrossServer$'` runs the mesh test with the production timing.
