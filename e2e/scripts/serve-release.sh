#!/usr/bin/env bash
# Serves the release build.sh made on the bridge, port 8080, in the layout
# GitHub uses: latest/download/<file> and download/v<version>/<file>. On a
# VPS the release is copied to the server and served there on 127.0.0.1,
# so nothing outside the server can reach it.
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
load_state

if [[ $E2E_TARGET == vps ]]; then
	www=/var/tmp/zimam-e2e-www
	in_c bash -c "rm -rf $www && mkdir -p $www/latest/download $www/download/v$E2E_VERSION"
	for f in "$E2E_WORK"/dist/*; do
		lxc file push -q "$f" "$E2E_CONTAINER$www/latest/download/${f##*/}"
	done
	in_c bash -c "cp $www/latest/download/* $www/download/v$E2E_VERSION/ &&
		systemctl stop zimam-e2e-www 2>/dev/null; systemctl reset-failed zimam-e2e-www 2>/dev/null;
		systemd-run -q --unit=zimam-e2e-www python3 -m http.server $E2E_PORT --bind 127.0.0.1 --directory $www"
	for _ in $(seq 30); do
		if in_c curl -fsS -o /dev/null "http://127.0.0.1:$E2E_PORT/latest/download/manifest.json" 2>/dev/null; then
			log "serving http://127.0.0.1:$E2E_PORT/ on the server"
			exit 0
		fi
		sleep 1
	done
	die "the release server didn't start on the server"
fi

www=$E2E_WORK/www
rm -rf "$www"
mkdir -p "$www/latest/download" "$www/download/v$E2E_VERSION"
cp "$E2E_WORK"/dist/* "$www/latest/download/"
cp "$E2E_WORK"/dist/* "$www/download/v$E2E_VERSION/"
as_root systemctl stop zimam-e2e-www 2>/dev/null || true
as_root systemctl reset-failed zimam-e2e-www 2>/dev/null || true
as_root systemd-run -q --unit=zimam-e2e-www "$(type -P python3)" -m http.server "$E2E_PORT" --bind "$BRIDGE" --directory "$www"
for _ in $(seq 30); do
	if curl -fsS -o /dev/null "http://$BRIDGE:$E2E_PORT/latest/download/manifest.json" 2>/dev/null; then
		log "serving http://$BRIDGE:$E2E_PORT/"
		exit 0
	fi
	sleep 1
done
die "the release server didn't start"
