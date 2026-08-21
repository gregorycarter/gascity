#!/bin/bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
CITY=${GC_DAILY_RESTART_CITY:-/Users/gregcarter/gascity-workspace}
SCRIPT_DEST=${GC_DAILY_RESTART_SCRIPT_DEST:-$CITY/.gc/scripts/daily-city-restart.sh}
PLIST_DEST=${GC_DAILY_RESTART_PLIST_DEST:-$HOME/Library/LaunchAgents/com.gascity.daily-restart.plist}

if [ "${1:-}" = --dry-run ]; then
  printf 'source=%s\nscript=%s\nplist=%s\nschedule=05:30\n' \
    "$ROOT/scripts/daily-city-restart.sh" "$SCRIPT_DEST" "$PLIST_DEST"
  exit 0
fi

[ -x "$ROOT/scripts/daily-city-restart.sh" ] || {
  echo "daily restart source script is not executable" >&2
  exit 1
}
mkdir -p "$(dirname -- "$SCRIPT_DEST")" "$(dirname -- "$PLIST_DEST")"

script_tmp=$(mktemp "$(dirname -- "$SCRIPT_DEST")/.daily-city-restart.XXXXXX")
plist_tmp=$(mktemp "$(dirname -- "$PLIST_DEST")/.com.gascity.daily-restart.XXXXXX")
cleanup() { rm -f "$script_tmp" "$plist_tmp"; }
trap cleanup EXIT

cp -f "$ROOT/scripts/daily-city-restart.sh" "$script_tmp"
chmod 0755 "$script_tmp"
mv -f "$script_tmp" "$SCRIPT_DEST"

cat >"$plist_tmp" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.gascity.daily-restart</string>
  <key>ProgramArguments</key>
  <array>
    <string>$SCRIPT_DEST</string>
  </array>
  <key>StartCalendarInterval</key>
  <dict>
    <key>Hour</key><integer>5</integer>
    <key>Minute</key><integer>30</integer>
  </dict>
  <key>StandardOutPath</key>
  <string>$CITY/.gc/logs/daily-city-restart.launchd.log</string>
  <key>StandardErrorPath</key>
  <string>$CITY/.gc/logs/daily-city-restart.launchd.log</string>
</dict>
</plist>
EOF
plutil -lint "$plist_tmp" >/dev/null
mv -f "$plist_tmp" "$PLIST_DEST"
trap - EXIT
echo "installed daily restart script and 05:30 launchd plist"
