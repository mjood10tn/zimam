#!/usr/bin/env bats
# Step 5 of design §4: the agent's socket and service, byte for byte from
# §5.1 (the fixtures).

load helpers

setup() {
	setup_installer
	stub_log systemctl
}

@test "the socket unit is §5.1's" {
	installer 'socket_unit >"$T/socket"'
	[ "$status" -eq 0 ]
	cmp "$T/socket" "$FIXTURES/zimam-agent.socket"
}

@test "the service unit is §5.1's" {
	installer 'service_unit >"$T/service"'
	[ "$status" -eq 0 ]
	cmp "$T/service" "$FIXTURES/zimam-agent.service"
}

@test "write_units installs both, then enables and starts the socket" {
	installer write_units
	[ "$status" -eq 0 ]
	cmp "$T/units/zimam-agent.socket" "$FIXTURES/zimam-agent.socket"
	cmp "$T/units/zimam-agent.service" "$FIXTURES/zimam-agent.service"
	[ "$(stat -c %a "$T/units/zimam-agent.socket")" = 644 ]
	[ "$(stat -c %a "$T/units/zimam-agent.service")" = 644 ]
	[ "$(cat "$T/systemctl.log")" = "$(printf 'daemon-reload\nenable --now zimam-agent.socket\ntry-restart zimam-agent.service')" ]
}

@test "a re-run leaves the units alone" {
	installer write_units
	touch -d '2001-01-01' "$T/units/zimam-agent.socket"
	installer write_units
	[ "$status" -eq 0 ]
	[ "$(stat -c %Y "$T/units/zimam-agent.socket")" = "$(date -d 2001-01-01 +%s)" ]
}

@test "systemd-analyze verify accepts them" {
	command -v systemd-analyze >/dev/null || skip "no systemd-analyze"
	if [[ ! -x /usr/local/sbin/zimam-agent ]]; then
		# The test image has no agent; ExecStart needs a program there.
		[[ -w /usr/local/sbin ]] || skip "/usr/local/sbin/zimam-agent is missing and can't be made"
		printf '#!/bin/sh\nexit 0\n' >/usr/local/sbin/zimam-agent
		chmod 0755 /usr/local/sbin/zimam-agent
	fi
	installer write_units
	run systemd-analyze verify "$T/units/zimam-agent.socket" "$T/units/zimam-agent.service"
	echo "$output"
	[ "$status" -eq 0 ]
	[ -z "$output" ]
}
