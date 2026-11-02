#!/usr/bin/env bash
# Points the test names at the container: in its /etc/hosts, with pebble
# at this host on the bridge, and in challtestsrv's DNS, which Pebble's
# validation asks (Task 2 spike, steps 3 and 4).
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
load_state

in_c bash -c "sed -i '/# zimam-e2e\$/d' /etc/hosts && printf '%s\n' '$CIP ${E2E_NAMES[*]} # zimam-e2e' '$BRIDGE pebble # zimam-e2e' >>/etc/hosts"
for name in "${E2E_NAMES[@]}"; do
	curl -fsS -X POST -d "{\"host\":\"$name.\",\"addresses\":[\"$CIP\"]}" http://localhost:8055/add-a >/dev/null
done
log "${E2E_NAMES[*]} -> $CIP, pebble -> $BRIDGE"
