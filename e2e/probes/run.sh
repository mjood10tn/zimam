#!/usr/bin/env bash
# Runs the probes (design §14.4): the caller, protocol, validation, log
# archive name, log symlink, journal paging and job-limit (OOM) probes
# between two fs-hash.sh snapshots, which must be identical, then the
# site.destroy symlink and maintenance probes, which change /etc on purpose
# and compare their own snapshots. The
# milestone 2 scenarios have run, so the site user s_php84 exists.
# Everything that writes /etc, /root or /home happens before the first
# snapshot: the probe client and fs-hash.sh are copied in, and maintenance
# runs once, since its first run refreshes the sury keyring in /etc/apt.
# Between the two snapshots only the probes run.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

lxc file push -q --mode 0755 "$E2E_WORK/bin/probeclient" "$E2E_CONTAINER$PROBECLIENT"
lxc file push -q --mode 0755 "$E2E_ROOT/e2e/scripts/fs-hash.sh" "$E2E_CONTAINER/usr/local/bin/zimam-fs-hash"
in_c id s_php84 >/dev/null || die "no s_php84: run the milestone 2 scenarios first"
in_c systemctl start zimam-maintenance.service

in_c zimam-fs-hash >"$E2E_WORK/fs-before.txt"
code=0
bash "$(dirname "$0")/callers.sh" || code=1
bash "$(dirname "$0")/protocol.sh" || code=1
bash "$(dirname "$0")/validation.sh" || code=1
bash "$(dirname "$0")/archive-names.sh" || code=1
bash "$(dirname "$0")/log-symlink.sh" || code=1
bash "$(dirname "$0")/journal-paging.sh" || code=1
bash "$(dirname "$0")/oom.sh" || code=1
in_c zimam-fs-hash >"$E2E_WORK/fs-after.txt"

if diff -u "$E2E_WORK/fs-before.txt" "$E2E_WORK/fs-after.txt"; then
	pass "/etc, /usr/local/sbin, /root and /home are unchanged ($(wc -l <"$E2E_WORK/fs-before.txt") lines)"
else
	fail "the probes changed the files above"
	code=1
fi
bash "$(dirname "$0")/destroy-symlink.sh" || code=1
bash "$(dirname "$0")/maintenance.sh" || code=1
exit "$code"
