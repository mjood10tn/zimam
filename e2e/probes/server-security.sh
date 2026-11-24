#!/usr/bin/env bash
# v0.6.0's guarded changes (R06-D1): change.confirm and change.revert of a
# change that doesn't exist answer not_found and are audited, and leave
# nothing behind. Where UFW is active (the VM and VPS runs), an unconfirmed
# change reverts even across an agent restart.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

made_up=01JD5Z3W4X6Y7Z8A9B0C1D2E3F
for command in change.confirm change.revert; do
	probe "$command of a made-up change" not_found "{$env_ok,\"command\":\"$command\",\"params\":{\"change\":\"$made_up\"}}"
done
if [[ -z $(in_c ls /var/lib/zimam/changes 2>/dev/null) ]]; then
	pass "no change record left"
else
	fail "change records left: $(in_c ls /var/lib/zimam/changes)"
fi

if ! in_c ufw status | grep -q '^Status: active'; then
	echo "SKIP  an unconfirmed change across an agent restart: UFW isn't active here (--skip-firewall)"
	exit "$failed"
fi
before=$(in_c cat /etc/ufw/user.rules)
id=$(submit firewall.rules '{"rules":[{"id":"vprobe","action":"allow","proto":"tcp","port_from":9191,"port_to":9191,"from":[]}]}')
[[ $(job_end "$id" 120) == succeeded* ]] || fail "firewall.rules didn't apply"
in_c systemctl restart zimam-agent.service
end=$((SECONDS + 180))
until [[ $(in_c cat /etc/ufw/user.rules) == "$before" ]]; do
	((SECONDS < end)) || {
		fail "the unconfirmed change outlived an agent restart"
		exit "$failed"
	}
	sleep 5
done
pass "an unconfirmed change reverts across an agent restart"
exit "$failed"
