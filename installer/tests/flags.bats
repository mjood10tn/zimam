#!/usr/bin/env bats
# The flags of design §4's usage table.

load helpers

setup() {
	setup_installer
}

@test "a missing --panel-domain prints usage" {
	installer 'parse_args "$@"' --email admin@example.com
	[ "$status" -eq 2 ]
	[[ $output == *"--panel-domain is required"* ]]
	[[ $output == *"Usage: "* ]]
}

@test "an unknown flag prints usage" {
	installer 'parse_args "$@"' --panel-domain panel.example.com --bogus
	[ "$status" -eq 2 ]
	[[ $output == *"unknown option --bogus"* ]]
	[[ $output == *"Usage: "* ]]
}

@test "a flag without its value prints usage" {
	installer 'parse_args "$@"' --panel-domain
	[ "$status" -eq 2 ]
	[[ $output == *"Usage: "* ]]
}

@test "every flag is parsed" {
	installer 'parse_args "$@"; printf "%s|" "$PANEL_DOMAIN" "$EMAIL" "$PHP" "$VERSION" "$ACME_SERVER" "$ACME_CA_BUNDLE" "$SKIP_FIREWALL" "$YES"' \
		--panel-domain panel.example.com --email admin@example.com --php 8.4 --version 1.2.3 \
		--acme-server https://pebble:14000/dir --acme-ca-bundle /root/pebble-ca.pem --skip-firewall --yes
	[ "$status" -eq 0 ]
	[ "$output" = "panel.example.com|admin@example.com|8.4|1.2.3|https://pebble:14000/dir|/root/pebble-ca.pem|1|1|" ]
}

@test "the defaults" {
	installer 'parse_args "$@"; printf "%s|" "$EMAIL" "$PHP" "$VERSION" "$ACME_SERVER" "$ACME_CA_BUNDLE" "$SKIP_FIREWALL" "$YES"' --panel-domain panel.example.com
	[ "$status" -eq 0 ]
	[ "$output" = "|||||0|0|" ]
}

@test "values that can't be right are refused" {
	for args in \
		"--panel-domain panel_example" \
		"--panel-domain -panel.example.com" \
		"--panel-domain panel.example.com --email nobody" \
		"--panel-domain panel.example.com --php 8" \
		"--panel-domain panel.example.com --version v1.2.3" \
		"--panel-domain panel.example.com --version 1.2" \
		"--panel-domain panel.example.com --acme-server http://pebble:14000/dir" \
		"--panel-domain panel.example.com --acme-ca-bundle pebble-ca.pem"; do
		# shellcheck disable=SC2086 # each case is a list of words
		installer 'parse_args "$@"' $args
		[ "$status" -eq 2 ] || {
			echo "accepted: $args"
			return 1
		}
	done
}

# The panel domain meets the agent's own rule (contract.Domain) before
# anything is installed, so bootstrap can't refuse it after packages and the
# firewall are set up (M1 review, M6).
@test "the panel domain meets the agent's rule" {
	label63=$(printf 'a%.0s' {1..63})
	label64=$(printf 'a%.0s' {1..64})
	long="$label63.$label63.$label63.$label63.com"
	for domain in 1.2.3.4 panel.example.123 panel..example.com panel.example.com. "$label64.example.com" "$long" \
		panel.-example.com panel.example-.com example; do
		installer 'parse_args "$@"' --panel-domain "$domain"
		[ "$status" -eq 2 ] || {
			echo "accepted: $domain"
			return 1
		}
	done
	for domain in panel.example.com p.example.co.uk "$label63.example.com" panel.xn--mgbab2bd 0panel.example.com; do
		installer 'parse_args "$@"' --panel-domain "$domain"
		[ "$status" -eq 0 ] || {
			echo "refused: $domain: $output $stderr"
			return 1
		}
	done
}

@test "a panel domain in capitals is lowercased" {
	installer 'parse_args "$@"; printf "%s" "$PANEL_DOMAIN"' --panel-domain Panel.Example.COM
	[ "$status" -eq 0 ]
	[ "$output" = panel.example.com ]
}

@test "bootstrap gets the flags and the verified release" {
	installer 'parse_args "$@"; RELEASE_VERSION=1.2.3; bootstrap_args; printf "%s\n" "${BOOTSTRAP_ARGS[@]}"' \
		--panel-domain panel.example.com --email admin@example.com --php 8.4 \
		--acme-server https://pebble:14000/dir --acme-ca-bundle /root/pebble-ca.pem --skip-firewall --yes
	[ "$status" -eq 0 ]
	[ "$output" = "$(printf '%s\n' --panel-domain panel.example.com --version 1.2.3 --yes --email admin@example.com --php 8.4 \
		--acme-server https://pebble:14000/dir --acme-ca-bundle /root/pebble-ca.pem)" ]
}
