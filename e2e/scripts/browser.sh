#!/usr/bin/env bash
# Runs the browser tests (e2e/browser) on this host: Playwright's Chromium,
# trusting the run's root (common.sh's E2E_CA) through the NSS database
# Chromium reads. In LXD that is Pebble's issuing root, which changes every
# time Pebble starts, so it is fetched each run; on a VPS, Let's Encrypt's
# staging roots, which create.sh wrote.
# Arguments go to playwright test: --project recovery for milestone 1,
# --project m2-default for milestone 2.
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
load_state

if [[ $E2E_TARGET == lxd ]]; then
	curl -fsS --cacert "$E2E_WORK/pebble/ca.pem" -o "$E2E_CA" https://localhost:15000/roots/0
fi
[[ -s $E2E_CA ]] || die "no $E2E_CA"
if ! type -P certutil >/dev/null; then
	as_root apt-get update -qq
	as_root apt-get install -y -qq libnss3-tools >/dev/null
fi
nssdb=$HOME/.pki/nssdb
if [[ ! -f $nssdb/cert9.db ]]; then
	mkdir -p "$nssdb"
	certutil -d "sql:$nssdb" -N --empty-password
fi
# One NSS entry per certificate in the file.
split=$E2E_WORK/ca-split
rm -rf "$split" && mkdir -p "$split"
awk -v dir="$split" '/-----BEGIN CERTIFICATE-----/ { n++ } n { print > (dir "/" n ".pem") }' "$E2E_CA"
for pem in "$split"/*.pem; do
	nick="Zimam e2e root $(basename "$pem" .pem)"
	certutil -d "sql:$nssdb" -D -n "$nick" 2>/dev/null || true
	certutil -d "sql:$nssdb" -A -t "C,," -n "$nick" -i "$pem"
done

cd "$E2E_ROOT/e2e/browser" || exit
npm ci --no-audit --no-fund
npx playwright install --with-deps chromium
E2E_WORK=$E2E_WORK E2E_DOMAIN=$E2E_DOMAIN E2E_TARGET=$E2E_TARGET npx playwright test "$@"
