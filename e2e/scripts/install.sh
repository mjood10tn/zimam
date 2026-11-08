#!/usr/bin/env bash
# Runs the installer in the container the way §4 tells an admin to, with
# Pebble as the ACME server, and keeps its stdout, stderr and exit code for
# m1-install.sh. On a VPS it runs on the server, against Let's Encrypt
# staging, with the firewall on (common.sh's E2E_INSTALL_FLAGS).
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
load_state

if [[ $E2E_TARGET == lxd ]]; then
	lxc file push -q "$E2E_WORK/pebble/ca.pem" "$E2E_CONTAINER/root/pebble-ca.pem"
	bash "$(dirname "$0")/hosts.sh"
fi
cmd="curl -fsSL http://$BRIDGE:$E2E_PORT/latest/download/install.sh | bash -s -- --panel-domain $E2E_PANEL"
cmd+=" $E2E_INSTALL_FLAGS --yes"
log "$cmd"
code=0
in_c bash -c "$cmd" >"$E2E_WORK/install.out" 2>"$E2E_WORK/install.err" || code=$?
cat "$E2E_WORK/install.err" >&2
echo "$code" >"$E2E_WORK/install.status"
log "the installer exited $code"
exit "$code"
