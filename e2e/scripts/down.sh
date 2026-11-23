#!/usr/bin/env bash
# Tears the environment down: the container, the Git host (e2e/githost),
# the backup and mail sinks (e2e/sinks), the fill disk (v05-backups.sh),
# Pebble, challtestsrv and the release server. Safe to run whatever up.sh
# got to.
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
set +e

lxc delete --force "$E2E_CONTAINER" >/dev/null 2>&1
bash "$E2E_ROOT/e2e/githost/githost.sh" down
[[ $E2E_TARGET != lxd ]] || bash "$E2E_ROOT/e2e/sinks/sinks.sh" down
if [[ -f $E2E_WORK/fill.img ]]; then
	as_root umount "$E2E_WORK/fill" 2>/dev/null
	rm -f "$E2E_WORK/fill.img"
fi
for unit in "$E2E_UNIT-pebble" "$E2E_UNIT-challtestsrv" "$E2E_UNIT-www"; do
	as_root systemctl stop "$unit" 2>/dev/null
	as_root systemctl reset-failed "$unit" 2>/dev/null
done
# A profile this run made for itself goes; the default one is CI's and stays.
[[ $E2E_PROFILE == zimam-e2e ]] || lxc profile delete "$E2E_PROFILE" >/dev/null 2>&1
log "down"
exit 0
