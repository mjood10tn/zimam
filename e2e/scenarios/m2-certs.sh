#!/usr/bin/env bash
# Done-when 7 (design §2, §10), outside the browser: each site answers 200
# over HTTPS with a certificate from the run's root (Pebble's, or Let's Encrypt staging's on a VPS), and plain HTTP answers
# 301 to https://. browser.sh keeps this Pebble run's root.
# shellcheck source=../scripts/common.sh
source "$(dirname "$0")/../scripts/common.sh"
load_state

root=$E2E_CA
[[ -s $root ]] || die "no $root: browser.sh fetches it"
for name in static php84 php85; do
	host=$name.$E2E_DOMAIN
	code=$(curl -sS -m 10 -o /dev/null -w '%{http_code}' --resolve "$host:443:$CIP" --cacert "$root" "https://$host/") ||
		die "FAIL done-when 7: $host over HTTPS: curl failed"
	[[ $code == 200 ]] || die "FAIL done-when 7: $host over HTTPS answered $code"
	echo "PASS  done-when 7: $host answers 200 over HTTPS, its certificate chaining to the run's root"
	got=$(curl -sS -m 10 -o /dev/null -w '%{http_code} %{redirect_url}' --resolve "$host:80:$CIP" "http://$host/x?y=1") ||
		die "FAIL done-when 7: $host over HTTP: curl failed"
	[[ $got == "301 https://$host/x?y=1" ]] || die "FAIL done-when 7: $host over HTTP answered $got"
	echo "PASS  done-when 7: $host redirects HTTP to HTTPS"
done
