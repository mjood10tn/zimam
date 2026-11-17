#!/usr/bin/env bash
# Gathers what explains a failed run into $E2E_WORK/logs, which e2e.yml
# uploads: the container's journal, audit log, Nginx, PHP-FPM, certbot and
# job logs and the panel's log, the installer's output, this host's Pebble,
# challtestsrv and release-server logs, and Playwright's traces. It keeps
# going past whatever is missing.
#
# The panel's requests (a slow or failed /admin/login) are explained by
# Nginx's own logs, which the panel's vhost writes to, and by its PHP-FPM:
# the php*-fpm units' journal and status and the FPM processes. Nginx's
# access and error logs can hold deploy URLs, whose path is the deploy
# token (/hooks/deploy/<site>/<token>), so neither is taken whole: only
# their lines without /hooks/deploy/ are kept, in nginx-access.txt and
# nginx-error.txt.
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
set +e

out=$E2E_WORK/logs
mkdir -p "$out"
cp "$E2E_WORK"/install.* "$out/" 2>/dev/null
in_c journalctl --no-pager -o short-iso >"$out/journal.txt" 2>&1
in_c tar -C / --exclude='var/log/nginx/access.log*' --exclude='var/log/nginx/error.log*' -czf - var/log/zimam var/log/nginx var/log/letsencrypt home/zimam/panel/shared/storage/logs \
	>"$out/container-logs.tar.gz" 2>/dev/null
in_c bash -c 'tar -C / -czf - var/log/php*-fpm.log' >"$out/php-fpm-logs.tar.gz" 2>/dev/null
# The panel's Nginx and PHP-FPM, as plain text. $f is the container's.
# shellcheck disable=SC2016
in_c bash -c 'for f in /var/log/nginx/error.log*; do echo "== $f"; zcat -f "$f" | grep -v -F /hooks/deploy/; done' >"$out/nginx-error.txt" 2>&1
# shellcheck disable=SC2016
in_c bash -c 'for f in /var/log/nginx/access.log*; do echo "== $f"; zcat -f "$f" | grep -v -F /hooks/deploy/; done' >"$out/nginx-access.txt" 2>&1
in_c journalctl --no-pager -o short-iso -u 'php*-fpm.service' >"$out/php-fpm-journal.txt" 2>&1
{
	in_c systemctl status --no-pager --full 'php*-fpm.service'
	echo "== FPM processes"
	in_c bash -c 'ps -eo pid,user,etime,stat,pcpu,rss,args | grep -E "^ *PID|[p]hp-fpm"'
} >"$out/php-fpm-status.txt" 2>&1
for unit in "$E2E_UNIT-pebble" "$E2E_UNIT-challtestsrv" "$E2E_UNIT-www"; do
	as_root journalctl -u "$unit" --no-pager >"$out/$unit.txt" 2>&1
done
cp -r "$E2E_WORK/playwright" "$out/" 2>/dev/null
as_root chown -R "$(id -u):$(id -g)" "$out"
ls -l "$out"
exit 0
