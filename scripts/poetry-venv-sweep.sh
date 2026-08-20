#!/usr/bin/env bash
# Backstop sweep for Poetry environments orphaned by crashed worktree teardown.
set -uo pipefail

CACHE="${POETRY_VENV_DIR:-$HOME/Library/Caches/pypoetry/virtualenvs}"
DAYS=2
DRY=0
while [ "$#" -gt 0 ]; do
	case "$1" in
		--dry-run) DRY=1 ;;
		--days)
			shift
			DAYS="${1:-2}"
			;;
	esac
	shift
done

[ -d "$CACHE" ] || { echo "poetry-venv-sweep: no cache at $CACHE"; exit 0; }

# Never remove a virtualenv while an agent is using it.
in_use=$(ps aux 2>/dev/null | grep -c "[p]ypoetry/virtualenvs" || true)
if [ "${in_use:-0}" -gt 0 ]; then
	echo "poetry-venv-sweep: $in_use process(es) currently using a virtualenv — refusing to sweep"
	exit 0
fi

before_n=$(find "$CACHE" -maxdepth 1 -mindepth 1 -type d 2>/dev/null | wc -l | tr -d ' ')
echo "poetry-venv-sweep: $before_n virtualenv(s) in $CACHE, removing those older than ${DAYS}d"

removed=0
while IFS= read -r directory; do
	[ -n "$directory" ] || continue
	if [ "$DRY" = "1" ]; then
		echo "  DRY: would remove $(basename "$directory")"
	elif rm -rf -- "$directory"; then
		removed=$((removed + 1))
	fi
done < <(find "$CACHE" -maxdepth 1 -mindepth 1 -type d -mtime "+$DAYS" 2>/dev/null)

after_n=$(find "$CACHE" -maxdepth 1 -mindepth 1 -type d 2>/dev/null | wc -l | tr -d ' ')
echo "poetry-venv-sweep: removed $removed; $before_n -> $after_n remaining"
