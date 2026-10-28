#!/usr/bin/env bash
# Tests one golden case in a fresh zimam-configcheck container (design §14.1).
# It copies the case's tree into /, adds what the configs refer to and a
# server would already have (dummy certificates and keys, log directories,
# site roots, users, the agent binary), then runs each tool's own test on the
# files the case holds. It stops at the first failure.
# Usage: zimam-configcheck <case-dir>
set -euo pipefail
shopt -s nullglob

src="${1:?usage: zimam-configcheck <case-dir>}"
if [ ! -d "$src" ]; then
	echo "configcheck: $src is not a directory" >&2
	exit 2
fi

fail() {
	echo "configcheck: $*" >&2
	exit 1
}

# directive NAMES FILE...: the first argument of every directive matching
# NAMES (an extended regex) in the Nginx files, one per line.
directive() {
	local names=$1
	shift
	[ "$#" -gt 0 ] || return 0
	{ grep -hoE "^[[:space:]]*($names)[[:space:]]+[^;[:space:]]+" "$@" || true; } | awk '{ print $2 }'
}

# setting NAMES FILE...: the value of every "name = value" line matching NAMES
# in the PHP-FPM files, one per line.
setting() {
	local names=$1
	shift
	{ grep -hoE "^[[:space:]]*($names)[[:space:]]*=[[:space:]]*[^;[:space:]]+" "$@" || true; } | sed 's/.*=[[:space:]]*//'
}

# dummy_pair DIR: a throwaway key and self-signed certificate in DIR.
dummy_pair() {
	mkdir -p "$1"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 \
		-subj /CN=zimam-configcheck -keyout "$1/configcheck-key.pem" -out "$1/configcheck-cert.pem" 2>/dev/null
}

ensure_user() {
	id -u "$1" >/dev/null 2>&1 || useradd --system --user-group --no-create-home --shell /usr/sbin/nologin "$1"
}

ensure_group() {
	getent group "$1" >/dev/null || groupadd --system "$1"
}

# Files land owned by root with default modes, as configtx writes them;
# logrotate skips config files that aren't.
cp -R --no-preserve=all "$src/." /

if [ -d "$src/etc/nginx" ]; then
	mapfile -d '' nginx_files < <(find "$src/etc/nginx" -type f -print0)

	# Bootstrap removes the stock site (§4 step 5), whose default_server
	# would clash with Zimam's, and Zimam enables a site by linking it.
	rm -f /etc/nginx/sites-enabled/default
	for f in "$src"/etc/nginx/sites-available/*.conf; do
		ln -sfn "/etc/nginx/sites-available/$(basename "$f")" /etc/nginx/sites-enabled/
	done

	# A certificate and its key must match, so each directory holding either
	# gets one dummy pair.
	certs=$(directive ssl_certificate "${nginx_files[@]}")
	keys=$(directive ssl_certificate_key "${nginx_files[@]}")
	for dir in $(for p in $certs $keys; do dirname "$p"; done | sort -u); do
		dummy_pair "$dir"
	done
	for p in $certs; do cp "$(dirname "$p")/configcheck-cert.pem" "$p"; done
	for p in $keys; do cp "$(dirname "$p")/configcheck-key.pem" "$p"; done

	for p in $(directive 'access_log|error_log' "${nginx_files[@]}"); do
		case "$p" in off | stderr | syslog:*) continue ;; esac
		mkdir -p "$(dirname "$p")"
	done
	for p in $(directive root "${nginx_files[@]}"); do
		mkdir -p "$p"
	done

	# Nginx parses a shared snippet such as tls.conf only where a server
	# includes it, so a server that exists only here includes each one.
	snippets=("$src"/etc/nginx/zimam/*.conf)
	if [ "${#snippets[@]}" -gt 0 ]; then
		dummy_pair /etc/zimam-configcheck
		{
			echo 'server {'
			echo '    listen 127.0.0.1:8443 ssl;'
			echo '    server_name configcheck.invalid;'
			echo '    ssl_certificate /etc/zimam-configcheck/configcheck-cert.pem;'
			echo '    ssl_certificate_key /etc/zimam-configcheck/configcheck-key.pem;'
			for s in "${snippets[@]}"; do
				echo "    include /etc/nginx/zimam/$(basename "$s");"
			done
			echo '}'
		} >/etc/nginx/conf.d/zimam-configcheck.conf
	fi

	echo "== nginx -t"
	nginx -t || fail "nginx -t failed"
fi

if [ -d "$src/etc/apt" ]; then
	# apt-cache policy reads every source and pin; it fails on a malformed
	# entry, but not on a Signed-By keyring that doesn't exist.
	for f in "$src"/etc/apt/sources.list.d/*.sources; do
		for keyring in $(sed -n 's/^Signed-By:[[:space:]]*//p' "$f"); do
			[ -f "$keyring" ] || fail "$(basename "$f") names a keyring that doesn't exist: $keyring"
		done
	done
	echo "== apt-cache policy"
	out=$(apt-cache policy 2>&1) || {
		printf '%s\n' "$out"
		fail "apt-cache policy failed"
	}
	if grep -E '^(E|W):' <<<"$out"; then
		fail "apt-cache policy reported a problem"
	fi
	echo "every source and pin parses"
fi

# The image keeps each version's stock www.conf, so a version whose case
# holds no pool (only ini files or the global config) still has one, and
# php-fpm -t can run.
for fpm in "$src"/etc/php/*/fpm; do
	v=$(basename "$(dirname "$fpm")")
	pools=("$fpm"/pool.d/*.conf)
	if [ "${#pools[@]}" -gt 0 ]; then
		for u in $(setting 'user|listen\.owner' "${pools[@]}"); do ensure_user "$u"; done
		for g in $(setting 'group|listen\.group' "${pools[@]}"); do ensure_group "$g"; done
	fi
	echo "== php-fpm$v -t"
	out=$("php-fpm$v" -t 2>&1) || {
		printf '%s\n' "$out"
		fail "php-fpm$v -t failed"
	}
	printf '%s\n' "$out"
	# PHP reports a broken ini file as a warning and still passes -t.
	if grep -qiE 'PHP +(Warning|Parse error|Fatal error)|syntax error' <<<"$out"; then
		fail "php-fpm$v -t reported an error"
	fi
done

if [ -f "$src/etc/zimam/logrotate.conf" ]; then
	confs=("$src"/etc/zimam/logrotate.conf "$src"/etc/zimam/logrotate.d/*.conf)
	while read -r u g; do
		ensure_user "$u"
		ensure_group "$g"
	done < <(grep -hE '^[[:space:]]*su[[:space:]]' "${confs[@]}" | awk '{ print $2, $3 }' || true)
	# While logrotate -d tests a changed rules file, configtx keeps its
	# previous version beside it as .zimam-prev. logrotate.conf must skip
	# those, or each rule counts twice.
	for f in "$src"/etc/zimam/logrotate.d/*.conf; do
		cp "/etc/zimam/logrotate.d/$(basename "$f")" "/etc/zimam/logrotate.d/$(basename "$f").zimam-prev"
	done
	echo "== logrotate -d"
	out=$(logrotate -d --state /tmp/configcheck-logrotate.state /etc/zimam/logrotate.conf 2>&1) || {
		printf '%s\n' "$out"
		fail "logrotate -d failed"
	}
	printf '%s\n' "$out"
	# logrotate skips an unknown option with only a warning. -d always warns
	# that it is in debug mode, which is no problem.
	if grep -E '^(error|warning):' <<<"$out" | grep -qv 'in debug mode does nothing'; then
		fail "logrotate -d reported a problem"
	fi
fi

units=("$src"/etc/systemd/system/*.service "$src"/etc/systemd/system/*.timer "$src"/etc/systemd/system/*.socket)
if [ "${#units[@]}" -gt 0 ]; then
	# The image has no agent, so units that run it get a stand-in. Any other
	# missing program fails the check.
	if grep -qs /usr/local/sbin/zimam-agent "${units[@]}" && [ ! -e /usr/local/sbin/zimam-agent ]; then
		printf '#!/bin/sh\nexit 0\n' >/usr/local/sbin/zimam-agent
		chmod 0755 /usr/local/sbin/zimam-agent
	fi
	installed=()
	for u in "${units[@]}"; do
		installed+=("/etc/systemd/system/$(basename "$u")")
	done
	echo "== systemd-analyze verify"
	out=$(systemd-analyze verify "${installed[@]}" 2>&1) || {
		printf '%s\n' "$out"
		fail "systemd-analyze verify failed"
	}
	printf '%s\n' "$out"
	if [ -n "$out" ]; then
		fail "systemd-analyze verify reported problems"
	fi
fi

echo "configcheck: passed"
