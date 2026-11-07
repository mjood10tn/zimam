#!/usr/bin/env bash
# Done-when 9 (design §2, §9.6, §9.7), outside the browser: the checks of
# php84's security settings with real requests, from this host over HTTPS
# and from inside the container, after the panel applied them.
#   m3-security.sh setup          writes exec.php and hostname.php as s_php84
#   m3-security.sh mark           remembers the newest panel job
#   m3-security.sh job <status> [command] [count]
#                                 waits for the jobs since the mark
#   m3-security.sh on             force_https off, HSTS 30 days, the header
#                                 preset, risky functions blocked, PHP kept
#                                 in the site's folder
#   m3-security.sh off            force_https on, everything else off
#   m3-security.sh deny | allow | password
#   m3-security.sh checks         every check's stored status
#   m3-security.sh break | broken | restore
#                                 an invalid tls.conf, then what it left
#   m3-security.sh cleanup        removes the PHP files and the ACME token
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"
load_state

host=php84.zimam.test
root=$E2E_WORK/pebble-root.pem
public=/home/s_php84/site/current/public
token=/var/www/zimam-acme/.well-known/acme-challenge/zimam-e2e-token
creds=$E2E_WORK/basic-auth # user:password, written by the browser spec
tls=/etc/nginx/zimam/tls.conf
saved=/var/tmp/zimam-e2e-tls.conf

pass() { echo "PASS  done-when 9: $*"; }

# https <path> [curl args...] prints the status line and headers of a
# request from this host, and keeps the body for body().
https() {
	local path=$1
	shift
	curl -sS -m 10 -D - -o "$E2E_WORK/body" --resolve "$host:443:$CIP" --cacert "$root" "$@" "https://$host$path" | tr -d '\r'
}

body() { cat "$E2E_WORK/body"; }

# status reads the HTTP code from the status line on stdin.
status() { awk 'NR == 1 { print $2 }'; }

# code <scheme> <path> [curl args...] prints the HTTP code of a request
# from this host.
code() {
	local scheme=$1 path=$2 port=443
	shift 2
	[[ $scheme == https ]] || port=80
	curl -sS -m 10 -o /dev/null -w '%{http_code}' --resolve "$host:$port:$CIP" --cacert "$root" "$@" "$scheme://$host$path"
}

# inside <path> prints the HTTPS code of a request from the container
# itself, so from another address than this host's.
inside() {
	in_c curl -sS -m 10 -o /dev/null -w '%{http_code}' -k --resolve "$host:443:127.0.0.1" "https://$host$1"
}

# expect <what> <got> <want>
expect() {
	[[ $2 == "$3" ]] || die "FAIL done-when 9: $1: got $2, want $3"
	pass "$1: $2"
}

case ${1:-} in
setup)
	# shellcheck disable=SC2016 # PHP
	printf '%s\n' '<?php echo function_exists("exec") ? "exec enabled" : "exec disabled";' |
		in_c runuser -u s_php84 -- tee "$public/exec.php" >/dev/null
	# shellcheck disable=SC2016 # PHP
	printf '%s\n' '<?php echo @file_get_contents("/etc/hostname") === false ? "hostname refused" : "hostname read";' |
		in_c runuser -u s_php84 -- tee "$public/hostname.php" >/dev/null
	expect "exec.php's owner" "$(in_c stat -c %U "$public/exec.php")" s_php84
	;;
mark)
	jobs_mark >"$E2E_WORK/jobs-mark"
	;;
job)
	command=${3:-site.security}
	wait_jobs "$(cat "$E2E_WORK/jobs-mark")" "$command" "${4:-1}" "$2" || die "FAIL done-when 9: the $command jobs didn't end $2"
	pass "the $command jobs ended $2"
	;;
on)
	expect "HTTP with force_https off" "$(code http /)" 200
	headers=$(https /)
	expect "HTTPS" "$(status <<<"$headers")" 200
	grep -qix 'strict-transport-security: max-age=2592000' <<<"$headers" || die "FAIL done-when 9: no 30-day HSTS header: $headers"
	pass "HSTS: strict-transport-security: max-age=2592000"
	for h in 'x-content-type-options: nosniff' 'x-frame-options: SAMEORIGIN' 'referrer-policy: strict-origin-when-cross-origin' \
		'permissions-policy: camera=(), microphone=(), geolocation=()'; do
		grep -qixF "$h" <<<"$headers" || die "FAIL done-when 9: the header preset lacks $h: $headers"
	done
	pass "the header preset: all four headers"
	https /exec.php >/dev/null
	expect "exec.php with risky functions blocked" "$(body)" "exec disabled"
	https /hostname.php >/dev/null
	expect "hostname.php with PHP kept in the site's folder" "$(body)" "hostname refused"
	;;
off)
	got=$(curl -sS -m 10 -o /dev/null -w '%{http_code} %{redirect_url}' --resolve "$host:80:$CIP" "http://$host/x?y=1")
	expect "HTTP with force_https on" "$got" "301 https://$host/x?y=1"
	headers=$(https /)
	expect "HTTPS" "$(status <<<"$headers")" 200
	for h in strict-transport-security x-content-type-options x-frame-options referrer-policy permissions-policy; do
		if grep -qi "^$h:" <<<"$headers"; then
			die "FAIL done-when 9: $h is still sent: $headers"
		fi
	done
	pass "with HSTS and the preset off, none of their headers"
	https /exec.php >/dev/null
	expect "exec.php with risky functions allowed" "$(body)" "exec enabled"
	https /hostname.php >/dev/null
	expect "hostname.php with PHP not kept in the site's folder" "$(body)" "hostname read"
	;;
deny)
	expect "this host ($BRIDGE) on the deny list" "$(code https /)" 403
	expect "the container (127.0.0.1), not on the deny list" "$(inside /)" 200
	;;
allow)
	expect "this host ($BRIDGE), alone on the allow list" "$(code https /)" 200
	expect "the container (127.0.0.1), not on the allow list" "$(inside /)" 403
	;;
password)
	[[ -s $creds ]] || die "no $creds: the browser spec writes it"
	expect "no credentials" "$(code https /)" 401
	expect "a wrong password" "$(code https / -u "$(cut -d: -f1 "$creds"):wrong")" 401
	expect "the user's credentials" "$(code https / -u "$(cat "$creds")")" 200
	in_c bash -c "mkdir -p ${token%/*} && echo zimam-e2e >$token && chmod 0644 $token"
	expect "an ACME challenge over HTTP, without credentials" "$(code http /.well-known/acme-challenge/zimam-e2e-token)" 200
	expect "an ACME challenge over HTTPS, without credentials" "$(code https /.well-known/acme-challenge/zimam-e2e-token)" 200
	;;
checks)
	rows=$(panel_sql 'select coalesce(s.name, "") as site, f."check" as name, f.status, f.detail from security_findings f left join sites s on s.id = f.site_id order by f.id')
	python3 -I - "$rows" <<'PY' || die "FAIL done-when 9: the checks above"
import json, sys

rows = json.loads(sys.argv[1])
site_checks = ["dotfiles_blocked", "config_matches_template", "no_world_writable_files", "php_version_supported",
               "pool_runs_as_site_user", "home_permissions", "certificate_valid", "https_redirect"]
# The e2e container has no firewall (install.sh --skip-firewall); its
# pending updates and reboot flag depend on the image of the day.
server = {"firewall_active": {"fail"}, "ssh_password_login": {"pass"}, "ssh_root_login": {"pass"},
          "security_updates_pending": {"pass", "warn"}, "reboot_required": {"pass", "warn"},
          "stock_www_pools_removed": {"pass"}, "agent_socket_permissions": {"pass"}}
want = {(site, c): {"pass"} for site in ("static", "php84", "php85") for c in site_checks}
want |= {("", c): s for c, s in server.items()}
got = {(r["site"], r["name"]): r for r in rows}
failed = False
for key, statuses in want.items():
    r = got.get(key)
    ok = r is not None and r["status"] in statuses
    failed |= not ok
    print(("PASS  " if ok else "FAIL  ") + f"done-when 9: check {key[0] or 'server'} {key[1]}: "
          + (f"{r['status']} ({r['detail']})" if r else "missing"))
sys.exit(1 if failed else 0)
PY
	;;
break)
	in_c cp -p "$tls" "$saved"
	in_c sha256sum /etc/nginx/zimam/php84/security.conf "$tls" >"$E2E_WORK/security-before.sha"
	in_c bash -c "echo 'this is not nginx {' >$tls"
	if in_c nginx -t >/dev/null 2>&1; then
		die "the broken tls.conf passes nginx -t"
	fi
	pass "tls.conf is broken: nginx -t fails"
	;;
broken)
	in_c sha256sum /etc/nginx/zimam/php84/security.conf | cmp -s - <(head -n 1 "$E2E_WORK/security-before.sha") ||
		die "FAIL done-when 9: security.conf changed although the job failed"
	pass "security.conf is still the previous version"
	left=$(in_c find /etc/nginx /etc/php \( -name '*.zimam-tmp' -o -name '*.zimam-prev' \) -print)
	[[ -z $left ]] || die "FAIL done-when 9: the failed change left $left"
	pass "the failed change left no .zimam-tmp or .zimam-prev file"
	expect "php84 while tls.conf is broken, with credentials" "$(code https / -u "$(cat "$creds")")" 200
	;;
restore)
	if in_c test -f "$saved"; then
		in_c cp -p "$saved" "$tls"
		in_c rm -f "$saved"
	fi
	in_c sha256sum "$tls" | cmp -s - <(tail -n 1 "$E2E_WORK/security-before.sha") || die "tls.conf wasn't restored"
	in_c nginx -t >/dev/null 2>&1 || die "nginx -t fails after restoring tls.conf"
	pass "tls.conf restored, and nginx -t passes"
	;;
cleanup)
	in_c rm -f "$public/exec.php" "$public/hostname.php" "$token"
	;;
*) die "usage: m3-security.sh setup|mark|job|on|off|deny|allow|password|checks|break|broken|restore|cleanup" ;;
esac
