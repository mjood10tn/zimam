#!/usr/bin/env bash
# The log symlink probe (design §11.2, §14.4): s_php84 replaces its PHP
# error log with a link to /etc/shadow, and plants an archive-named link
# too. Read as zimam through the agent, the PHP error log answers an error
# and never shadow's content, and the planted archive isn't listed. The
# links live in the home's logs/, which fs-hash.sh leaves out, so run-all.sh
# runs this between its two file snapshots; the real log is put back.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

logs=/home/s_php84/logs
as_site() { in_c runuser -u s_php84 -- "$@"; }
planted='php-error.log-20261101-1761955200.gz'

had_log=0
if in_c test -e "$logs/php-error.log"; then
	as_site mv "$logs/php-error.log" "$logs/php-error.log.e2e-saved"
	had_log=1
fi
# shellcheck disable=SC2329 # the EXIT trap calls it
restore() {
	as_site rm -f "$logs/php-error.log" "$logs/$planted"
	if ((had_log)); then
		as_site mv "$logs/php-error.log.e2e-saved" "$logs/php-error.log"
	fi
}
trap restore EXIT
as_site ln -s /etc/shadow "$logs/php-error.log"
as_site ln -s /etc/shadow "$logs/$planted"
[[ $(in_c readlink "$logs/php-error.log") == /etc/shadow ]] || die "the link wasn't planted"
pass "s_php84 planted logs/php-error.log and logs/$planted -> /etc/shadow"

# shadow's first line is root's.
shadow_first=$(in_c head -c 5 /etc/shadow)
[[ $shadow_first == root: ]] || die "/etc/shadow starts $shadow_first"

check() {
	local what=$1 reply
	reply=$(request logs.read "$2")
	if [[ $reply == *root:* ]]; then
		fail "$what shows shadow's content"
	elif [[ $(field type <<<"$reply") == error ]]; then
		pass "$what answers $(field code <<<"$reply"), and nothing of shadow"
	else
		fail "$what answers ${reply:0:300}"
	fi
}
check "logs.read of the linked PHP error log" '{"source":"site.php-error","name":"php84","direction":"back"}'
check "a search of the linked PHP error log" '{"source":"site.php-error","name":"php84","direction":"back","search":"root"}'
check "logs.read of the planted archive" "{\"source\":\"site.php-error\",\"name\":\"php84\",\"archive\":\"$planted\",\"direction\":\"back\"}"

reply=$(request logs.list '{"source":"site.php-error","name":"php84"}')
if [[ $reply == *"$planted"* || $reply == *root:* ]]; then
	fail "logs.list shows the planted link: ${reply:0:300}"
else
	pass "logs.list doesn't list the planted archive (${reply:0:200})"
fi
exit "$failed"
