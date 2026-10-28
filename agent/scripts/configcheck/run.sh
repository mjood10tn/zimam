#!/usr/bin/env bash
# Runs the config check image on every golden case, each in a fresh container
# without network (design §14.1). ci.yml and local runs both use this script.
# Usage: scripts/configcheck/run.sh <image> <golden-dir>
set -euo pipefail
shopt -s nullglob

image="${1:?usage: run.sh <image> <golden-dir>}"
golden="$(cd "${2:?usage: run.sh <image> <golden-dir>}" && pwd)"

cases=()
for dir in "$golden"/*/; do
	cases+=("$(basename "$dir")")
done
if [ "${#cases[@]}" -eq 0 ]; then
	echo "configcheck: no golden cases in $golden" >&2
	exit 1
fi

failed=()
for c in "${cases[@]}"; do
	echo "=== $c"
	if ! docker run --rm --network none --volume "$golden/$c:/case:ro" "$image" /case; then
		failed+=("$c")
	fi
done
if [ "${#failed[@]}" -gt 0 ]; then
	echo "configcheck: ${#failed[@]} of ${#cases[@]} cases failed: ${failed[*]}" >&2
	exit 1
fi
echo "configcheck: all ${#cases[@]} cases passed"
