#!/usr/bin/env bash
set -euo pipefail

# Verify the two source-built commands that must agree with this checkout.
# GC_BINARY and BD_BINARY are overrides for callers that need to inspect an
# explicit install location instead of PATH.
gc_binary=${GC_BINARY:-$(command -v gc || true)}
bd_binary=${BD_BINARY:-$(command -v bd || true)}

if [[ -z "$gc_binary" || ! -x "$gc_binary" ]]; then
	echo "ERROR: gc is not an executable installed binary; run 'make install'" >&2
	exit 1
fi
if [[ -z "$bd_binary" || ! -x "$bd_binary" ]]; then
	echo "ERROR: bd is not an executable installed binary; run 'make install'" >&2
	exit 1
fi

expected_gc_commit=$(git rev-parse --short HEAD)
gc_json=$($gc_binary version --json --long)
actual_gc_commit=$(jq -er '.commit | select(type == "string" and length > 0)' <<<"$gc_json")
actual_gc_commit=${actual_gc_commit%-dirty}
if [[ "$actual_gc_commit" != "$expected_gc_commit" ]]; then
	echo "ERROR: installed gc is stale: commit $actual_gc_commit, checkout is $expected_gc_commit" >&2
	echo "       Run 'make install' from this checkout." >&2
	exit 1
fi

bd_module_info=$(go list -m -f '{{if .Replace}}{{.Replace.Path}}{{else}}{{.Path}}{{end}} {{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' github.com/steveyegge/beads)
read -r expected_bd_path expected_bd_version <<<"$bd_module_info"
if [[ -z "$expected_bd_path" || -z "$expected_bd_version" ]]; then
	echo "ERROR: could not resolve the checkout's beads module version" >&2
	exit 1
fi

bd_build_info=$(go version -m "$bd_binary")
actual_bd_version=$(awk -v module="$expected_bd_path" '
	$1 == "=>" && $2 == module { print $3; found = 1; exit }
	$1 == "mod" && ($2 == module || $2 == "github.com/steveyegge/beads") { mod = $3 }
	END { if (!found && mod != "") print mod }
' <<<"$bd_build_info")
actual_bd_version=${actual_bd_version%%+dirty}
if [[ -z "$actual_bd_version" ]]; then
	echo "ERROR: installed bd has no Go module metadata for $expected_bd_path" >&2
	echo "       Rebuild it with 'make install' from this checkout." >&2
	exit 1
fi
if [[ "$actual_bd_version" != "$expected_bd_version" ]]; then
	echo "ERROR: installed bd is stale: beads $actual_bd_version, checkout links $expected_bd_version" >&2
	echo "       Run 'make install' from this checkout." >&2
	exit 1
fi

echo "installed binaries: OK (gc=$actual_gc_commit bd=$actual_bd_version)"
