#!/bin/sh
# Unit test for post-flatten verification being pinned to the flatten commit
# (ga-kh5).
#
# WHY THIS EXISTS
# ---------------
# verify_counts() used to re-probe LIVE tables after the flatten, so any write
# landing between the flatten commit and the probe was indistinguishable from
# corruption. The step-4a HEAD heuristic downgraded the INSERT signature
# (row-count gain + hash drift) to a retry, but explicitly excluded
# same_row_count_hash_drift -- which is exactly what a concurrent UPDATE looks
# like. On a live write-heavy db that is the normal case, not a rare race, so
# hq and bt sat permanently quarantined (seen_count 39 and 51) and their
# DOLT_GC starved.
#
# The fix pins the probes to the flatten commit, which makes verification
# deterministic: the flatten either preserved values AT ITS OWN COMMIT or it
# did not, and concurrent writers stop mattering by construction.
#
# This test asserts the probes are issued against the revision-qualified
# database. It deliberately does NOT try to reproduce a live writer race --
# per #2846 that is not deterministic in a unit test, which is the same reason
# compact_gain_drift_proof_test.sh stubs its probes.
set -u

HERE=$(unset CDPATH; cd -- "$(dirname "$0")" && pwd)
RUN_SH="$HERE/../../examples/bd/dolt/commands/compact/run.sh"
[ -f "$RUN_SH" ] || { echo "FAIL: run.sh not found at $RUN_SH"; exit 1; }

fails=0
check() {
  _what="$1"; _want="$2"; _got="$3"
  if [ "$_want" = "$_got" ]; then
    printf 'ok   %s\n' "$_what"
  else
    printf 'FAIL %s\n     want: %s\n     got:  %s\n' "$_what" "$_want" "$_got"
    fails=$((fails + 1))
  fi
}

# Source the script for its helpers only; the guard stops main from running.
# run.sh hard-requires GC_CITY_PATH at load time; a temp dir satisfies it
# without the test touching a real city.
COMPACT_LIB_ONLY=1
export COMPACT_LIB_ONLY
GC_CITY_PATH=$(mktemp -d)
export GC_CITY_PATH
# PACK_DIR is derived from $0, which is this test when run.sh is sourced rather
# than executed, so point it at the real pack explicitly.
GC_PACK_DIR="$HERE/../../examples/bd/dolt"
export GC_PACK_DIR
# run.sh resolves a live Dolt port at load time. The probes are stubbed here so
# nothing connects; a fixed port just satisfies the resolver.
GC_DOLT_PORT=55424
export GC_DOLT_PORT
# Not a managed-local city; skip the managed runtime-port cross-check.
GC_DOLT_MANAGED_LOCAL=0
export GC_DOLT_MANAGED_LOCAL
# shellcheck source=../../examples/bd/dolt/commands/compact/run.sh disable=SC1090
. "$RUN_SH"
# run.sh sets -e for production use; the test needs to inspect return codes
# rather than abort on the first non-zero, so drop it after sourcing.
set +e

# --- stubs -------------------------------------------------------------------
# Record every database name the probes are handed, so we can assert they were
# pinned to the flatten commit rather than left pointing at live HEAD.
#
# NOTE: verify_counts invokes the probes inside $( ) command substitution, so a
# shell variable assigned in a stub lives and dies in that subshell. The probe
# log therefore has to be a file.
PROBE_LOG=$(mktemp)

row_count() {
  printf 'row_count:%s\n' "$1" >> "$PROBE_LOG"
  printf '10'
}

table_value_hash() {
  printf 'table_value_hash:%s\n' "$1" >> "$PROBE_LOG"
  printf 'samehash'
}

# verify_counts also re-lists tables to detect table-list drift. Stub it to the
# same single table the pre-flight file carries, so the run reaches the probe
# assertions instead of failing on a table-list mismatch.
user_tables() {
  printf 'issues\n'
}

# Set by the pre-flight writer in production; empty here (no excluded tables).
preflight_excluded_tables=""

# Pre-flight file: "<table> <rows> <hash>" per line, matching what the real
# pre-flight writer emits. Values match the stubs so verification passes and we
# are asserting purely on WHICH database was probed.
preflight_file=$(mktemp)
trap 'rm -f "$preflight_file" "$PROBE_LOG"; rm -rf "$GC_CITY_PATH"' EXIT
printf 'issues 10 samehash\n' > "$preflight_file"

# --- the assertion -----------------------------------------------------------
# verify_counts must accept an explicit verification target and route the
# probes through it. The caller passes "<db>/<flatten_head>".
FLATTEN_HEAD=abc123def456
verify_counts bt "$preflight_file" "bt/$FLATTEN_HEAD" >/dev/null 2>&1

check "row_count is pinned to the flatten commit" \
  "row_count:bt/$FLATTEN_HEAD" \
  "$(grep '^row_count:' "$PROBE_LOG" | head -1)"

check "table_value_hash is pinned to the flatten commit" \
  "table_value_hash:bt/$FLATTEN_HEAD" \
  "$(grep '^table_value_hash:' "$PROBE_LOG" | head -1)"

# Back-compat: omitting the third argument must keep probing the bare db, so
# any other caller of verify_counts is unaffected.
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
exit 0
