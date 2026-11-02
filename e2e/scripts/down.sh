#!/usr/bin/env bash
# Tears the environment down: the container, Pebble, challtestsrv and the
# release server. Safe to run whatever up.sh got to.
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
set +e

lxc delete --force "$E2E_CONTAINER" >/dev/null 2>&1
for unit in zimam-e2e-pebble zimam-e2e-challtestsrv zimam-e2e-www; do
	as_root systemctl stop "$unit" 2>/dev/null
	as_root systemctl reset-failed "$unit" 2>/dev/null
done
log "down"
exit 0
