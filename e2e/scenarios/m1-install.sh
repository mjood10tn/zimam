#!/usr/bin/env bash
# Done-when 1 (design §2): the installer exits 0, and its last line is the
# setup link. Keeps the link for the browser tests.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"

status=$(cat "$E2E_WORK/install.status")
[[ $status == 0 ]] || die "FAIL done-when 1: the installer exited $status"
last=$(tail -n 1 "$E2E_WORK/install.out")
[[ $last =~ $E2E_SETUP_LINK ]] ||
	die "FAIL done-when 1: the installer's last line isn't a setup link: $last"
printf '%s\n' "$last" >"$E2E_WORK/setup-link"
echo "PASS  done-when 1: the installer exited 0 and printed a setup link last"

# The panel's user owns the panel's root (current, public), and Nginx's
# www-data reads every site and /etc/nginx/zimam, so Nginx must not follow a
# link the panel's user repoints at another owner's files (§9.2). Before any
# site exists, /etc/nginx/zimam/tls.conf (root's) is the other owner's file.
load_state
panel=/home/zimam/panel
current=$(in_c readlink "$panel/current") || die "FAIL isolation: the panel has no current link"
asset=$(in_c sh -c "cd $panel/current/public && find build -name '*.css' | head -n 1")
[ -n "$asset" ] || die "FAIL isolation: the panel has no built CSS"
restore() {
	in_c runuser -u zimam -- sh -c "cd $panel && ln -sfn '$current' cur.tmp && mv -T cur.tmp current && rm -rf x" || true
}
trap restore EXIT
# Only the status matters here; the run's root is fetched later, by browser.sh.
status() {
	curl -sS -m 10 -o /dev/null -w '%{http_code}' -k --resolve "$E2E_PANEL:443:$CIP" "https://$E2E_PANEL/$1"
}
code=$(status "$asset") || die "FAIL isolation: the panel didn't answer for /$asset"
[ "$code" = 200 ] || die "FAIL isolation: the panel answered $code for /$asset"
in_c runuser -u zimam -- sh -c "cd $panel && mkdir x && ln -s /etc/nginx/zimam x/public && ln -s x cur.tmp && mv -T cur.tmp current"
code=$(status tls.conf) || die "FAIL isolation: the panel didn't answer for tls.conf"
[ "$code" != 200 ] || die "FAIL isolation: a repointed panel served /etc/nginx/zimam/tls.conf (200)"
restore
trap - EXIT
again=$(status "$asset") || die "FAIL isolation: the panel didn't answer once restored"
[ "$again" = 200 ] || die "FAIL isolation: the panel answered $again for /$asset once restored"
echo "PASS  isolation: the panel serves /$asset, and $code for root's file through a repointed current"
