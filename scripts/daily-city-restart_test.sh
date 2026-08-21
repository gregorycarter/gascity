#!/bin/bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_ROOT=$(mktemp -d -p /var/tmp gc-daily-restart.XXXXXX)
trap 'rm -rf "$TEST_ROOT"' EXIT

git_config() {
  git -C "$1" config user.email test@example.com
  git -C "$1" config user.name "Daily Restart Test"
}

make_fixture() {
  local name="$1"
  local fixture="$TEST_ROOT/$name"
  local remote="$fixture/remote.git"
  local source="$fixture/source"

  mkdir -p "$fixture"
  git init --bare -q "$remote"
  git init -q -b main "$source"
  git_config "$source"
  cat >"$source/Makefile" <<'EOF'
.PHONY: build
build:
	./build-fixture.sh
EOF
  cat >"$source/build-fixture.sh" <<'EOF'
#!/bin/sh
set -eu

if [ "${GC_DAILY_RESTART_TEST_BUILD_MODE:-}" = build-failure ]; then
  exit 1
fi

sha=$(git rev-parse --short HEAD)
reported_sha="$sha"
if [ "${GC_DAILY_RESTART_TEST_BUILD_MODE:-}" = version-failure ]; then
  reported_sha=deadbeef
fi

mkdir -p bin
cat >bin/gc <<SCRIPT
#!/bin/sh
set -eu
if [ "\$1" = version ] && [ "\$2" = --json ]; then
  printf '{"commit":"$reported_sha"}\\n'
  exit 0
fi
case "\$1 \${2:-}" in
  "stop "*) printf '%s\\n' stop >> "\${GC_DAILY_RESTART_TEST_STOP_MARKER:?}" ;;
  "start "*) printf '%s\\n' start >> "\${GC_DAILY_RESTART_TEST_STOP_MARKER:?}" ;;
  "dolt health") printf '%s\\n' healthy ;;
  "dolt status") printf '%s\\n' running ;;
esac
SCRIPT
chmod 0755 bin/gc
EOF
  chmod 0755 "$source/build-fixture.sh"
  git -C "$source" add Makefile build-fixture.sh
  git -C "$source" commit -q -m fixture
  git -C "$source" remote add origin "$remote"
  git -C "$source" push -q -u origin main
  printf '%s\n' "$source"
}

make_fake_dependencies() {
  local fake_bin="$1"
  mkdir -p "$fake_bin"
  for dependency in tmux dolt bd flock; do
    cat >"$fake_bin/$dependency" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod 0755 "$fake_bin/$dependency"
  done
  cat >"$fake_bin/gt" <<'EOF'
#!/bin/sh
printf '%s\n' escalation-ok
EOF
  chmod 0755 "$fake_bin/gt"
}

run_case() {
  local name="$1"
  local source="$2"
  local install_path="$3"
  local mode="${4:-}"
  local fake_bin="$TEST_ROOT/$name/fake-bin"
  local city="$TEST_ROOT/$name/city"
  local stop_marker="$TEST_ROOT/$name/stopped"
  local log="$city/.gc/logs/daily-city-restart.log"
  local sentinel="$city/.gc/logs/daily-city-restart.FAILED"

  mkdir -p "$city/.gc/logs"
  make_fake_dependencies "$fake_bin"
  GC_DAILY_RESTART_CITY="$city" \
  GC_DAILY_RESTART_SOURCE_REPO="$source" \
  GC_DAILY_RESTART_INSTALL="$install_path" \
  GC_DAILY_RESTART_GC="$install_path" \
  GC_DAILY_RESTART_GT="$fake_bin/gt" \
  GC_DAILY_RESTART_ESCALATE_FROM="$city" \
  GC_DAILY_RESTART_TMP_ROOT="$city/.gc/tmp" \
  GC_DAILY_RESTART_TEST_BUILD_MODE="$mode" \
  GC_DAILY_RESTART_TEST_STOP_MARKER="$stop_marker" \
  GC_DAILY_RESTART_SKIP_SLEEP=1 \
  GC_DAILY_RESTART_PATH="$fake_bin:$PATH" \
  PATH="$fake_bin:$PATH" \
    "$ROOT/scripts/daily-city-restart.sh" >"$TEST_ROOT/$name/output" 2>&1
}

assert_failure_before_stop() {
  local name="$1"
  local source="$2"
  local install_path="$3"
  local mode="${4:-}"
  local rc=0

  if run_case "$name" "$source" "$install_path" "$mode"; then
    rc=0
  else
    rc=$?
  fi
  [ "$rc" -ne 0 ] || { echo "$name unexpectedly succeeded" >&2; return 1; }
  [ ! -e "$TEST_ROOT/$name/stopped" ] || { echo "$name stopped the city" >&2; return 1; }
  [ -e "$TEST_ROOT/$name/city/.gc/logs/daily-city-restart.FAILED" ] || {
    echo "$name did not write the failure sentinel" >&2
    return 1
  }
}

success_source=$(make_fixture success)
printf '%s\n' dirty >"$success_source/active-work.txt"
success_install="$TEST_ROOT/success/install/bin/gc"
mkdir -p "$(dirname -- "$success_install")"
run_case success "$success_source" "$success_install"
[ -x "$success_install" ] || { echo "successful refresh did not install gc" >&2; exit 1; }
[ -e "$TEST_ROOT/success/stopped" ] || { echo "successful restart did not stop city" >&2; exit 1; }
[ "$(cat "$success_source/active-work.txt")" = dirty ] || { echo "active worktree changed" >&2; exit 1; }
git -C "$success_source" status --porcelain | grep -qx '?? active-work.txt'

fetch_source=$(make_fixture fetch-failure)
git -C "$fetch_source" remote set-url origin "$TEST_ROOT/no-such-remote"
assert_failure_before_stop fetch-failure "$fetch_source" "$TEST_ROOT/fetch-failure/install/bin/gc"

build_source=$(make_fixture build-failure)
assert_failure_before_stop build-failure "$build_source" "$TEST_ROOT/build-failure/install/bin/gc" build-failure

version_source=$(make_fixture version-failure)
assert_failure_before_stop version-failure "$version_source" "$TEST_ROOT/version-failure/install/bin/gc" version-failure

install_source=$(make_fixture install-failure)
assert_failure_before_stop install-failure "$install_source" "$TEST_ROOT/install-failure/missing/bin/gc"

printf '%s\n' 'daily city restart tests passed'
