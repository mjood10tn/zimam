#!/usr/bin/env bash
# Validation probes (design §14.4), sent as zimam for milestone 2's
# commands (bad site names, domains and PHP versions), milestone 3's
# site.security, and every field of v0.2.0's six commands: each answers
# validation_failed before anything runs, and leaves exactly one
# error:validation_failed audit entry. run-all.sh runs them between its two
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

# v0.2.0's six commands, every field. A JSON type the field can't take
# fails the decode, which names params rather than the field.
# json_str prints its argument as a JSON string.
json_str() {
	python3 -I -c 'import json, sys; print(json.dumps(sys.argv[1]))' "$1"
}
repo() {
	echo "{\"name\":\"$1\",\"repository\":$(json_str "$2"),\"branch\":$(json_str "$3")$4}"
}
# site.repo: names, repositories (owner/name, no .git, no path, no space),
# branches (Git's ref rules) and rotate_key.
for name in ../laravel Laravel 'laravel/..'; do
	send_field "site.repo of $name" site.repo "$(repo "$name" zimam-e2e/laravel-app main '')" name
done
# shellcheck disable=SC2016 # $(id) is the probe
for r in octo/app.git -octo/app octo/.. octo/. 'octo/a b' octo '' ../etc/passwd octo/app/x 'octo/$(id)' "$(printf 'o%.0s' {1..40})/app"; do
	send_field "site.repo of the repository '$r'" site.repo "$(repo laravel "$r" main '')" repository
done
for b in -rf a..b refs/heads/../x .hidden/x x.lock a//b 'a@{1}' x/ x. '' 'a b' 'a;id' 'a~1' 'a^' 'a:b' '/main' "$(printf 'b%.0s' {1..201})"; do
	send_field "site.repo of the branch '$b'" site.repo "$(repo laravel zimam-e2e/laravel-app "$b" '')" branch
done
for v in '"yes"' 1 '""'; do
	send_field "site.repo with rotate_key $v" site.repo "$(repo laravel zimam-e2e/laravel-app main ",\"rotate_key\":$v")" params
done
# site.deploy: name, commit (40 lowercase hex), script (UTF-8, no NUL, at
# most 64 KiB), composer, trigger and keep_releases (1 to 20).
deploy() {
	echo "{\"name\":\"laravel\",\"composer\":true,\"trigger\":\"manual\"$1}"
}
send_field "site.deploy of ../laravel" site.deploy '{"name":"../laravel","composer":true,"trigger":"manual"}' name
for c in "$(printf 'A%.0s' {1..40})" "$(printf 'a%.0s' {1..39})" "$(printf 'a%.0s' {1..41})" HEAD main "$(printf 'g%.0s' {1..40})" ''; do
	send_field "site.deploy of the commit '$c'" site.deploy "$(deploy ",\"commit\":$(json_str "$c")")" commit
done
send_field "site.deploy with a NUL in its script" site.deploy "$(deploy ',"script":"php artisan migrate\u0000 --force"')" script
send_field "site.deploy with a script of 64 KiB + 1" site.deploy "$(deploy ",\"script\":\"$(printf 'x%.0s' $(seq 65537))\"")" script
for v in '"yes"' 1 '""'; do
	send_field "site.deploy with composer $v" site.deploy "{\"name\":\"laravel\",\"composer\":$v,\"trigger\":\"manual\"}" params
done
for t in push MANUAL '' 'manual ' webhooks; do
	send_field "site.deploy with the trigger '$t'" site.deploy "{\"name\":\"laravel\",\"composer\":true,\"trigger\":$(json_str "$t")}" trigger
done
for k in 0 21 -1 100; do
	send_field "site.deploy keeping $k releases" site.deploy "$(deploy ",\"keep_releases\":$k")" keep_releases
done
send_field "site.deploy keeping \"5\" releases" site.deploy "$(deploy ',"keep_releases":"5"')" params
# site.rollback: name and release (<14 digits>-<7 lowercase hex>).
send_field "site.rollback of ../laravel" site.rollback '{"name":"../laravel","release":"20261116000000-abcdef0"}' name
for r in ../initial initial / 20261116000000-ABCDEF0 20261116-abcdef0 '20261116000000-abcdef0/..' 20261116000000-abcdef01 ''; do
	send_field "site.rollback to the release '$r'" site.rollback "{\"name\":\"laravel\",\"release\":$(json_str "$r")}" release
done
# site.releases and site.env.read take a name only.
for command in site.releases site.env.read; do
	for name in ../laravel Laravel 'laravel/../..' ''; do
		send_field "$command of '$name'" "$command" "{\"name\":$(json_str "$name")}" name
	done
done
# site.env.write: name, and content (UTF-8, no NUL, at most 64 KiB).
send_field "site.env.write of ../laravel" site.env.write '{"name":"../laravel","content":"A=1\n"}' name
send_field "site.env.write with a NUL" site.env.write '{"name":"laravel","content":"APP_KEY=x\u0000\n"}' content
send_field "site.env.write of 64 KiB + 1" site.env.write "{\"name\":\"laravel\",\"content\":\"$(printf 'x%.0s' $(seq 65537))\"}" content
send_field "site.env.write with content 1" site.env.write '{"name":"laravel","content":1}' params
exit "$failed"
