#!/bin/sh
# Verify post-flatten table probes use the flatten commit rather than live HEAD.
set -u

HERE=$(unset CDPATH; cd -- "$(dirname "$0")" && pwd)
RUN_SH="$HERE/../../examples/bd/dolt/commands/compact/run.sh"
[ -f "$RUN_SH" ] || { echo "FAIL: run.sh not found at $RUN_SH"; exit 1; }

fails=0
check() {
  what="$1"; want="$2"; got="$3"
  if [ "$want" = "$got" ]; then
    printf 'ok   %s\n' "$what"
  else
    printf 'FAIL %s\n     want: %s\n     got:  %s\n' "$what" "$want" "$got"
    fails=$((fails + 1))
  fi
}

COMPACT_LIB_ONLY=1
export COMPACT_LIB_ONLY
GC_CITY_PATH=$(mktemp -d)
export GC_CITY_PATH
GC_PACK_DIR="$HERE/../../examples/bd/dolt"
export GC_PACK_DIR
GC_DOLT_PORT=55424
export GC_DOLT_PORT
GC_DOLT_MANAGED_LOCAL=0
export GC_DOLT_MANAGED_LOCAL
. "$RUN_SH"
set +e

PROBE_LOG=$(mktemp)
preflight_file=$(mktemp)
trap 'rm -f "$preflight_file" "$PROBE_LOG"; rm -rf "$GC_CITY_PATH"' EXIT

row_count() {
  printf 'row_count:%s\n' "$1" >> "$PROBE_LOG"
  printf '10'
}

table_value_hash() {
  printf 'table_value_hash:%s\n' "$1" >> "$PROBE_LOG"
  printf 'samehash'
}

user_tables() {
  printf 'issues\n'
}

preflight_excluded_tables=""
printf 'issues 10 samehash\n' > "$preflight_file"

FLATTEN_HEAD=abc123def456
verify_counts bt "$preflight_file" "bt/$FLATTEN_HEAD" >/dev/null 2>&1
check "row_count is pinned to the flatten commit" \
  "row_count:bt/$FLATTEN_HEAD" \
  "$(grep '^row_count:' "$PROBE_LOG" | head -1)"
check "table_value_hash is pinned to the flatten commit" \
  "table_value_hash:bt/$FLATTEN_HEAD" \
  "$(grep '^table_value_hash:' "$PROBE_LOG" | head -1)"

: > "$PROBE_LOG"
verify_counts bt "$preflight_file" >/dev/null 2>&1
check "defaults to the bare db when no revision is given" \
  "row_count:bt" \
  "$(grep '^row_count:' "$PROBE_LOG" | head -1)"

if [ "$fails" -ne 0 ]; then
  printf '\n%s check(s) failed\n' "$fails"
  exit 1
fi
printf '\nall checks passed\n'
