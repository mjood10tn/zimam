#!/usr/bin/env bash
# v0.6.0's server security, through the socket as zimam (probeclient). In
# order:
#   1. automatic security updates off: the automatic_security_updates check
#      warns; on again: it passes; the drop-ins apt-config reads say so;
#   2. Install updates now (security): the result names what was upgraded,
#      and security_pending_after is a count;
#   3. a reboot scheduled 30 minutes ahead shows in system.status, and
#      system.reboot.cancel removes it;
#   4. fail2ban: every jail on; five failed panel sign-ins from a TEST-NET
#      address (journal lines as the panel writes them) ban it in the
#      zimam-panel jail; fail2ban.unban lifts the ban;
#   5. Cloudflare mode: zimam-realip.conf passes nginx -t; a request with a
#      forged CF-Connecting-IP from an address outside Cloudflare's ranges
#      is logged with its real peer; off removes the file;
#   6. only where UFW is active (the VM and the VPS run): a firewall rule
#      for 8181 is kept and a listener there answers; one not kept is gone
#      after 120 s; SSH moves to 2223 alongside 22 and is kept after a login
#      on 2223; change.confirm of a made-up id is not_found.
# Containers run with --skip-firewall, so part 6 says SKIP there.
# shellcheck source=../probes/lib.sh
source "$(dirname "$0")/../probes/lib.sh"
load_state

# grep reads all its input here (no -q): under pipefail, an early exit would
# kill the command before it with SIGPIPE and fail the check.
py() { python3 -I -c "$1" "${@:2}"; }

data() {
	local reply
	reply=$(request "$1" "$2")
	py 'import json, sys; r = json.load(sys.stdin); assert r["type"] == "result", r; print(json.dumps(r["data"]))' <<<"$reply" ||
		die "FAIL $1 answered: ${reply:0:300}"
}

# job <command> <params> [seconds] runs a job, dies unless it succeeds, and prints its result.
job() {
	local id status
	id=$(submit "$1" "$2") || die "FAIL $1 wasn't accepted"
	status=$(job_end "$id" "${3:-600}")
	[[ $status == succeeded* ]] || die "FAIL $1 ended $status"
	request job.status "{\"job\":\"$id\"}" | py 'import json, sys; print(json.dumps(json.load(sys.stdin)["data"]["result"]))'
}

# check_status <check> prints the server check's status.
check_status() {
	job security.check '{"scope":"server"}' |
		py 'import json, sys; print(next(f["status"] for f in json.load(sys.stdin)["findings"] if f["check"] == sys.argv[1]))' "$1"
}

# 1. Automatic security updates.
job updates.auto '{"enabled":false,"auto_reboot":false}' >/dev/null
[[ $(check_status automatic_security_updates) == warn ]] || die "FAIL automatic_security_updates doesn't warn with updates off"
pass "automatic_security_updates warns with updates off"
job updates.auto '{"enabled":true,"auto_reboot":true,"reboot_time":"04:30"}' >/dev/null
in_c apt-config dump | grep -xe 'Unattended-Upgrade::Automatic-Reboot-Time "04:30";' >/dev/null || die "FAIL apt-config doesn't read the reboot time"
job updates.auto '{"enabled":true,"auto_reboot":false}' >/dev/null
[[ $(check_status automatic_security_updates) == pass ]] || die "FAIL automatic_security_updates doesn't pass with updates on"
in_c apt-config dump | grep -xe 'APT::Periodic::Unattended-Upgrade "1";' >/dev/null || die "FAIL apt-config doesn't read updates as on"
pass "updates.auto turns automatic updates off and on, as apt-config reads them"

# 2. Install updates now.
result=$(job updates.install '{"scope":"security"}' 3600)
py 'import json, sys; r = json.loads(sys.argv[1]); assert r["scope"] == "security" and isinstance(r["upgraded"], list) and isinstance(r["security_pending_after"], int), r' "$result" ||
	die "FAIL updates.install answered $result"
pass "updates.install (security) upgraded $(py 'import json, sys; print(len(json.loads(sys.argv[1])["upgraded"]))' "$result") packages; security updates left: $(py 'import json, sys; print(json.loads(sys.argv[1])["security_pending_after"])' "$result")"

# 3. A scheduled reboot, seen and cancelled.
at=$(in_c date -d '+30 minutes' +%H:%M)
job system.reboot "{\"at\":\"$at\"}" >/dev/null
data system.status '{}' | py 'import json, sys; assert json.load(sys.stdin).get("reboot_scheduled")' || die "FAIL system.status shows no scheduled reboot"
job system.reboot.cancel '{}' | py 'import json, sys; assert json.load(sys.stdin)["cancelled"]' || die "FAIL system.reboot.cancel cancelled nothing"
data system.status '{}' | py 'import json, sys; assert "reboot_scheduled" not in json.load(sys.stdin)' || die "FAIL the reboot is still scheduled"
pass "a reboot at $at shows in system.status and is cancelled"

# 4. fail2ban.
jails='[{"name":"sshd","enabled":true,"max_retry":5,"find_time":600,"ban_time":3600},{"name":"zimam-panel","enabled":true,"max_retry":5,"find_time":600,"ban_time":3600},{"name":"zimam-site-auth","enabled":true,"max_retry":10,"find_time":600,"ban_time":600}]'
job fail2ban.config "{\"jails\":$jails,\"ignore\":[]}" 900 >/dev/null
banned=192.0.2.77
for _ in 1 2 3 4 5 6; do
	in_c logger -t zimam-panel-auth "auth failure from $banned kind=password"
done
bans() { data fail2ban.status '{}' | py 'import json, sys; print(" ".join(b["address"] for j in json.load(sys.stdin)["jails"] if j["name"] == "zimam-panel" for b in j["banned"]))'; }
end=$((SECONDS + 60))
until [[ " $(bans) " == *" $banned "* ]]; do
	((SECONDS < end)) || die "FAIL $banned isn't banned after six failed sign-ins: $(bans)"
	sleep 3
done
pass "six failed panel sign-ins ban $banned in zimam-panel"
job fail2ban.unban "{\"jail\":\"zimam-panel\",\"address\":\"$banned\"}" | py 'import json, sys; assert json.load(sys.stdin)["unbanned"]' || die "FAIL fail2ban.unban unbanned nothing"
[[ " $(bans) " != *" $banned "* ]] || die "FAIL $banned is still banned"
pass "fail2ban.unban lifts the ban"

# 5. Cloudflare mode.
job cloudflare.mode '{"enabled":true,"restrict_origin":false}' >/dev/null
in_c test -f /etc/nginx/conf.d/zimam-realip.conf || die "FAIL no zimam-realip.conf"
in_c nginx -t 2>/dev/null || die "FAIL nginx -t fails in Cloudflare mode"
log=/var/log/zimam/sites/php84/access.log
mark=v06-$RANDOM$RANDOM
in_c curl -s -o /dev/null -H "Host: php84.$E2E_DOMAIN" -H 'CF-Connecting-IP: 9.9.9.9' "http://127.0.0.1/$mark" || true
sleep 1
line=$(in_c grep "$mark" "$log" | tail -1)
[[ $line == 127.0.0.1* ]] || die "FAIL a forged CF-Connecting-IP from outside Cloudflare was logged as: $line"
pass "a forged CF-Connecting-IP from outside Cloudflare's ranges keeps the real peer"
job cloudflare.mode '{"enabled":false,"restrict_origin":false}' >/dev/null
! in_c test -e /etc/nginx/conf.d/zimam-realip.conf || die "FAIL zimam-realip.conf is still there"
pass "Cloudflare mode off removes zimam-realip.conf"

# 6. Guarded firewall and SSH changes, where UFW is active.
if ! in_c ufw status | grep -e '^Status: active' >/dev/null; then
	echo "SKIP  guarded firewall and SSH changes: UFW isn't active here (--skip-firewall)"
	exit 0
fi
reply=$(request change.confirm '{"change":"01JD5Z3W4X6Y7Z8A9B0C1D2E3F"}')
[[ $(field type <<<"$reply") == error && $reply == *not_found* ]] || die "FAIL change.confirm of a made-up id answered $reply"
pass "change.confirm of a made-up id is not_found"

rules() { echo "{\"rules\":[$1]}"; }
# Not 8080: on a VPS the release is served on 127.0.0.1:8080.
web='{"id":"web8181","action":"allow","proto":"tcp","port_from":8181,"port_to":8181,"from":[]}'
change=$(job firewall.rules "$(rules "$web")" | py 'import json, sys; print(json.load(sys.stdin)["change"])')
data change.confirm "{\"change\":\"$change\"}" >/dev/null
in_c sh -c 'setsid -f python3 -m http.server 8181 --bind 0.0.0.0 >/dev/null 2>&1'
sleep 2
curl -s -o /dev/null --max-time 5 "http://$CIP:8181/" || die "FAIL 8181 doesn't answer after the rule was kept"
pass "a kept rule for 8181 lets a listener there answer"
in_c pkill -f 'http.server 8181' || true

temp='{"id":"temp9090","action":"allow","proto":"tcp","port_from":9090,"port_to":9090,"from":[]}'
job firewall.rules "$(rules "$web,$temp")" >/dev/null
in_c ufw status | grep -e 9090 >/dev/null || die "FAIL the unconfirmed rule isn't in force"
sleep 130
! in_c ufw status | grep -e 9090 >/dev/null || die "FAIL the unconfirmed rule is still there after 130 s"
in_c ufw status | grep -e 8181 >/dev/null || die "FAIL the revert lost the kept rule"
pass "a rule nobody kept is gone after 120 s; the kept one stays"

# Not 2222: on a VPS the Git host's tunnel holds 127.0.0.1:2222.
ssh='{"password_auth":false,"root_login":"prohibit-password","ports":[22,2223],"max_auth_tries":6,"login_grace_seconds":120}'
change=$(job ssh.settings "$ssh" | py 'import json, sys; print(json.load(sys.stdin)["change"])')
# On a VPS the server takes only the harness's key (lxc-ssh's E2E_SSH_KEY).
key=()
[[ -z ${E2E_SSH_KEY:-} ]] || key=(-i "$E2E_SSH_KEY" -o IdentitiesOnly=yes)
ssh -p 2223 "${key[@]}" -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 "root@$CIP" true || die "FAIL no SSH login on 2223"
data change.confirm "{\"change\":\"$change\"}" >/dev/null
# sshd -T refuses to run without /run/sshd, which goes when ssh.service stops.
in_c sh -c 'mkdir -p /run/sshd && sshd -T' | grep -xe 'port 2223' >/dev/null || die "FAIL sshd doesn't listen on 2223 after keeping the change"
in_c sh -c 'mkdir -p /run/sshd && sshd -T' | grep -xe 'passwordauthentication no' >/dev/null || die "FAIL password login is still on"
pass "SSH moved to 22 and 2223, password login off, kept after a login on 2223"
# The owner's rules go again, kept: later scenarios expect only the locked ones.
change=$(job firewall.rules "$(rules '')" | py 'import json, sys; print(json.load(sys.stdin)["change"])')
data change.confirm "{\"change\":\"$change\"}" >/dev/null
