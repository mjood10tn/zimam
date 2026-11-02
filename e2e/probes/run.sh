#!/usr/bin/env bash
# Runs milestone 1's probes between two fs-hash.sh snapshots, which must be
# identical (design §14.4's proof). Everything that writes /etc, /root or
# /home happens before the first snapshot: the probe client and fs-hash.sh
# are copied in, s_probe is created, and maintenance runs once, since its
# first run refreshes the sury keyring in /etc/apt. Between the two
# snapshots only the probes run.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

lxc file push -q --mode 0755 "$E2E_WORK/bin/probeclient" "$E2E_CONTAINER$PROBECLIENT"
lxc file push -q --mode 0755 "$E2E_ROOT/e2e/scripts/fs-hash.sh" "$E2E_CONTAINER/usr/local/bin/zimam-fs-hash"
in_c bash -c 'id s_probe >/dev/null 2>&1 || useradd --system --no-create-home --user-group --shell /usr/sbin/nologin s_probe'
in_c systemctl start zimam-maintenance.service

in_c zimam-fs-hash >"$E2E_WORK/fs-before.txt"
code=0
bash "$(dirname "$0")/callers.sh" || code=1
bash "$(dirname "$0")/protocol.sh" || code=1
in_c zimam-fs-hash >"$E2E_WORK/fs-after.txt"

if diff -u "$E2E_WORK/fs-before.txt" "$E2E_WORK/fs-after.txt"; then
	pass "/etc, /usr/local/sbin, /root and /home are unchanged ($(wc -l <"$E2E_WORK/fs-before.txt") lines)"
else
	fail "the probes changed the files above"
	code=1
fi
in_c userdel s_probe
exit "$code"
