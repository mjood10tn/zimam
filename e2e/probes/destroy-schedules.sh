#!/usr/bin/env bash
# Deleting a site leaves no scheduled job and no reusable uid. As zimam
# through the agent: provision the throwaway site sb, install a crontab as
# its user and, if `at` is installed, an at job; destroy it. Nothing under
# /var/spool/cron may belong to its old uid or name, and provisioning sb
# again must give a different uid. The second site is destroyed too, so
# fs-hash.sh's snapshots match afterwards (it covers /var/spool/cron).
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

site=sb
user=s_$site
body="{\"name\":\"$site\",\"domain\":\"$site.zimam.test\",\"type\":\"static\"}"

run_job site.provision "$body"
old=$(in_c id -u "$user")
in_c sh -c "echo '* * * * * true' | runuser -u $user -- crontab -"
in_c test -e "/var/spool/cron/crontabs/$user" || die "the crontab of $user wasn't installed"
pass "$user has a crontab, uid $old"
have_at=0
if in_c test -x /usr/bin/at; then
	in_c sh -c "echo true | runuser -u $user -- at now + 1 day 2>/dev/null"
	in_c sh -c "atq | grep -q '$user\$'" || die "the at job of $user wasn't queued"
	have_at=1
	pass "$user has an at job"
else
	echo "NOTE  at isn't installed: the at job is skipped"
fi

run_job site.destroy "{\"name\":\"$site\"}"

if in_c test -e "/var/spool/cron/crontabs/$user"; then
	fail "/var/spool/cron/crontabs/$user is still there"
else
	pass "the crontab of $user is gone"
fi
if left=$(in_c find /var/spool/cron -uid "$old" 2>/dev/null) && [[ -z $left ]]; then
	pass "nothing in /var/spool/cron belongs to uid $old"
else
	fail "entries of uid $old are left in /var/spool/cron:"
	printf '%s\n' "$left"
fi
if ((have_at)); then
	if in_c sh -c "atq | grep -q ."; then
		fail "an at job is left"
	else
		pass "no at job is left"
	fi
fi

run_job site.provision "$body"
new=$(in_c id -u "$user")
if [[ $new != "$old" ]]; then
	pass "$site got uid $new, not the old $old"
else
	fail "$site got its old uid $old again"
fi
if in_c test -e "/var/spool/cron/crontabs/$user"; then
	fail "the new $user has the old crontab"
else
	pass "the new $user starts with no crontab"
fi
run_job site.destroy "{\"name\":\"$site\"}"
exit "$failed"
