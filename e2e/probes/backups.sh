#!/usr/bin/env bash
# The backup probes (v0.5.0, R05-D4, R05-D2, R05-D3), as zimam through the
# agent, on the throwaway static site probe-bk and the S3 destination that
# scenarios/v05-backups.sh left; run-all.sh runs them between its two file
# snapshots, and they put back what they change (the plan and the site go).
# The malformed destinations and restores are validation.sh's. Here:
#   - a link the site user planted to /etc is archived as a link, never as
#     /etc's files: the restore leaves it out and says so (skipped_links),
#     and no file of /etc lands in the restore folder;
#   - no secret (the S3 credentials, the encryption keys) is in any
#     process's command line while a run goes (sampled every 50 ms), in the
#     audit log, in the job logs or in the backup run logs.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
load_state

if [[ $E2E_TARGET != lxd || ! -f $E2E_WORK/v05/secrets ]]; then
	echo "SKIP  backups.sh needs v05-backups.sh's destination (LXD only)"
	exit 0
fi

site=probe-bk
user=s_$site
home=/home/$user
plan=probe-bk

jq_py() {
	python3 -I -c 'import json, sys; d = json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"
}

# job <command> <params> runs a job and prints its result, or "" after a failure.
job() {
	local id status
	id=$(submit "$1" "$2") || return 1
	status=$(job_end "$id" 900)
	[[ $status == succeeded* ]] || {
		echo "e2e: $1 ended $status" >&2
		return 1
	}
	request job.status "{\"job\":\"$id\"}" | jq_py 'json.dumps(d["data"]["result"])'
}

job site.provision "{\"name\":\"$site\",\"domain\":\"$site.$E2E_DOMAIN\",\"type\":\"static\"}" >/dev/null || die "$site wasn't provisioned"
in_c runuser -u "$user" -- ln -s /etc "$home/site/releases/initial/etc-link"
job backup.plan "{\"id\":\"$plan\",\"destination\":\"s3\",\"sites\":[\"$site\"],\"schedule\":\"weekly sun 05:00\",\"retention\":{\"keep_last\":1}}" >/dev/null ||
	die "the probe plan wasn't saved"

# A run while the command lines are sampled, until the stop file appears:
# one cat of every cmdline per sweep, so a sweep takes milliseconds.
in_c rm -f /root/zimam-probe-cmdlines /root/zimam-probe-stop /root/zimam-probe-started
# shellcheck disable=SC2016 # expanded by the container's bash
in_c bash -c 'touch /root/zimam-probe-started; until [[ -e /root/zimam-probe-stop ]]; do cat /proc/[0-9]*/cmdline 2>/dev/null | tr "\0" " " >>/root/zimam-probe-cmdlines; echo >>/root/zimam-probe-cmdlines; sleep 0.02; done' &
sampler=$!
for _ in $(seq 50); do
	in_c test -e /root/zimam-probe-started && break
	sleep 0.2
done
result=$(job backup.run "{\"plan\":\"$plan\"}") || fail "the probe plan's run failed"
backup=$(jq_py 'd["backup"]' <<<"$result")
in_c touch /root/zimam-probe-stop
wait "$sampler"
cmdlines=$(in_c cat /root/zimam-probe-cmdlines)
in_c rm -f /root/zimam-probe-cmdlines /root/zimam-probe-stop /root/zimam-probe-started
if grep -q rclone-1 <<<"$cmdlines"; then
	pass "the command lines were sampled while rclone ran ($(grep -c rclone-1 <<<"$cmdlines") sweeps saw it)"
else
	fail "the sampler never saw rclone run"
fi

# The restore drops the planted link and says so.
restore=$(job backup.restore "{\"plan\":\"$plan\",\"backup\":\"$backup\",\"part\":\"files-$site\",\"site\":\"$site\"}")
folder=$(jq_py 'd["folder"]' <<<"$restore")
skipped=$(jq_py '" ".join(d.get("skipped_links") or [])' <<<"$restore")
if [[ $skipped == *etc-link* ]] && ! in_c find "$folder" -name passwd -o -name shadow | grep -q .; then
	pass "the planted link to /etc was archived as a link and left out of the restore (skipped: $skipped); no /etc file in $folder"
else
	fail "the restore of the planted link: skipped '$skipped', found: $(in_c find "$folder" -name passwd -o -name shadow)"
fi

# No secret anywhere it could leak.
logs=$(in_c bash -c 'cat /var/log/zimam/audit.log /var/log/zimam/jobs/*.log; find /var/log/zimam/backups -type f -exec cat {} +')
leaks=0
while read -r s; do
	[[ -n $s ]] || continue
	if grep -qF -- "$s" <<<"$cmdlines"; then
		fail "a secret (${s:0:4}…) is in a command line"
		leaks=1
	fi
	if grep -qF -- "$s" <<<"$logs"; then
		fail "a secret (${s:0:4}…) is in the audit log, a job log or a run log"
		leaks=1
	fi
done <"$E2E_WORK/v05/secrets"
((leaks)) || pass "none of the $(wc -l <"$E2E_WORK/v05/secrets") backup secrets is in a command line, the audit log, a job log or a run log"

job backup.plan.delete "{\"plan\":\"$plan\"}" >/dev/null || fail "the probe plan wasn't deleted"
job site.destroy "{\"name\":\"$site\"}" >/dev/null || fail "$site wasn't destroyed"
exit "$failed"
