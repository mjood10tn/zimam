#!/usr/bin/env bash
# The Go tests that need root and real users (the roottest tag): build.sh
# compiled them on this host, and they run in the container as root.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"

tests=("$E2E_WORK"/bin/roottest-*.test)
[[ -e ${tests[0]} ]] || die "no roottest binaries in $E2E_WORK/bin: run build.sh"
in_c mkdir -p /root/roottests
for t in "${tests[@]}"; do
	name=$(basename "$t")
	lxc file push -q --mode 0755 "$t" "$E2E_CONTAINER/root/roottests/$name"
	log "$name"
	in_c "/root/roottests/$name" -test.v -test.count=1
done
in_c rm -rf /root/roottests
echo "PASS  the root-only Go tests: ${tests[*]##*/}"
