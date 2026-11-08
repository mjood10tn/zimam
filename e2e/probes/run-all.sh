#!/usr/bin/env bash
# Runs every root-escalation probe (design §14.4, done-when 12) in one pass
# between two fs-hash.sh snapshots, which must be identical: README.md maps
# each §14.4 item to its script. The snapshots' exclusions live in one
# place, fs-hash.sh's prune list. The probes that change files on purpose
# put them back: destroy-symlink.sh destroys the site it provisions, and
# maintenance.sh sends the old log policy back and removes what it planted.
# The milestone 2 scenarios have run, so the site users s_php84 and s_php85
# exist. Everything that would change /etc, /root or /home by itself
# happens before the first snapshot: the probe client and fs-hash.sh are
# copied in; the current log policy is sent once, so agent.json and the
# logrotate rules are in the form the agent writes, as maintenance.sh's
# restore will write them; and maintenance runs once, since its first run
# refreshes the sury keyring in /etc/apt. maintenance.sh runs last: its
# 1 KiB cap deletes the php84 archive archive-names.sh reads.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

lxc file push -q --mode 0755 "$E2E_WORK/bin/probeclient" "$E2E_CONTAINER$PROBECLIENT"
lxc file push -q --mode 0755 "$E2E_ROOT/e2e/scripts/fs-hash.sh" "$E2E_CONTAINER/usr/local/bin/zimam-fs-hash"
in_c id s_php84 >/dev/null || die "no s_php84: run the milestone 2 scenarios first"
policy=$(in_c cat /etc/zimam/agent.json | python3 -I -c 'import json, sys; c = json.load(sys.stdin); print(json.dumps({"retention_days": c["log_retention_days"], "audit_retention_days": c["audit_retention_days"], "site_cap_bytes": c["site_log_cap_bytes"]}))')
run_job logs.policy "$policy"
in_c systemctl start zimam-maintenance.service

in_c zimam-fs-hash >"$E2E_WORK/fs-before.txt"
code=0
for probe in callers protocol validation archive-names log-symlink journal-paging oom destroy-symlink maintenance; do
	echo "== $probe.sh"
	bash "$(dirname "$0")/$probe.sh" || code=1
done
in_c zimam-fs-hash >"$E2E_WORK/fs-after.txt"

if diff -u "$E2E_WORK/fs-before.txt" "$E2E_WORK/fs-after.txt"; then
	pass "every probe left /etc, /usr/local/sbin, /root and /home unchanged ($(wc -l <"$E2E_WORK/fs-before.txt") lines)"
else
	fail "the probes changed the files above"
	code=1
fi
exit "$code"
