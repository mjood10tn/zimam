#!/usr/bin/env bash
# The site.destroy symlink probe (design §14.4), as zimam through the agent:
# provision the throwaway site probe-static, plant site/escape -> / as its
# user, then destroy it. Taken before the provision and after the destroy,
# the file snapshots must match, but for the account files' backups that
# useradd and userdel rewrite (/etc/passwd- and its kind), and the home
# must be gone: the delete never left the home.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

site=probe-static
user=s_$site

# request <command> <params> sends one request as zimam and prints the reply.
request() {
	printf '%s\n' "{$env_ok,\"command\":\"$1\",\"params\":$2}" | bash "$E2E_ROOT/e2e/scripts/as.sh" zimam "$PROBECLIENT" "$SOCKET"
}

# run_job <command> <params> submits a job and waits for it to succeed.
run_job() {
	local reply job status
	reply=$(request "$1" "$2")
	job=$(python3 -I -c 'import json, sys; r = json.load(sys.stdin); assert r["type"] == "accepted"; print(r["job"])' <<<"$reply") ||
		die "$1 wasn't accepted: $reply"
	for _ in $(seq 1 120); do
		status=$(request job.status "{\"job\":\"$job\"}" | python3 -I -c 'import json, sys; d = json.load(sys.stdin)["data"]; print(d["status"], d.get("message") or "")')
		case $status in
		succeeded*) return 0 ;;
		failed* | cancelled*) die "$1 ended $status" ;;
		esac
		sleep 2
	done
	die "$1 didn't finish within 4 minutes"
}

in_c zimam-fs-hash >"$E2E_WORK/fs-destroy-before.txt"
run_job site.provision "{\"name\":\"$site\",\"domain\":\"$site.zimam.test\",\"type\":\"static\"}"
in_c runuser -u "$user" -- ln -s / "/home/$user/site/escape"
target=$(in_c readlink "/home/$user/site/escape")
[[ $target == / ]] || die "the planted link points at $target"
pass "$user planted site/escape -> /"
run_job site.destroy "{\"name\":\"$site\"}"
in_c zimam-fs-hash >"$E2E_WORK/fs-destroy-after.txt"

if in_c test -e "/home/$user"; then
	fail "/home/$user is still there"
else
	pass "/home/$user is gone"
fi
if in_c id "$user" >/dev/null 2>&1; then
	fail "the user $user is still there"
else
	pass "the user $user is gone"
fi
changed=$(python3 -I - "$E2E_WORK/fs-destroy-before.txt" "$E2E_WORK/fs-destroy-after.txt" <<'PY'
import re, sys

before, after = (set(open(p).read().splitlines()) for p in sys.argv[1:])
allowed = re.compile(r"^/etc/(passwd|group|shadow|gshadow|subuid|subgid)-$")
for line in sorted(before ^ after):
    path = line.split("\t", 1)[0] if "\t" in line else line.split("  ", 1)[1]
    if not allowed.match(path):
        print(("- " if line in before else "+ ") + line)
PY
)
if [[ -z $changed ]]; then
	pass "nothing outside /home/$user changed ($(wc -l <"$E2E_WORK/fs-destroy-before.txt") lines compared)"
else
	fail "site.destroy changed files outside the home:"
	printf '%s\n' "$changed"
fi
exit "$failed"
