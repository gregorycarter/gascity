# Pool work_dir collision audit (shared-slot poisoning, 2026-08)

Root-cause record for the polecat worktree-corruption cluster: multiple
pool sessions resolving one shared working directory, branch switches
under running sessions, and unpushed commits stranded when a pooled
worktree was reassigned. Written from the ga-33p investigation
(2026-08-10); symptom beads bt-3cq6r and bt-wy7md, original 7-way
collision ggc-wisp-fuc3gg.

## TL;DR

A deployed `gc` binary built from the v1.4.0 release tag kept rebinding
pool sessions' `work_dir`/`gc.work_dir` metadata to the pool template's
*base* qualified name — one shared directory per pool — on every
reconcile tick that rebound an existing session to new work. The fix
had already landed upstream **one day after** the release snapshot.
Changing the pack's `work_dir` template cannot fix this: the collapse
happens in the binary's identity resolution, and a template edit only
moves *where* the shared directory appears.

**Resolution: rebuild and reinstall `gc` from current `main`.** No
additional SDK code change is required. Cleanup of leftover shared
directories and stale bead metadata must follow the runbook below.

## Causal chain (all verified against source)

1. **Rebind identity collapse** — fixed by `1bc642727` (#4659,
   2026-07-25). Before the fix, `realizePoolDesiredSessions` called
   `bindPoolSessionTriggerBead(bp, cfgAgent, qualifiedName, ...)` with
   the pool *base* qualified name instead of `qualifiedInstance`. Every
   rebind of a live pooled session to a new work bead rewrote the
   session bead's `work_dir`/`gc.work_dir` cluster to
   `resolveConfiguredWorkDir(base)` — the shared slot. First-creates
   were correct (`poolTriggerMetadata` already received
   `qualifiedInstance`), which is why live trees show a *mix* of
   per-instance and shared directories.
2. **Unguarded propagation onto work beads** — fixed by `e938a1906`
   (2026-08-05). Before the fix, `stampRunSessionIdentity` copied the
   session bead's (poisoned) work dir onto every in-progress assigned
   work bead as `gc.work_dir`. The fix added
   `workDirStampHasOwnershipEvidence`: a pool-managed session may only
   mirror a `gc.work_dir` that the worktree creator already recorded in
   the legacy `work_dir` key.
3. **Named-session twin** — fixed by `a2599ab64` (#5095, 2026-08-07).
   Named-session rediscovery collapsed identity to the backing
   template's qualified name, sharing one work_dir across all named
   sessions on a template. Same disease, different session shape.

## The stale-binary trap: check `vcs.revision`, not mtime

The installed binary's file mtime said Aug 8 — "2 days stale". Its
embedded build info said otherwise:

```console
$ go version -m ~/.local/bin/gc | grep -E 'vcs.revision|vcs.time'
        build   vcs.revision=a7297c511d637a3609947386f3389d76ddb2f23b
        build   vcs.time=2026-07-24T17:43:35Z
```

`a7297c511` is the `chore: release v1.4.0` commit — the *source* was 17
days old and predated all three fixes above. mtime tells you when
someone last compiled; only `vcs.revision` tells you what they
compiled. Any future "is the fleet binary stale?" check must compare
`vcs.revision` against `origin/main`, never file timestamps.

## Why the pack template fix could not work

The pack change (`packs/gastown/agents/polecat/agent.toml`) replaced
`{{.AgentBase}}` with `{{.Agent}}` in `work_dir`. Both variables are
rendered from the qualified name *the binary passes in*
(`internal/workdir.PathContextForQualifiedName`). When the binary
resolves a pool session to the base identity, `{{.Agent}}` renders the
base name too. The template edit only changed the collision path's
shape:

| Path under `.gc/worktrees/<rig>/polecats/` | Template era | Identity | Meaning |
| --- | --- | --- | --- |
| `<instance>` (e.g. `gastown.capable`) | `{{.AgentBase}}` | per-instance | correct, old era |
| `<pool-base>` (e.g. `gastown.polecat`) | `{{.AgentBase}}` | collapsed | shared slot, old era |
| `<rig>/<instance>` | `{{.Agent}}` | per-instance | correct, current |
| `<rig>/<pool-base>` | `{{.Agent}}` | collapsed | shared slot, current |

A `<pool-base>`-shaped leaf under `polecats/` is always the collapse
signature, in either era.

## Verification that current `main` is fixed

Pinning tests, all green at `365f111ea`:

```bash
go test ./cmd/gc/ -count=1 -run \
  'TestRealizePoolDesiredSessionsRebindPreservesDistinctWorkDirPerSlot|TestRealizePoolDesiredSessionsBindsTriggerBeadToFreshSession|TestStampRunSessionIdentity|TestCanonicalSessionIdentityWithConfigInfoMatchesRaw|TestExistingPoolSlotWithConfigInfoMatchesRaw'
```

- `...RebindPreservesDistinctWorkDirPerSlot` pins the #4659 rebind fix.
- `TestStampRunSessionIdentityDoesNotManufactureWorktreeEvidence` pins
  the e938a1906 guard.
- The `...MatchesRaw` oracles pin slot resolution (byte-identical
  raw/typed forms).

Residual sharp edge, deliberately unchanged: a pool session bead whose
slot cannot be resolved (no stamp, out-of-bounds after a pool-cap
reduction) still resolves to the base identity at rediscovery seams.
With the stamp guard this is transient and no longer persisted; the
realize path re-claims and stamps a valid slot on the next tick.

## Cleanup runbook (after the rebuilt binary is installed)

Deleting a shared slot while sessions are live destroys work —
bt-wy7md stranded a real unpushed commit exactly this way. Order is
mandatory: **inventory → rescue → remove.**

1. **Gate on the binary.** Confirm the installed `gc` reports a
   `vcs.revision` at or after `1bc642727` and the fleet has restarted
   onto it. Until then, rebinds keep re-creating shared paths.
2. **Inventory every collapse-signature path** (both eras, per the
   table above) in every rig. For each, determine whether it is a real
   git worktree (`.git` file present) or a bare scaffold directory. A
   scaffold with no `.git` was only ever `MkdirAll`'d — anything git
   reports there belongs to an *ancestor* repo, so do not trust `git
   status` output run inside it.
3. **Check for live users.** `lsof +D <dir>` / process table — never
   trust bead state alone. An active session mid-merge in a shared slot
   must drain before cleanup.
4. **Rescue.** In each real worktree: commit or branch any dirty state
   (`git checkout -b rescue/<slot>-<date> && git add -A && git
   commit`), push it, and walk `git reflog` for commits reachable only
   from the slot's HEAD history (the bt-wy7md stranding pattern), then
   push those under `rescue/` refs too.
5. **Remove** with `git worktree remove` from the owning rig repo (or
   `rmdir` for empty scaffolds). Never `rm -rf` a worktree — it leaves
   a stale pointer in the rig's `.git/worktrees` that fails future
   spawns closed (`ValidateAncestorWorktreesNotStale`).
6. **Sweep stale metadata.** Non-terminal beads whose `gc.work_dir` or
   `work_dir` matches a collapse-signature path keep feeding the wrong
   directory to consumers (`work_record_gate` uses `gc.work_dir` as the
   repo dir for close-gate git checks; the worktree reaper treats it as
   a borrow veto). Clear the poisoned key or re-point it at the bead's
   real per-bead worktree. Session beads self-heal on their next
   dispatch (the rebind now writes the per-instance dir); closed beads
   can keep the stale value — nothing operational reads them.

Live inventory as of 2026-08-10T15:30Z, for the executor of the
cleanup bead:

| Path (under `.gc/worktrees/`) | Kind | State |
| --- | --- | --- |
| `bridge_town_core/polecats/bridge_town_core/gastown.polecat` | worktree | ACTIVE: detached HEAD, mid-merge conflict, 17 dirty files — drain first |
| `bridge_town_core/polecats/gastown.polecat` | worktree | on `polecat/bt-m5mnf.1`, clean, no unpushed; reflog holds the bt-wy7md churn history |
| `bridge_town_core/polecats/gastown.polecat-adhoc-45fec25c00` | scaffold | empty |
| `gascity/polecats/gascity/gastown.polecat` | scaffold | empty (no `.git`; ancestor git output is the town repo, not stranded work) |
