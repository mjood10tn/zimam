#!/usr/bin/env bash
# Runs every Fuzz* target in the agent module briefly (design §14.1). Longer
# runs happen in nightly.yml. New targets are picked up automatically.
# Usage: scripts/fuzz-short.sh [fuzztime]   (default 10s)
set -euo pipefail
cd "$(dirname "$0")/.."
fuzztime="${1:-10s}"

# `go test -list` prints each package's matching names, then "ok <package>".
listing="$(go test -list '^Fuzz' ./...)"
targets=()
names=()
while read -r first second _; do
	case "$first" in
	Fuzz*) names+=("$first") ;;
	ok)
		for name in "${names[@]}"; do
			targets+=("$second $name")
		done
		names=()
		;;
	esac
done <<<"$listing"

if [ "${#targets[@]}" -eq 0 ]; then
	echo "fuzz-short: no Fuzz targets found" >&2
	exit 1
fi

# -fuzzminimizetime=0 skips minimizing new inputs, which otherwise takes most
# of a short run's budget. A failing input is then reported unminimized.
for target in "${targets[@]}"; do
	read -r pkg name <<<"$target"
	echo "== $name ($pkg)"
	go test -run='^$' -fuzz="^${name}\$" -fuzztime="$fuzztime" -fuzzminimizetime=0 "$pkg"
done
echo "fuzz-short: ran ${#targets[@]} targets for $fuzztime each"
