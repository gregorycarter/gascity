---
title: "Configure the Self-Healing Loop"
description: Detect stalled delivery, recover common failure modes, and keep the remediation bounded.
---

Long-running software factories can lose throughput without losing
connectivity: work sits assigned but unclaimed, a high-priority bead waits
behind newer work, a session stops making progress, or the mainline turns red.
Gas City's **self-healing loop** watches for those conditions and applies a
small, configured set of mechanical repairs so delivery can recover without a
human in the loop.

The loop is opt-in. It is a watchdog for the platform's delivery path, not a
replacement for the agents that make engineering decisions. For the broader
model, see [How Gas City works](/getting-started/how-gas-city-works).

## The detect → diagnose → remediate → verify loop

Each pass follows the same evidence-driven shape:

| Stage | What it does |
| --- | --- |
| Detect | Measures commits landed on each target rig's mainline and looks for actionable demand that has aged past the stall window. A red-mainline check is measured independently. |
| Diagnose | Identifies applicable conditions across the fixed remediation ladder: orphaned work, priority inversion, a dead or stuck session, a red mainline, or a critical session that is down. |
| Remediate | Applies only configured mechanics, such as releasing a bead to the pool, assigning it to an idle worker, restarting a session, or filing repair work. |
| Verify | Records the detection and every before/after action as typed `heal.*` events. The next pass measures mainline throughput again; capped actions remain visible instead of disappearing. |

The signal is intentionally conservative. A rig is stalled only when no commit
landed on its mainline during `stall_after` *and* actionable demand at least as
old is waiting. If the mainline measurement cannot be read, the loop does not
guess that the rig is stalled.

## The remediation ladder

The first three rungs are gated by a stalled rig. The last two have their own
signals, so a red mainline or a down critical session is still handled when
ordinary throughput is not the problem.

| Rung | Condition | Action |
| --- | --- | --- |
| 1. Orphaned routed work | An open routed bead has been assigned but unclaimed past `orphan_stale_after`. | Return it to the pool. Assignees listed in `queue_addresses` are excluded because a merge queue is expected to hold work while it scans. |
| 2. Priority inversion | A ready bead at or above `inversion_priority` has waited past `inversion_after` while an idle pool worker is available. | Force-assign the bead to an eligible idle worker, preserving the configured route. |
| 3. Dead or stuck sessions | In-progress work has no live holder, or a live pool worker has not updated its bead past `stuck_after`. | Release work held by a dead session; restart an eligible stuck session. Queue addresses are not treated as stuck work holders. |
| 4. Red mainline | `main_red_check` exits non-zero for a target rig. | File one routed repair bead using `main_red_route`, optionally attaching `main_red_workflow`. Existing repair work is deduplicated. |
| 5. Critical session down | A name in `critical_sessions` is absent or its agent process has died. | Start or restart that configured session. This rung is independent of the stalled-rig gate. |

The ladder contains no built-in role names. Addresses, session names, routes,
and repair formulas come from `[heal]` in your City configuration.

## Safety rails

Self-healing should reduce recovery time without becoming a second incident.
These bounds are applied to every pass:

| Setting | Default | Purpose |
| --- | --- | --- |
| `action_cooldown` | `1h` | Prevents the same bead or session from being acted on repeatedly. A suppressed action emits `heal.capped`. |
| `max_actions_per_pass` | `5` | Caps the number of mutating actions in one pass. Remaining actions are reported as capped. |
| `queue_addresses` | empty | Protects configured scan queues from orphan release and stuck-holder recovery. |
| `--dry-run` | off | Reports proposed actions without mutating beads or sessions. |
| typed events | always | `heal.stall_detected`, `heal.action`, and `heal.capped` provide the audit trail, including action rung and before/after state. |

If the event history needed to enforce a cooldown is unavailable, a mutating
pass fails closed. Keep the event stream available and treat capped events as
signals to inspect rather than as silent retries.

## Activate it for a City

Add a `[heal]` section to `city.toml`, then declare one `[[heal.target]]` for
each rig whose throughput the loop should watch. This example uses neutral
names; replace them with addresses and session names that exist in your City.

```toml
[heal]
enabled = true
interval = "5m"
stall_after = "30m"
orphan_stale_after = "20m"
inversion_after = "15m"
inversion_priority = 0       # protect P0 beads
stuck_after = "2h"
action_cooldown = "1h"
max_actions_per_pass = 5
critical_sessions = ["ops-coordinator"]

[[heal.target]]
rig = "app"
queue_addresses = ["app/merge-queue"]
main_red_check = "./scripts/check-mainline"
main_red_route = "app/repair"
main_red_workflow = "repair-mainline"
```

The `main_red_check` command runs from the rig repository and must exit zero
when the mainline is green. Omit the three `main_red_*` settings when mainline
health is monitored elsewhere. `rig` must name a declared rig, and every
`queue_addresses` value must be the actual assignee of a protected queue.

After saving the configuration, reload a running City:

```console
gc reload
```

The controller then runs a pass every `interval`. To preview the first pass,
use the standalone command:

```console
gc heal --dry-run --json
```

`gc heal` performs one pass regardless of whether `enabled` is true, so an
external scheduler can drive recovery even when the controller is unavailable.
Remove `--dry-run` only after reviewing the proposed subjects and confirming
that queue addresses and routes match your deployment:

```console
gc heal --json
```

Use the event stream or [event reference](/reference/events) to inspect
`heal.stall_detected`, `heal.action`, and `heal.capped` records. For the full
field list and validation rules, see the generated
[configuration reference](/reference/config#healconfig).
