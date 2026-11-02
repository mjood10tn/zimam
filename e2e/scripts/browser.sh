#!/usr/bin/env bash
# Runs the browser tests (e2e/browser) on this host: Playwright's Chromium,
# trusting Pebble's issuing root through the NSS database Chromium reads.
# The root changes every time Pebble starts, so it is fetched each run.
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
load_state

root=$E2E_WORK/pebble-root.pem
curl -fsS --cacert "$E2E_WORK/pebble/ca.pem" -o "$root" https://localhost:15000/roots/0
if ! type -P certutil >/dev/null; then
	as_root apt-get update -qq
	as_root apt-get install -y -qq libnss3-tools >/dev/null
fi
nssdb=$HOME/.pki/nssdb
if [[ ! -f $nssdb/cert9.db ]]; then
	mkdir -p "$nssdb"
	certutil -d "sql:$nssdb" -N --empty-password
fi
certutil -d "sql:$nssdb" -D -n "Zimam e2e Pebble root" 2>/dev/null || true
certutil -d "sql:$nssdb" -A -t "C,," -n "Zimam e2e Pebble root" -i "$root"

cd "$E2E_ROOT/e2e/browser" || exit
npm ci --no-audit --no-fund
npx playwright install --with-deps chromium
E2E_WORK=$E2E_WORK npx playwright test
