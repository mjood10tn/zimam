#!/usr/bin/env bash
# Gathers what explains a failed run into $E2E_WORK/logs, which e2e.yml
# uploads: the container's journal, audit log, Nginx, PHP-FPM, certbot and
# job logs and the panel's log, the installer's output, this host's Pebble,
# challtestsrv and release-server logs, and Playwright's traces. It keeps
# going past whatever is missing.
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
set +e

out=$E2E_WORK/logs
mkdir -p "$out"
cp "$E2E_WORK"/install.* "$out/" 2>/dev/null
in_c journalctl --no-pager -o short-iso >"$out/journal.txt" 2>&1
in_c tar -C / -czf - var/log/zimam var/log/nginx var/log/letsencrypt home/zimam/panel/shared/storage/logs \
	>"$out/container-logs.tar.gz" 2>/dev/null
in_c bash -c 'tar -C / -czf - var/log/php*-fpm.log' >"$out/php-fpm-logs.tar.gz" 2>/dev/null
for unit in zimam-e2e-pebble zimam-e2e-challtestsrv zimam-e2e-www; do
	as_root journalctl -u "$unit" --no-pager >"$out/$unit.txt" 2>&1
done
cp -r "$E2E_WORK/playwright" "$out/" 2>/dev/null
as_root chown -R "$(id -u):$(id -g)" "$out"
ls -l "$out"
exit 0
