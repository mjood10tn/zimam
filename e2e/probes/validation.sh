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

# send_field <name> <command> <params> <field> also wants the reply to name
# the field.
send_field() {
	probe "$1" validation_failed "{$env_ok,\"command\":\"$2\",\"params\":$3}" "$4"
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
# site.security (milestone 3): php84's valid settings with one bad entry.
# IP lists: a prefix past 32 or 128, host bits set, a bad address, a
# hostname, an address with a port, and a command after the address.
security() {
	local allow=$1 deny=$2 users=$3
	echo "{\"name\":\"php84\",\"force_https\":true,\"hsts\":{\"max_age\":null,\"include_subdomains\":false},\"headers_preset\":false,\"block_risky_functions\":false,\"restrict_to_site_folder\":false,\"ip_allow\":$allow,\"ip_deny\":$deny,\"password_protection\":$users}"
}
off='{"enabled":false,"users":[]}'
for cidr in 10.0.0.0/33 '2001:db8::/129' 10.0.0.1/8 300.1.2.3 10.0.0 example.com 10.0.0.1:80 '10.0.0.1; deny all' '10.0.0.1/-1' '10.0.0.1/ 8'; do
	send_field "site.security allowing $cidr" site.security "$(security "[\"$cidr\"]" '[]' "$off")" ip_allow
	send_field "site.security denying $cidr" site.security "$(security '[]' "[\"$cidr\"]" "$off")" ip_deny
done
# Password hashes: plain text, MD5, bcrypt's $2a$ and $2b$ prefixes, a cost
# outside 10–14, and a bcrypt hash one character short.
salt=abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0
# shellcheck disable=SC2016 # the $s are the hashes' own
for hash in hunter2 5f4dcc3b5aa765d61d8327deb882cf99 "\$2a\$12\$$salt" "\$2b\$12\$$salt" "\$2y\$04\$$salt" "\$2y\$15\$$salt" "\$2y\$12\$${salt:1}"; do
	send_field "site.security with the hash $hash" site.security \
		"$(security '[]' '[]' "{\"enabled\":true,\"users\":[{\"username\":\"e2e\",\"password_hash\":\"$hash\"}]}")" password_protection
done
# Usernames: a colon, which would split the htpasswd line, and a newline
# (escaped in the JSON), which would add one; the hash is valid.
for name in 'a:b' 'a\nb' 'a\rb' ''; do
	send_field "site.security with the username '$name'" site.security \
		"$(security '[]' '[]' "{\"enabled\":true,\"users\":[{\"username\":\"$name\",\"password_hash\":\"\$2y\$12\$$salt\"}]}")" password_protection
done
exit "$failed"
