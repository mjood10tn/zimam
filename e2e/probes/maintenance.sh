#!/usr/bin/env bash
# The maintenance probe (design §11.4, §14.4): zimam-agent maintenance is
# the one root process that works in site folders unattended. As s_php84,
# plant three links in logs/ (to /etc/passwd, /root and s_php85's home),
# named as archives and backdated, so only the regular-file rule spares
# them, and a backdated archive of its own. Set the cap to 1 KiB with
# logs.policy (e2e builds allow it), then run maintenance by hand. It may
# delete only regular archives in a site's own log folders, the planted one
# among them, every one listed in its audit entry; fs-hash.sh's proof, every
# site's log folders and the links with their targets must otherwise be as
# they were. logs.policy rewrites /etc, so run.sh runs this after its
# snapshots, and the old policy is sent back at the end.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

user=s_php84
logs=/home/$user/logs
as_site() { in_c runuser -u "$user" -- "$@"; }
in_c id s_php85 >/dev/null || die "no s_php85: run the milestone 2 scenarios first"

# Archive-named links, older than any retention; and the planted archive.
declare -A links=(
	[access.log-20200101-1577836800.gz]=/etc/passwd
	[error.log-20200101-1577836800.gz]=/root
	[php-error.log-20200101-1577836800.gz]=/home/s_php85
)
planted='php-error.log-20200102-1577923200.gz'

policy=$(in_c cat /etc/zimam/agent.json | python3 -I -c 'import json, sys; c = json.load(sys.stdin); print(json.dumps({"retention_days": c["log_retention_days"], "audit_retention_days": c["audit_retention_days"], "site_cap_bytes": c["site_log_cap_bytes"]}))')
tiny=$(python3 -I -c 'import json, sys; p = json.loads(sys.argv[1]); p["site_cap_bytes"] = 1024; print(json.dumps(p))' "$policy")

# shellcheck disable=SC2329 # the EXIT trap calls it
restore() {
	for name in "${!links[@]}" "$planted"; do
		as_site rm -f "$logs/$name"
	done
	run_job logs.policy "$policy"
	in_c systemctl start zimam-maintenance.timer
}
# A timer run mustn't delete before the snapshot; a run already under way
# finishes first, since maintenance waits for its lock.
in_c systemctl stop zimam-maintenance.timer
trap restore EXIT

run_job logs.policy "$tiny"
pass "logs.policy set the cap to 1 KiB (was $policy)"
for name in "${!links[@]}"; do
	as_site ln -s "${links[$name]}" "$logs/$name"
	as_site touch -h -d '30 days ago' "$logs/$name"
	[[ $(in_c readlink "$logs/$name") == "${links[$name]}" ]] || die "logs/$name wasn't planted"
done
as_site bash -c "printf 'e2e maintenance probe\n' | gzip >$logs/$planted && touch -d '30 days ago' $logs/$planted"
pass "$user planted three archive-named links to ${links[*]} and logs/$planted, all 30 days old"

# snapshot is fs-hash.sh's proof, plus every site's log folders (which it
# leaves out) and their files' hashes, but for the current logs' contents.
snapshot() {
	in_c zimam-fs-hash
	in_c bash -c 'find /var/log/zimam/sites /home/s_*/logs -xdev -printf "%p\t%y\t%m\t%u:%g\t%l\n" | LC_ALL=C sort'
	in_c bash -c 'find /var/log/zimam/sites /home/s_*/logs -xdev -type f ! -name "*.log" -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum'
}
snapshot >"$E2E_WORK/maint-before.txt"
before=$(audit_lines)
if out=$(in_c /usr/local/sbin/zimam-agent maintenance 2>&1); then
	pass "zimam-agent maintenance exited 0"
else
	fail "zimam-agent maintenance failed: ${out:0:500}"
fi
snapshot >"$E2E_WORK/maint-after.txt"
reported=$(audit_since "$before" | python3 -I -c '
import json, sys
out = []
for line in sys.stdin:
    e = json.loads(line)
    if e["command"] == "maintenance":
        out += (e.get("params") or {}).get("deleted_archives") or []
print(json.dumps(out))')

report=$(python3 -I - "$E2E_WORK/maint-before.txt" "$E2E_WORK/maint-after.txt" "$reported" "$logs/$planted" <<'PY'
import json, re, sys

before, after = (open(p).read().splitlines() for p in sys.argv[1:3])
reported, planted = set(json.loads(sys.argv[3])), sys.argv[4]
# A regular archive in a site's own log folder: root's or its home's.
archive = re.compile(r"^(/var/log/zimam/sites/[a-z0-9-]+|/home/s_[a-z0-9-]+/logs)/[a-z0-9-]+\.log-[0-9]{8}-[0-9]+\.gz$")

def path(line):
    return line.split("\t", 1)[0] if "\t" in line else line.split("  ", 1)[1]

listed = {path(l) for l in before}
regular = {l.split("\t")[0] for l in before if "\t" in l and l.split("\t")[1] == "f"}
removed = {path(l) for l in set(before) - set(after)}
bad = ["+ " + l for l in sorted(set(after) - set(before))]
bad += ["- " + p for p in sorted(removed) if not (archive.match(p) and p in regular)]
bad += ["unaudited deletion " + p for p in sorted(removed - reported)]
bad += ["audited but still there " + p for p in sorted((reported & listed) - removed)]
# A run that rotated a log may delete the new archive too; it is never in before.
bad += ["audited, never listed and not an archive " + p for p in sorted(reported - listed) if not archive.match(p)]
if planted not in removed:
    bad.append("the planted archive is still there: " + planted)
print("\n".join(bad) if bad else "deleted " + ", ".join(sorted(removed)))
sys.exit(1 if bad else 0)
PY
) && ok=1 || ok=0
if ((ok)); then
	pass "maintenance deleted only regular archives in sites' own log folders, every one audited: $report"
else
	fail "maintenance changed more than old archives:"
	printf '%s\n' "$report"
fi
for name in "${!links[@]}"; do
	if [[ $(in_c readlink "$logs/$name") == "${links[$name]}" ]]; then
		pass "logs/$name still links to ${links[$name]}"
	else
		fail "logs/$name is gone or changed"
	fi
done
exit "$failed"
