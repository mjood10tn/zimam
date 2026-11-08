#!/usr/bin/env bash
# The site.destroy symlink probe (design §14.4), as zimam through the agent:
# provision the throwaway site probe-static, plant site/escape -> / as its
# user, then destroy it. Taken before the provision and after the destroy,
# the file snapshots must match (fs-hash.sh leaves out the account files'
# backups that useradd and userdel rewrite), and the home must be gone:
# the delete never left the home.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

site=probe-static
user=s_$site

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
if changed=$(diff "$E2E_WORK/fs-destroy-before.txt" "$E2E_WORK/fs-destroy-after.txt"); then
	pass "nothing outside /home/$user changed ($(wc -l <"$E2E_WORK/fs-destroy-before.txt") lines compared)"
else
	fail "site.destroy changed files outside the home:"
	printf '%s\n' "$changed"
fi
exit "$failed"
