#!/usr/bin/env bash
# Deleting a site leaves no scheduled job and no reusable uid. As zimam
# through the agent: provision the throwaway site sb, which may not use
# crontab or at (cron.allow, at.allow), install a crontab as its user and,
# if `at` is installed, an at job, as before v0.4.0; destroy it. Nothing under
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
# Since v0.4.0 /etc/cron.allow and /etc/at.allow name only root (OQ1): the
# site user can't schedule anything itself.
if out=$(in_c sh -c "echo '* * * * * true' | runuser -u $user -- crontab - 2>&1"); then
	fail "$user may install a crontab: $out"
else
	pass "$user may not use crontab: $out"
fi
if in_c test -x /usr/bin/at; then
	if out=$(in_c sh -c "echo true | runuser -u $user -- at now + 1 day 2>&1"); then
		fail "$user may queue an at job: $out"
	else
		pass "$user may not use at: $out"
	fi
fi
# A crontab and an at job from before v0.4.0, when site users could make
# them: the user is let in for a moment, and both allow files are put back
# byte for byte.
in_c cp -p /etc/cron.allow /etc/at.allow /root/
in_c sh -c "echo $user >>/etc/cron.allow; echo $user >>/etc/at.allow"
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
in_c cp -p /root/cron.allow /root/at.allow /etc/
in_c rm /root/cron.allow /root/at.allow

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
