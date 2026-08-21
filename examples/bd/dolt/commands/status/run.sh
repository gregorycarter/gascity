#!/bin/sh
# gc dolt status — Report whether the Dolt server is available.
#
# Prints a one-line human-readable status and exits 0 when the server is
# reachable, 1 otherwise. For a configured external Dolt endpoint (non-local
# GC_DOLT_HOST) the message names the remote endpoint rather than a managed
# local process, so operators are not told a reachable remote server is "not
# running" (su-deol8). A local listener that is serving but cannot prove
# process ownership is reported as serving with ownership unknown, never as
# "not running". The dolt-health order uses structured diagnostics.
#
# Environment: GC_CITY_PATH, GC_DOLT_HOST, GC_DOLT_PORT
set -e

: "${GC_CITY_PATH:?GC_CITY_PATH must be set}"
PACK_DIR="${GC_PACK_DIR:-$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)}"
. "$PACK_DIR/assets/scripts/runtime.sh"

if [ ! -x "$GC_BEADS_BD_SCRIPT" ]; then
  echo "gc dolt status: gc-beads-bd not found" >&2
  exit 1
fi

host="${GC_DOLT_HOST:-127.0.0.1}"

# probe exits 0 when serving_owned, 2 when unreachable, and 3 when the
# listener/runtime is degraded. Capture output and status without tripping
# `set -e`.
probe_out=$(GC_CITY_PATH="$GC_CITY_PATH" "$GC_BEADS_BD_SCRIPT" probe 2>/dev/null) && probe_status=0 || probe_status=$?

case "$probe_status" in
  0)
    if is_local_dolt_host "$host"; then
      echo "Dolt server: running (managed, 127.0.0.1:$GC_DOLT_PORT)"
    else
      echo "Dolt server: reachable (external endpoint $host:$GC_DOLT_PORT)"
    fi
    exit 0
    ;;
  3)
    if is_local_dolt_host "$host"; then
      pid=$(printf '%s\n' "$probe_out" | sed -n 's/^degraded[[:space:]][[:space:]]*pid=\([0-9][0-9]*\).*$/\1/p' | head -1)
      state=$(printf '%s\n' "$probe_out" | sed -n 's/^degraded[[:space:]][[:space:]]*pid=[^[:space:]]*[[:space:]]*state=\([^[:space:]]*\).*$/\1/p' | head -1)
      case "$state" in
        serving_ownership_unknown)
          verdict="SERVING BUT OWNERSHIP-UNKNOWN"
          ;;
        identity_mismatch)
          verdict="IDENTITY-MISMATCH"
          ;;
        stale_runtime)
          verdict="STALE-RUNTIME"
          ;;
        unreachable)
          verdict="UNRESPONSIVE"
          ;;
        *)
          verdict="SERVING BUT IDENTITY-UNVERIFIED"
          ;;
      esac
      echo "Dolt server: $verdict (managed, 127.0.0.1:$GC_DOLT_PORT, pid ${pid:-unknown}) — do not restart blindly; see gc dolt-state probe-managed"
    else
      echo "Dolt server: unreachable (external endpoint $host:$GC_DOLT_PORT)"
    fi
    exit 1
    ;;
esac

if is_local_dolt_host "$host"; then
  echo "Dolt server: not running (managed, 127.0.0.1:$GC_DOLT_PORT)"
else
  echo "Dolt server: unreachable (external endpoint $host:$GC_DOLT_PORT)"
fi
exit 1
