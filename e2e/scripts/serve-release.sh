#!/usr/bin/env bash
# Serves the release build.sh made on the bridge, port 8080, in the layout
# GitHub uses: latest/download/<file> and download/v<version>/<file>.
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
load_state

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
