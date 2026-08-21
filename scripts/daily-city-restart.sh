#!/bin/bash
set -uo pipefail

CITY=${GC_DAILY_RESTART_CITY:-/Users/gregcarter/gascity-workspace}
GC=${GC_DAILY_RESTART_GC:-/Users/gregcarter/.local/bin/gc}
GC_INSTALL=${GC_DAILY_RESTART_INSTALL:-/Users/gregcarter/go/bin/gc}
GT=${GC_DAILY_RESTART_GT:-/Users/gregcarter/.local/bin/gt}
SOURCE_REPO=${GC_DAILY_RESTART_SOURCE_REPO:-$CITY/rigs/gascity}
LOG=${GC_DAILY_RESTART_LOG:-$CITY/.gc/logs/daily-city-restart.log}
SENTINEL=${GC_DAILY_RESTART_SENTINEL:-$CITY/.gc/logs/daily-city-restart.FAILED}
ESCALATE_FROM=${GC_DAILY_RESTART_ESCALATE_FROM:-$CITY/rigs/bridge-town-core}
TMP_ROOT=${GC_DAILY_RESTART_TMP_ROOT:-$CITY/.gc/tmp}
BUILD_TOOL=${GC_DAILY_RESTART_BUILD_TOOL:-make}

export PATH=${GC_DAILY_RESTART_PATH:-/opt/homebrew/bin:/opt/homebrew/sbin:/Users/gregcarter/go/bin:/Users/gregcarter/.local/bin:/usr/bin:/bin:/usr/sbin:/sbin}

pause() {
  [ "${GC_DAILY_RESTART_SKIP_SLEEP:-0}" = 1 ] || sleep "$1"
}

alarm() {
  local severity="$1" msg="$2"
  echo "!! $msg"
  date "+%F %T %z" >"$SENTINEL"
  echo "$msg" >>"$SENTINEL"
  if [ "${GC_DAILY_RESTART_SKIP_ALARM_ESCALATION:-0}" = 1 ]; then
    return 0
  fi
  if ( cd "$ESCALATE_FROM" 2>/dev/null && \
       "$GT" escalate "daily-city-restart: $msg" --severity "$severity" \
         --reason "Scheduled 05:30 city restart. See $LOG and $SENTINEL." \
         --source "cron:daily-city-restart" 2>&1 \
       | grep -vE '^named_session|^⚠|^2[0-9]{3}/' ) ; then
    echo "   escalation raised (severity=$severity)"
  else
    echo "   (escalation failed — sentinel at $SENTINEL is the alarm)"
  fi
}

refresh_cleanup() {
  local source_repo="$1" build_repo="$2" build_root="$3"
  git -C "$source_repo" worktree remove --force "$build_repo" >/dev/null 2>&1 || true
  rm -rf "$build_root"
}

refresh_gc() {
  local build_root build_repo selected_sha reported_commit version_output
  local install_dir staging_path

  [ -d "$SOURCE_REPO" ] || {
    echo "gc refresh: source repository does not exist: $SOURCE_REPO"
    return 1
  }
  mkdir -p "$TMP_ROOT" || {
    echo "gc refresh: cannot create temporary build root: $TMP_ROOT"
    return 1
  }
  build_root=$(mktemp -d "$TMP_ROOT/gc-refresh.XXXXXX") || {
    echo "gc refresh: cannot allocate temporary build root"
    return 1
  }
  build_repo="$build_root/source"

  if ! git -C "$SOURCE_REPO" fetch --quiet origin main; then
    refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
    echo "gc refresh: failed to fetch origin/main"
    return 1
  fi
  if ! selected_sha=$(git -C "$SOURCE_REPO" rev-parse origin/main^{commit}); then
    refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
    echo "gc refresh: cannot resolve origin/main"
    return 1
  fi
  if ! git -C "$SOURCE_REPO" worktree add --detach --quiet "$build_repo" "$selected_sha"; then
    refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
    echo "gc refresh: cannot create detached clean build worktree"
    return 1
  fi
  if [ -n "$(git -C "$build_repo" status --porcelain)" ] || \
     [ "$(git -C "$build_repo" rev-parse HEAD)" != "$selected_sha" ]; then
    refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
    echo "gc refresh: build worktree is not clean at selected SHA $selected_sha"
    return 1
  fi
  if ! "$BUILD_TOOL" -C "$build_repo" build; then
    refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
    echo "gc refresh: build failed for $selected_sha"
    return 1
  fi

  if [ ! -x "$build_repo/bin/gc" ]; then
    refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
    echo "gc refresh: build did not produce executable bin/gc"
    return 1
  fi
  if ! version_output=$("$build_repo/bin/gc" version --json); then
    refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
    echo "gc refresh: candidate version command failed"
    return 1
  fi
  reported_commit=$(printf '%s\n' "$version_output" | sed -n 's/.*"commit"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
  case "$reported_commit" in
    ???????*)
      case "$selected_sha" in
        "$reported_commit"*) ;;
        *)
          refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
          echo "gc refresh: candidate reports commit $reported_commit, want $selected_sha"
          return 1
          ;;
      esac
      ;;
    *)
      refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
      echo "gc refresh: candidate did not report a usable commit"
      return 1
      ;;
  esac

  install_dir=$(dirname -- "$GC_INSTALL")
  if [ ! -d "$install_dir" ]; then
    refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
    echo "gc refresh: install directory does not exist: $install_dir"
    return 1
  fi
  staging_path="$install_dir/.gc.tmp.$$.$RANDOM"
  if ! cp -f "$build_repo/bin/gc" "$staging_path" || \
     ! chmod 0755 "$staging_path" || \
     ! mv -f "$staging_path" "$GC_INSTALL"; then
    rm -f "$staging_path"
    refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
    echo "gc refresh: atomic install failed: $GC_INSTALL"
    return 1
  fi
  refresh_cleanup "$SOURCE_REPO" "$build_repo" "$build_root"
  echo "gc refresh: installed $GC_INSTALL at $reported_commit (origin/main $selected_sha)"
}

cd "$CITY" || { echo "daily-city-restart: cannot cd to $CITY" >&2; exit 1; }
mkdir -p "$(dirname -- "$LOG")"

{
  echo "=== $(date '+%F %T %z') daily city restart begin"
  rm -f "$SENTINEL"

  echo "-- preflight (dependencies must resolve BEFORE we stop anything):"
  missing=""
  for dep in tmux dolt bd flock; do
    if path=$(command -v "$dep" 2>/dev/null); then
      echo "   ok   $dep -> $path"
    else
      echo "   MISS $dep"
      missing="$missing $dep"
    fi
  done
  if [ -n "$missing" ]; then
    alarm critical "preflight failed, refusing to stop the city — missing:$missing (PATH=$PATH)"
    echo "=== $(date '+%F %T %z') daily city restart ABORTED (city left running)"
    exit 1
  fi

  echo "-- refresh gc before stopping the city:"
  if ! refresh_gc; then
    alarm critical "gc refresh failed, refusing to stop the city"
    echo "=== $(date '+%F %T %z') daily city restart ABORTED (city left running)"
    exit 1
  fi

  echo "-- pre-restart dolt health:"
  "$GC" dolt health 2>&1 | grep -v '^named_session' | head -2

  echo "-- gc stop:"
  "$GC" stop "$CITY" --timeout=120s 2>&1 | grep -v '^named_session'
  stop_rc=${PIPESTATUS[0]}
  [ "$stop_rc" -eq 0 ] || echo "!! gc stop FAILED (exit $stop_rc) — continuing to start anyway"
  pause 10

  echo "-- waiting for dolt to be reachable before start:"
  for i in 1 2 3 4 5 6; do
    if "$GC" dolt status 2>&1 | grep -qi 'running'; then
      echo "   dolt reachable after ${i} check(s)"
      break
    fi
    echo "   dolt not reachable yet (check $i/6)"
    pause 10
  done

  start_rc=1
  for attempt in 1 2 3; do
    echo "-- gc start (attempt $attempt/3):"
    "$GC" start "$CITY" 2>&1 | grep -v '^named_session'
    start_rc=${PIPESTATUS[0]}
    [ "$start_rc" -eq 0 ] && { echo "   start succeeded on attempt $attempt"; break; }
    echo "!! gc start attempt $attempt FAILED (exit $start_rc)"
    [ "$attempt" -lt 3 ] && pause 30
  done

  if [ "$start_rc" -ne 0 ]; then
    alarm critical "gc start failed 3/3 attempts (exit $start_rc) — CITY IS DOWN"
  fi

  pause 30
  echo "-- post-restart dolt health:"
  "$GC" dolt health 2>&1 | grep -v '^named_session' | head -2

  echo "-- session count after restart:"
  sessions=$(tmux -L greg-gas-city ls 2>/dev/null | wc -l | tr -d ' ')
  echo "   $sessions tmux session(s)"
  if [ "${sessions:-0}" -le 1 ]; then
    alarm high "city started but only ${sessions} session(s) are up — expected the agent fleet"
  fi

  echo "=== $(date '+%F %T %z') daily city restart done"
} >>"$LOG" 2>&1
