#!/usr/bin/env bash
# The worker and cron probes (v0.4.0, R04-D1, R04-D2, R04-D5), as zimam
# through the agent, on the throwaway PHP site probe-wk, which is destroyed
# at the end; run-all.sh runs them between its two file snapshots. The
# malformed commands and schedules are validation.sh's. Here:
#   - inside a worker, other homes (/home/s_php84, /home/zimam) are invisible
#     and /root is out of reach, /etc can't be written, NoNewPrivileges is set and sudo, a
#     setuid program, gives no root;
#   - a planted .zimam/workers/<id>.sh -> a root file doesn't make root read
#     it: the restarted unit fails as the site user without its content,
#     and the next site.workers replaces the link with the user's own
#     script, the root file untouched;
#   - the cron file holds only lines whose user is the site's, running the
#     site's own scripts, whatever the commands say; so does every other
#     zimam cron file.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"
load_state

site=probe-wk
user=s_$site
home=/home/$user
unit=zimam-worker-${site}_w@1.service
secret=/root/zimam-probe-worker
marker=ZQ_ROOT_ONLY_$RANDOM$RANDOM

json() {
	python3 -I -c 'import json, sys; print(json.dumps(sys.argv[1]))' "$1"
}

# until_true <seconds> <command...> retries the command every 2 seconds.
until_true() {
	local end=$((SECONDS + $1))
	shift
	until "$@"; do
		((SECONDS < end)) || return 1
		sleep 2
	done
}

run_job site.provision "{\"name\":\"$site\",\"domain\":\"$site.$E2E_DOMAIN\",\"type\":\"php\",\"php_version\":\"8.4\"}"

# A custom worker that looks around, writes what it saw to ~/probe.out,
# and stays up. Its script runs under set -e, so each attempt that must
# fail is followed by || to go on.
# shellcheck disable=SC2016 # the worker's command, run by the site user's bash
look='{ echo "home: $(ls -A /home | paste -sd " ")"; ls -d /home/s_php84 /home/zimam /root/.ssh || true; echo x >/etc/zimam-probe-worker || echo "etc: refused"; grep NoNewPrivs /proc/self/status; sudo -n id -u || echo "sudo: $?"; su -c "id -u" root </dev/null || echo "su: $?"; echo "uid: $(id -u) $(id -un)"; } >"$HOME/probe.out" 2>&1; exec sleep infinity'
workers="{\"name\":\"$site\",\"workers\":[{\"id\":\"w\",\"kind\":\"custom\",\"processes\":1,\"command\":$(json "$look")}]}"
run_job site.workers "$workers"
until_true 60 in_c grep -q '^uid: ' "$home/probe.out" || die "the probe worker wrote nothing: $(in_c systemctl status --no-pager "$unit" 2>&1 | tail -n 20)"
out=$(in_c cat "$home/probe.out")
in_c cat "$home/probe.out" | sed 's/^/      /'
if grep -qx "home: $user" <<<"$out"; then
	pass "inside the worker, /home holds only $user"
else
	fail "inside the worker, /home holds: $(grep '^home:' <<<"$out")"
fi
for p in /home/s_php84 /home/zimam; do
	if grep -q "^ls: cannot access '$p': No such file or directory" <<<"$out"; then
		pass "inside the worker, $p doesn't exist"
	else
		fail "inside the worker, $p is visible"
	fi
done
# ProtectHome=tmpfs puts an empty /root there, root's and 0700.
if grep -Eq "^ls: cannot (access|open file) '/root/.ssh': (No such file or directory|Permission denied)" <<<"$out"; then
	pass "inside the worker, /root/.ssh can't be reached: $(grep -m 1 "/root/.ssh" <<<"$out")"
else
	fail "inside the worker, /root/.ssh is reachable"
fi
if grep -q 'Read-only file system' <<<"$out" && ! in_c test -e /etc/zimam-probe-worker; then
	pass "inside the worker, writing /etc fails: Read-only file system"
else
	fail "inside the worker, writing /etc didn't fail as read-only"
	in_c rm -f /etc/zimam-probe-worker
fi
if grep -Eq '^NoNewPrivs:[[:space:]]+1$' <<<"$out"; then
	pass "the worker runs with NoNewPrivs 1"
else
	fail "NoNewPrivs isn't set in the worker"
fi
if grep -qx 'sudo: 0' <<<"$out" || grep -qx 'su: 0' <<<"$out" || grep -qx 0 <<<"$out"; then
	fail "sudo or su gave root inside the worker"
else
	pass "sudo and su give no root inside the worker: $(grep -E '^(sudo|su)' <<<"$out" | paste -sd ' ')"
fi
if grep -qx "uid: $(in_c id -u "$user") $user" <<<"$out"; then
	pass "the worker runs as $user"
else
	fail "the worker runs as $(grep '^uid:' <<<"$out")"
fi

# A planted script link to a root file, then a restart: the unit runs the
# link as the site user, who can't read the file (ProtectHome hides /root).
in_c sh -c "umask 077; printf '#!/bin/bash\necho %s\n' $marker >$secret"
secret_hash=$(in_c sha256sum "$secret")
in_c runuser -u "$user" -- sh -c "rm $home/.zimam/workers/w.sh && ln -s $secret $home/.zimam/workers/w.sh"
since=$(in_c date '+%Y-%m-%d %H:%M:%S')
run_job site.workers.restart "{\"name\":\"$site\",\"worker\":\"w\"}"
# shellcheck disable=SC2329 # called through until_true
failed_run() {
	in_c journalctl -u "$unit" --since "$since" --no-pager -o cat | grep -Eq 'No such file or directory|Permission denied'
}
if until_true 60 failed_run; then
	pass "the restarted worker fails as $user on the planted link: $(in_c journalctl -u "$unit" --since "$since" --no-pager -o cat | grep -Em 1 'No such file|Permission denied')"
else
	fail "the restarted worker didn't fail on the planted link: $(in_c journalctl -u "$unit" --since "$since" --no-pager -o cat | tail -n 5)"
fi
if in_c journalctl --since "$since" --no-pager -o cat | grep -q "$marker"; then
	fail "the root file's content reached the journal"
else
	pass "the root file's content is nowhere in the journal"
fi
run_job site.workers "$workers"
mode=$(in_c stat -c '%F %U %a' "$home/.zimam/workers/w.sh")
if [[ $mode == "regular file $user 700" && $(in_c sha256sum "$secret") == "$secret_hash" ]]; then
	pass "site.workers replaced the planted link with $user's own 0700 script; $secret is unchanged"
else
	fail "after site.workers, w.sh is $mode and $secret: $(in_c sha256sum "$secret")"
fi
in_c rm -f "$secret"

# The cron file: commands that look like cron lines, or name root, stay in
# the site's scripts; every line of every zimam cron file is its site user's.
# shellcheck disable=SC2016 # the commands are the site user's
cron="{\"name\":\"$site\",\"scheduler\":true,\"jobs\":[{\"id\":\"a\",\"schedule\":\"*/5 * * * *\",\"command\":$(json '0 * * * * root id >/etc/zimam-probe-cron'),\"enabled\":true},{\"id\":\"b\",\"schedule\":\"0 3 * * mon\",\"command\":$(json 'true; MAILTO=root; SHELL=/bin/sh #'),\"enabled\":true},{\"id\":\"c\",\"schedule\":\"@daily\",\"command\":$(json 'echo 100%'),\"enabled\":false}]}"
run_job site.cron "$cron"
file=/etc/cron.d/zimam-$site
in_c cat "$file" | sed 's/^/      /'
if in_c cat "$file" | awk -v u="$user" -v h="$home" '
	/^#/ || /^$/ { next }
	/^(SHELL|PATH|MAILTO)=/ { env++; next }
	{
		n = ($1 ~ /^@/) ? 2 : 6
		if ($n != u || $(n + 1) != "/bin/bash" || $(n + 2) !~ "^" h "/\\.zimam/cron/[a-z][a-z0-9-]*\\.sh$" || NF != n + 2) bad = 1
		jobs++
	}
	END { exit (bad || env != 3 || jobs != 3) }'; then
	pass "$file has the three env lines and three entries (the scheduler, a and b), each as $user running its own script"
else
	fail "$file has a line that isn't $user running its own script"
fi
if in_c grep -qF '0 * * * * root id >/etc/zimam-probe-cron' "$home/.zimam/cron/a.sh"; then
	pass "the command that looks like a root cron line is in $user's own script"
else
	fail "a.sh doesn't hold job a's command"
fi
if in_c test -e "$home/.zimam/cron/c.sh"; then
	fail "the disabled job c has a script"
else
	pass "the disabled job c has no script"
fi
for f in $(in_c sh -c 'ls /etc/cron.d/zimam-* 2>/dev/null'); do
	owner=s_${f#/etc/cron.d/zimam-}
	if in_c cat "$f" | awk -v u="$owner" '/^#/ || /^$/ || /^(SHELL|PATH|MAILTO)=/ { next } { n = ($1 ~ /^@/) ? 2 : 6; if ($n != u) bad = 1 } END { exit bad }'; then
		pass "$f names only $owner"
	else
		fail "$f names a user other than $owner"
	fi
done

run_job site.destroy "{\"name\":\"$site\"}"
left=$(in_c sh -c "ls -d /etc/systemd/system/zimam-worker-${site}_* /etc/systemd/system/zimam-site-$site.target $file $home 2>/dev/null" || true)
if [[ -z $left ]]; then pass "$site is destroyed with its unit, target, cron file and home"; else fail "$site left: $left"; fi
exit "$failed"
