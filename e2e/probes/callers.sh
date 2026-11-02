#!/usr/bin/env bash
# Caller probes (design §14.4): nobody and a site user, s_probe, connect to
# the agent. The socket's mode (0660 root:zimam-agent) stops them first, so
# each also gets the zimam-agent group, to reach the agent's own check
# (SO_PEERCRED): that must send no reply and leave exactly one
# error:forbidden entry with the caller's uid. run.sh creates s_probe.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

req="{\"v\":1,\"id\":\"$ULID\",\"command\":\"agent.version\",\"actor\":\"probe\",\"params\":{}}"
for user in nobody s_probe; do
	uid=$(in_c id -u "$user")
	gid=$(in_c id -g "$user")

	before=$(audit_lines)
	if out=$(printf '%s\n' "$req" | in_c setpriv --reuid="$uid" --regid="$gid" --clear-groups "$PROBECLIENT" "$SOCKET" 2>&1); then
		fail "$user connected without the zimam-agent group: $out"
	elif [[ $out == *"permission denied"* ]]; then
		pass "$user is kept off the socket by its mode"
	else
		fail "$user without the group: $out"
	fi
	[[ $(audit_lines) == "$before" ]] || fail "$user's refused connect left an audit entry"

	before=$(audit_lines)
	out=$(printf '%s\n' "$req" | in_c setpriv --reuid="$uid" --regid="$gid" --groups=zimam-agent "$PROBECLIENT" "$SOCKET" 2>&1) ||
		fail "$user with the group: probeclient failed: $out"
	if [[ -z $out ]]; then
		pass "$user with the group gets no reply"
	else
		fail "$user with the group got a reply: $out"
	fi
	entries=$(audit_since "$before")
	if [[ $(wc -l <<<"$entries") == 1 && $(field outcome <<<"$entries") == error:forbidden && $(field peer_uid <<<"$entries") == "$uid" ]]; then
		pass "$user's attempt left one error:forbidden entry with uid $uid"
	else
		fail "$user's attempt left: $entries"
	fi
done
exit "$failed"
