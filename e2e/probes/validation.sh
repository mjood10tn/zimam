#!/usr/bin/env bash
# Validation probes (design §14.4), sent as zimam for milestone 2's
# commands: bad site names, domains and PHP versions each answer
# validation_failed before anything runs, and leave exactly one
# error:validation_failed audit entry. run.sh runs them between its two
# file snapshots.
# shellcheck source=lib.sh
source "$(dirname "$0")/lib.sh"

# send <name> <command> <params> probes one request.
send() {
	probe "$1" validation_failed "{$env_ok,\"command\":\"$2\",\"params\":$3}"
}

# Site names: dots, slashes, .., uppercase, Unicode, 25 characters, and
# the reserved names.
long=$(printf 'a%.0s' {1..25})
for name in shop.example s/hop .. ../etc Shop 'café' "$long" zimam panel default acme; do
	send "site.provision with the name $name" site.provision "{\"name\":\"$name\",\"domain\":\"vprobe.zimam.test\",\"type\":\"static\"}"
done
# Domains: a space, ;, {, a newline, $, quotes and a wildcard, each as it
# appears in the JSON, so the newline and the double quote are escaped.
# shellcheck disable=SC2016 # the $ is the probe
for domain in 'a b.zimam.test' 'a;b.zimam.test' 'a{b.zimam.test' 'a\nb.zimam.test' 'a$b.zimam.test' 'a\"b.zimam.test' "a'b.zimam.test" '*.zimam.test'; do
	send "site.provision with the domain $domain" site.provision "{\"name\":\"vprobe\",\"domain\":\"$domain\",\"type\":\"static\"}"
done
# PHP versions: a command after the version, a path, and one before the
# allowlist.
for v in '8.5; rm -rf /' '../8.4' '8.1'; do
	send "php.install of $v" php.install "{\"version\":\"$v\"}"
	send "site.provision on PHP $v" site.provision "{\"name\":\"vprobe\",\"domain\":\"vprobe.zimam.test\",\"type\":\"php\",\"php_version\":\"$v\"}"
	send "site.php to $v" site.php "{\"name\":\"php84\",\"php_version\":\"$v\"}"
done
# The other site commands take a name only.
for command in site.destroy cert.issue; do
	for name in ../php84 'php84/../..' PHP84; do
		send "$command of $name" "$command" "{\"name\":\"$name\"}"
	done
done
exit "$failed"
