#!/usr/bin/env bash
# Done-when 10 (design §2, §11.3, §11.4), outside the browser:
#   m3-logs.sh rotate      zimam-maintenance.timer runs every 15 minutes; a
#                          201 MiB access log, once maintenance has run, is
#                          a .gz archive and a small current log
#   m3-logs.sh mark        remembers the newest panel job
#   m3-logs.sh policy      the panel's logs.policy job succeeded, and the
#                          agent's config and php84's rules keep 1 day
#   m3-logs.sh retention   that archive, backdated 3 days, goes with the
#                          next maintenance run, and nothing else changes
# The browser sets the retention in Settings between policy and retention.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"

dir=/var/log/zimam/sites/php84
pass() { echo "PASS  done-when 10: $*"; }

# archives lists every site's log archives, root's and the homes'.
archives() {
	in_c bash -c 'find /var/log/zimam/sites /home/s_*/logs -xdev -name "*.gz" -printf "%p\t%s\t%m\t%u:%g\t%T@\n" | LC_ALL=C sort'
}

# snapshot is fs-hash.sh's proof plus the archives and their hashes.
snapshot() {
	in_c zimam-fs-hash
	archives
	in_c bash -c 'find /var/log/zimam/sites /home/s_*/logs -xdev -name "*.gz" -print0 | LC_ALL=C sort -z | xargs -0 -r sha256sum'
}

case ${1:-} in
rotate)
	cal=$(in_c systemctl show -p TimersCalendar --value zimam-maintenance.timer)
	[[ $cal == *'OnCalendar=*-*-* *:00/15:00'* ]] || die "FAIL done-when 10: zimam-maintenance.timer runs on $cal"
	pass "zimam-maintenance.timer runs every 15 minutes ($cal)"
	# Twice: retention deletes the first archive, and the archive probes
	# (e2e/probes/archive-names.sh) read the second.
	for round in 1 2; do
		archives >"$E2E_WORK/archives-before.txt"
		# A request to find in the archive (e2e/probes/archive-names.sh).
		in_c curl -sS -o /dev/null -H "Host: php84.zimam.test" "http://127.0.0.1/e2e-archive-probe-$round"
		in_c truncate -s 201M "$dir/access.log"
		in_c systemctl start zimam-maintenance.service
		end=$((SECONDS + 900))
		for (( ; ; )); do
			new=$(archives | cut -f1 | grep -vxFf <(cut -f1 "$E2E_WORK/archives-before.txt") | grep "^$dir/access\.log-" || true)
			size=$(in_c stat -c %s "$dir/access.log")
			if [[ -n $new ]] && ((size < 1048576)); then
				break
			fi
			((SECONDS < end)) || die "FAIL done-when 10: no new archive within 15 minutes (access.log is $size bytes)"
			sleep 10
		done
		[[ $(wc -l <<<"$new") == 1 ]] || die "FAIL done-when 10: more than one new archive: $new"
		in_c gzip -t "$new" || die "FAIL done-when 10: $new isn't a valid gzip file"
		((round == 2)) || echo "$new" >"$E2E_WORK/rotated-archive"
		pass "round $round: the 201 MiB access.log is $new ($(in_c stat -c %s "$new") bytes), and access.log is $size bytes"
		# dateext names archives by the second.
		sleep 1
	done
	;;
mark)
	jobs_mark >"$E2E_WORK/jobs-mark"
	;;
policy)
	wait_jobs "$(cat "$E2E_WORK/jobs-mark")" logs.policy 1 succeeded || die "FAIL done-when 10: the logs.policy job didn't succeed"
	days=$(in_c cat /etc/zimam/agent.json | python3 -I -c 'import json, sys; print(json.load(sys.stdin)["log_retention_days"])')
	[[ $days == 1 ]] || die "FAIL done-when 10: agent.json keeps logs for $days days"
	in_c grep -qx '    maxage 1' /etc/zimam/logrotate.d/site-php84.conf || die "FAIL done-when 10: php84's rules don't say maxage 1"
	pass "logs.policy stored 1 day in agent.json and php84's rotation rules"
	;;
retention)
	archive=$(cat "$E2E_WORK/rotated-archive")
	in_c touch -d '3 days ago' "$archive"
	lxc file push -q --mode 0755 "$E2E_ROOT/e2e/scripts/fs-hash.sh" "$E2E_CONTAINER/usr/local/bin/zimam-fs-hash"
	audit=$(in_c bash -c 'wc -l </var/log/zimam/audit.log')
	snapshot >"$E2E_WORK/fs-retention-before.txt"
	in_c systemctl start zimam-maintenance.service
	snapshot >"$E2E_WORK/fs-retention-after.txt"
	if in_c test -e "$archive"; then
		die "FAIL done-when 10: $archive, backdated 3 days, is still there"
	fi
	pass "$archive, backdated past the 1-day retention, is deleted"
	if ! diff -u <(grep -vF "$archive" "$E2E_WORK/fs-retention-before.txt") "$E2E_WORK/fs-retention-after.txt"; then
		die "FAIL done-when 10: maintenance changed more than that archive (above)"
	fi
	pass "nothing else changed ($(wc -l <"$E2E_WORK/fs-retention-after.txt") lines of fs-hash and archives compared)"
	in_c tail -n "+$((audit + 1))" /var/log/zimam/audit.log | python3 -I -c '
import json, sys
archive = sys.argv[1]
ok = any(e["command"] == "maintenance" and archive in (e.get("params") or {}).get("deleted_archives", [])
         for e in map(json.loads, sys.stdin))
sys.exit(0 if ok else 1)' "$archive" || die "FAIL done-when 10: no maintenance audit entry lists $archive"
	pass "the maintenance audit entry lists the deleted archive"
	;;
*) die "usage: m3-logs.sh rotate|mark|policy|retention" ;;
esac
